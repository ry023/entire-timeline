package timeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

type diagnostics struct{ out io.Writer }

func (d diagnostics) printf(format string, args ...any) {
	if d.out != nil {
		fmt.Fprintf(d.out, "entire-timeline: verbose: "+format+"\n", args...)
	}
}

func diagnosticJSON(raw []byte) (any, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Never round large integers through float64 during comparison.
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func (d diagnostics) mismatch(a, b snapshot, line int) {
	if d.out == nil {
		return
	}
	different, last := 0, line
	for i := line; i < len(a.lines); i++ {
		if !bytes.Equal(bytes.TrimSpace(a.lines[i]), bytes.TrimSpace(b.lines[i])) {
			different++
			last = i
		}
	}
	d.printf("prefix_mismatch session=%q checkpoint=%q reference_checkpoint=%q raw_line=%d physical_line=%d common_prefix_lines=%d compared_lines=%d different_lines=%d last_different_raw_line=%d", a.metadata.SessionID, a.checkpoint, b.checkpoint, line, line+1, line, len(a.lines), different, last)
	x, xOK := diagnosticJSON(a.lines[line])
	y, yOK := diagnosticJSON(b.lines[line])
	d.printf("first_mismatch bytes=%d->%d json_valid=%t->%t inside_selected_range=%t->%t", len(a.lines[line]), len(b.lines[line]), xOK, yOK, line >= a.metadata.StartLine(), line >= b.metadata.StartLine())
	if xOK && yOK {
		equal := reflect.DeepEqual(x, y)
		d.printf("decoded_json_equal=%t (object key order, whitespace and string escaping ignored; number spelling preserved)", equal)
		if equal {
			d.printf("possible_serialization_only_difference=true; unattributed context skipped, checkpoint restoration continues")
		} else {
			diff := structuralDiff{diag: d}
			diff.walk("", x, true, y, true, 0)
			if diff.truncated {
				d.printf("changed_paths_truncated=true (maximum 16 differences, depth 8, 512 visited nodes)")
			}
		}
		d.eventIdentity(x, y)
	}
	// Nearby exact matches help distinguish insertion/deletion from a changed
	// field without logging either event's text or identifier values.
	for _, pair := range []struct{ from, to snapshot }{{a, b}, {b, a}} {
		if len(bytes.TrimSpace(pair.from.lines[line])) == 0 {
			continue
		}
		for i := max(0, line-5); i < min(len(pair.to.lines), line+6); i++ {
			if i != line && bytes.Equal(bytes.TrimSpace(pair.from.lines[line]), bytes.TrimSpace(pair.to.lines[i])) {
				d.printf("nearby_exact_match checkpoint=%q raw_line=%d other_checkpoint=%q other_raw_line=%d shift=%+d (possible insertion/deletion or repeated event)", pair.from.checkpoint, line, pair.to.checkpoint, i, i-line)
			}
		}
	}
}

func (d diagnostics) eventIdentity(x, y any) {
	a, aOK := x.(map[string]any)
	b, bOK := y.(map[string]any)
	if !aOK || !bOK {
		return
	}
	d.printf("event_type=%q->%q event_type_equal=%t", diagnosticEventType(a["type"]), diagnosticEventType(b["type"]), reflect.DeepEqual(a["type"], b["type"]))
	for _, key := range []string{"id", "uuid", "timestamp", "ts"} {
		av, ap := a[key]
		bv, bp := b[key]
		if ap || bp {
			d.printf("field=%s present=%t->%t equal=%t (values omitted)", key, ap, bp, ap == bp && reflect.DeepEqual(av, bv))
		}
	}
}

// The event type is also source data: print only recognized schema enums.
func diagnosticEventType(v any) string {
	s, ok := v.(string)
	if !ok {
		return "<missing-or-non-string>"
	}
	switch s {
	case "user", "assistant", "system", "tool", "user.message", "assistant.message", "assistant.message_delta", "tool.execution_start", "tool.execution_complete", "tool.execution_partial_result", "session.start", "session.shutdown", "session.model_change", "subagent.started", "subagent.completed":
		return s
	default:
		return "<other>"
	}
}

func diagnosticShape(v any, present bool) string {
	if !present {
		return "missing"
	}
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("string(bytes=%d)", len(v))
	case []any:
		return fmt.Sprintf("array(items=%d)", len(v))
	case map[string]any:
		return fmt.Sprintf("object(keys=%d)", len(v))
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "other"
	}
}

// Only schema field names may appear in paths. Arbitrary keys can themselves
// contain private file names, prompts, secrets, model names, etc.
func diagnosticKey(key string) string {
	switch key {
	case "type", "v", "agent", "role", "id", "uuid", "timestamp", "ts", "data", "message", "content", "text", "thinking", "reasoningText", "deltaContent", "toolRequests", "toolCallId", "toolName", "tool_use_id", "name", "arguments", "input", "result", "output", "success", "model", "messageId", "parentId", "agentId", "interactionId", "sessionId", "context", "usage", "inputTokens", "outputTokens":
		return key
	default:
		return "<other-key>"
	}
}

type structuralDiff struct {
	diag           diagnostics
	count, visited int
	truncated      bool
}

func (d *structuralDiff) walk(path string, a any, ap bool, b any, bp bool, depth int) {
	if d.count >= 16 || d.visited >= 512 {
		d.truncated = true
		return
	}
	d.visited++
	if ap == bp && reflect.DeepEqual(a, b) {
		return
	}
	// Collapse payload objects instead of disclosing any application-defined
	// argument/result/context field names. Their parent field is enough to locate
	// the difference locally in the original checkpoint.
	segment := path[strings.LastIndex(path, "/")+1:]
	opaque := segment == "arguments" || segment == "input" || segment == "result" || segment == "context" || segment == "<other-key>"
	if ap && bp && depth < 8 && !opaque {
		if x, ok := a.(map[string]any); ok {
			if y, ok := b.(map[string]any); ok {
				keys := map[string]bool{}
				for k := range x {
					keys[k] = true
				}
				for k := range y {
					keys[k] = true
				}
				var ordered []string
				for k := range keys {
					ordered = append(ordered, k)
				}
				sort.Strings(ordered)
				for _, k := range ordered {
					av, aPresent := x[k]
					bv, bPresent := y[k]
					d.walk(path+"/"+diagnosticKey(k), av, aPresent, bv, bPresent, depth+1)
					if d.count >= 16 || d.visited >= 512 {
						d.truncated = true
						break
					}
				}
				return
			}
		}
		if x, ok := a.([]any); ok {
			if y, ok := b.([]any); ok {
				for i := 0; i < max(len(x), len(y)); i++ {
					var av, bv any
					aPresent, bPresent := i < len(x), i < len(y)
					if aPresent {
						av = x[i]
					}
					if bPresent {
						bv = y[i]
					}
					d.walk(fmt.Sprintf("%s/%d", path, i), av, aPresent, bv, bPresent, depth+1)
					if d.count >= 16 || d.visited >= 512 {
						d.truncated = true
						break
					}
				}
				return
			}
		}
	}
	if depth >= 8 {
		d.truncated = true
	}
	if path == "" {
		path = "/"
	}
	change := "value-or-type"
	if !ap {
		change = "added"
	} else if !bp {
		change = "removed"
	}
	d.diag.printf("changed_path=%s change=%s %s -> %s (values omitted)", path, change, diagnosticShape(a, ap), diagnosticShape(b, bp))
	d.count++
}
