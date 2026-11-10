package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

// errorLogLines is how much of a failed pull's log is printed.
const errorLogLines = 40

func (a *App) deployCmd() *cobra.Command {
	var opts struct {
		replace, noComposer, noMigrations, rebuild, noWait bool
		timeout                                            time.Duration
	}
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Pull the connected Git branch and deploy it",
		Long: `Pull the app's connected Git branch and deploy it, as the portal's Pull
button does: the code is checked again before anything restarts.

It waits until the pull ends and exits non-zero if it failed, so a CI job
fails with it. --no-wait returns once the pull is queued.

--replace deletes /app and clones the repository fresh. It needs a token
with the Destructive permission, and asks first.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			if opts.replace {
				if err := a.confirm(fmt.Sprintf("Replace /app of %s with a fresh clone? Files in /app that are not in the repository are deleted.", t)); err != nil {
					return err
				}
			}

			resp, pull, err := c.StartPull(ctx, t.ID, api.PullOptions{
				ReplaceExisting: opts.replace,
				RunComposer:     !opts.noComposer,
				RunMigrations:   !opts.noMigrations,
				ForceRebuild:    opts.rebuild,
			})
			if err != nil {
				return err
			}
			if opts.noWait {
				if a.flags.json {
					return a.printBody(resp)
				}
				a.out("Pull #%d started for %s. Follow it with: talksasa deploy status", pull.ID, t)
				return nil
			}
			if !a.flags.json {
				a.note("Pull #%d started for %s.", pull.ID, t)
			}
			return a.waitForPull(ctx, c, t, pull.ID, opts.timeout)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.replace, "replace", false, "delete /app and clone the repository fresh (Destructive permission)")
	f.BoolVar(&opts.noComposer, "no-composer", false, "skip composer install")
	f.BoolVar(&opts.noMigrations, "no-migrations", false, "skip database migrations")
	f.BoolVar(&opts.rebuild, "rebuild", false, "rebuild the image even when nothing changed")
	f.BoolVar(&opts.noWait, "no-wait", false, "return once the pull is queued")
	f.DurationVar(&opts.timeout, "timeout", 30*time.Minute, "how long to wait for the pull")

	cmd.AddCommand(a.deployStatusCmd())
	return cmd
}

// waitForPull follows one pull until it ends, printing its steps.
func (a *App) waitForPull(ctx context.Context, c *api.Client, t target, pullID int, timeout time.Duration) error {
	steps := newStepPrinter()
	var last *api.Response
	var final api.Pull

	err := a.follow(ctx, timeout, "pull", func(ctx context.Context) error {
		resp, status, err := c.PullStatus(ctx, t.ID)
		if err != nil {
			return err
		}
		if status.Pull == nil {
			return errStillRunning
		}
		if status.Pull.ID != pullID {
			return withExit(ExitConflict, fmt.Errorf("pull #%d was replaced by a newer pull (#%d); follow it with: talksasa deploy status", pullID, status.Pull.ID))
		}
		last, final = resp, *status.Pull
		if !a.flags.json {
			steps.print(a, final.Steps)
		}
		if final.IsActive {
			return errStillRunning
		}
		return nil
	})
	if err != nil {
		return err
	}

	if a.flags.json {
		if perr := a.printBody(last); perr != nil {
			return perr
		}
	}
	if final.Status == "completed" {
		if !a.flags.json {
			commit := ""
			if final.Commit != nil && *final.Commit != "" {
				commit = " at " + shortCommit(*final.Commit)
			}
			a.out("Deployed %s%s.", t, commit)
		}
		return nil
	}

	if !a.flags.json {
		a.printPullFailure(final)
	}
	return withExit(ExitFailed, fmt.Errorf("the pull %s", final.Status))
}

func (a *App) printPullFailure(p api.Pull) {
	if p.Error != nil {
		a.note("")
		a.note("%s", p.Error.Title)
		if p.Error.Guidance != "" {
			a.note("%s", p.Error.Guidance)
		}
		if p.Error.Details != "" {
			a.note("")
			a.note("%s", p.Error.Details)
		}
	}
	if n := len(p.LogTail); n > 0 {
		from := 0
		if n > errorLogLines {
			from = n - errorLogLines
		}
		a.note("")
		a.note("Last %d lines of the pull's log:", n-from)
		for _, line := range p.LogTail[from:] {
			a.note("  %s", line)
		}
	}
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func (a *App) deployStatusCmd() *cobra.Command {
	var showLog bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the latest pull: its steps, and what failed",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, status, err := c.PullStatus(ctx, t.ID)
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			repo := str(status.Repository.URL)
			if status.Repository.Branch != nil && *status.Repository.Branch != "" {
				repo += " (" + *status.Repository.Branch + ")"
			}
			a.out("Repository: %s", repo)
			if status.Pull == nil {
				a.out("No pull yet.")
				return nil
			}
			p := status.Pull
			a.out("Pull #%d: %s (started %s, finished %s)", p.ID, p.Status, when(p.StartedAt), when(p.CompletedAt))
			if p.Commit != nil && *p.Commit != "" {
				a.out("Commit: %s", shortCommit(*p.Commit))
			}
			for _, step := range p.Steps {
				line := fmt.Sprintf("  %s %s", stepMark(step.Status), step.Label)
				if step.Message != nil && *step.Message != "" && step.Status == "running" {
					line += ": " + *step.Message
				}
				a.out("%s", line)
			}
			if p.Status != "completed" && !p.IsActive {
				a.printPullFailure(*p)
			} else if showLog && len(p.LogTail) > 0 {
				a.out("")
				a.out("%s", strings.Join(p.LogTail, "\n"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showLog, "log", false, "print the end of the pull's log")
	return cmd
}

func (a *App) redeployCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "redeploy",
		Short: "Redeploy the app on the stack it already has",
		Long: `Redeploy the app on the stack it already has: the same database, the same
files. Changing the stack or resetting the database stays in the portal.

It waits until the redeploy ends and exits non-zero if it failed.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}

			// The attempt shown before queuing: the redeploy is ours once
			// started_at moves past it.
			var before *string
			canFollow := false
			if !noWait {
				_, d, err := c.Deployment(ctx, t.ID)
				if err != nil {
					return err
				}
				before, canFollow = d.StartedAt, d.HasAttempt
			}

			resp, err := c.Redeploy(ctx, t.ID)
			if err != nil {
				return err
			}
			if noWait || !canFollow {
				if a.flags.json {
					return a.printBody(resp)
				}
				a.out("Redeploy queued for %s. Follow it with: talksasa status", t)
				if !noWait {
					a.note("(this platform version cannot report when a redeploy ends, so not waiting)")
				}
				return nil
			}
			if !a.flags.json {
				a.note("Redeploy queued for %s.", t)
			}
			return a.waitForRedeploy(ctx, c, t, before, timeout)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the redeploy is queued")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait for the redeploy")
	return cmd
}

