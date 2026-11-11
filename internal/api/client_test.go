package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"https://api.talksasa.com":           "https://api.talksasa.com/api/v1",
		"api.talksasa.com":                   "https://api.talksasa.com/api/v1",
		"https://api.talksasa.com/":          "https://api.talksasa.com/api/v1",
		"https://api.talksasa.com/api/v1/":   "https://api.talksasa.com/api/v1",
		"https://example.com:8443/cloud":     "https://example.com:8443/cloud/api/v1",
		"http://localhost:8000":              "http://localhost:8000/api/v1",
		"http://127.0.0.1:8011/api/v1":       "http://127.0.0.1:8011/api/v1",
		"http://[::1]:8000":                  "http://[::1]:8000/api/v1",
		"  https://api.talksasa.com/api/v1 ": "https://api.talksasa.com/api/v1",
	}
	for in, want := range ok {
		got, err := NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	for _, bad := range []string{
		"",
		"http://api.talksasa.com",          // plain http off this machine
		"http://10.0.0.5:8000",             // a private address is still the network
		"ftp://api.talksasa.com",           // not http
		"https://user:pw@api.talksasa.com", // credentials in the address
		"https://api.talksasa.com/?x=1",
		"https://",
	} {
		if got, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("NormalizeBaseURL(%q) = %q; want an error", bad, got)
		}
	}
}

func TestErrorAnswerIsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tsk_abc" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.Header.Get("User-Agent") != "talksasa-cli/test" {
			t.Errorf("user agent: %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"The change was saved, but applying it failed.","code":"upstream_unavailable","saved":true,"applied":false}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1", "tsk_abc", "talksasa-cli/test")
	_, err := c.Get(context.Background(), "services/1/container/env", nil)

	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if apiErr.Code != "upstream_unavailable" || apiErr.Status != 503 || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("parsed %+v", apiErr)
	}
	if !apiErr.Bool("saved") || apiErr.Bool("applied") {
		t.Fatalf("extra fields: %v", apiErr.Extra)
	}
}

func TestMissingAbilityAndValidationFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"DB_PASSWORD is managed by the platform.","code":"validation_failed","errors":{"variables":["DB_PASSWORD is managed by the platform."]}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", "").Do(context.Background(), http.MethodPut, "x", nil, map[string]any{"variables": map[string]string{"DB_PASSWORD": "x"}})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "validation_failed" || len(apiErr.Errors["variables"]) != 1 {
		t.Fatalf("got %#v", err)
	}
}

func TestAProxyErrorPageIsNotTakenForTheAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", "").Get(context.Background(), "me", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "server_error" || apiErr.Status != 502 {
		t.Fatalf("got %#v", err)
	}
}

func TestAWebPageAnsweringOKIsNotTheAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>Welcome</html>"))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", "").Get(context.Background(), "me", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unexpected_response" {
		t.Fatalf("got %#v", err)
	}
}

// A redirect is never followed: the token must not travel to wherever it
// points, and a same-host redirect means the address is wrong anyway.
func TestRedirectsAreRefusedNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed, with Authorization %q", r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal?sig=secret", http.StatusFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tsk_secret", "").Get(context.Background(), "me", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "redirected" {
		t.Fatalf("got %#v", err)
	}
	if contains(apiErr.Message, "sig=secret") {
		t.Fatalf("the redirect's query was printed: %s", apiErr.Message)
	}
}

func TestUnreachableIsANetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := New(url, "t", "").Get(context.Background(), "me", nil)
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("got %#v", err)
	}
}

