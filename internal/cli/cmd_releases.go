package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

func (a *App) releasesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "releases",
		Aliases: []string{"release"},
		Short:   "List the app's uploaded releases, and deploy one again",
		Long: `List the releases "talksasa deploy --dir" uploaded, newest first. The
platform keeps the last few it deployed; "releases deploy N" deploys one of
those again, such as to roll back.`,
		Args: noArgs,
		RunE: a.listReleases,
	}
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List the app's uploaded releases", Args: noArgs, RunE: a.listReleases}
	cmd.AddCommand(list, a.releaseDeployCmd())
	return cmd
}

func (a *App) listReleases(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	c, t, err := a.appClient(ctx)
	if err != nil {
		return err
	}
	resp, rel, err := c.Releases(ctx, t.ID)
	if err != nil {
		return err
	}
	if a.flags.json {
		return a.printBody(resp)
	}
	if len(rel.Releases) == 0 {
		a.out("%s has no uploaded releases. Upload one with: talksasa deploy --dir .", t)
		if rel.CodeSource != nil && *rel.CodeSource == api.SourceGit {
			a.out("It deploys from its Git repository.")
		}
		return nil
	}
	w := a.table()
	row(w, "RELEASE", "LIVE", "STATUS", "SIZE", "FILES", "UPLOADED", "DEPLOYED")
	for _, r := range rel.Releases {
		number, live, files := "-", "", "-"
		if r.Number != nil {
			number = strconv.Itoa(*r.Number)
		}
		if r.IsLive {
			live = "live"
		}
		if r.FileCount != nil {
			files = strconv.Itoa(*r.FileCount)
		}
		status := r.Status
		if !r.Kept {
			status += " (not kept)"
		}
		row(w, number, live, status, bytesHuman(r.SizeBytes), files, when(r.UploadedAt), when(r.LastDeployedAt))
	}
	return w.Flush()
}

func (a *App) releaseDeployCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "deploy <number>",
		Short: "Deploy a kept release again (asks first)",
		Long: `Deploy a release the platform still keeps, by its number in
"talksasa releases". It runs through the same pipeline as any deploy and
needs a token with the Deploy permission.

It waits until the deploy ends and exits non-zero if it failed.`,
		Args: exactArgs(1, "a release number, such as: talksasa releases deploy 3"),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
			if err != nil || number <= 0 {
				return &usageError{msg: fmt.Sprintf("%q is not a release number; list them with: talksasa releases", args[0])}
			}
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			_, rel, err := c.Releases(ctx, t.ID)
			if err != nil {
				return err
			}
			var chosen *api.Release
			for i := range rel.Releases {
				if r := &rel.Releases[i]; r.Number != nil && *r.Number == number {
					chosen = r
					break
				}
			}
			if chosen == nil {
				return withExit(ExitNotFound, fmt.Errorf("%s has no release %d. List them with: talksasa releases", t, number))
			}
			if !chosen.Kept {
				return withExit(ExitConflict, fmt.Errorf("release %d is no longer kept on the platform (%s); upload the code again with: talksasa deploy --dir .", number, chosen.Status))
			}

			question := fmt.Sprintf("Deploy release %d to %s? It replaces the code the app runs now.", number, t)
			if chosen.IsLive {
				question = fmt.Sprintf("Release %d is already live on %s. Deploy it again?", number, t)
			}
			if err := a.confirm(question); err != nil {
				return err
			}

			resp, pull, err := c.DeployRelease(ctx, t.ID, chosen.ID)
			if err != nil {
				return err
			}
			if noWait {
				if a.flags.json {
					return a.printBody(resp)
				}
				a.out("Release %d is deploying. Follow it with: talksasa deploy status", number)
				return nil
			}
			if !a.flags.json {
				a.note("Release %d is deploying to %s (pull #%d).", number, t, pull.ID)
			}
			return a.waitForPull(ctx, c, t, pull.ID, timeout)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the deploy is queued")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait for the deploy")
	return cmd
}