// waitForRedeploy follows the app's deploy attempts until one that started
// after `before` has an outcome. The service status is no guide: it reads
// "active" for a redeploy's first seconds.
func (a *App) waitForRedeploy(ctx context.Context, c *api.Client, t target, before *string, timeout time.Duration) error {
	steps := newStepPrinter()
	var last *api.Response
	var final api.Deployment

	err := a.follow(ctx, timeout, "redeploy", func(ctx context.Context) error {
		resp, d, err := c.Deployment(ctx, t.ID)
		if err != nil {
			return err
		}
		if d.StartedAt == nil || (before != nil && *d.StartedAt == *before) {
			return errStillRunning // not begun yet: still queued
		}
		last, final = resp, d
		if !a.flags.json {
			steps.print(a, d.Steps)
		}
		if d.Outcome == nil {
			return errStillRunning
		}
		return nil
	})
	if err != nil {
		return err
	}

	if a.flags.json {
		if perr := a.printBody(last); perr != nil {
			return perr
		}
	}
	switch *final.Outcome {
	case "succeeded":
		if !a.flags.json {
			a.out("Redeployed %s.", t)
		}
		return nil
	case "awaiting_configuration":
		if !a.flags.json && len(final.MissingVariables) > 0 {
			a.note("Set these, then redeploy: %s", strings.Join(final.MissingVariables, ", "))
		}
		return withExit(ExitFailed, fmt.Errorf("the app needs configuration before it can run: %s", final.Headline))
	case "rolled_back":
		return withExit(ExitFailed, fmt.Errorf("the redeploy failed and the previous release was put back: %s", final.Headline))
	default:
		return withExit(ExitFailed, fmt.Errorf("the redeploy %s: %s", *final.Outcome, final.Headline))
	}
}
