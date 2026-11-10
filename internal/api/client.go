// Package api is a small client for the Talksasa customer API (/api/v1).
//
// Every answer is {"data": ..., "message": ...} on success and
// {"message": ..., "code": ...} on failure. A caller branches on Error.Code,
// never on Error.Message, which the server may reword.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxResponseBytes caps what is read from one answer. The largest the API
// sends is a 2,000-line log tail.
const MaxResponseBytes = 16 << 20

// Client calls one API base URL with one token.
type Client struct {
	// BaseURL ends in /api/v1, without a trailing slash (see NormalizeBaseURL).
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// New returns a client with redirects refused: a redirect to another host
// must never carry the token, and one to the same host means --api-url is
// not the address the API answers on.
func New(baseURL, token, userAgent string) *Client {
	return &Client{
		BaseURL:   baseURL,
		Token:     token,
		UserAgent: userAgent,
		HTTP: &http.Client{
			Timeout: 90 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Response is a successful answer: its message (actions) and its raw data.
type Response struct {
	Status  int
	Message string
	Data    json.RawMessage
	// Body is the whole answer, for --json output.
	Body json.RawMessage
}

// Error is a failure the API answered with.
type Error struct {
	Status            int
	Code              string
	Message           string
	Errors            map[string][]string
	RequiredAbilities []string
	RetryAfter        time.Duration
	// Fields beyond the standard shape, such as saved/applied on a failed
	// environment apply.
	Extra map[string]json.RawMessage
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("the API answered HTTP %d", e.Status)
}

// Bool reads a boolean from the answer's extra fields.
func (e *Error) Bool(key string) bool {
	var v bool
	if raw, ok := e.Extra[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// NetworkError is a failure to reach the API at all.
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string {
	var dnsErr *net.DNSError
	if errors.As(e.Err, &dnsErr) {
		return fmt.Sprintf("could not reach %s: %s does not resolve", hostOf(e.URL), dnsErr.Name)
	}
	if errors.Is(e.Err, context.DeadlineExceeded) || isTimeout(e.Err) {
		return fmt.Sprintf("could not reach %s: it did not answer in time", hostOf(e.URL))
	}
	return fmt.Sprintf("could not reach %s: %v", hostOf(e.URL), e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// Get calls GET path (relative to the base URL) with an optional query.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, http.MethodGet, path, query, nil)
}

// Do calls the API. body, when not nil, is sent as JSON.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	target := c.BaseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &NetworkError{URL: target, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, &NetworkError{URL: target, Err: err}
	}
	if len(raw) > MaxResponseBytes {
		return nil, &Error{Status: resp.StatusCode, Code: "response_too_large", Message: "The API's answer was larger than this CLI accepts."}
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, &Error{
			Status: resp.StatusCode,
			Code:   "redirected",
			Message: fmt.Sprintf("%s answered with a redirect to %s. Set --api-url to the address the API answers on.",
				hostOf(target), redactLocation(resp.Header.Get("Location"))),
		}
	}

	var envelope struct {
		Message           string              `json:"message"`
		Code              string              `json:"code"`
		Data              json.RawMessage     `json:"data"`
		Errors            map[string][]string `json:"errors"`
		RequiredAbilities []string            `json:"required_abilities"`
	}
	// The answer is its first JSON value. Anything after it is not the API's:
	// work a server runs after answering can print there when the server
	// has not closed the response yet (PHP's development server does this).
	var first json.RawMessage
	isJSON := strings.Contains(resp.Header.Get("Content-Type"), "json") &&
		json.NewDecoder(bytes.NewReader(raw)).Decode(&first) == nil &&
		json.Unmarshal(first, &envelope) == nil
	if isJSON {
		raw = first
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if !isJSON {
			return nil, &Error{
				Status:  resp.StatusCode,
				Code:    "unexpected_response",
				Message: fmt.Sprintf("%s answered, but not as the Talksasa API. Check --api-url.", hostOf(target)),
			}
		}
		return &Response{Status: resp.StatusCode, Message: envelope.Message, Data: envelope.Data, Body: raw}, nil
	}

	apiErr := &Error{
		Status:            resp.StatusCode,
		Code:              envelope.Code,
		Message:           envelope.Message,
		Errors:            envelope.Errors,
		RequiredAbilities: envelope.RequiredAbilities,
		RetryAfter:        parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	if isJSON {
		var all map[string]json.RawMessage
		if json.Unmarshal(raw, &all) == nil {
			for _, k := range []string{"message", "code", "errors", "required_abilities"} {
				delete(all, k)
			}
			if len(all) > 0 {
				apiErr.Extra = all
			}
		}
	}
	if apiErr.Code == "" {
		// Not the API's own answer: a proxy's error page, or a wrong address.
		apiErr.Code = fallbackCode(resp.StatusCode)
		apiErr.Message = fmt.Sprintf("%s answered HTTP %d without an API error. %s",
			hostOf(target), resp.StatusCode, fallbackHint(resp.StatusCode))
	}
	return nil, apiErr
}

func fallbackCode(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "unauthenticated"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "server_error"
	default:
		return "http_" + strconv.Itoa(status)
	}
}

func fallbackHint(status int) string {
	switch {
	case status == http.StatusNotFound:
		return "Check --api-url."
	case status >= 500:
		return "Try again in a minute."
	default:
		return ""
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// redactLocation drops a redirect target's query and credentials: it is
// printed, and a query can carry a signature.
func redactLocation(location string) string {
	u, err := url.Parse(location)
	if err != nil {
		return "another address"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
