package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
