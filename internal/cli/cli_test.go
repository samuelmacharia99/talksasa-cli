package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samuelmacharia99/talksasa-cli/internal/config"
)

// fakeAPI answers "METHOD /path" with scripted handlers. Each handler gets
// how many times its route has been called before (0 on the first call).
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]func(w http.ResponseWriter, r *http.Request, n int)
	calls  map[string]int
	bodies map[string][]string
	srv    *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t:      t,
		routes: map[string]func(http.ResponseWriter, *http.Request, int){},
		calls:  map[string]int{},
		bodies: map[string][]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1")
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		handler, ok := f.routes[key]
		n := f.calls[key]
		f.calls[key]++
		f.bodies[key] = append(f.bodies[key], string(body))
		f.mu.Unlock()

		if r.Header.Get("Authorization") == "" {
			t.Errorf("%s sent without a token", key)
		}
		if !ok {
			t.Errorf("unexpected call %s", key)
			jsonAnswer(w, 404, `{"message":"Not found.","code":"not_found"}`)
			return
		}
		handler(w, r, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) on(route string, handler func(w http.ResponseWriter, r *http.Request, n int)) {
	f.routes[route] = handler
}

// answer replies with the same body every time.
func (f *fakeAPI) answer(route string, status int, body string) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request, _ int) { jsonAnswer(w, status, body) })
}

// sequence replies with each body in turn, then repeats the last.
func (f *fakeAPI) sequence(route string, bodies ...string) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		status := 200
		body := bodies[n]
		if strings.HasPrefix(body, "503:") {
			status, body = 503, strings.TrimPrefix(body, "503:")
		}
		jsonAnswer(w, status, body)
	})
}

func (f *fakeAPI) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[route]
}

func (f *fakeAPI) lastBody(route string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	bodies := f.bodies[route]
	if len(bodies) == 0 {
		f.t.Fatalf("%s was not called", route)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &out); err != nil {
		f.t.Fatalf("%s body %q: %v", route, bodies[len(bodies)-1], err)
	}
	return out
}

func jsonAnswer(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

type run struct {
	code           int
	stdout, stderr string
}

// harness is an App wired to the fake API, a private config directory and
// a working folder, with no keychain and a fast poll.
type harness struct {
	t    *testing.T
	api  *fakeAPI
	dir  string
	cfg  string
	tty  bool
	pass string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, api: newFakeAPI(t), dir: t.TempDir(), cfg: t.TempDir()}
	t.Setenv("TALKSASA_CONFIG_DIR", h.cfg)
	t.Setenv("TALKSASA_NO_KEYRING", "1")
	t.Setenv("TALKSASA_API_URL", h.api.srv.URL)
	t.Setenv("TALKSASA_TOKEN", "tsk_test")
	t.Setenv("TALKSASA_APP", "")
	return h
}

func (h *harness) run(stdin string, args ...string) run {
	return h.runContext(context.Background(), stdin, args...)
}

