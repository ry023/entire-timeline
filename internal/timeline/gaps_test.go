package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func missingMiddleRepo(t *testing.T) (testRepo, string, []string) {
	t.Helper()
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	shas := []string{
		r.commit("first\n\nEntire-Checkpoint: aaaaaaaaaaaa"),
		r.commit("missing\n\nEntire-Checkpoint: bbbbbbbbbbbb"),
		r.commit("last\n\nEntire-Checkpoint: cccccccccccc"),
	}
	data := strings.Join([]string{
		compactEvent("2026-10-23T09:00:00Z", "outside selected intervals"),
		compactEvent("2026-10-23T09:01:00Z", "confirmed A"),
		compactEvent("2026-10-23T09:02:00Z", "unattributed context B1"),
		compactEvent("2026-10-23T09:03:00Z", "unattributed context B2"),
		compactEvent("2026-10-23T09:04:00Z", "confirmed C"),
	}, "\n")
	files := checkpointFiles(t, "aaaaaaaaaaaa", "aa/aaaaaaaaaa", fixtureSession{id: "s", start: 1, data: prefix([]byte(data), 2)})
	mergeFiles(files, checkpointFiles(t, "cccccccccccc", "cc/cccccccccc", fixtureSession{id: "s", start: 4, data: []byte(data)}))
	r.store("refs/heads/entire/checkpoints/v1", files)
	return r, base + "..HEAD", shas
}

