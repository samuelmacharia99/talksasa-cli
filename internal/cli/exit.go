package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

// Exit codes. Scripts branch on these, so they never change meaning.
const (
	ExitOK          = 0
	ExitFailed      = 1  // the action ran and failed: a deploy, a restore
	ExitUsage       = 2  // the command line was wrong
	ExitAuth        = 3  // no token, or one that is wrong, expired or revoked
	ExitPermission  = 4  // the token, account or service may not do this
	ExitNotFound    = 5  // no such app, backup or restore
	ExitConflict    = 6  // not now: something else is running
	ExitInvalid     = 7  // the API refused the input, or the stack cannot do it
	ExitRateLimited = 8  // too many requests
	ExitUnavailable = 9  // the platform or the app's host did not answer
	ExitTimeout     = 10 // still running when --timeout ran out
	ExitInterrupted = 130
)

// codeExits maps every documented API error code (ApiError::documentedCodes
// in the platform) to an exit code. TestEveryDocumentedCodeHasAnExit and the
// e2e check keep the two lists in step.
var codeExits = map[string]int{
	"unauthenticated":       ExitAuth,
	"token_missing_ability": ExitPermission,
	"forbidden":             ExitPermission,
	"account_inactive":      ExitPermission,
	"service_suspended":     ExitPermission,
	"staff_setup_required":  ExitPermission,
	"not_found":             ExitNotFound,
	"conflict":              ExitConflict,
	"git_connected":         ExitConflict,
	"validation_failed":     ExitInvalid,
	"unsupported":           ExitInvalid,
	"bundle_rejected":       ExitInvalid,
	"rate_limited":          ExitRateLimited,
	"server_error":          ExitUnavailable,
	"upstream_unavailable":  ExitUnavailable,
}

// exitError carries an exit code with the message printed for it.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withExit(code int, err error) error { return &exitError{code: code, err: err} }

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// ExitCode is the process exit code for an error returned by a command.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if code, ok := codeExits[apiErr.Code]; ok {
			return code
		}
		switch {
		case apiErr.Status >= 500:
			return ExitUnavailable
		case apiErr.Status == 404:
			return ExitNotFound
		}
		return ExitFailed
	}
	var netErr *api.NetworkError
	if errors.As(err, &netErr) {
		return ExitUnavailable
	}
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	if isCobraUsage(err) {
		return ExitUsage
	}
	return ExitFailed
}

// isCobraUsage recognises the errors cobra returns before a command runs.
func isCobraUsage(err error) bool {
	msg := err.Error()
	for _, prefix := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "invalid argument", "flag needs an argument", "required flag", "accepts ", "requires at least", "requires at most"} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

// isTransient reports an error worth retrying while following something.
func isTransient(err error) bool {
	var netErr *api.NetworkError
	if errors.As(err, &netErr) {
		return true
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "rate_limited", "server_error", "upstream_unavailable":
			return true
		}
		return apiErr.Status >= 500
	}
	return false
}
