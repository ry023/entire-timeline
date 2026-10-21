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
	labels := map[string]string{}
	for i, id := range t.Sessions {
		labels[id] = fmt.Sprintf("Session %d", i+1)
		fmt.Fprintf(&b, "  %s = %s\n", labels[id], id)
	}
	if len(t.Events) == 0 {
		fmt.Fprintln(&b, "\nNo conversation events in this range.")
	}
	for _, e := range t.Events {
		ts := "time unknown"
		if e.Timestamp != nil {
			ts = e.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		fmt.Fprintf(&b, "\n%s\n\n[%s] [%s] %s (%s)\n\n", separator, ts, labels[e.SessionID], strings.ToUpper(e.Role), e.Agent)
		for _, p := range e.Parts {
			switch p.Kind {
			case "text":
				// No wrapping or truncation: terminal soft-wrap preserves Markdown,
				// indentation, code fences, and the entire original message.
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