func (h *harness) runContext(ctx context.Context, stdin string, args ...string) run {
	var stdout, stderr bytes.Buffer
	app := &App{
		Stdin:           strings.NewReader(stdin),
		Stdout:          &stdout,
		Stderr:          &stderr,
		Getwd:           func() (string, error) { return h.dir, nil },
		Version:         "test",
		PollInterval:    time.Millisecond,
		StdinIsTerminal: h.tty,
	}
	if h.tty {
		app.ReadPassword = func() (string, error) { return h.pass, nil }
	}
	code := app.RunContext(ctx, args)
	return run{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (r run) expect(t *testing.T, code int, contains ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
	all := r.stdout + r.stderr
	for _, s := range contains {
		if !strings.Contains(all, s) {
			t.Fatalf("output lacks %q\nstdout: %s\nstderr: %s", s, r.stdout, r.stderr)
		}
	}
}

// --- Exit codes ----------------------------------------------------------------

// testdata/api_error_codes.json is ApiError::documentedCodes() from the
// platform; scripts/e2e.sh checks it against a running platform.
func TestEveryDocumentedCodeHasAnExit(t *testing.T) {
	raw, err := os.ReadFile("testdata/api_error_codes.json")
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	if err := json.Unmarshal(raw, &codes); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, code := range codes {
		documented[code] = true
		if _, ok := codeExits[code]; !ok {
			t.Errorf("API code %q has no exit code", code)
		}
	}
	for code := range codeExits {
		if !documented[code] {
			t.Errorf("exit map has %q, which the platform does not document", code)
		}
	}
}

func TestAPIErrorsBecomeExitCodes(t *testing.T) {
	cases := map[string]struct {
		status int
		code   string
		exit   int
	}{
		"bad token":        {401, "unauthenticated", ExitAuth},
		"missing ability":  {403, "token_missing_ability", ExitPermission},
		"suspended":        {403, "service_suspended", ExitPermission},
		"not yours":        {404, "not_found", ExitNotFound},
		"busy":             {409, "conflict", ExitConflict},
		"wrong stack":      {422, "unsupported", ExitInvalid},
		"slow down":        {429, "rate_limited", ExitRateLimited},
		"host not there":   {503, "upstream_unavailable", ExitUnavailable},
		"unknown 5xx code": {500, "something_new", ExitUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.answer("POST /services/7/container/restart", c.status, `{"message":"Refused for a reason.","code":"`+c.code+`"}`)
			h.run("", "restart", "--app", "7").expect(t, c.exit, "Refused for a reason.")
		})
	}
}

func TestJSONErrorsAreMachineReadable(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/redeploy", 403,
		`{"message":"This token is not allowed to do this.","code":"token_missing_ability","required_abilities":["deploy"]}`)

	r := h.run("", "redeploy", "--no-wait", "--app", "7", "--json")
	r.expect(t, ExitPermission)

	var out struct {
		Error struct {
			Code              string   `json:"code"`
			ExitCode          int      `json:"exit_code"`
			RequiredAbilities []string `json:"required_abilities"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &out); err != nil {
		t.Fatalf("stderr is not JSON: %q", r.stderr)
	}
	if out.Error.Code != "token_missing_ability" || out.Error.ExitCode != ExitPermission || out.Error.RequiredAbilities[0] != "deploy" {
		t.Fatalf("got %+v", out)
	}
	if r.stdout != "" {
		t.Fatalf("stdout should be empty on failure: %q", r.stdout)
	}
}

// --- Sign in -------------------------------------------------------------------

const meCustomer = `{"data":{"id":5,"name":"Ann","email":"ann@example.com","company":null,
	"token":{"name":"CI","abilities":["read","deploy"],"expires_at":"2026-12-10T00:00:00+00:00","customer_token":true}}}`

func TestLoginChecksAndStoresTheToken(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TALKSASA_TOKEN", "")
	h.api.on("GET /me", func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("Authorization") != "Bearer 12|tsk_pasted" {
			jsonAnswer(w, 401, `{"message":"Send a valid API token.","code":"unauthenticated"}`)
			return
		}
		jsonAnswer(w, 200, meCustomer)
	})

	h.run("12|tsk_pasted\n", "login").expect(t, ExitOK, "Signed in to", "Ann <ann@example.com>", "read, deploy", "credentials.json")

	base := h.api.srv.URL + "/api/v1"
	token, source, err := config.LoadToken(base)
	if err != nil || token != "12|tsk_pasted" || source != config.SourceFile {
		t.Fatalf("stored %q from %s: %v", token, source, err)
	}

	// Later commands use it.
	h.run("", "whoami").expect(t, ExitOK, "Ann <ann@example.com>", "token from file")

	h.run("", "logout").expect(t, ExitOK, "Removed the token", "revoke it")
	if _, _, err := config.LoadToken(base); err == nil {
		t.Fatal("logout left the token stored")
	}
	h.run("", "whoami").expect(t, ExitAuth, "not signed in")
}

func TestLoginReadsTheTokenHiddenFromATerminal(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TALKSASA_TOKEN", "")
	h.tty, h.pass = true, "12|tsk_typed"
	h.api.on("GET /me", func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("Authorization") != "Bearer 12|tsk_typed" {
			t.Errorf("sent %q", r.Header.Get("Authorization"))
		}
		jsonAnswer(w, 200, meCustomer)
	})
	r := h.run("", "login")
	r.expect(t, ExitOK, "input hidden")
	if strings.Contains(r.stdout+r.stderr, "tsk_typed") {
		t.Fatal("the token was printed")
	}
}

func TestLoginRefusesABadOrNonCustomerToken(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TALKSASA_TOKEN", "")
	h.api.answer("GET /me", 401, `{"message":"Send a valid API token.","code":"unauthenticated"}`)
	h.run("12|tsk_wrong", "login").expect(t, ExitAuth, "did not accept that token")

	h2 := newHarness(t)
	t.Setenv("TALKSASA_TOKEN", "")
	h2.api.answer("GET /me", 200, `{"data":{"id":1,"name":"Staff","email":"s@x","token":{"name":"x","abilities":["*"],"customer_token":false}}}`)
	h2.run("1|other", "login").expect(t, ExitPermission, "not a customer API token")

	for _, h := range []*harness{h, h2} {
		if _, _, err := config.LoadToken(h.api.srv.URL + "/api/v1"); err == nil {
			t.Fatal("a refused token was stored")
		}
	}

	h.run("has a space", "login").expect(t, ExitUsage, "does not look like a token")
}

func TestLoginRemembersAChosenAddress(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TALKSASA_TOKEN", "")
	t.Setenv("TALKSASA_API_URL", "")
	h.api.answer("GET /me", 200, meCustomer)

	h.run("1|tsk_x", "login", "--api-url", h.api.srv.URL).expect(t, ExitOK)
	settings, _ := config.Load()
	if settings.APIURL != h.api.srv.URL+"/api/v1" {
		t.Fatalf("saved %q", settings.APIURL)
	}
	// No --api-url needed now.
	h.run("", "whoami").expect(t, ExitOK, "Ann")
}

// --- Choosing the app ------------------------------------------------------------

const servicesList = `{"data":[
	{"id":7,"name":"shop","type":"container_hosting","type_label":"App","status":"active","url":"https://shop.example.com","app":{"deployed":true,"status":"running","stack":"laravel"}},
	{"id":8,"name":"blog","type":"container_hosting","type_label":"App","status":"active","url":null,"app":{"deployed":false,"status":null,"stack":null}},
	{"id":9,"name":"Blog","type":"container_hosting","type_label":"App","status":"active","url":null,"app":null},
	{"id":10,"name":"mail","type":"email_hosting","type_label":"Email","status":"active","url":null,"app":null}]}`

func TestAppsAreListed(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services", 200, servicesList)
	h.run("", "apps").expect(t, ExitOK, "shop", "running · laravel", "https://shop.example.com", "not deployed")
}

func TestAnAppIsFoundByNameOrRefusedWhenAmbiguous(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services", 200, servicesList)
	h.api.answer("POST /services/7/container/restart", 200, `{"message":"App restarted."}`)

	h.run("", "restart", "--app", "SHOP").expect(t, ExitOK, "App restarted.")
	h.run("", "restart", "--app", "blog").expect(t, ExitUsage, "2 apps are named", "8, 9")
	h.run("", "restart", "--app", "nope").expect(t, ExitNotFound, "no app named")
	h.run("", "restart").expect(t, ExitUsage, "which app?")
}

func TestLinkWritesOnlyTheAppAndIsFoundFromSubfolders(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services", 200, servicesList)
	h.api.answer("GET /services/7", 200, `{"data":{"id":7,"name":"shop","type":"container_hosting","type_label":"App","status":"active"}}`)
	h.api.answer("GET /services/10", 200, `{"data":{"id":10,"name":"mail","type":"email_hosting","type_label":"Email","status":"active"}}`)
	h.api.answer("POST /services/7/container/restart", 200, `{"message":"App restarted."}`)

	h.run("", "link", "mail").expect(t, ExitInvalid, "not an app")
	h.run("", "link", "shop").expect(t, ExitOK, "Linked")

	raw, err := os.ReadFile(filepath.Join(h.dir, ".talksasa.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	_ = json.Unmarshal(raw, &file)
	if len(file) != 2 || file["service_id"].(float64) != 7 || file["name"] != "shop" {
		t.Fatalf("link file holds %s", raw)
	}

	sub := filepath.Join(h.dir, "src", "app")
	_ = os.MkdirAll(sub, 0o755)
	h.dir = sub
	h.run("", "restart").expect(t, ExitOK, "App restarted.")

	h.dir = filepath.Dir(filepath.Dir(sub))
	h.run("", "unlink").expect(t, ExitOK, "Unlinked")
}

// --- Destructive actions ask -------------------------------------------------------

func TestStopAsksAndRefusesWithoutATerminal(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/stop", 200, `{"message":"App stopped."}`)

	h.run("", "stop", "--app", "7").expect(t, ExitUsage, "Pass --yes")
	if h.api.count("POST /services/7/container/stop") != 0 {
		t.Fatal("stopped without confirmation")
	}

	h.tty = true
	h.run("n\n", "stop", "--app", "7").expect(t, ExitFailed, "cancelled")
	h.run("y\n", "stop", "--app", "7").expect(t, ExitOK, "App stopped.")
	h.tty = false
	h.run("", "stop", "--app", "7", "--yes").expect(t, ExitOK, "App stopped.")
	if n := h.api.count("POST /services/7/container/stop"); n != 2 {
		t.Fatalf("stop called %d times, want 2", n)
	}
}

func TestBackupRestoreAndDeleteAsk(t *testing.T) {
	h := newHarness(t)
	h.run("", "backups", "restore", "3", "--app", "7").expect(t, ExitUsage, "Pass --yes")
	h.run("", "backups", "delete", "3", "--app", "7").expect(t, ExitUsage, "Pass --yes")
	h.run("", "backups", "delete", "abc", "--app", "7", "--yes").expect(t, ExitUsage, "not a backup id")
}

// --- Deploy -----------------------------------------------------------------------

func pull(id int, status string, active bool, extra string) string {
	b, _ := json.Marshal(active)
	return `{"data":{"repository":{"url":"https://github.com/acme/shop.git","branch":"main"},"pull":{"id":` +
		itoa(id) + `,"status":"` + status + `","is_active":` + string(b) + `,"commit":"0123456789abcdef","steps":[
		{"key":"fetch","label":"Fetch the code","status":"completed"},
		{"key":"install","label":"Install dependencies","status":"` + map[bool]string{true: "running", false: status}[active] + `","message":null}]` + extra + `}}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestDeployWaitsAndSucceeds(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"message":"Git pull started.","data":{"id":41,"status":"pending","is_active":true,"steps":[]}}`)
	h.api.sequence("GET /services/7/container/pull",
		pull(41, "running", true, ""),
		`503:{"message":"busy","code":"upstream_unavailable"}`, // ridden out
		pull(41, "completed", false, ""),
	)

	r := h.run("", "deploy", "--app", "7")
	r.expect(t, ExitOK, "Pull #41 started", "Fetch the code", "Install dependencies", "Deployed app #7 at 0123456789ab.")

	body := h.api.lastBody("POST /services/7/container/pull")
	if body["replace_existing"] != false || body["run_composer"] != true || body["run_migrations"] != true || body["force_rebuild"] != false {
		t.Fatalf("pull options %v", body)
	}
}

func TestDeployFailureExitsNonZeroWithTheExplanation(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"message":"Git pull started.","data":{"id":41,"status":"pending","is_active":true,"steps":[]}}`)
	h.api.answer("GET /services/7/container/pull", 200, pull(41, "failed", false,
		`,"error":{"title":"Composer could not install","guidance":"Commit composer.lock.","details":"Your lock file needs PHP 8.4.1"},"log_tail":["line a","Problem 1"]`))

	h.run("", "deploy", "--app", "7").expect(t, ExitFailed, "Composer could not install", "Commit composer.lock.", "PHP 8.4.1", "Problem 1", "the pull failed")
}

func TestDeployNoticesANewerPull(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"data":{"id":41,"status":"pending","is_active":true}}`)
	h.api.answer("GET /services/7/container/pull", 200, pull(42, "running", true, ""))
	h.run("", "deploy", "--app", "7").expect(t, ExitConflict, "replaced by a newer pull (#42)")
}

func TestDeployReplaceNeedsConfirmationAndSendsTheFlags(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"message":"Git pull started.","data":{"id":41,"status":"pending","is_active":true}}`)

	h.run("", "deploy", "--app", "7", "--replace", "--no-wait").expect(t, ExitUsage, "Pass --yes")
	if h.api.count("POST /services/7/container/pull") != 0 {
		t.Fatal("replaced without confirmation")
	}

	h.run("", "deploy", "--app", "7", "--replace", "--no-composer", "--no-migrations", "--rebuild", "--no-wait", "--yes").expect(t, ExitOK, "Pull #41 started")
	body := h.api.lastBody("POST /services/7/container/pull")
	if body["replace_existing"] != true || body["run_composer"] != false || body["run_migrations"] != false || body["force_rebuild"] != true {
		t.Fatalf("pull options %v", body)
	}
}

