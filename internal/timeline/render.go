package timeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func RenderJSONL(w io.Writer, t Timeline) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, e := range t.Events {
		if e.Error != nil {
			// Synthetic errors have no source event, line, session or timestamp.
			// Keep their JSONL schema distinct instead of exporting dummy values.
			if err := enc.Encode(struct {
				Type  string                  `json:"type"`
				Role  string                  `json:"role"`
				Error *MissingCheckpointError `json:"error"`
			}{Type: "timeline.error", Role: "system", Error: e.Error}); err != nil {
				return err
			}
			continue
		}
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

type TextOptions struct {
	IncludeTools bool
	Width        int
}

func RenderText(w io.Writer, t Timeline, opts TextOptions) error {
	width := opts.Width
	if width <= 0 {
		width = 80
	}
	if width > 100 {
		width = 100
	}
	if width < 1 {
		width = 1
	}
	separator := strings.Repeat("─", width)
	// Buffer once so every write error, including a closed pipe, is returned.
	var b bytes.Buffer
	fmt.Fprintf(&b, "Entire Timeline\nRange: %s\nCheckpoints: %d\nSessions: %d\n", t.Range, t.Checkpoints, len(t.Sessions))
	if t.MissingCheckpoints > 0 {
		fmt.Fprintf(&b, "Incomplete: %d checkpoint(s) unavailable\n", t.MissingCheckpoints)
	}
	if t.UnattributedEvents > 0 {
		fmt.Fprintf(&b, "Unattributed context: %d event(s)\n", t.UnattributedEvents)
	}
	labels := map[string]string{}
	for i, id := range t.Sessions {
		labels[id] = fmt.Sprintf("Session %d", i+1)
		fmt.Fprintf(&b, "  %s = %s\n", labels[id], id)
	}
	if len(t.Events) == 0 {
		fmt.Fprintln(&b, "\nNo conversation events in this range.")
	}
	for _, e := range t.Events {
		if gap := e.Error; gap != nil {
			fmt.Fprintf(&b, "\n%s\n\n[CHECKPOINT GAP] %s\n\nCheckpoint: %s\n%s\n", separator, gap.Code, gap.CheckpointID, gap.Message)
			for _, ref := range gap.Commits {
				fmt.Fprintf(&b, "Commit: %s (Git commit time: %s)\n", ref.SHA, ref.Timestamp.Format("2006-01-02T15:04:05Z07:00"))
			}
			if gap.PreviousCheckpointID != "" {
				fmt.Fprintf(&b, "Previous available checkpoint: %s\n", gap.PreviousCheckpointID)
			}
			if gap.NextCheckpointID != "" {
				fmt.Fprintf(&b, "Next available checkpoint: %s\n", gap.NextCheckpointID)
			}
			fmt.Fprintf(&b, "Conversation time and session unknown; display placement: %s.\n", gap.Placement)
			continue
		}
		ts := "time unknown"
		if e.Timestamp != nil {
			ts = e.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		fmt.Fprintf(&b, "\n%s\n\n[%s] [%s] %s (%s)\n\n", separator, ts, labels[e.SessionID], strings.ToUpper(e.Role), e.Agent)
		if e.CheckpointID != "" {
			fmt.Fprintf(&b, "Checkpoint: %s\n\n", e.CheckpointID)
		}
		if a := e.Attribution; a != nil {
			fmt.Fprintf(&b, "[UNATTRIBUTED CONTEXT] No confirmed commit/checkpoint association.\nSource checkpoint: %s; between %s and %s.\n\n", a.SourceCheckpointID, a.PreviousCheckpointID, a.NextCheckpointID)
		}
		if e.Summary != "" {
			fmt.Fprintf(&b, "Summary: %s\n\n", e.Summary)
		}
		for _, p := range e.Parts {
			switch p.Kind {
			case "text":
				// No wrapping or truncation: terminal soft-wrap preserves Markdown,
				// indentation, code fences, and the entire original message.
				fmt.Fprintln(&b, p.Text)
			case "reasoning":
				fmt.Fprintln(&b, "[Reasoning]")
				fmt.Fprintln(&b, p.Text)
			case "tool_call":
				if opts.IncludeTools {
					fmt.Fprintln(&b, "↳ Tool call:", p.Name)
					pretty(&b, p.Data)
				} else {
					fmt.Fprintf(&b, "↳ Tool: %s %s\n", p.Name, toolSummary(p.Data, width))
				}
			case "tool_result":
				if opts.IncludeTools {
					fmt.Fprintln(&b, "↳ Tool result:", p.Name)
					pretty(&b, p.Data)
				} else {
					fmt.Fprintf(&b, "↳ Tool result: %s (details: --include-tools)\n", p.Name)
				}
			default:
				fmt.Fprintf(&b, "[%s]\n", e.Type)
				pretty(&b, p.Data)
			}
			fmt.Fprintln(&b)
		}
		if opts.IncludeTools {
			fmt.Fprintln(&b, "Original event:")
			pretty(&b, e.Raw)
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}

func pretty(w io.Writer, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") == nil {
		fmt.Fprintln(w, b.String())
	} else {
		fmt.Fprintln(w, string(raw))
	}
}

func toolSummary(raw json.RawMessage, width int) string {
	m := object(raw)
	args := m["input"]
	if len(args) == 0 {
		args = m["arguments"]
	}
	var b bytes.Buffer
	if json.Compact(&b, args) != nil {
		return "(details: --include-tools)"
	}
	s := b.String()
	limit := width - 12
	if limit < 16 {
		limit = 16
	}
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "… (details: --include-tools)"
	}
	return s
}
