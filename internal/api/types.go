package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// The shapes the API answers with. Fields mirror app/Http/Controllers/Api/V1
// in the platform; a field the CLI does not print still decodes harmlessly.

type Me struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Email   string  `json:"email"`
	Company *string `json:"company"`
	Token   *struct {
		Name          string   `json:"name"`
		Abilities     []string `json:"abilities"`
		ExpiresAt     *string  `json:"expires_at"`
		CustomerToken bool     `json:"customer_token"`
	} `json:"token"`
}

type Service struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	TypeLabel string  `json:"type_label"`
	Product   *string `json:"product"`
	Status    string  `json:"status"`
	Project   *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	URL *string `json:"url"`
	App *struct {
		Deployed bool    `json:"deployed"`
		Status   *string `json:"status"`
		Stack    *string `json:"stack"`
	} `json:"app"`
	CreatedAt *string `json:"created_at"`
}

// IsApp reports whether the service is an application the container
// commands act on.
func (s Service) IsApp() bool { return s.Type == "container_hosting" }

type Container struct {
	ServiceID         int     `json:"service_id"`
	Status            string  `json:"status"`
	DeployedAt        *string `json:"deployed_at"`
	LastStatusCheckAt *string `json:"last_status_check_at"`
	AutoRestart       bool    `json:"auto_restart"`
	RestartPolicy     *string `json:"restart_policy"`
}

type Logs struct {
	Logs      []string `json:"logs"`
	Truncated bool     `json:"truncated"`
}

type Metrics struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryPercent   float64 `json:"memory_percent"`
	MemoryMB        float64 `json:"memory_mb"`
	NetworkInBytes  int64   `json:"network_in_bytes"`
	NetworkOutBytes int64   `json:"network_out_bytes"`
	SampledAt       *string `json:"sampled_at"`
}

type Step struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Status  string  `json:"status"`
	Message *string `json:"message,omitempty"`
}

type Pull struct {
	ID          int     `json:"id"`
	Status      string  `json:"status"`
	IsActive    bool    `json:"is_active"`
	CanRestart  bool    `json:"can_restart"`
	Commit      *string `json:"commit"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	Steps       []Step  `json:"steps"`
	Error       *struct {
		Title    string `json:"title"`
		Guidance string `json:"guidance"`
		Details  string `json:"details"`
	} `json:"error"`
	LogTail []string `json:"log_tail"`
}

type PullStatus struct {
	Repository struct {
		URL    *string `json:"url"`
		Branch *string `json:"branch"`
	} `json:"repository"`
	Pull *Pull `json:"pull"`
}

// Deployment is the progress of an app's latest deploy attempt.
//
// StartedAt and Outcome came with the platform's 90925799; an older server
// leaves HasAttempt false, and a caller cannot follow a redeploy.
type Deployment struct {
	ServiceID               int      `json:"service_id"`
	ServiceStatus           string   `json:"service_status"`
	DeploymentStatus        *string  `json:"deployment_status"`
	StartedAt               *string  `json:"started_at"`
	Outcome                 *string  `json:"outcome"`
	FinishedAt              *string  `json:"finished_at"`
	IsActive                bool     `json:"is_active"`
	IsReady                 bool     `json:"is_ready"`
	IsFailed                bool     `json:"is_failed"`
	IsAwaitingConfiguration bool     `json:"is_awaiting_configuration"`
	Percent                 int      `json:"percent"`
	Headline                string   `json:"headline"`
	Steps                   []Step   `json:"steps"`
	MissingVariables        []string `json:"missing_variables"`

	// HasAttempt is true when the server sends started_at and outcome.
	HasAttempt bool `json:"-"`
}

type EnvVariable struct {
	Key             string `json:"key"`
	State           string `json:"state"`
	HasValue        bool   `json:"has_value"`
	Sensitive       bool   `json:"sensitive"`
	PlatformManaged bool   `json:"platform_managed"`
}

type Env struct {
	Variables      []EnvVariable `json:"variables"`
	RequiredByApp  []string      `json:"required_by_app"`
	RejectedByApp  []string      `json:"rejected_by_app"`
	SuggestedByApp []string      `json:"suggested_by_app"`
}

type EnvChange struct {
	Updated   *int     `json:"updated"`
	Deleted   *int     `json:"deleted"`
	Skipped   []string `json:"skipped"`
	Dismissed []string `json:"dismissed"`
	Applied   bool     `json:"applied"`
}

type Domain struct {
	Domain              string  `json:"domain"`
	Purpose             string  `json:"purpose"`
	PlatformHostname    bool    `json:"platform_hostname"`
	Status              string  `json:"status"`
	SSL                 bool    `json:"ssl"`
	URL                 string  `json:"url"`
	VerifiedAt          *string `json:"verified_at"`
	OwnershipVerifiedAt *string `json:"ownership_verified_at"`
	Error               *struct {
		Title    string `json:"title"`
		Guidance string `json:"guidance"`
		Details  string `json:"details"`
	} `json:"error"`
}

type Backup struct {
	ID          int     `json:"id"`
	Name        *string `json:"name"`
	Status      string  `json:"status"`
	Type        *string `json:"type"`
	SizeBytes   *int64  `json:"size_bytes"`
	CreatedAt   *string `json:"created_at"`
	CompletedAt *string `json:"completed_at"`
}

// Finished reports whether a backup has stopped changing.
func (b Backup) Finished() bool {
	return b.Status == "completed" || b.Status == "failed" || b.Status == "deleted"
}

type Restore struct {
	ID         int     `json:"id"`
	BackupID   int     `json:"backup_id"`
	BackupName *string `json:"backup_name"`
	Status     string  `json:"status"`
	Error      *string `json:"error"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

// Finished reports whether a restore has stopped changing.
func (r Restore) Finished() bool { return r.Status == "completed" || r.Status == "failed" }

// Decode unmarshals a response's data.
func Decode[T any](resp *Response) (T, error) {
	var out T
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return out, fmt.Errorf("the API's answer was not in the expected shape: %w", err)
	}
	return out, nil
}