func TestMissingCheckpointBecomesAnInlineError(t *testing.T) {
	r, spec, shas := missingMiddleRepo(t)
	before := r.git("show-ref")
	status := r.git("status", "--porcelain")
	got, err := Restore(context.Background(), GitStore{Dir: r.dir}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checkpoints != 3 || got.MissingCheckpoints != 1 || len(got.Sessions) != 1 || len(got.Events) != 3 || got.UnattributedEvents != 0 {
		t.Fatalf("%+v", got)
	}
	gap := got.Events[1]
	if gap.Type != "timeline.error" || gap.Error == nil || gap.Error.Code != "checkpoint_unavailable" || gap.Error.CheckpointID != "bbbbbbbbbbbb" {
		t.Fatalf("%+v", gap)
	}
	if gap.SessionID != "" || gap.Timestamp != nil || gap.Raw != nil || gap.Line != -1 {
		t.Fatal("invented source provenance for missing data")
	}
	if gap.Error.PreviousCheckpointID != "aaaaaaaaaaaa" || gap.Error.NextCheckpointID != "cccccccccccc" || gap.Error.Placement != "after_previous_checkpoint" {
		t.Fatalf("%+v", gap.Error)
	}
	if len(gap.Error.Commits) != 1 || gap.Error.Commits[0].SHA != shas[1] || gap.Error.Commits[0].Timestamp.IsZero() {
		t.Fatalf("lost referring commit: %+v", gap.Error)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "incomplete") {
		t.Fatal(got.Warnings)
	}
	var text, exported bytes.Buffer
	if err := RenderText(&text, got, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RenderJSONL(&exported, got); err != nil {
		t.Fatal(err)
	}
	if !(strings.Index(text.String(), "confirmed A") < strings.Index(text.String(), "[CHECKPOINT GAP]") && strings.Index(text.String(), "[CHECKPOINT GAP]") < strings.Index(text.String(), "confirmed C")) {
		t.Fatal(text.String())
	}
	if strings.Contains(text.String(), "unattributed context B") || strings.Contains(exported.String(), "outside selected intervals") {
		t.Fatal("expanded the selected intervals by default")
	}
	lines := bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'})
	var record map[string]any
	if err := json.Unmarshal(lines[1], &record); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"session_id", "timestamp", "source_line", "agent", "raw", "checkpoints"} {
		if _, ok := record[absent]; ok {
			t.Fatalf("gap invents %s: %s", absent, lines[1])
		}
	}
	if record["type"] != "timeline.error" || record["error"].(map[string]any)["code"] != "checkpoint_unavailable" {
		t.Fatal(record)
	}
	if before != r.git("show-ref") || status != r.git("status", "--porcelain") {
		t.Fatal("read-only restore modified repository")
	}
}

func TestIncludeUnattributedUsesOnlyBoundedUncoveredLines(t *testing.T) {
	r, spec, _ := missingMiddleRepo(t)
	got, err := RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, spec, RestoreOptions{IncludeUnattributed: true})
	if err != nil || len(got.Events) != 5 || got.UnattributedEvents != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	var recovered []int
	for _, e := range got.Events {
		if e.Attribution != nil {
			recovered = append(recovered, e.Line)
			if e.Attribution.Status != "unattributed" || e.Attribution.SourceCheckpointID != "cccccccccccc" || e.Attribution.PreviousCheckpointID != "aaaaaaaaaaaa" || e.Attribution.NextCheckpointID != "cccccccccccc" || !reflect.DeepEqual(e.Attribution.NearbyMissingCheckpointIDs, []string{"bbbbbbbbbbbb"}) {
				t.Fatalf("%+v", e.Attribution)
			}
			if len(e.Checkpoints) != 0 || e.Raw == nil || e.SessionID != "s" {
				t.Fatal("unconfirmed context was assigned to a checkpoint or lost its source")
			}
		}
		if strings.Contains(string(e.Raw), "outside selected intervals") {
			t.Fatal("included unbounded prefix")
		}
	}
	if !reflect.DeepEqual(recovered, []int{2, 3}) {
		t.Fatal(recovered)
	}
	var text, exported bytes.Buffer
	RenderText(&text, got, TextOptions{})
	RenderJSONL(&exported, got)
	if strings.Count(text.String(), "[UNATTRIBUTED CONTEXT]") != 2 || !strings.Contains(exported.String(), `"checkpoints":[],`) || !strings.Contains(exported.String(), `"status":"unattributed"`) {
		t.Fatal(text.String(), exported.String())
	}
	if got.Events[1].Error == nil || got.Events[2].Attribution == nil {
		t.Fatal("gap was not placed before recovered context")
	}
	// The available checkpoints outside a narrower selection cannot supply
	// inferred context for an otherwise entirely unavailable range.
	narrow := r.git("rev-parse", "HEAD~2") + "..HEAD~1"
	got, err = RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, narrow, RestoreOptions{IncludeUnattributed: true})
	if err != nil || got.UnattributedEvents != 0 || len(got.Events) != 1 || got.Events[0].Error == nil {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestMissingOnlyRangeDeduplicatesReferences(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	first := r.commit("missing first\n\nEntire-Checkpoint: bbbbbbbbbbbb\nEntire-Checkpoint: bbbbbbbbbbbb")
	second := r.commit("same missing again\n\nEntire-Checkpoint: bbbbbbbbbbbb")
	r.commit("another missing\n\nEntire-Checkpoint: dddddddddddd")
	got, err := Restore(context.Background(), GitStore{Dir: r.dir}, base+"..HEAD")
	if err != nil || got.MissingCheckpoints != 2 || len(got.Sessions) != 0 || len(got.Events) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	refs := got.Events[0].Error.Commits
	if len(refs) != 2 || refs[0].SHA != first || refs[1].SHA != second {
		t.Fatal(refs)
	}
	if got.Events[0].Error.CheckpointID != "bbbbbbbbbbbb" || got.Events[1].Error.CheckpointID != "dddddddddddd" {
		t.Fatal("unstable checkpoint ordering")
	}
}

func TestUnattributedContextNeedsAMissingCheckpointBetweenBothBounds(t *testing.T) {
	x := fixture(t, "copilot-compact.jsonl")
	for _, cps := range [][][]fixtureSession{
		{{{id: "s", start: 1, data: prefix(x, 3)}}, {{id: "s", start: 5, data: x}}},
	} {
		got, err := RestoreWithOptions(context.Background(), fixtureStore{cps}, "base..HEAD", RestoreOptions{IncludeUnattributed: true})
		if err != nil || got.UnattributedEvents != 0 || len(got.Events) != 4 {
			t.Fatalf("%+v %v", got, err)
		}
	}
	r, spec, _ := missingMiddleRepo(t)
	r.git("update-ref", "-d", "refs/heads/entire/checkpoints/v1")
	// Keeping only the final valid snapshot gives no earlier raw-line bound.
	r.store("refs/entire/checkpoints/cc/cccccccccccc", checkpointFiles(t, "cccccccccccc", "", fixtureSession{id: "s", start: 5, data: x}))
	got, err := RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, spec, RestoreOptions{IncludeUnattributed: true})
	if err != nil || got.UnattributedEvents != 0 || got.MissingCheckpoints != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestUnattributedContextDoesNotBridgeAnIntentionalExclusion(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.commit("first\n\nEntire-Checkpoint: aaaaaaaaaaaa")
	r.commit("last\n\nEntire-Checkpoint: cccccccccccc")
	r.commit("missing after both bounds\n\nEntire-Checkpoint: dddddddddddd")
	x := fixture(t, "copilot-compact.jsonl")
	files := checkpointFiles(t, "aaaaaaaaaaaa", "aa/aaaaaaaaaa", fixtureSession{id: "s", start: 1, data: prefix(x, 3)})
	mergeFiles(files, checkpointFiles(t, "cccccccccccc", "cc/cccccccccc", fixtureSession{id: "s", start: 5, data: x}))
	r.store("refs/heads/entire/checkpoints/v1", files)
	got, err := RestoreWithOptions(context.Background(), GitStore{Dir: r.dir}, base+"..HEAD", RestoreOptions{IncludeUnattributed: true})
	if err != nil || len(got.Events) != 5 || got.UnattributedEvents != 0 || got.MissingCheckpoints != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, e := range got.Events {
		if strings.Contains(string(e.Raw), "excluded") {
			t.Fatal("an unrelated missing checkpoint widened an intentionally noncontiguous range")
		}
	}
}

func TestMissingMarkersBeforeAndAfterTheAvailableConversation(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.commit("missing before\n\nEntire-Checkpoint: aaaaaaaaaaaa")
	r.commit("available middle\n\nEntire-Checkpoint: bbbbbbbbbbbb")
	r.commit("missing after\n\nEntire-Checkpoint: cccccccccccc")
	r.store("refs/entire/checkpoints/bb/bbbbbbbbbbbb", checkpointFiles(t, "bbbbbbbbbbbb", "", fixtureSession{id: "s", data: []byte(compactEvent("2026-10-23T09:00:00Z", "available"))}))
	got, err := Restore(context.Background(), GitStore{Dir: r.dir}, base+"..HEAD")
	if err != nil || len(got.Events) != 3 || got.MissingCheckpoints != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got.Events[0].Error == nil || got.Events[0].Error.Placement != "before_next_checkpoint" || got.Events[1].Error != nil || got.Events[2].Error == nil || got.Events[2].Error.Placement != "after_previous_checkpoint" {
		t.Fatalf("%+v", got.Events)
	}
}
