package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/dotenv"
)

// MaxEnvBatch is the most variables one request may set (the API's limit).
const MaxEnvBatch = 100

func (a *App) envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Environment variables: list names, set and unset values",
		Long: `The app's environment variables. Values can be written, never read back.

The variables the platform manages (database credentials, the app's own
URLs) are changed only in the portal.`,
	}
	cmd.AddCommand(a.envListCmd(), a.envSetCmd(), a.envUnsetCmd())
	return cmd
}

func (a *App) envListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List variable names and their state (never values)",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, env, err := c.Env(ctx, t.ID)
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			if len(env.Variables) == 0 {
				a.out("%s has no environment variables.", t)
			} else {
				w := a.table()
				row(w, "KEY", "STATE", "HAS VALUE", "SENSITIVE", "MANAGED BY")
				for _, v := range env.Variables {
					managed := "you"
					if v.PlatformManaged {
						managed = "platform"
					}
					row(w, v.Key, v.State, yesNo(v.HasValue), yesNo(v.Sensitive), managed)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
			missing := missingRequired(env)
			if len(missing) > 0 {
				a.out("")
				a.out("The app needs a value for: %s", strings.Join(missing, ", "))
			}
			if len(env.RejectedByApp) > 0 {
				a.out("The app refuses: %s", strings.Join(env.RejectedByApp, ", "))
			}
			return nil
		},
	}
}

// missingRequired is what the app requires that has no value yet.
func missingRequired(env api.Env) []string {
	has := map[string]bool{}
	for _, v := range env.Variables {
		has[v.Key] = v.HasValue
	}
	var out []string
	for _, key := range env.RequiredByApp {
		if !has[key] {
			out = append(out, key)
		}
	}
	return out
}

func (a *App) envSetCmd() *cobra.Command {
	var fromFile string
	var noApply bool
	cmd := &cobra.Command{
		Use:   "set KEY=value... | KEY",
		Short: "Set variables and apply them (the app restarts)",
		Long: `Set one or more variables, then apply them: the app restarts on the new
values. --no-apply saves them for a later apply or redeploy.

A value typed on the command line lands in shell history. For a secret,
give the name alone and the value is asked for without echo, or read from
standard input:
  talksasa env set STRIPE_SECRET
  printf %s "$SECRET" | talksasa env set STRIPE_SECRET
  talksasa env set --from-file .env.production

Names are upper-cased by the platform.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			variables, err := a.collectVariables(args, fromFile)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, change, err := c.SetEnv(ctx, t.ID, variables, !noApply)
			if err != nil {
				return explainSavedNotApplied(err)
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			a.out("%s", envMessage(resp.Message))
			if len(change.Skipped) > 0 {
				a.note("Skipped: %s", strings.Join(change.Skipped, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read KEY=value lines from a file (- for standard input)")
	cmd.Flags().BoolVar(&noApply, "no-apply", false, "save without restarting the app")
	return cmd
}

// collectVariables builds the variables to send from the arguments, a
// file, and at most one value read without echo.
func (a *App) collectVariables(args []string, fromFile string) (map[string]string, error) {
	variables := map[string]string{}
	var prompted []string

	if fromFile != "" {
		var r io.Reader
		if fromFile == "-" {
			r = a.stdin()
		} else {
			f, err := os.Open(fromFile)
			if err != nil {
				return nil, &usageError{msg: err.Error()}
			}
			defer f.Close()
			r = f
		}
		vars, err := dotenv.Parse(r)
		if err != nil {
			return nil, &usageError{msg: fmt.Sprintf("%s: %v", fromFile, err)}
		}
		for _, v := range vars {
			variables[v.Key] = v.Value
		}
	}

	for _, arg := range args {
		key, value, hasValue := strings.Cut(arg, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, &usageError{msg: fmt.Sprintf("%q has no name before =", arg)}
		}
		if hasValue {
			variables[key] = value
			continue
		}
		prompted = append(prompted, key)
	}

	if len(prompted) > 1 {
		return nil, &usageError{msg: "give values for all but one name; only one value can be read without echo"}
	}
	if len(prompted) == 1 {
		if fromFile == "-" {
			return nil, &usageError{msg: "standard input is already the --from-file; give " + prompted[0] + " a value"}
		}
		value, err := a.readSecret(fmt.Sprintf("Value for %s (input hidden): ", prompted[0]), true)
		if err != nil {
			return nil, err
		}
		variables[prompted[0]] = value
	}

	if len(variables) == 0 {
		return nil, &usageError{msg: "nothing to set: give KEY=value, a KEY to be asked for, or --from-file"}
	}
	if len(variables) > MaxEnvBatch {
		return nil, &usageError{msg: fmt.Sprintf("%d variables is more than the %d one request may set; split the file", len(variables), MaxEnvBatch)}
	}
	return variables, nil
}

// envMessage rewords the API's "not applied" hint, which speaks of the
// API's apply field, for someone at the command line.
func envMessage(msg string) string {
	if i := strings.Index(msg, " Not applied yet"); i >= 0 {
		return msg[:i] + " Not applied yet: the app uses it from its next redeploy (talksasa redeploy)."
	}
	return msg
}

// explainSavedNotApplied says plainly what a 503 with saved=true means.
func explainSavedNotApplied(err error) error {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Bool("saved") && !apiErr.Bool("applied") {
		return withExit(ExitUnavailable, fmt.Errorf("saved, but not applied: %s", apiErr.Message))
	}
	return err
}

func (a *App) envUnsetCmd() *cobra.Command {
	var noApply bool
	cmd := &cobra.Command{
		Use:   "unset KEY...",
		Short: "Remove variables and apply (the app restarts)",
		Args:  minArgs(1, "at least one variable name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			seen := map[string]bool{}
			var keys []string
			for _, arg := range args {
				key := strings.ToUpper(strings.TrimSpace(arg))
				if strings.Contains(key, "=") || key == "" {
					return &usageError{msg: fmt.Sprintf("%q is not a variable name", arg)}
				}
				if !seen[key] {
					seen[key] = true
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)

			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, _, err := c.UnsetEnv(ctx, t.ID, keys, !noApply)
			if err != nil {
				return explainSavedNotApplied(err)
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			a.out("%s", envMessage(resp.Message))
			return nil
		},
	}
	cmd.Flags().BoolVar(&noApply, "no-apply", false, "save without restarting the app")
	return cmd
}