// --- Calls ------------------------------------------------------------------

func servicePath(id int, suffix string) string {
	p := "services/" + strconv.Itoa(id)
	if suffix != "" {
		p += "/" + suffix
	}
	return p
}

func containerPath(id int, suffix string) string {
	return servicePath(id, "container/"+suffix)
}

func (c *Client) Me(ctx context.Context) (*Response, Me, error) {
	return call[Me](ctx, c, http.MethodGet, "me", nil, nil)
}

func (c *Client) Services(ctx context.Context) (*Response, []Service, error) {
	return call[[]Service](ctx, c, http.MethodGet, "services", nil, nil)
}

func (c *Client) Service(ctx context.Context, id int) (*Response, Service, error) {
	return call[Service](ctx, c, http.MethodGet, servicePath(id, ""), nil, nil)
}

func (c *Client) Container(ctx context.Context, id int) (*Response, Container, error) {
	return call[Container](ctx, c, http.MethodGet, servicePath(id, "container"), nil, nil)
}

func (c *Client) Power(ctx context.Context, id int, action string) (*Response, error) {
	return c.Do(ctx, http.MethodPost, containerPath(id, action), nil, nil)
}

func (c *Client) Logs(ctx context.Context, id, lines int) (*Response, Logs, error) {
	return call[Logs](ctx, c, http.MethodGet, containerPath(id, "logs"), url.Values{"lines": {strconv.Itoa(lines)}}, nil)
}

func (c *Client) Metrics(ctx context.Context, id int) (*Response, Metrics, error) {
	return call[Metrics](ctx, c, http.MethodGet, containerPath(id, "metrics"), nil, nil)
}

type PullOptions struct {
	ReplaceExisting bool `json:"replace_existing"`
	RunComposer     bool `json:"run_composer"`
	RunMigrations   bool `json:"run_migrations"`
	ForceRebuild    bool `json:"force_rebuild"`
}

func (c *Client) StartPull(ctx context.Context, id int, opts PullOptions) (*Response, Pull, error) {
	return call[Pull](ctx, c, http.MethodPost, containerPath(id, "pull"), nil, opts)
}

func (c *Client) PullStatus(ctx context.Context, id int) (*Response, PullStatus, error) {
	return call[PullStatus](ctx, c, http.MethodGet, containerPath(id, "pull"), nil, nil)
}

func (c *Client) Redeploy(ctx context.Context, id int) (*Response, error) {
	return c.Do(ctx, http.MethodPost, containerPath(id, "redeploy"), nil, nil)
}

func (c *Client) Deployment(ctx context.Context, id int) (*Response, Deployment, error) {
	resp, d, err := call[Deployment](ctx, c, http.MethodGet, containerPath(id, "deployment"), nil, nil)
	if err == nil {
		var keys map[string]json.RawMessage
		if json.Unmarshal(resp.Data, &keys) == nil {
			_, hasStart := keys["started_at"]
			_, hasOutcome := keys["outcome"]
			d.HasAttempt = hasStart && hasOutcome
		}
	}
	return resp, d, err
}

func (c *Client) Env(ctx context.Context, id int) (*Response, Env, error) {
	return call[Env](ctx, c, http.MethodGet, containerPath(id, "env"), nil, nil)
}

func (c *Client) SetEnv(ctx context.Context, id int, variables map[string]string, apply bool) (*Response, EnvChange, error) {
	body := map[string]any{"variables": variables, "apply": apply}
	return call[EnvChange](ctx, c, http.MethodPut, containerPath(id, "env"), nil, body)
}

func (c *Client) UnsetEnv(ctx context.Context, id int, keys []string, apply bool) (*Response, EnvChange, error) {
	body := map[string]any{"keys": keys, "apply": apply}
	return call[EnvChange](ctx, c, http.MethodDelete, containerPath(id, "env"), nil, body)
}

func (c *Client) Domains(ctx context.Context, id int) (*Response, []Domain, error) {
	return call[[]Domain](ctx, c, http.MethodGet, containerPath(id, "domains"), nil, nil)
}

func (c *Client) Backups(ctx context.Context, id int) (*Response, []Backup, error) {
	return call[[]Backup](ctx, c, http.MethodGet, containerPath(id, "backups"), nil, nil)
}

func (c *Client) CreateBackup(ctx context.Context, id int) (*Response, Backup, error) {
	return call[Backup](ctx, c, http.MethodPost, containerPath(id, "backups"), nil, nil)
}

func (c *Client) RestoreBackup(ctx context.Context, id, backupID int) (*Response, Restore, error) {
	return call[Restore](ctx, c, http.MethodPost, containerPath(id, "backups/"+strconv.Itoa(backupID)+"/restore"), nil, nil)
}

func (c *Client) RestoreStatus(ctx context.Context, id, restoreID int) (*Response, Restore, error) {
	return call[Restore](ctx, c, http.MethodGet, containerPath(id, "restores/"+strconv.Itoa(restoreID)), nil, nil)
}

func (c *Client) DeleteBackup(ctx context.Context, id, backupID int) (*Response, error) {
	return c.Do(ctx, http.MethodDelete, containerPath(id, "backups/"+strconv.Itoa(backupID)), nil, nil)
}

func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any) (*Response, T, error) {
	var zero T
	resp, err := c.Do(ctx, method, path, query, body)
	if err != nil {
		return nil, zero, err
	}
	out, err := Decode[T](resp)
	return resp, out, err
}
