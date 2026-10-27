package summary

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ry023/entire-timeline/internal/timeline"
)

func scopedEvent(checkpoint, session string, line int, text string) timeline.Event {
	e := event(session, line, text)
	e.CheckpointID, e.Checkpoints = checkpoint, []string{checkpoint}
	return e
}

func TestCheckpointScopedGenerationKeepsSameLineDistinct(t *testing.T) {
	a, b := scopedEvent("a", "same", 9, "Checkpoint Aの依頼"), scopedEvent("b", "same", 9, "Checkpoint Bの応答")
	b.Role = "assistant"
	g := generatorFunc(func(_ context.Context, prompt, _ string) ([]byte, error) {
		ms := promptMessages(t, prompt)
		if len(ms) != 2 || ms[0].CheckpointID != "a" || ms[1].CheckpointID != "b" {
			t.Fatalf("%+v", ms)
		}
		return response(t, ms), nil
	})
	result, err := Generate(context.Background(), []timeline.Event{a, b}, nil, g, Options{})
	if err != nil || result.Generated != 2 || result.Records[0].Key() == result.Records[1].Key() {
		t.Fatal(result, err)
	}
	for _, row := range result.Records {
		if row.SchemaVersion != 2 || row.CheckpointID == "" {
			t.Fatalf("%+v", row)
		}
	}
	r := newRepo(t)
	if err := r.Save(context.Background(), result.Records); err != nil {
		t.Fatal(err)
	}
	rows, err := r.Load(context.Background(), []timeline.Event{a, b})
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	view := timeline.Timeline{Events: []timeline.Event{a, b}}
	Attach(&view, rows)
	if view.Events[0].Summary != "要約: Checkpoint Aの依頼" || view.Events[1].Summary != "要約: Checkpoint Bの応答" {
		t.Fatal(view.Events)
	}
	if rows[eventKey(a)].Matches(b) || rows[eventKey(b)].Matches(a) {
		t.Fatal("cross-checkpoint reuse")
	}
}

func TestLegacyRecordsAreRetainedAndReusedOnlyWithEventIdentity(t *testing.T) {
	oldA, oldB := event("s", 2, "既存の応答"), event("s", 3, "IDのない依頼")
	oldA.ID = "stable-event-id"
	legacyA, legacyB := record(oldA, "既存の要約"), record(oldB, "IDなしの旧要約")
	r := newRepo(t)
	if err := r.Save(context.Background(), []Record{legacyA, legacyB}); err != nil {
		t.Fatal(err)
	}
	a, b := oldA, oldB
	a.CheckpointID, b.CheckpointID = "a", "b"
	rows, err := r.Load(context.Background(), []timeline.Event{a, b})
	if err != nil {
		t.Fatal(err)
	}
	view := timeline.Timeline{Events: []timeline.Event{a, b}}
	Attach(&view, rows)
	if view.Events[0].Summary != legacyA.Summary || view.Events[1].Summary != "" {
		t.Fatal("legacy summaries were reused without matching event identity", view.Events)
	}
	g := generatorFunc(func(_ context.Context, prompt, _ string) ([]byte, error) {
		ms := promptMessages(t, prompt)
		if len(ms) != 2 || ms[0].Summarize || !ms[1].Summarize {
			t.Fatal(ms)
		}
		return response(t, ms), nil
	})
	result, err := Generate(context.Background(), view.Events, rows, g, Options{})
	if err != nil || result.Generated != 1 || result.Reused != 1 || result.Records[0].SchemaVersion != 2 {
		t.Fatal(result, err)
	}
	if err := r.Save(context.Background(), result.Records); err != nil {
		t.Fatal(err)
	}
	rows, err = r.Load(context.Background(), view.Events)
	if err != nil || len(rows) != 3 || rows[legacyA.Key()].Summary != legacyA.Summary || rows[legacyB.Key()].Summary != legacyB.Summary {
		t.Fatal("legacy data was removed", rows, err)
	}
	never := generatorFunc(func(context.Context, string, string) ([]byte, error) {
		t.Fatal("reusable records called the provider")
		return nil, nil
	})
	result, err = Generate(context.Background(), view.Events, rows, never, Options{})
	if err != nil || result.Reused != 2 || result.Requests != 0 {
		t.Fatal(result, err)
	}
	changedID := a
	changedID.ID = "different-event"
	if legacyA.Matches(changedID) {
		t.Fatal("legacy ID mismatch was ignored")
	}
}

func TestConcurrentScopedSavesAndRemoteRoundTrip(t *testing.T) {
	a, b := scopedEvent("a", "same", 1, "A"), scopedEvent("b", "same", 1, "B")
	r := newRepo(t)
	var wg sync.WaitGroup
	start, errs := make(chan struct{}), make(chan error, 2)
	for _, e := range []timeline.Event{a, b} {
		wg.Add(1)
		go func(e timeline.Event) {
			defer wg.Done()
			<-start
			errs <- r.Save(context.Background(), []Record{record(e, e.ConversationText())})
		}(e)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	other := newRepo(t)
	bareRemote(t, r, other)
	syncRepo(t, r)
	syncRepo(t, other)
	rows, err := other.Load(context.Background(), []timeline.Event{a, b})
	if err != nil || len(rows) != 2 || !rows[eventKey(a)].Matches(a) || !rows[eventKey(b)].Matches(b) {
		t.Fatal("scoped rows lost in persistence or sync", rows, err)
	}
}

func TestInvalidScopedSchemasDoNotOverwriteStoredRecords(t *testing.T) {
	r := newRepo(t)
	e := scopedEvent("a", "s", 0, "body")
	valid := record(e, "summary")
	if err := r.Save(context.Background(), []Record{valid}); err != nil {
		t.Fatal(err)
	}
	before := git(t, r, "rev-parse", MetadataRef)
	for _, mutate := range []func(*Record){func(r *Record) { r.CheckpointID = "" }, func(r *Record) { r.SchemaVersion = 1 }, func(r *Record) { r.SchemaVersion = 3 }} {
		bad := valid
		mutate(&bad)
		if err := r.Save(context.Background(), []Record{bad}); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatal(err)
		}
	}
	if git(t, r, "rev-parse", MetadataRef) != before {
		t.Fatal("invalid schema changed the branch")
	}
}
