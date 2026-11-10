package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
)

// printJSON writes an API answer (or any value) indented, for --json.
func (a *App) printJSON(v any) error {
	var raw []byte
	switch value := v.(type) {
	case json.RawMessage:
		raw = value
	case []byte:
		raw = value
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = encoded
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := a.Stdout.Write(out.Bytes())
	return err
}

// printBody prints a whole answer ({"data", "message"}) for --json.
func (a *App) printBody(resp *api.Response) error { return a.printJSON(resp.Body) }

func (a *App) out(format string, args ...any) { fmt.Fprintf(a.Stdout, format+"\n", args...) }

// note goes to stderr, so stdout stays what a script reads.
func (a *App) note(format string, args ...any) { fmt.Fprintf(a.Stderr, format+"\n", args...) }

func (a *App) table() *tabwriter.Writer { return tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0) }

func row(w io.Writer, cells ...string) { fmt.Fprintln(w, strings.Join(cells, "\t")) }

// when formats an API timestamp in local time; "-" when absent.
func when(ts *string) string {
	if ts == nil || *ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, *ts)
	if err != nil {
		return *ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

func str(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func bytesHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// stepMark is the prefix of a progress step.
func stepMark(status string) string {
	switch status {
	case "completed", "succeeded", "done", "skipped":
		return "✓"
	case "failed", "error":
		return "✗"
	case "running", "in_progress", "active":
		return "…"
	default:
		return "·"
	}
}

// stepPrinter prints each step when it first appears or its status changes.
type stepPrinter struct {
	seen map[string]string
}

func newStepPrinter() *stepPrinter { return &stepPrinter{seen: map[string]string{}} }

func (p *stepPrinter) print(a *App, steps []api.Step) {
	for _, step := range steps {
		if step.Status == "pending" {
			continue
		}
		if p.seen[step.Key] == step.Status {
			continue
		}
		p.seen[step.Key] = step.Status
		line := fmt.Sprintf("  %s %s", stepMark(step.Status), step.Label)
		if step.Message != nil && *step.Message != "" && step.Status == "running" {
			line += ": " + *step.Message
		}
		a.note("%s", line)
	}
}

// printJSONLine writes one compact JSON value per line (NDJSON), for
// streams such as logs --follow --json.
func (a *App) printJSONLine(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, string(raw))
	return err
}
