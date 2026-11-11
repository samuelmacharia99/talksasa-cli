// Package cli is the talksasa command.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

// DefaultAPIURL is the platform's API. Release builds may set it with
// -ldflags "-X github.com/samuelmacharia99/talksasa-cli/internal/cli.DefaultAPIURL=...".
var DefaultAPIURL = "https://api.talksasa.com"

// App is one run of the command: its streams, and what it was asked.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	StdinIsTerminal bool
	// StderrIsTerminal turns on progress lines that redraw in place.
	StderrIsTerminal bool
	// ReadPassword reads a line from the terminal without echoing it.
	ReadPassword func() (string, error)
	// Getwd is the folder link files are looked for from.
	Getwd func() (string, error)

	Version string
	// PollInterval is how often --wait and --follow ask again.
	PollInterval time.Duration
	// RetryDelay is the first wait before an upload request is tried
	// again; it doubles each time, up to 30 times itself.
	RetryDelay time.Duration

	in    *bufio.Reader
	flags struct {
		apiURL string
		app    string
		json   bool
		yes    bool
	}
}

// NewApp is an App on the process's own streams.
func NewApp(version string) *App {
	return &App{
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Getwd:        os.Getwd,
		Version:      version,
		PollInterval: 4 * time.Second,
		RetryDelay:   time.Second,
	}
}

func (a *App) userAgent() string {
	return fmt.Sprintf("talksasa-cli/%s (%s/%s)", a.Version, runtime.GOOS, runtime.GOARCH)
}

const rootLong = `Run your Talksasa apps from a terminal or CI: status, logs, deploys
from Git or straight from a folder, environment variables, domains and
backups.

Sign in once with "talksasa login", using a token from Developers in the
portal. In CI, set TALKSASA_TOKEN instead; nothing is stored then.

Which app a command acts on: --app (an id or a name), then TALKSASA_APP,
then the .talksasa.json that "talksasa link" writes in a project folder.

Exit codes:
  0   done
  1   the action ran and failed (a deploy, a restore), or was cancelled
  2   the command line was wrong
  3   not signed in, or the token is wrong, expired or revoked
  4   the token lacks a permission, or the account or app is suspended
  5   no such app, backup or restore
  6   something else is running; wait and try again
  7   the input was refused, or the app's stack cannot do this
  8   too many requests; wait and try again
  9   the platform or the app's host did not answer
  10  still running when --timeout ran out
  130 interrupted`

// Command builds the command tree.
func (a *App) Command() *cobra.Command {
	root := &cobra.Command{
		Use:           "talksasa",
		Short:         "Run your Talksasa apps from a terminal or CI",
		Long:          rootLong,
		Version:       a.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{msg: err.Error()} })
	root.SetVersionTemplate("talksasa {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&a.flags.apiURL, "api-url", "", "API address (default: the one chosen at login, else "+DefaultAPIURL+"; env TALKSASA_API_URL)")
	pf.StringVar(&a.flags.app, "app", "", "app id or name (env TALKSASA_APP; default: the linked app)")
	pf.BoolVar(&a.flags.json, "json", false, "print the API's answer as JSON")
	pf.BoolVarP(&a.flags.yes, "yes", "y", false, "answer yes to confirmations")

	root.AddGroup(
		&cobra.Group{ID: "account", Title: "Account:"},
		&cobra.Group{ID: "apps", Title: "Apps:"},
		&cobra.Group{ID: "ship", Title: "Deploys:"},
		&cobra.Group{ID: "config", Title: "Configuration:"},
	)

	for _, c := range []*cobra.Command{a.loginCmd(), a.logoutCmd(), a.whoamiCmd()} {
		c.GroupID = "account"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{a.appsCmd(), a.linkCmd(), a.unlinkCmd(), a.statusCmd(), a.logsCmd(), a.metricsCmd(),
		a.powerCmd("start", "Start the app", false), a.powerCmd("restart", "Restart the app", false), a.powerCmd("stop", "Stop the app (asks first)", true)} {
		c.GroupID = "apps"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{a.deployCmd(), a.redeployCmd(), a.releasesCmd(), a.backupsCmd()} {
		c.GroupID = "ship"
		root.AddCommand(c)
	}
	for _, c := range []*cobra.Command{a.envCmd(), a.domainsCmd()} {
		c.GroupID = "config"
		root.AddCommand(c)
	}
	root.AddCommand(a.versionCmd())

	return root
}

// Run executes the command line and returns the process exit code.
func (a *App) Run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.RunContext(ctx, args)
}

// RunContext is Run with a caller's context (tests).
func (a *App) RunContext(ctx context.Context, args []string) int {
	root := a.Command()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code := ExitCode(err)
	a.reportError(err, code)
	return code
}

// reportError prints a failure on stderr: plainly, or as JSON with --json.
func (a *App) reportError(err error, code int) {
	var apiErr *api.Error
	hasAPI := errors.As(err, &apiErr)

	if a.flags.json {
		payload := map[string]any{"message": err.Error(), "exit_code": code}
		if hasAPI {
			payload["code"] = apiErr.Code
			payload["status"] = apiErr.Status
			if len(apiErr.Errors) > 0 {
				payload["errors"] = apiErr.Errors
			}
			if len(apiErr.RequiredAbilities) > 0 {
				payload["required_abilities"] = apiErr.RequiredAbilities
			}
			for k, v := range apiErr.Extra {
				payload[k] = v
			}
		}
		raw, _ := json.Marshal(map[string]any{"error": payload})
		fmt.Fprintln(a.Stderr, string(raw))
		return
	}

	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(a.Stderr, "Interrupted.")
		return
	}

	fmt.Fprintln(a.Stderr, "Error: "+err.Error())
	if !hasAPI {
		var ue *usageError
		if errors.As(err, &ue) || isCobraUsage(err) {
			fmt.Fprintln(a.Stderr, "Run talksasa --help for usage.")
		}
		return
	}

	// The message already says which field when there is one; list the
	// others so a multi-field refusal is complete.
	if len(apiErr.Errors) > 1 {
		fields := make([]string, 0, len(apiErr.Errors))
		for field := range apiErr.Errors {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			fmt.Fprintf(a.Stderr, "  %s: %s\n", field, strings.Join(apiErr.Errors[field], " "))
		}
	}
	if apiErr.Code == "unauthenticated" {
		fmt.Fprintln(a.Stderr, "Sign in with: talksasa login")
	}
	if apiErr.Code == "rate_limited" && apiErr.RetryAfter > 0 {
		fmt.Fprintf(a.Stderr, "Try again in %s.\n", apiErr.RetryAfter.Round(time.Second))
	}
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI's version",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			if a.flags.json {
				return a.printJSON(map[string]string{"version": a.Version, "os": runtime.GOOS, "arch": runtime.GOARCH})
			}
			a.out("talksasa %s (%s/%s)", a.Version, runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

// Argument checks that fail as usage errors (exit 2).
func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{msg: fmt.Sprintf("unexpected argument %q", args[0])}
	}
	return nil
}

func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return &usageError{msg: "expected " + what}
		}
		return nil
	}
}

func maxArgs(n int, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > n {
			return &usageError{msg: "expected at most " + what}
		}
		return nil
	}
}

func minArgs(n int, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < n {
			return &usageError{msg: "expected " + what}
		}
		return nil
	}
}
