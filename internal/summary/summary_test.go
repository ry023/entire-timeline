package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ry023/entire-timeline/internal/timeline"
)

func newRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--initial-branch=main", dir)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, b)
	}
	r, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	git(t, r, "config", "user.name", "Timeline Test")
	git(t, r, "config", "user.email", "test@example.invalid")
	git(t, r, "config", "commit.gpgsign", "false")
	git(t, r, "config", "core.hooksPath", "/dev/null")
	if err := os.WriteFile(filepath.Join(dir, "code.txt"), []byte("code\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, r, "add", "code.txt")
	git(t, r, "commit", "-m", "code")
	return r
}
func git(t *testing.T, r *Repo, args ...string) string {
	t.Helper()
	b, err := r.git(context.Background(), nil, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
func event(session string, line int, text string) timeline.Event {
	return timeline.Event{SessionID: session, Agent: "copilot-cli", Role: "user", Type: "user.message", Line: line, Parts: []timeline.Part{{Kind: "text", Text: text}}}
}
func record(e timeline.Event, text string) Record {
	return Record{1, e.SessionID, e.Agent, e.Line, e.ID, TextHash(e.ConversationText()), text, Generation{"copilot-cli", "", PromptVersion}, time.Now().UTC()}
}

type generatorFunc func(context.Context, string, string) ([]byte, error)

func (f generatorFunc) Generate(ctx context.Context, prompt, model string) ([]byte, error) {
	return f(ctx, prompt, model)
}
func promptMessages(t *testing.T, prompt string) []message {
	t.Helper()
	if !strings.HasPrefix(prompt, instructions) {
		t.Fatal("missing instructions")
	}
	var ms []message
	if err := json.Unmarshal([]byte(strings.TrimPrefix(prompt, instructions)), &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}
func response(t *testing.T, ms []message) []byte {
	t.Helper()
	var b bytes.Buffer
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Summarize {
			json.NewEncoder(&b).Encode(map[string]string{"id": ms[i].ID, "summary": "要約: " + ms[i].Text})
		}
	}
	return b.Bytes()
}

func TestBatchConversationOnlyAndReuse(t *testing.T) {
	a, b := event("s", 0, "依頼A"), event("s", 1, "応答B")
	b.Role = "assistant"
	b.Parts = append(b.Parts, timeline.Part{Kind: "reasoning", Text: "PRIVATE COT"}, timeline.Part{Kind: "tool_call", Data: json.RawMessage(`{"command":"SECRET TOOL"}`)})
	c := event("other", 0, "並行した依頼")
	existing := map[string]Record{Key(a.Agent, a.SessionID, a.Line): record(a, "既存要約")}
	calls := 0
	g := generatorFunc(func(_ context.Context, prompt, model string) ([]byte, error) {
		calls++
		if strings.Contains(prompt, "PRIVATE COT") || strings.Contains(prompt, "SECRET TOOL") {
			t.Fatal("excluded data sent to model")
		}
		ms := promptMessages(t, prompt)
		if len(ms) != 3 || ms[0].Summarize || !ms[1].Summarize || model != "test-model" {
			t.Fatalf("bad request: %+v", ms)
		}
		return response(t, ms), nil
	})
	unattributed := event("s", 2, "UNCONFIRMED CONTEXT")
	unattributed.Attribution = &timeline.Attribution{Status: "unattributed"}
	gap := timeline.Event{Type: "timeline.error", Error: &timeline.MissingCheckpointError{Code: "checkpoint_unavailable"}}
	result, err := Generate(context.Background(), []timeline.Event{a, gap, b, unattributed, c}, existing, g, Options{Model: "test-model"})
	if err != nil || result.Generated != 2 || result.Reused != 1 || calls != 1 || result.Missing != 0 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
	for _, r := range result.Records {
		if r.SessionID == "s" && !r.Matches(b) {
			t.Fatal("response order mapped to wrong source event")
		}
	}
}

func TestPartialResponsesAndProviderFailure(t *testing.T) {
	ms := []timeline.Event{event("s", 0, "A"), event("s", 1, "B"), event("s", 2, "C")}
	g := generatorFunc(func(context.Context, string, string) ([]byte, error) {
		return []byte("{\"id\":\"m1\",\"summary\":\"成功\"}\n{\"id\":\"unknown\",\"summary\":\"不正\"}\n{\"id\":\"m2\",\"summary\":\"曖昧\"}\n{\"id\":\"m2\",\"summary\":\"重複\"}\n{\"id\":"), errors.New("provider stopped")
	})
	result, err := Generate(context.Background(), ms, nil, g, Options{})
	if err == nil || result.Generated != 1 || result.Missing != 2 || result.Records[0].SourceLine != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	r := newRepo(t)
	if err := r.Save(context.Background(), result.Records); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.Load(context.Background(), ms)
	if err != nil || len(loaded) != 1 {
		t.Fatal(loaded, err)
	}
	calls := 0
	g = generatorFunc(func(_ context.Context, prompt, _ string) ([]byte, error) {
		calls++
		return response(t, promptMessages(t, prompt)), nil
	})
	result, err = Generate(context.Background(), ms, loaded, g, Options{})
	if err != nil || result.Generated != 2 || result.Reused != 1 || calls != 1 {
		t.Fatal(result, err)
	}
}

func TestBatchSplittingAndOversizedMessagesNeverTruncate(t *testing.T) {
	var events []timeline.Event
	for i := 0; i < 12; i++ {
		events = append(events, event("s", i, strings.Repeat(fmt.Sprint(i), 200)))
	}
	calls := 0
	g := generatorFunc(func(_ context.Context, prompt, _ string) ([]byte, error) {
		calls++
		return response(t, promptMessages(t, prompt)), nil
	})
	result, err := Generate(context.Background(), events, nil, g, Options{BatchBytes: len(instructions) + 1000})
	if err != nil || result.Generated != 12 || calls < 2 {
		t.Fatal(result, err, calls)
	}
	long := strings.Repeat("長文", 10000)
	g = generatorFunc(func(_ context.Context, prompt, _ string) ([]byte, error) {
		ms := promptMessages(t, prompt)
		if ms[0].Text != long {
			t.Fatal("long message truncated")
		}
		return []byte("{\"id\":\"m1\",\"summary\":\"長文の要約\"}\n"), nil
	})
	result, err = Generate(context.Background(), []timeline.Event{event("s", 0, long)}, nil, g, Options{BatchBytes: len(instructions) + 1000})
	if err != nil || result.Generated != 1 {
		t.Fatal(result, err)
	}
}

func TestLocalGitPersistencePreservesCodeAndOtherRecords(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	head, status := git(t, r, "rev-parse", "HEAD"), git(t, r, "status", "--porcelain")
	// Deliberately leave staged and unstaged user changes in place.
	os.WriteFile(filepath.Join(r.Dir, "code.txt"), []byte("staged\n"), 0600)
	git(t, r, "add", "code.txt")
	os.WriteFile(filepath.Join(r.Dir, "code.txt"), []byte("unstaged\n"), 0600)
	indexBefore := git(t, r, "write-tree")
	dirty := git(t, r, "status", "--porcelain")
	a, b, c := event("session/../%日本語", 0, "A"), event("session/../%日本語", 1, "B"), event("session/../%日本語", 2, "C")
	if err := r.Save(ctx, []Record{record(a, "要約A"), record(b, "要約B")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, []Record{record(c, "要約C")}); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.Load(ctx, []timeline.Event{a, b, c})
	if err != nil || len(loaded) != 3 {
		t.Fatal(loaded, err)
	}
	view := timeline.Timeline{Events: []timeline.Event{a, b, c}}
	Attach(&view, loaded)
	if view.Events[0].Summary != "要約A" || view.Events[2].Summary != "要約C" {
		t.Fatal(view)
	}
	view.Events[0].Parts[0].Text = "changed"
	Attach(&view, loaded)
	if loaded[Key(a.Agent, a.SessionID, a.Line)].Matches(view.Events[0]) {
		t.Fatal("stale summary matched rewritten body")
	}
	if git(t, r, "rev-parse", "HEAD") != head || git(t, r, "write-tree") != indexBefore || git(t, r, "status", "--porcelain") != dirty || status != "" {
		t.Fatal("metadata save changed user checkout/index")
	}
	path := sessionPath(a.Agent, a.SessionID)
	if strings.Contains(path, "../") || !strings.Contains(path, "%2F") {
		t.Fatal(path)
	}
	git(t, r, "cat-file", "blob", MetadataRef+":"+path)
}

func writeSettings(t *testing.T, r *Repo, name, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(r.Dir, ".entire"), 0700)
	if err := os.WriteFile(filepath.Join(r.Dir, ".entire", name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestEntireSettingsMergeAndReadOnly(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	writeSettings(t, r, "settings.json", `{"unrelated_future_key":true,"summary_generation":{"provider":"claude-code","model":"old-model"},"summary_timeout_seconds":30,"strategy_options":{"push_sessions":false}}`)
	local := `{"summary_generation":{"provider":"copilot-cli"}}`
	writeSettings(t, r, "settings.local.json", local)
	s, err := r.LoadSettings(ctx)
	if err != nil || s.Model != "" || s.Provider != "copilot-cli" || s.PushSessions || s.Timeout != 30*time.Second {
		t.Fatal(s, err)
	}
	if err := s.CheckGenerator(); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, r, "settings.local.json", `{"summary_generation":{"provider":"copilot-cli","model":"new"},"strategy_options":{"push_sessions":true}}`)
	s, err = r.LoadSettings(ctx)
	if err != nil || s.Model != "new" || !s.PushSessions {
		t.Fatal(s, err)
	}
	git(t, r, "add", ".entire/settings.local.json")
	s, err = r.LoadSettings(ctx)
	if err != nil || s.Provider != "claude-code" || s.CheckGenerator() == nil {
		t.Fatal("tracked local override was not ignored", s, err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, ".entire", "settings.json")); !bytes.Contains(b, []byte(`"old-model"`)) {
		t.Fatal("settings modified")
	}
}

func TestProviderErrorsAndMissingConfiguration(t *testing.T) {
	r := newRepo(t)
	s, err := r.LoadSettings(context.Background())
	if err != nil || s.CheckGenerator() == nil {
		t.Fatal(s, err)
	}
	_, err = Generate(context.Background(), []timeline.Event{{Agent: "claude-code", SessionID: "s", Role: "user", Parts: []timeline.Part{{Kind: "text", Text: "hello"}}}}, nil, nil, Options{})
	if err == nil {
		t.Fatal("unsupported source agent accepted")
	}
}

func TestConcurrentLocalSavesKeepBothMessages(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(line int) {
			defer wg.Done()
			<-start
			results <- r.Save(ctx, []Record{record(event("same", line, fmt.Sprint(line)), fmt.Sprint(line))})
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.Load(ctx, []timeline.Event{event("same", 0, "0")})
	if err != nil || len(rows) != 2 {
		t.Fatalf("lost concurrent write: %+v %v", rows, err)
	}
}

func TestMetadataCheckoutIsNeverMovedUnderAnotherWorktree(t *testing.T) {
	r := newRepo(t)
	save(t, r, record(event("s", 0, "A"), "A"))
	worktree := filepath.Join(t.TempDir(), "inspect-metadata")
	git(t, r, "worktree", "add", worktree, "entire-timeline/metadata/v1")
	before := git(t, r, "rev-parse", MetadataRef)
	if err := r.Save(context.Background(), []Record{record(event("s", 1, "B"), "B")}); err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatal(err)
	}
	if _, err := r.Sync(context.Background(), Settings{PushSessions: true}, ""); err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatal(err)
	}
	if after := git(t, r, "rev-parse", MetadataRef); after != before {
		t.Fatal("moved checked-out metadata HEAD")
	}
}
