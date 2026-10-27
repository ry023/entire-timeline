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
)

type fixtureSession struct {
	id, agent string
	start     int
	data      []byte
}
type fixtureStore struct{ checkpoints [][]fixtureSession }

func (f fixtureStore) Select(context.Context, string) ([]Checkpoint, error) {
	var out []Checkpoint
	for i, sessions := range f.checkpoints {
		cp := Checkpoint{Summary: Summary{CheckpointID: fmt.Sprint(i)}}
		for j := range sessions {
			cp.Summary.Sessions = append(cp.Summary.Sessions, SessionPaths{Metadata: fmt.Sprintf("%d/%d", i, j)})
		}
		out = append(out, cp)
	}
	return out, nil
}
func (f fixtureStore) Session(_ context.Context, cp Checkpoint, p SessionPaths) (Metadata, []byte, error) {
	var i, j int
	fmt.Sscanf(p.Metadata, "%d/%d", &i, &j)
	s := f.checkpoints[i][j]
	agent := s.agent
	if agent == "" {
		agent = "Copilot CLI"
	}
	return Metadata{CheckpointID: cp.Summary.CheckpointID, SessionID: s.id, Agent: agent, Start: s.start}, s.data, nil
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func prefix(b []byte, end int) []byte { return bytes.Join(rawLines(b)[:end], []byte{'\n'}) }
func restoreFixture(t *testing.T, cps ...[]fixtureSession) Timeline {
	t.Helper()
	result, err := Restore(context.Background(), fixtureStore{cps}, "base..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestRestoreCheckpointSessionMatrix(t *testing.T) {
	x := fixture(t, "copilot-compact.jsonl")
	y := fixture(t, "copilot-native.jsonl")
	tests := []struct {
		name             string
		cps              [][]fixtureSession
		sessions, events int
	}{
		{"single", [][]fixtureSession{{{id: "x", data: prefix(x, 3)}}}, 1, 3},
		{"multiple sessions", [][]fixtureSession{{{id: "x", data: prefix(x, 3)}, {id: "y", data: y}}}, 2, 9},
		{"cumulative", [][]fixtureSession{{{id: "x", data: prefix(x, 3)}}, {{id: "x", start: 3, data: x}}}, 1, 7},
		{"continues before range", [][]fixtureSession{{{id: "x", start: 1, data: prefix(x, 3)}}}, 1, 2},
		{"noncontiguous", [][]fixtureSession{{{id: "x", start: 1, data: prefix(x, 3)}}, {{id: "x", start: 5, data: x}}}, 1, 4},
		{"overlapping", [][]fixtureSession{{{id: "x", data: prefix(x, 3)}}, {{id: "x", start: 1, data: x}}}, 1, 9},
		{"no checkpoints", nil, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restoreFixture(t, tt.cps...)
			if len(got.Sessions) != tt.sessions || len(got.Events) != tt.events {
				t.Fatalf("sessions=%d events=%d", len(got.Sessions), len(got.Events))
			}
			if tt.name == "noncontiguous" {
				var lines []int
				for _, e := range got.Events {
					lines = append(lines, e.Line)
					if strings.Contains(string(e.Raw), "excluded") {
						t.Fatal("out-of-range content leaked")
					}
				}
				if !reflect.DeepEqual(lines, []int{1, 2, 5, 6}) {
					t.Fatal(lines)
				}
			}
			for _, e := range got.Events {
				if e.CheckpointID == "" || len(e.Checkpoints) != 1 || e.Checkpoints[0] != e.CheckpointID {
					t.Fatal("checkpoint-local provenance lost")
				}
			}
			if tt.name == "overlapping" {
				copies := 0
				for _, e := range got.Events {
					if e.Line == 1 {
						copies++
					}
				}
				if copies != 2 {
					t.Fatal("overlapping checkpoint records were deduplicated")
				}
			}
		})
	}
}

func TestMergeSessionsByTime(t *testing.T) {
	x, y := fixture(t, "copilot-compact.jsonl"), fixture(t, "copilot-native.jsonl")
	got := restoreFixture(t, []fixtureSession{{id: "x", start: 1, data: prefix(x, 3)}, {id: "y", start: 1, data: y}})
	var ids []string
	for _, e := range got.Events {
		ids = append(ids, e.SessionID)
	}
	if !reflect.DeepEqual(ids, []string{"x", "y", "x", "y", "y", "y", "y"}) {
		t.Fatal(ids)
	}
}

func compactEvent(ts, text string) string {
	b, _ := json.Marshal(map[string]any{"v": 1, "agent": "copilot-cli", "type": "user", "ts": ts, "content": []map[string]string{{"text": text}}})
	return string(b)
}
func TestTimestampsAndIdentity(t *testing.T) {
	ts := "2026-08-21T08:59:00Z"
	// Equal/missing times, identical no-ID events and a regressing clock must
	// retain every source line and deterministic per-session source order.
	x := strings.Join([]string{compactEvent("", "head"), compactEvent(ts, "same"), compactEvent("", "middle"), compactEvent(ts, "same"), compactEvent("2020-01-01T00:00:00Z", "back")}, "\n")
	got := restoreFixture(t, []fixtureSession{{id: "b", data: []byte(x)}, {id: "a", data: []byte(compactEvent(ts, "tie"))}, {id: "z", data: []byte(compactEvent("", "untimed"))}})
	var order []string
	for _, e := range got.Events {
		order = append(order, fmt.Sprintf("%s:%d", e.SessionID, e.Line))
	}
	want := []string{"a:0", "b:0", "b:1", "b:2", "b:3", "b:4", "z:0"}
	if !reflect.DeepEqual(order, want) {
		t.Fatal(order)
	}
	if len(got.Warnings) != 3 {
		t.Fatalf("warnings: %v", got.Warnings)
	}
	if got.Events[1].Timestamp != nil || got.Events[1].ID != "" {
		t.Fatal("invented source timestamp/ID")
	}
}

func TestInvalidRestorationFails(t *testing.T) {
	x := fixture(t, "copilot-compact.jsonl")
	tests := []struct {
		name, contains string
		cps            [][]fixtureSession
	}{
		{"offset past end", "offset", [][]fixtureSession{{{id: "x", start: 99, data: x}}}},
		{"negative offset", "offset", [][]fixtureSession{{{id: "x", start: -1, data: x}}}},
		{"bad JSON", "invalid JSON", [][]fixtureSession{{{id: "x", data: []byte("{bad}")}}}},
		{"unsupported agent", "unsupported agent", [][]fixtureSession{{{id: "x", agent: "Gemini CLI", data: x}}}},
		{"duplicate session", "duplicate session", [][]fixtureSession{{{id: "x", data: x}, {id: "x", data: x}}}},
		{"invalid time", "invalid timestamp", [][]fixtureSession{{{id: "x", data: []byte(compactEvent("yesterday", "text"))}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Restore(context.Background(), fixtureStore{tt.cps}, "base..HEAD")
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestPhysicalLineOffsetsAndLargeMessage(t *testing.T) {
	long := strings.Repeat("全文・Long message\n", 10000)
	data := "\n" + compactEvent("", "skip") + "\r\n\n" + compactEvent("", long)
	got := restoreFixture(t, []fixtureSession{{id: "x", start: 3, data: []byte(data)}})
	if len(got.Events) != 1 || got.Events[0].Line != 3 || got.Events[0].Parts[0].Text != long {
		t.Fatal("physical offset / long line corrupted")
	}
}
