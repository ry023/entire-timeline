package timeline

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestInsertedLinesDoNotChangeOtherCheckpointOffsets(t *testing.T) {
	header := strings.Repeat("{\"type\":\"session.start\"}\n", 9)
	a := `{"type":"user.message","id":"event-a","timestamp":"2026-10-27T10:00:00Z","data":{"content":"依頼A"}}`
	b := `{"type":"assistant.message","id":"event-b","timestamp":"2026-10-27T10:01:00Z","data":{"content":"応答B"}}`
	c := `{"type":"assistant.message","id":"event-c","timestamp":"2026-10-27T10:02:00Z","data":{"content":"続きC"}}`
	one := []byte(header + a + "\n" + b)
	two := []byte(header + "{\"type\":\"session.info\"}\n{\"type\":\"session.info\"}\n" + a + "\n" + b + "\n" + c)
	store := fixtureStore{[][]fixtureSession{{{id: "s", start: 9, data: one}}, {{id: "s", start: 11, data: two}}}}
	var log bytes.Buffer
	got, err := RestoreWithOptions(context.Background(), store, "base..HEAD", RestoreOptions{Diagnostics: &log})
	if err != nil || len(got.Events) != 5 {
		t.Fatalf("%+v %v", got, err)
	}
	var positions []string
	for _, e := range got.Events {
		positions = append(positions, fmt.Sprintf("%s:%s:%d", e.CheckpointID, e.ID, e.Line))
	}
	want := []string{"0:event-a:9", "1:event-a:11", "0:event-b:10", "1:event-b:12", "1:event-c:13"}
	if !reflect.DeepEqual(positions, want) {
		t.Fatal(positions)
	}
	if strings.Contains(log.String(), "prefix_mismatch") {
		t.Fatal("prefix comparison is still required", log.String())
	}
	var text bytes.Buffer
	RenderText(&text, got, TextOptions{})
	if strings.Count(text.String(), "依頼A") != 2 || !strings.Contains(text.String(), "Checkpoint: 0") || !strings.Contains(text.String(), "Checkpoint: 1") {
		t.Fatal(text.String())
	}
}

func TestIndependentChangedAndShortenedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, later string
		count       int
	}{
		{"rewritten", compactEvent("", "different event at the same line") + "\n" + compactEvent("", "changed tail"), 4},
		{"shortened", compactEvent("", "reset history"), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := []byte(compactEvent("", "unselected") + "\n" + compactEvent("", "original event") + "\n" + compactEvent("", "original tail"))
			second := []byte("{INVALID UNSELECTED PREFIX}\n" + tc.later)
			got, err := Restore(context.Background(), fixtureStore{[][]fixtureSession{{{id: "s", start: 1, data: first}}, {{id: "s", start: 1, data: second}}}}, "base..HEAD")
			if err != nil || len(got.Events) != tc.count {
				t.Fatalf("%+v %v", got, err)
			}
			if got.Events[0].CheckpointID != "0" || got.Events[2].CheckpointID != "1" || got.Events[2].Line != 1 {
				t.Fatal(got.Events)
			}
		})
	}
}

func TestCheckpointClocksAreIndependent(t *testing.T) {
	first := []byte(compactEvent("2026-10-27T10:00:00Z", "later") + "\n" + compactEvent("2026-10-27T10:01:00Z", "later tail"))
	second := []byte(compactEvent("2026-10-27T09:00:00Z", "earlier"))
	got := restoreFixture(t, []fixtureSession{{id: "s", data: first}}, []fixtureSession{{id: "s", data: second}})
	if len(got.Warnings) != 0 || got.Events[0].CheckpointID != "1" || got.Events[1].CheckpointID != "0" {
		t.Fatal("clock was clamped using a different checkpoint", got.Events, got.Warnings)
	}
}

func TestUnattributedPrefixMismatchSkipsOnlyOptionalContext(t *testing.T) {
	r, spec, _ := missingMiddleRepo(t)
	full := []byte(r.git("show", "refs/heads/entire/checkpoints/v1:cc/cccccccccc/0/full.jsonl"))
	first := prefix(full, 2)
	changed := bytes.Replace(full, []byte("outside selected intervals"), []byte("PRIVATE CHANGED PREFIX"), 1)
	files := checkpointFiles(t, "aaaaaaaaaaaa", "aa/aaaaaaaaaa", fixtureSession{id: "s", start: 1, data: first})
	mergeFiles(files, checkpointFiles(t, "cccccccccccc", "cc/cccccccccc", fixtureSession{id: "s", start: 4, data: changed}))
	r.store("refs/heads/entire/checkpoints/v1", files)
	var log bytes.Buffer
	got, err := RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, spec, RestoreOptions{IncludeUnattributed: true, Diagnostics: &log})
	if err != nil || len(got.Events) != 3 || got.UnattributedEvents != 0 || got.MissingCheckpoints != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "unattributed context skipped") || !strings.Contains(log.String(), "prefix_mismatch") || strings.Contains(log.String(), "PRIVATE") {
		t.Fatal(got.Warnings, log.String())
	}
}
