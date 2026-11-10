package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/logtail"
)

// MaxLogLines is the most lines the API returns (ContainerLogTail::MAX_LINES).
const MaxLogLines = 2000

func (a *App) logsCmd() *cobra.Command {
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the app's recent logs",
		Long: `Print the app's recent logs.

With --follow, new lines are printed as they arrive until Ctrl-C. The API
returns a log's tail, so a burst larger than --lines between two reads can
lose lines; a note on stderr says when that may have happened.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if lines < 1 || lines > MaxLogLines {
				return &usageError{msg: fmt.Sprintf("--lines must be between 1 and %d", MaxLogLines)}
			}
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, logs, err := c.Logs(ctx, t.ID, lines)
			if err != nil {
				return err
			}
			if a.flags.json && !follow {
				return a.printBody(resp)
			}
			a.printLogLines(logs.Logs)
			if !follow {
				if logs.Truncated {
					a.note("(older lines were cut; ask for more with --lines, up to %d)", MaxLogLines)
				}
				return nil
			}
			return a.followLogs(ctx, c, t, lines, logs.Logs)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, fmt.Sprintf("how many recent lines (1-%d)", MaxLogLines))
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines until Ctrl-C")
	return cmd
}

func (a *App) printLogLines(lines []string) {
	if a.flags.json {
		for _, line := range lines {
			_ = a.printJSONLine(map[string]string{"line": line})
		}
		return
	}
	for _, line := range lines {
		fmt.Fprintln(a.Stdout, line)
	}
}

// followLogs reads the tail again and again, printing what is new. Reads
// overlap generously (at least 200 lines) so a quiet gap does not lose lines.
func (a *App) followLogs(ctx context.Context, c *api.Client, t target, lines int, prev []string) error {
	window := int(math.Max(float64(lines), 200))
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil // Ctrl-C ends a follow; that is not a failure
		case <-time.After(a.PollInterval):
		}

		_, logs, err := c.Logs(ctx, t.ID, window)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isTransient(err) && failures < 10 {
				failures++
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(apiErr.RetryAfter):
					}
				}
				continue
			}
			return err
		}
		failures = 0

		fresh, gap := logtail.New(prev, logs.Logs)
		if gap && len(prev) > 0 {
			a.note("(the log moved on faster than it was read, or the app restarted: lines may be missing)")
		}
		a.printLogLines(fresh)
		prev = logs.Logs
	}
}

func (a *App) metricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Show the app's latest CPU, memory and network sample",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			resp, m, err := c.Metrics(ctx, t.ID)
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printBody(resp)
			}
			if m.SampledAt == nil {
				a.out("No sample recorded for %s yet.", t)
				return nil
			}
			w := a.table()
			row(w, "CPU:", fmt.Sprintf("%.1f%%", m.CPUPercent))
			row(w, "Memory:", fmt.Sprintf("%.0f MB (%.1f%%)", m.MemoryMB, m.MemoryPercent))
			row(w, "Network in:", bytesHuman(m.NetworkInBytes))
			row(w, "Network out:", bytesHuman(m.NetworkOutBytes))
			row(w, "Sampled:", when(m.SampledAt))
			return w.Flush()
		},
	}
}

func (a *App) powerCmd(action, short string, destructive bool) *cobra.Command {
	return &cobra.Command{
		Use:   action,
		Short: short,
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, t, err := a.appClient(ctx)
			if err != nil {
				return err
			}
			if destructive {
				if err := a.confirm(fmt.Sprintf("Stop %s? It goes offline until started again.", t)); err != nil {
					return err
				}
			}
			resp, err := c.Power(ctx, t.ID, action)
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
