package summary

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bareRemote(t *testing.T, repos ...*Repo) *Repo {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, r := range repos {
		git(t, r, "remote", "add", "origin", dir)
	}
	return &Repo{Dir: dir}
}
func save(t *testing.T, r *Repo, records ...Record) {
	t.Helper()
	if err := r.Save(context.Background(), records); err != nil {
		t.Fatal(err)
	}
}
func syncRepo(t *testing.T, r *Repo) string {
	t.Helper()
	state, err := r.Sync(context.Background(), Settings{PushSessions: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func summaryBlob(t *testing.T, r *Repo, session string) string {
	t.Helper()
	return git(t, r, "show", MetadataRef+":"+sessionPath("copilot-cli", session))
}

func TestSyncFetchReapplySessionFilesAndPush(t *testing.T) {
	a, b := newRepo(t), newRepo(t)
	remote := bareRemote(t, a, b)
	if state := syncRepo(t, b); !strings.Contains(state, "No summary") {
		t.Fatal(state)
	}
	first := record(event("same", 0, "A"), "A summary")
	save(t, a, first)
	syncRepo(t, a)
	if state := syncRepo(t, b); !strings.Contains(state, "fetched") {
		t.Fatal(state)
	}
	base := git(t, b, "rev-parse", MetadataRef)
	// Both machines have A. A adds B and another session; B independently adds C.
	save(t, a, record(event("same", 1, "B"), "remote B"), record(event("remote-only", 0, "D"), "keep remote session"))
	syncRepo(t, a)
	remoteBefore := git(t, remote, "rev-parse", MetadataRef)
	save(t, b, record(event("same", 2, "C"), "local C"), record(event("local-only", 0, "E"), "keep local session"))
	head := git(t, b, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(b.CommonDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	syncRepo(t, b)
	got := summaryBlob(t, remote, "same")
	if !strings.Contains(got, "A summary") || !strings.Contains(got, "local C") || strings.Contains(got, "remote B") {
		t.Fatalf("not a whole-session local win: %s", got)
	}
	if !strings.Contains(summaryBlob(t, remote, "remote-only"), "keep remote session") || !strings.Contains(summaryBlob(t, remote, "local-only"), "keep local session") {
		t.Fatal("lost a different session")
	}
	git(t, b, "merge-base", "--is-ancestor", remoteBefore, MetadataRef)
	git(t, b, "merge-base", "--is-ancestor", base, MetadataRef)
	if git(t, b, "rev-parse", "HEAD") != head {
		t.Fatal("moved code HEAD")
	}
	after, _ := os.ReadFile(filepath.Join(b.CommonDir, "index"))
	if string(index) != string(after) {
		t.Fatal("modified code index")
	}
	if got := git(t, b, "for-each-ref", "--format=%(refname)", "refs/entire-timeline/fetch/"); got != "" {
		t.Fatalf("fetch ref leaked: %s", got)
	}
	// A later fetch-only sync fast-forwards the local metadata branch.
	syncRepo(t, a)
	if git(t, a, "rev-parse", MetadataRef) != git(t, remote, "rev-parse", MetadataRef) {
		t.Fatal("did not fast-forward")
	}
}

func TestSyncUnrelatedHistoriesAndHooksDoNotRecurse(t *testing.T) {
	a, b := newRepo(t), newRepo(t)
	remote := bareRemote(t, a, b)
	hooks := t.TempDir()
	fail := []byte("#!/bin/sh\nprintf 'must not run' >&2\nexit 97\n")
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), fail, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, a, "config", "core.hooksPath", hooks)
	save(t, a, record(event("a", 0, "A"), "A"))
	save(t, b, record(event("b", 0, "B"), "B"))
	syncRepo(t, a)
	syncRepo(t, b)
	if summaryBlob(t, remote, "a") == "" || summaryBlob(t, remote, "b") == "" {
		t.Fatal("lost independent initial history")
	}
	before := git(t, remote, "rev-parse", MetadataRef)
	save(t, a, record(event("a", 1, "AA"), "AA"))
	state, err := a.Sync(context.Background(), Settings{PushSessions: false}, "")
	if err != nil || !strings.Contains(state, "disabled") || before != git(t, remote, "rev-parse", MetadataRef) {
		t.Fatal(state, err)
	}
	state, err = a.Sync(context.Background(), Settings{PushSessions: true}, "some-other-remote")
	if err != nil || !strings.Contains(state, "Skipping") || before != git(t, remote, "rev-parse", MetadataRef) {
		t.Fatal(state, err)
	}
}

func TestRemoteElectionAndSeparateCheckpointRepository(t *testing.T) {
	r := newRepo(t)
	git(t, r, "remote", "add", "work", "git@github.com:me/code.git")
	git(t, r, "remote", "add", "origin", "git@github.com:me/main.git")
	get := func(s Settings) Destination {
		t.Helper()
		d, err := r.Destination(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := get(Settings{}); d.Remote != "origin" {
		t.Fatal(d)
	}
	os.WriteFile(filepath.Join(r.CommonDir, "entire-checkpoint-sync-remotes.json"), []byte(`{"remotes":["missing","work"]}`), 0600)
	if d := get(Settings{}); d.Remote != "work" {
		t.Fatal(d)
	}
	if d := get(Settings{PushRemote: "origin"}); d.Remote != "origin" {
		t.Fatal(d)
	}
	if _, err := r.Destination(context.Background(), Settings{PushRemote: "missing"}); err == nil {
		t.Fatal("invalid remote silently fell back")
	}
	s := Settings{CheckpointRemote: &CheckpointRemote{"github", "me/history"}}
	if d := get(s); d.Push != "git@github.com:me/history.git" || d.Fetch != d.Push {
		t.Fatal(d)
	}
	s.CheckpointRemote.Repo = "someone-else/history"
	if d := get(s); d.Push != "git@github.com:me/code.git" {
		t.Fatalf("didn't ignore inherited remote: %+v", d)
	}
	s.localCheckpointRemote = true
	if d := get(s); d.Push != "git@github.com:someone-else/history.git" {
		t.Fatal(d)
	}
	t.Setenv("ENTIRE_CHECKPOINT_TOKEN", "secret-not-in-url")
	if d := get(s); d.Push != "https://github.com/someone-else/history.git" || d.TokenUser != "x-access-token" || strings.Contains(d.Push, "secret") {
		t.Fatal(d)
	}
	for _, tc := range []struct {
		base string
		cp   CheckpointRemote
		want string
	}{
		{"ssh://git@git.example.com:2222/team/code.git", CheckpointRemote{"gitlab", "team/sub/history"}, "ssh://git@git.example.com:2222/team/sub/history.git"},
		{"https://user:password@github.com/team/code.git", CheckpointRemote{"gitlab", "team/history"}, "https://gitlab.com/team/history.git"},
	} {
		got, err := deriveURL(tc.base, tc.cp)
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
}

func TestLocalChangesRetainIntentOfRevertedSession(t *testing.T) {
	r := newRepo(t)
	row := record(event("s", 0, "A"), "original")
	save(t, r, row)
	base := git(t, r, "rev-parse", MetadataRef)
	changed := row
	changed.Summary = "changed"
	save(t, r, changed)
	save(t, r, row)
	local := git(t, r, "rev-parse", MetadataRef)
	updates, err := r.localChanges(context.Background(), base, local)
	if err != nil || len(updates) != 1 || !strings.Contains(string(updates[sessionPath(row.Agent, row.SessionID)]), "original") {
		t.Fatal(updates, err)
	}
}
