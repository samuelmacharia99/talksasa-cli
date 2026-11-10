package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/link"
)

func (a *App) appsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "apps",
		Aliases: []string{"services", "ls"},
		Short:   "List your services",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			resp, services, err := c.Services(cmd.Context())
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			if len(services) == 0 {
				a.out("No services yet.")
				return nil
			}
			w := a.table()
			row(w, "ID", "NAME", "TYPE", "STATUS", "APP", "URL")
			for _, s := range services {
				app := "-"
				if s.App != nil {
					app = "not deployed"
					if s.App.Deployed {
						app = str(s.App.Status)
						if s.App.Stack != nil {
							app += " · " + *s.App.Stack
						}
					}
				}
				row(w, strconv.Itoa(s.ID), s.Name, s.TypeLabel, s.Status, app, str(s.URL))
			}
			return w.Flush()
		},
	}
}

func (a *App) linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <app>",
		Short: "Link this folder to an app (writes .talksasa.json)",
		Long: `Link this folder to an app, so commands run here act on it without --app.

The link file holds the app's id and name only, never a token or an
address, and is safe to commit.`,
		Args: exactArgs(1, "an app id or name: talksasa link <app>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			t, err := a.lookupApp(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			_, service, err := c.Service(cmd.Context(), t.ID)
			if err != nil {
				return err
			}
			if !service.IsApp() {
				return withExit(ExitInvalid, fmt.Errorf("%s is %s, not an app", service.Name, service.TypeLabel))
			}

			dir, err := a.Getwd()
			if err != nil {
				return err
			}
			path, err := link.Write(dir, link.Link{ServiceID: service.ID, Name: service.Name})
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printJSON(map[string]any{"service_id": service.ID, "name": service.Name, "path": path})
			}
			a.out("Linked %s to %s (#%d).", dir, service.Name, service.ID)
			return nil
		},
	}
}

func (a *App) unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Remove this folder's .talksasa.json",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.Getwd()
			if err != nil {
				return err
			}
			removed, err := link.Remove(dir)
			if err != nil {
				return err
			}
			if removed {
				a.out("Unlinked %s.", dir)
			} else {
				a.out("%s has no %s.", dir, link.FileName)
			}
			return nil
		},
	}
}

func (a *App) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the app: service, container and latest deploy",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			serviceResp, service, err := c.Service(ctx, t.ID)
			if err != nil {
				return err
			}

			var containerResp, deployResp *api.Response
			var container api.Container
			var deploy api.Deployment
			if service.IsApp() && service.App != nil && service.App.Deployed {
				containerResp, container, err = c.Container(ctx, t.ID)
				if err != nil && !isNotFound(err) {
					return err
				}
				deployResp, deploy, err = c.Deployment(ctx, t.ID)
				if err != nil && !isNotFound(err) {
					return err
				}
			}

			if a.flags.json {
				out := map[string]json.RawMessage{"service": serviceResp.Data, "container": json.RawMessage("null"), "deployment": json.RawMessage("null")}
				if containerResp != nil {
					out["container"] = containerResp.Data
				}
				if deployResp != nil {
					out["deployment"] = deployResp.Data
				}
				return a.printJSON(map[string]any{"data": out})
			}

			heading := fmt.Sprintf("%s (#%d)", service.Name, service.ID)
			if service.Product != nil {
				heading += "  " + *service.Product
			}
			a.out("%s", heading)
			w := a.table()
			row(w, "Service:", service.Status)
			if service.App != nil {
				stack := "-"
				if service.App.Stack != nil {
					stack = *service.App.Stack
				}
				row(w, "Stack:", stack)
				if containerResp != nil {
					row(w, "App:", fmt.Sprintf("%s (deployed %s)", container.Status, when(container.DeployedAt)))
				} else {
					row(w, "App:", "not deployed")
				}
			}
			row(w, "URL:", str(service.URL))
			if service.Project != nil {
				row(w, "Project:", fmt.Sprintf("%s (#%d)", service.Project.Name, service.Project.ID))
			}
			if deployResp != nil {
				last := deploy.Headline
				if deploy.Outcome != nil {
					last = fmt.Sprintf("%s, %s: %s", *deploy.Outcome, when(deploy.FinishedAt), deploy.Headline)
				} else if deploy.IsActive {
					last = fmt.Sprintf("running (%d%%): %s", deploy.Percent, deploy.Headline)
				}
				row(w, "Last deploy:", last)
				if len(deploy.MissingVariables) > 0 {
					row(w, "Needs:", fmt.Sprint(deploy.MissingVariables))
				}
			}
			return w.Flush()
		},
	}
}

func isNotFound(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.Code == "not_found"
}
