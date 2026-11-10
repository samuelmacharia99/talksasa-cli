package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/config"
	"github.com/samuelmacharia99/talksasa-cli/internal/link"
)

// apiURL is the API base this run talks to: --api-url, TALKSASA_API_URL,
// the address chosen at login, then the default.
func (a *App) apiURL() (string, error) {
	raw := a.flags.apiURL
	if raw == "" {
		raw = os.Getenv("TALKSASA_API_URL")
	}
	if raw == "" {
		settings, err := config.Load()
		if err != nil {
			return "", err
		}
		raw = settings.APIURL
	}
	if raw == "" {
		raw = DefaultAPIURL
	}
	base, err := api.NormalizeBaseURL(raw)
	if err != nil {
		return "", &usageError{msg: err.Error()}
	}
	return base, nil
}

// token finds the token for an API base, and where it came from.
func (a *App) token(base string) (string, string, error) {
	if token := strings.TrimSpace(config.TokenFromEnv()); token != "" {
		return token, config.SourceEnv, nil
	}
	token, source, err := config.LoadToken(base)
	if errors.Is(err, config.ErrNoToken) {
		return "", "", withExit(ExitAuth, fmt.Errorf("not signed in to %s. Run: talksasa login (or set TALKSASA_TOKEN)", api.Origin(base)))
	}
	if err != nil {
		return "", "", err
	}
	return token, source, nil
}

// client is a signed-in API client.
func (a *App) client() (*api.Client, error) {
	base, err := a.apiURL()
	if err != nil {
		return nil, err
	}
	token, _, err := a.token(base)
	if err != nil {
		return nil, err
	}
	return api.New(base, token, a.userAgent()), nil
}

// target is the app a command acts on.
type target struct {
	ID   int
	Name string
}

func (t target) String() string {
	if t.Name == "" {
		return "app #" + strconv.Itoa(t.ID)
	}
	return fmt.Sprintf("%s (#%d)", t.Name, t.ID)
}

// resolveApp finds the app: --app, TALKSASA_APP, then .talksasa.json.
// A name is looked up among the account's services.
func (a *App) resolveApp(ctx context.Context, c *api.Client) (target, error) {
	ref := strings.TrimSpace(a.flags.app)
	if ref == "" {
		ref = strings.TrimSpace(os.Getenv("TALKSASA_APP"))
	}
	if ref == "" {
		dir, err := a.Getwd()
		if err != nil {
			return target{}, err
		}
		l, _, err := link.Find(dir)
		if err != nil {
			return target{}, err
		}
		if l == nil {
			return target{}, &usageError{msg: "which app? Pass --app, set TALKSASA_APP, or run talksasa link in the project folder"}
		}
		return target{ID: l.ServiceID, Name: l.Name}, nil
	}
	return a.lookupApp(ctx, c, ref)
}

// lookupApp turns an id or a name into an app.
func (a *App) lookupApp(ctx context.Context, c *api.Client, ref string) (target, error) {
	ref = strings.TrimPrefix(ref, "#")
	if id, err := strconv.Atoi(ref); err == nil {
		if id <= 0 {
			return target{}, &usageError{msg: "an app id is a positive number"}
		}
		return target{ID: id}, nil
	}

	_, services, err := c.Services(ctx)
	if err != nil {
		return target{}, err
	}
	var matches []api.Service
	for _, s := range services {
		if strings.EqualFold(s.Name, ref) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 1:
		return target{ID: matches[0].ID, Name: matches[0].Name}, nil
	case 0:
		return target{}, withExit(ExitNotFound, fmt.Errorf("no app named %q. List them with: talksasa apps", ref))
	}
	ids := make([]string, len(matches))
	for i, s := range matches {
		ids[i] = strconv.Itoa(s.ID)
	}
	return target{}, &usageError{msg: fmt.Sprintf("%d apps are named %q (ids %s); pass the id instead", len(matches), ref, strings.Join(ids, ", "))}
}

// appClient is the client and the app, for the commands that act on one.
func (a *App) appClient(ctx context.Context) (*api.Client, target, error) {
	c, err := a.client()
	if err != nil {
		return nil, target{}, err
	}
	t, err := a.resolveApp(ctx, c)
	return c, t, err
}

// errStillRunning is returned by a check that wants another round.
var errStillRunning = errors.New("still running")

// follow calls check every PollInterval until it returns nil (done) or a
// final error. It rides out a few transient failures (the platform busy,
// a rate limit, a dropped connection) and stops at timeout.
func (a *App) follow(ctx context.Context, timeout time.Duration, what string, check func(context.Context) error) error {
	deadline := time.Now().Add(timeout)
	failures := 0
	for {
		err := check(ctx)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errStillRunning):
			failures = 0
		case isTransient(err) && failures < 5:
			failures++
		default:
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return withExit(ExitInterrupted, fmt.Errorf("stopped following; the %s carries on", what))
			}
			return err
		}

		wait := a.PollInterval
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.RetryAfter > wait {
			wait = apiErr.RetryAfter
		}
		if time.Now().Add(wait).After(deadline) {
			return withExit(ExitTimeout, fmt.Errorf("the %s was still running after %s", what, timeout))
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return withExit(ExitInterrupted, fmt.Errorf("stopped following; the %s carries on", what))
		case <-timer.C:
		}
	}
}