func TestDeployTimesOut(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"data":{"id":41,"status":"pending","is_active":true}}`)
	h.api.answer("GET /services/7/container/pull", 200, pull(41, "running", true, ""))
	h.run("", "deploy", "--app", "7", "--timeout", "20ms").expect(t, ExitTimeout, "still running after")
}

func TestDeployJSONPrintsTheFinalPullOnly(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 202, `{"data":{"id":41,"status":"pending","is_active":true}}`)
	h.api.answer("GET /services/7/container/pull", 200, pull(41, "completed", false, ""))
	r := h.run("", "deploy", "--app", "7", "--json")
	r.expect(t, ExitOK)
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON document: %q", r.stdout)
	}
	if r.stderr != "" {
		t.Fatalf("progress went to stderr in --json: %q", r.stderr)
	}
}

// --- Redeploy -----------------------------------------------------------------------

func deployment(startedAt, outcome, status string) string {
	q := func(s string) string {
		if s == "" {
			return "null"
		}
		return `"` + s + `"`
	}
	return `{"data":{"service_id":7,"service_status":"` + status + `","started_at":` + q(startedAt) + `,"outcome":` + q(outcome) +
		`,"finished_at":null,"is_active":false,"is_ready":` + map[bool]string{true: "true", false: "false"}[status == "active"] +
		`,"is_failed":false,"percent":50,"headline":"Deploying","steps":[{"key":"deploy_started","label":"Prepare the application","status":"completed"}],"missing_variables":["APP_KEY"]}}`
}

func TestRedeployWaitsForItsOwnAttemptToEnd(t *testing.T) {
	h := newHarness(t)
	old := "2026-11-10T10:00:00+00:00"
	next := "2026-11-10T12:00:00+00:00"
	h.api.answer("POST /services/7/container/redeploy", 202, `{"message":"Redeploy queued.","data":{"service_id":7}}`)
	h.api.sequence("GET /services/7/container/deployment",
		deployment(old, "succeeded", "active"), // read before queuing
		deployment(old, "succeeded", "active"), // still queued: the old success must not count
		deployment(next, "", "active"),         // begun, status not yet changed: not done
		deployment(next, "", "provisioning"),
		deployment(next, "succeeded", "active"),
	)

	h.run("", "redeploy", "--app", "7").expect(t, ExitOK, "Redeploy queued", "Redeployed app #7.")
	if n := h.api.count("GET /services/7/container/deployment"); n != 5 {
		t.Fatalf("deployment read %d times, want 5 (stopped early on a stale outcome?)", n)
	}
}

func TestRedeployOutcomesThatFail(t *testing.T) {
	for outcome, want := range map[string]string{
		"failed":                 "the redeploy failed",
		"rolled_back":            "previous release was put back",
		"awaiting_configuration": "APP_KEY",
		"abandoned":              "the redeploy abandoned",
	} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			h.api.answer("POST /services/7/container/redeploy", 202, `{"message":"Redeploy queued."}`)
			h.api.sequence("GET /services/7/container/deployment",
				deployment("2026-11-10T10:00:00+00:00", "succeeded", "active"),
				deployment("2026-11-10T12:00:00+00:00", outcome, "failed"),
			)
			h.run("", "redeploy", "--app", "7").expect(t, ExitFailed, want)
		})
	}
}

func TestRedeployOnAnOlderPlatformDoesNotPretendToWait(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/redeploy", 202, `{"message":"Redeploy queued."}`)
	h.api.answer("GET /services/7/container/deployment", 200, `{"data":{"service_id":7,"service_status":"active","is_ready":true,"steps":[]}}`)
	h.run("", "redeploy", "--app", "7").expect(t, ExitOK, "Redeploy queued", "cannot report when a redeploy ends")
	if n := h.api.count("GET /services/7/container/deployment"); n != 1 {
		t.Fatalf("followed an attempt it cannot see: %d reads", n)
	}
}

func TestAFirstEverDeployIsFollowed(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/redeploy", 202, `{"message":"Redeploy queued."}`)
	h.api.sequence("GET /services/7/container/deployment",
		deployment("", "", "failed"),
		deployment("2026-11-10T12:00:00+00:00", "succeeded", "active"),
	)
	h.run("", "redeploy", "--app", "7").expect(t, ExitOK, "Redeployed")
}

// --- Environment ---------------------------------------------------------------------

func TestEnvListShowsNamesNeverValues(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services/7/container/env", 200, `{"data":{"variables":[
		{"key":"APP_NAME","state":"set","has_value":true,"sensitive":false,"platform_managed":false},
		{"key":"DB_PASSWORD","state":"platform","has_value":true,"sensitive":true,"platform_managed":true},
		{"key":"APP_KEY","state":"needs_value","has_value":false,"sensitive":true,"platform_managed":false}],
		"required_by_app":["APP_KEY"],"rejected_by_app":[],"suggested_by_app":[]}}`)
	h.run("", "env", "list", "--app", "7").expect(t, ExitOK, "APP_NAME", "platform", "needs a value for: APP_KEY")
}

func TestEnvSetFromArgumentsFileAndHiddenInput(t *testing.T) {
	h := newHarness(t)
	h.api.answer("PUT /services/7/container/env", 200, `{"message":"Environment variables saved. Applied to the app.","data":{"updated":3,"applied":true}}`)

	envFile := filepath.Join(h.dir, ".env.production")
	_ = os.WriteFile(envFile, []byte("MAIL_FROM=shop@example.com\nAPP_NAME=\"From file\"\n"), 0o600)

	r := h.run("-----BEGIN KEY-----\nabc\n-----END KEY-----\n", "env", "set", "APP_NAME=Shop", "PRIVATE_KEY", "--from-file", envFile, "--app", "7")
	r.expect(t, ExitOK, "Applied to the app.")
	if strings.Contains(r.stdout+r.stderr, "BEGIN KEY") {
		t.Fatal("a value was printed")
	}

	body := h.api.lastBody("PUT /services/7/container/env")
	vars := body["variables"].(map[string]any)
	if vars["APP_NAME"] != "Shop" || vars["MAIL_FROM"] != "shop@example.com" ||
		vars["PRIVATE_KEY"] != "-----BEGIN KEY-----\nabc\n-----END KEY-----" || body["apply"] != true {
		t.Fatalf("sent %v", body)
	}

	h.run("", "env", "set", "A=1", "--no-apply", "--app", "7").expect(t, ExitOK)
	if h.api.lastBody("PUT /services/7/container/env")["apply"] != false {
		t.Fatal("--no-apply still applied")
	}
}

func TestEnvSetRefusalsBeforeAnyRequest(t *testing.T) {
	h := newHarness(t)
	h.run("", "env", "set", "--app", "7").expect(t, ExitUsage, "nothing to set")
	h.run("", "env", "set", "A", "B", "--app", "7").expect(t, ExitUsage, "only one value")
	h.run("", "env", "set", "=x", "--app", "7").expect(t, ExitUsage, "no name")
	if h.api.count("PUT /services/7/container/env") != 0 {
		t.Fatal("a refused command still called the API")
	}
}

func TestEnvSetSavedButNotApplied(t *testing.T) {
	h := newHarness(t)
	h.api.answer("PUT /services/7/container/env", 503,
		`{"message":"The change was saved, but applying it to the app failed.","code":"upstream_unavailable","saved":true,"applied":false}`)
	h.run("", "env", "set", "A=1", "--app", "7").expect(t, ExitUnavailable, "saved, but not applied")
}

func TestEnvSetPlatformKeyIsRefusedByThePlatform(t *testing.T) {
	h := newHarness(t)
	h.api.answer("PUT /services/7/container/env", 422,
		`{"message":"DB_PASSWORD is managed by the platform and can only be changed in the portal.","code":"validation_failed","errors":{"variables":["DB_PASSWORD is managed by the platform and can only be changed in the portal."]}}`)
	h.run("", "env", "set", "DB_PASSWORD=x", "--app", "7").expect(t, ExitInvalid, "managed by the platform")
}

func TestEnvUnsetSendsUpperCasedUniqueKeys(t *testing.T) {
	h := newHarness(t)
	h.api.answer("DELETE /services/7/container/env", 200, `{"message":"Removed 2 variables. Applied to the app.","data":{"deleted":2,"applied":true}}`)
	h.run("", "env", "unset", "old_key", "OLD_KEY", "other", "--app", "7").expect(t, ExitOK, "Removed 2")
	keys := h.api.lastBody("DELETE /services/7/container/env")["keys"].([]any)
	if len(keys) != 2 || keys[0] != "OLD_KEY" || keys[1] != "OTHER" {
		t.Fatalf("sent %v", keys)
	}
}

// --- Logs ----------------------------------------------------------------------------

func TestLogsFollowPrintsOnlyNewLines(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.api.on("GET /services/7/container/logs", func(w http.ResponseWriter, r *http.Request, n int) {
		switch n {
		case 0:
			jsonAnswer(w, 200, `{"data":{"logs":["a","b"],"truncated":false}}`)
		case 1:
			jsonAnswer(w, 200, `{"data":{"logs":["a","b","c"],"truncated":false}}`)
		case 2:
			jsonAnswer(w, 429, `{"message":"Too many requests.","code":"rate_limited"}`)
		case 3:
			jsonAnswer(w, 200, `{"data":{"logs":["b","c","d"],"truncated":false}}`)
		default:
			cancel()
			jsonAnswer(w, 200, `{"data":{"logs":["b","c","d"],"truncated":false}}`)
		}
	})

	r := h.runContext(ctx, "", "logs", "-f", "--app", "7")
	r.expect(t, ExitOK)
	if r.stdout != "a\nb\nc\nd\n" {
		t.Fatalf("printed %q", r.stdout)
	}
}

func TestLogsLinesAreBounded(t *testing.T) {
	h := newHarness(t)
	h.run("", "logs", "-n", "5000", "--app", "7").expect(t, ExitUsage, "between 1 and 2000")
}

func TestStatusCombinesTheService(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services/7", 200, `{"data":{"id":7,"name":"shop","type":"container_hosting","type_label":"App","product":"App Starter","status":"active","url":"https://shop.example.com","project":{"id":3,"name":"Acme"},"app":{"deployed":true,"status":"running","stack":"laravel"}}}`)
	h.api.answer("GET /services/7/container", 200, `{"data":{"service_id":7,"status":"running","deployed_at":"2026-11-01T10:00:00+00:00"}}`)
	h.api.answer("GET /services/7/container/deployment", 200, deployment("2026-11-10T12:00:00+00:00", "succeeded", "active"))
	h.run("", "status", "--app", "7").expect(t, ExitOK, "shop (#7)", "App Starter", "laravel", "running (deployed", "Acme (#3)", "succeeded")
}
