package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

func (a *App) domainsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "domains",
		Short: "List the app's domains and their SSL",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, domains, err := c.Domains(ctx, t.ID)
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			if len(domains) == 0 {
				a.out("%s has no domains.", t)
				return nil
			}
			w := a.table()
			row(w, "DOMAIN", "STATUS", "SSL", "URL")
			for _, d := range domains {
				row(w, d.Domain, d.Status, yesNo(d.SSL), d.URL)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, d := range domains {
				if d.Error == nil {
					continue
				}
				a.out("")
				a.out("%s: %s", d.Domain, d.Error.Title)
				if d.Error.Guidance != "" {
					a.out("  %s", d.Error.Guidance)
				}
			}
			return nil
		},
	}
}

func (a *App) backupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backups",
		Aliases: []string{"backup"},
		Short:   "List, take, restore and delete backups",
		Args:    noArgs,
		RunE:    a.listBackups,
	}
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List the app's backups", Args: noArgs, RunE: a.listBackups}
	cmd.AddCommand(list, a.backupCreateCmd(), a.backupRestoreCmd(), a.backupDeleteCmd())
	return cmd
}

func (a *App) listBackups(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	c, t, err := a.appClient(ctx)
	if err != nil {
		return err
	}
	resp, backups, err := c.Backups(ctx, t.ID)
	if err != nil {
		return err
	}
	if a.flags.json {
		return a.printBody(resp)
	}
	if len(backups) == 0 {
		a.out("%s has no backups.", t)
		return nil
	}
	w := a.table()
	row(w, "ID", "NAME", "STATUS", "TYPE", "SIZE", "CREATED")
	for _, b := range backups {
		size := "-"
		if b.SizeBytes != nil {
			size = bytesHuman(*b.SizeBytes)
		}
		row(w, strconv.Itoa(b.ID), str(b.Name), b.Status, str(b.Type), size, when(b.CreatedAt))
	}
	return w.Flush()
}

func (a *App) backupCreateCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Take a backup now (waits until it is done)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, backup, err := c.CreateBackup(ctx, t.ID)
			if err != nil {
				return err
			}
			if noWait {
				if a.flags.json {
					return a.printBody(resp)
				}
				a.out("Backup #%d queued for %s.", backup.ID, t)
				return nil
			}
			if !a.flags.json {
				a.note("Backup #%d queued for %s.", backup.ID, t)
			}
			return a.waitForBackup(ctx, c, t, backup.ID, timeout)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the backup is queued")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "how long to wait for the backup")
	return cmd
}

func (a *App) waitForBackup(ctx context.Context, c *api.Client, t target, id int, timeout time.Duration) error {
	var final api.Backup
	err := a.follow(ctx, timeout, "backup", func(ctx context.Context) error {
		_, backups, err := c.Backups(ctx, t.ID)
		if err != nil {
			return err
		}
		for _, b := range backups {
			if b.ID != id {
				continue
			}
			final = b
			if b.Finished() {
				return nil
			}
			return errStillRunning
		}
		return withExit(ExitNotFound, fmt.Errorf("backup #%d is no longer listed", id))
	})
	if err != nil {
		return err
	}
	if a.flags.json {
		if perr := a.printJSON(map[string]any{"data": final}); perr != nil {
			return perr
		}
	}
	if final.Status != "completed" {
		return withExit(ExitFailed, fmt.Errorf("backup #%d %s", id, final.Status))
	}
	if !a.flags.json {
		size := ""
		if final.SizeBytes != nil {
			size = " (" + bytesHuman(*final.SizeBytes) + ")"
		}
		a.out("Backup #%d completed%s.", id, size)
	}
	return nil
}

func backupID(arg string) (int, error) {
	id, err := strconv.Atoi(arg)
	if err != nil || id <= 0 {
		return 0, &usageError{msg: fmt.Sprintf("%q is not a backup id; list them with: talksasa backups", arg)}
	}
	return id, nil
}

func (a *App) backupRestoreCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "restore <backup-id>",
		Short: "Restore a backup over the app (Destructive permission; asks first)",
		Args:  exactArgs(1, "a backup id: talksasa backups restore <backup-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := backupID(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Restore backup #%d over %s? Its current files and database are replaced.", id, t)); err != nil {
				return err
			}
			resp, restore, err := c.RestoreBackup(ctx, t.ID, id)
			if err != nil {
				return err
			}
			if noWait {
				if a.flags.json {
					return a.printBody(resp)
				}
				a.out("Restore #%d queued for %s.", restore.ID, t)
				return nil
			}
			if !a.flags.json {
				a.note("Restore #%d queued for %s.", restore.ID, t)
			}

			var last *api.Response
			var final api.Restore
			err = a.follow(ctx, timeout, "restore", func(ctx context.Context) error {
				resp, r, err := c.RestoreStatus(ctx, t.ID, restore.ID)
				if err != nil {
					return err
				}
				last, final = resp, r
				if r.Finished() {
					return nil
				}
				return errStillRunning
			})
			if err != nil {
				return err
			}
			if a.flags.json {
				if perr := a.printBody(last); perr != nil {
					return perr
				}
			}
			if final.Status != "completed" {
				msg := fmt.Sprintf("restore #%d %s", final.ID, final.Status)
				if final.Error != nil {
					msg = *final.Error
				}
				return withExit(ExitFailed, fmt.Errorf("%s", msg))
			}
			if !a.flags.json {
				a.out("Restored backup #%d over %s.", id, t)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the restore is queued")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "how long to wait for the restore")
	return cmd
}

func (a *App) backupDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <backup-id>",
		Short: "Delete a backup (Destructive permission; asks first)",
		Args:  exactArgs(1, "a backup id: talksasa backups delete <backup-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := backupID(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete backup #%d of %s? It cannot be recovered.", id, t)); err != nil {
				return err
			}
			resp, err := c.DeleteBackup(ctx, t.ID, id)
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			a.out("%s", resp.Message)
			return nil
		},
	}
}
