package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func timelineSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("../../schemas/timeline.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	// Reject external loads: both our references and the embedded metaschema
	// must resolve without file/network access through a loader.
	c.UseLoader(jsonschema.SchemeURLLoader{})
	const id = "urn:entire-timeline:jsonl:v1"
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateJSONLLines(t *testing.T, schema *jsonschema.Schema, data []byte) []map[string]any {
	t.Helper()
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("JSONL must end with LF")
	}
	var records []map[string]any
	for i, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(line))
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if err := schema.Validate(v); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		records = append(records, v.(map[string]any))
	}
	return records
}

func TestJSONLSchemaActualExports(t *testing.T) {
	schema := timelineSchema(t)
	stamp := "2026-10-23T09:01:00Z"
	// Include duplicate messages and a regressing clock. Rendering must preserve
	// restoration order, not sort again or deduplicate to satisfy the schema.
	mixed := restoreFixture(t, []fixtureSession{
		{id: "compact", data: fixture(t, "copilot-compact.jsonl")},
		{id: "native", data: fixture(t, "copilot-native.jsonl")},
		{id: "claude", agent: "Claude Code", data: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"本文"},{"type":"thinking","thinking":"思考"},{"type":"image","source":{"data":"example"}}]}}`)},
		{id: "unknown", data: []byte(`{"type":"future.event","data":{"future":true}}`)},
		{id: "order", data: []byte(strings.Join([]string{compactEvent(stamp, "same"), compactEvent(stamp, "same"), compactEvent("2020-01-01T00:00:00Z", "back"), compactEvent("", "unknown time")}, "\n"))},
	})
	for i := range mixed.Events {
		if mixed.Events[i].ConversationText() != "" {
			mixed.Events[i].Summary = "保存済み要約"
		}
	}
	r, spec, _ := missingMiddleRepo(t)
	gap, err := Restore(context.Background(), GitStore{Dir: r.dir}, spec)
	if err != nil {
		t.Fatal(err)
	}
	contextView, err := RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, spec, RestoreOptions{IncludeUnattributed: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]Timeline{"mixed": mixed, "gap": gap, "unattributed": contextView, "empty": restoreFixture(t)} {
		for _, selector := range []string{"all", "conversation"} {
			t.Run(name+"/"+selector, func(t *testing.T) {
				filter, err := ParseFilter(selector)
				if err != nil {
					t.Fatal(err)
				}
				view := filter.Apply(source)
				var out bytes.Buffer
				if err := RenderJSONL(&out, view); err != nil {
					t.Fatal(err)
				}
				records := validateJSONLLines(t, schema, out.Bytes())
				if len(records) != len(view.Events) {
					t.Fatal("record count changed")
				}
				for i, record := range records {
					e := view.Events[i]
					if e.Error != nil {
						if len(record) != 3 || record["error"].(map[string]any)["checkpoint_id"] != e.Error.CheckpointID {
							t.Fatal("gap source/position changed")
						}
						continue
					}
					// Compare the complete event, including parts, raw, timestamps,
					// summaries and provenance, at its original position.
					want, err := json.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}
					expected, err := jsonschema.UnmarshalJSON(bytes.NewReader(want))
					if err != nil || !reflect.DeepEqual(record, expected) {
						t.Fatalf("event %d changed: %v", i, err)
					}
					if selector == "conversation" {
						if _, exists := record["raw"]; exists {
							t.Fatal("conversation export retained raw")
						}
					}
					if e.Attribution == nil && (len(e.Checkpoints) != 1 || e.Checkpoints[0] != e.CheckpointID) {
						t.Fatal("checkpoint provenance disagrees")
					}
				}
			})
		}
	}
}

func TestJSONLSchemaPublicExample(t *testing.T) {
	data, err := os.ReadFile("../../docs/examples/timeline.v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(validateJSONLLines(t, timelineSchema(t), data)) == 0 {
		t.Fatal("empty public example")
	}
}

func TestJSONLSchemaContract(t *testing.T) {
	schema := timelineSchema(t)
	const event = `{"checkpoint_id":"a","session_id":"s","agent":"future-agent","timestamp":null,"type":"future.event","role":"future-role","source_line":0,"checkpoints":["a"]}`
	const gap = `{"type":"timeline.error","role":"system","error":{"code":"checkpoint_unavailable","checkpoint_id":"b","message":"missing","commits":[{"sha":"abc","timestamp":"2026-10-23T09:01:00Z"}],"placement":"after_previous_checkpoint"}}`
	check := func(t *testing.T, source string, mutate func(map[string]any), valid bool) {
		t.Helper()
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(source))
		if err != nil {
			t.Fatal(err)
		}
		mutate(v.(map[string]any))
		if err := schema.Validate(v); (err == nil) != valid {
			t.Fatalf("want valid=%v; got %v", valid, err)
		}
	}
	for _, field := range []string{"checkpoint_id", "session_id", "agent", "timestamp", "type", "role", "source_line", "checkpoints"} {
		t.Run("missing/"+field, func(t *testing.T) {
			check(t, event, func(v map[string]any) { delete(v, field) }, false)
		})
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"timestamp", "yesterday"}, {"timestamp", 123}, {"session_id", nil}, {"type", ""},
		{"source_line", -1}, {"source_line", 1.5}, {"checkpoints", nil}, {"checkpoints", []any{}},
		{"parts", []any{map[string]any{"text": "no kind"}}}, {"summary", false}, {"error", nil},
	} {
		t.Run(fmt.Sprintf("invalid/%s/%v", tc.field, tc.value), func(t *testing.T) {
			check(t, event, func(v map[string]any) { v[tc.field] = tc.value }, false)
		})
	}
	for _, field := range []string{"type", "role", "error"} {
		t.Run("gap-missing/"+field, func(t *testing.T) {
			check(t, gap, func(v map[string]any) { delete(v, field) }, false)
		})
	}
	for _, field := range []string{"code", "checkpoint_id", "message", "commits", "placement"} {
		t.Run("gap-error-missing/"+field, func(t *testing.T) {
			check(t, gap, func(v map[string]any) { delete(v["error"].(map[string]any), field) }, false)
		})
	}
	for _, field := range []string{"checkpoint_id", "session_id", "agent", "timestamp", "source_line", "checkpoints", "parts", "raw", "summary", "attribution", "event_id"} {
		t.Run("gap-provenance/"+field, func(t *testing.T) {
			check(t, gap, func(v map[string]any) { v[field] = nil }, false)
		})
	}
	t.Run("gap-invalid-date", func(t *testing.T) {
		check(t, gap, func(v map[string]any) {
			v["error"].(map[string]any)["commits"].([]any)[0].(map[string]any)["timestamp"] = "invalid"
		}, false)
	})
	t.Run("gap-invalid-role", func(t *testing.T) {
		check(t, gap, func(v map[string]any) { v["role"] = "user" }, false)
	})
	for _, payload := range []any{nil, true, "text", 42, []any{1, "two"}, map[string]any{"arbitrary": true}} {
		t.Run(fmt.Sprintf("open-payload/%v", payload), func(t *testing.T) {
			check(t, event, func(v map[string]any) {
				v["raw"] = payload
				v["future_field"] = payload
				v["parts"] = []any{map[string]any{"kind": "future-kind", "data": payload, "future_field": true}}
			}, true)
		})
	}
	t.Run("source-event-named-timeline-error", func(t *testing.T) {
		check(t, event, func(v map[string]any) { v["type"] = "timeline.error" }, true)
	})
	t.Run("gap-future-fields", func(t *testing.T) {
		check(t, gap, func(v map[string]any) {
			v["future_field"] = true
			v["error"].(map[string]any)["code"] = "future-code"
			v["error"].(map[string]any)["placement"] = "future-placement"
		}, true)
	})
	const unattributed = `{"session_id":"s","agent":"copilot-cli","timestamp":null,"type":"user","role":"user","source_line":2,"checkpoints":[],"attribution":{"status":"unattributed","source_checkpoint_id":"c","previous_checkpoint_id":"a","next_checkpoint_id":"c","nearby_missing_checkpoint_ids":["b"]}}`
	t.Run("unattributed", func(t *testing.T) {
		check(t, unattributed, func(v map[string]any) {}, true)
	})
	for _, field := range []string{"status", "source_checkpoint_id", "previous_checkpoint_id", "next_checkpoint_id", "nearby_missing_checkpoint_ids"} {
		t.Run("attribution-missing/"+field, func(t *testing.T) {
			check(t, unattributed, func(v map[string]any) { delete(v["attribution"].(map[string]any), field) }, false)
		})
	}
	t.Run("unattributed-cannot-claim-checkpoint", func(t *testing.T) {
		check(t, unattributed, func(v map[string]any) { v["checkpoint_id"] = "b" }, false)
		check(t, unattributed, func(v map[string]any) { v["checkpoints"] = []any{"b"} }, false)
	})
}