func TestDeploymentKnowsWhetherTheServerReportsAttempts(t *testing.T) {
	body := `{"data":{"service_id":1,"started_at":null,"outcome":null,"is_ready":true}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "")

	_, d, err := c.Deployment(context.Background(), 1)
	if err != nil || !d.HasAttempt {
		t.Fatalf("with the fields: %+v %v", d, err)
	}

	body = `{"data":{"service_id":1,"is_ready":true}}`
	_, d, err = c.Deployment(context.Background(), 1)
	if err != nil || d.HasAttempt {
		t.Fatalf("an older server: %+v %v", d, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestOnlyTheFirstJSONValueIsTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message":"Git pull started.","data":{"id":2}}{"message":"Something went wrong.","code":"server_error"}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "t", "").Do(context.Background(), http.MethodPost, "pull", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "Git pull started." || string(resp.Data) != `{"id":2}` || contains(string(resp.Body), "server_error") {
		t.Fatalf("got %+v", resp)
	}
}

func TestDoRawSendsTheBodyAsIs(t *testing.T) {
	payload := []byte("\x1f\x8b\x08 raw bytes, not JSON")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/services/7/container/uploads/3/chunk" || r.URL.Query().Get("offset") != "4096" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength != int64(len(payload)) {
			t.Errorf("content type %q, length %d", r.Header.Get("Content-Type"), r.ContentLength)
		}
		if r.Header.Get("Authorization") != "Bearer tsk_abc" || r.Header.Get("Accept") != "application/json" || len(r.TransferEncoding) != 0 {
			t.Errorf("headers %v, transfer encoding %v", r.Header, r.TransferEncoding)
		}
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, payload) {
			t.Errorf("body %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Chunk received.","data":{"received_bytes":4128,"size_bytes":9000,"complete":false}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1", "tsk_abc", "talksasa-cli/test")
	// A section of a larger reader, as the CLI sends chunks of a file.
	file := bytes.NewReader(append(append([]byte("before"), payload...), "after"...))
	_, chunk, err := c.UploadChunk(context.Background(), 7, 3, 4096, io.NewSectionReader(file, 6, int64(len(payload))), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if chunk.ReceivedBytes != 4128 || chunk.SizeBytes != 9000 || chunk.Complete {
		t.Fatalf("chunk %+v", chunk)
	}
}

func TestDoRawErrorsCarryTheirExtraFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Chunk out of order: the platform has 8192 bytes.","code":"conflict","received_bytes":8192,"status":"uploading"}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1", "tsk_abc", "talksasa-cli/test")
	_, err := c.DoRaw(context.Background(), http.MethodPut, "x", nil, strings.NewReader("abc"), 3, "application/octet-stream")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "conflict" || apiErr.Status != 409 {
		t.Fatalf("got %v", err)
	}
	if n, ok := apiErr.Int64("received_bytes"); !ok || n != 8192 {
		t.Fatalf("received_bytes %d %v", n, ok)
	}
	if apiErr.Text("status") != "uploading" || apiErr.Text("missing") != "" {
		t.Fatalf("status %q", apiErr.Text("status"))
	}
	if _, ok := apiErr.Int64("status"); ok {
		t.Fatal("a string read as a number")
	}
}

func TestDoRawIsNotCutByTheClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"received_bytes":3,"size_bytes":3,"complete":true}}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1", "tsk_abc", "talksasa-cli/test")
	c.HTTP.Timeout = 30 * time.Millisecond

	// The usual calls stop at the client's timeout...
	if _, err := c.Get(context.Background(), "slow", nil); err == nil {
		t.Fatal("Get outlived the client timeout")
	}
	// ...a raw one gets its own, longer, deadline.
	if _, err := c.DoRaw(context.Background(), http.MethodPut, "slow", nil, strings.NewReader("abc"), 3, "application/octet-stream"); err != nil {
		t.Fatalf("DoRaw was cut short: %v", err)
	}
	if c.HTTP.Timeout != 30*time.Millisecond {
		t.Fatal("DoRaw changed the shared client")
	}

	// Running out of the raw deadline is a network failure, worth a retry...
	c.RawTimeout = 30 * time.Millisecond
	_, err := c.DoRaw(context.Background(), http.MethodPut, "slow", nil, strings.NewReader("abc"), 3, "application/octet-stream")
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("want a NetworkError, got %v", err)
	}

	// ...while the caller cancelling is reported as such.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	c.RawTimeout = 0
	_, err = c.DoRaw(ctx, http.MethodPut, "slow", nil, strings.NewReader("abc"), 3, "application/octet-stream")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestUploadCallsUseTheDocumentedPaths(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/v1")+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_, _ = w.Write([]byte(`{"data":{"code_source":"upload","releases":[{"id":12,"number":3,"status":"deployed","is_live":true,"kept":true,"sha256":"ab","size_bytes":10}]}}`))
		case strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":41,"status":"pending","is_active":true,"source":"upload","release":{"id":12,"number":3}}}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":5,"status":"uploading","size_bytes":10,"received_bytes":0,"sha256":"ab","chunk_bytes":4194304,"release_number":null,"expires_at":null}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api/v1", "tsk_abc", "talksasa-cli/test")
	ctx := context.Background()

	unpacked, files := int64(20), 2
	resp, up, err := c.CreateUpload(ctx, 7, NewUpload{SizeBytes: 10, SHA256: "ab", UnpackedBytes: &unpacked, FileCount: &files})
	if err != nil || resp.Status != 201 || up.ID != 5 || up.ChunkBytes != 4194304 {
		t.Fatalf("create: %+v %v", up, err)
	}
	if _, _, err := c.GetUpload(ctx, 7, 5); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CancelUpload(ctx, 7, 5); err != nil {
		t.Fatal(err)
	}
	_, pull, err := c.DeployUpload(ctx, 7, 5, UploadDeployOptions{RunComposer: true, RunMigrations: true})
	if err != nil || pull.Source != SourceUpload || pull.Release == nil || *pull.Release.Number != 3 {
		t.Fatalf("deploy: %+v %v", pull, err)
	}
	_, rel, err := c.Releases(ctx, 7)
	if err != nil || *rel.CodeSource != SourceUpload || !rel.Releases[0].IsLive {
		t.Fatalf("releases: %+v %v", rel, err)
	}
	if _, _, err := c.DeployRelease(ctx, 7, 12); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`POST /services/7/container/uploads {"size_bytes":10,"sha256":"ab","unpacked_bytes":20,"file_count":2}`,
		`GET /services/7/container/uploads/5 `,
		`DELETE /services/7/container/uploads/5 `,
		`POST /services/7/container/uploads/5/deploy {"run_composer":true,"run_migrations":true,"force_rebuild":false,"replace_git":false}`,
		`GET /services/7/container/releases `,
		`POST /services/7/container/releases/12/deploy `,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}
