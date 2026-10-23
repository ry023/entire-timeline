package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type testRepo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) testRepo {
	t.Helper()
	r := testRepo{t, t.TempDir()}
	r.git("init", "--initial-branch=main")
	r.git("config", "user.email", "test@example.invalid")
	r.git("config", "user.name", "Timeline Test")
	r.git("config", "core.hooksPath", "/dev/null")
	r.git("config", "commit.gpgsign", "false")
	r.commit("base")
	return r
}
func (r testRepo) command(input []byte, env []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func (r testRepo) git(args ...string) string { return r.command(nil, nil, args...) }
func (r testRepo) commit(message string) string {
	r.git("commit", "--allow-empty", "-m", message)
	return r.git("rev-parse", "HEAD")
}
func (r testRepo) store(ref string, files map[string][]byte) {
	r.t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(r.t.TempDir(), "index")}
	r.command(nil, env, "read-tree", "--empty")
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		hash := r.command(files[name], nil, "hash-object", "-w", "--stdin")
		r.command(nil, env, "update-index", "--add", "--cacheinfo", "100644,"+hash+","+name)
	}
	tree := r.command(nil, env, "write-tree")
	commit := r.command(nil, nil, "commit-tree", tree, "-m", "checkpoint fixture")
	r.git("update-ref", ref, commit)
}
func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func checkpointFiles(t *testing.T, id, prefix string, sessions ...fixtureSession) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	summary := Summary{CheckpointID: id}
	for i, s := range sessions {
		dir := path.Join(prefix, string(rune('0'+i)))
		p := SessionPaths{Metadata: "/" + path.Join(dir, "metadata.json"), Transcript: "/" + path.Join(dir, "full.jsonl")}
		summary.Sessions = append(summary.Sessions, p)
		agent := s.agent
		if agent == "" {
			agent = "Copilot CLI"
		}
		files[strings.TrimPrefix(p.Metadata, "/")] = marshal(t, Metadata{CheckpointID: id, SessionID: s.id, Agent: agent, Start: s.start})
		files[strings.TrimPrefix(p.Transcript, "/")] = s.data
	}
	files[path.Join(prefix, "metadata.json")] = marshal(t, summary)
	return files
}
func mergeFiles(dst, src map[string][]byte) {
	for k, v := range src {
		dst[k] = v
	}
}

func TestGitRangeSemantics(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-b", "feature")
	feature := r.commit("feature")
	r.git("checkout", "main")
	main := r.commit("main advanced")
	g := GitStore{Dir: r.dir}
	for spec, want := range map[string]string{"main..feature": main + ".." + feature, "main...feature": base + ".." + feature} {
		got, err := g.ResolveRange(context.Background(), spec)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", spec, got, err)
		}
	}
	for _, spec := range []string{"HEAD", "a....b", "--all..HEAD", "missing..HEAD"} {
		if _, err := g.ResolveRange(context.Background(), spec); err == nil {
			t.Fatal("accepted invalid range", spec)
		}
	}
}

func TestGitBranchCheckpointRestoration(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	id1, id2, id3 := "aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc"
	r.commit("first\n\nEntire-Checkpoint: " + id1)
	r.git("branch", "side")
	r.git("checkout", "side")
	r.commit("excluded\n\nEntire-Checkpoint: " + id2)
	r.git("checkout", "main")
	r.commit("ordinary commit without checkpoint")
	r.commit("last\n\nEntire-Checkpoint: " + id3)
	x := fixture(t, "copilot-compact.jsonl")
	y := fixture(t, "copilot-native.jsonl")
	files := checkpointFiles(t, id1, "aa/aaaaaaaaaa", fixtureSession{id: "x", start: 1, data: prefix(x, 3)})
	mergeFiles(files, checkpointFiles(t, id2, "bb/bbbbbbbbbb", fixtureSession{id: "x", start: 3, data: prefix(x, 5)}))
	mergeFiles(files, checkpointFiles(t, id3, "cc/cccccccccc", fixtureSession{id: "y", start: 1, data: y}, fixtureSession{id: "x", start: 5, data: x}))
	r.store("refs/heads/entire/checkpoints/v1", files)
	g := GitStore{Dir: r.dir}
	got, err := Restore(context.Background(), g, base+"..main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Checkpoints != 2 || len(got.Sessions) != 2 || len(got.Events) != 9 {
		t.Fatalf("checkpoints=%d sessions=%d events=%d", got.Checkpoints, len(got.Sessions), len(got.Events))
	}
	for _, e := range got.Events {
		if strings.Contains(string(e.Raw), "excluded") || strings.Contains(string(e.Raw), "before range") {
			t.Fatal("leaked out-of-range conversation")
		}
	}
	if _, err := Restore(context.Background(), g, "HEAD..HEAD"); err != nil {
		t.Fatal(err)
	}
}

func TestGitRefsAndTrailerlessLinks(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	sha := r.commit("trailerless linked commit")
	const id = "01KVBJCWYA4YW6J5M9GP655HZ9"
	for _, link := range []string{"linked_commits", "commit_sha"} {
		t.Run(link, func(t *testing.T) {
			files := checkpointFiles(t, id, "", fixtureSession{id: "s", start: 1, data: fixture(t, "copilot-native.jsonl")})
			var root map[string]any
			json.Unmarshal(files["metadata.json"], &root)
			if link == "linked_commits" {
				root[link] = []map[string]string{{"sha": sha}}
			} else {
				root[link] = sha
			}
			files["metadata.json"] = marshal(t, root)
			r.store("refs/entire/checkpoints/Z9/"+id, files)
			got, err := Restore(context.Background(), GitStore{Dir: r.dir}, base+"..HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if got.Checkpoints != 1 || len(got.Events) != 5 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestGitMissingCorruptAndChunkedTranscripts(t *testing.T) {
	const id = "aaaaaaaaaaaa"
	for _, mode := range []string{"missing checkpoint", "missing raw", "chunked", "missing chunk", "bad offset", "compact offset differs", "legacy offset", "invalid metadata", "unknown agent"} {
		t.Run(mode, func(t *testing.T) {
			r := newRepo(t)
			base := r.git("rev-parse", "HEAD")
			r.commit("work\n\nEntire-Checkpoint: " + id)
			g := GitStore{Dir: r.dir}
			x := fixture(t, "copilot-native.jsonl")
			files := checkpointFiles(t, id, "aa/aaaaaaaaaa", fixtureSession{id: "s", start: 1, data: x})
			full := "aa/aaaaaaaaaa/0/full.jsonl"
			meta := "aa/aaaaaaaaaa/0/metadata.json"
			switch mode {
			case "missing checkpoint":
				files = map[string][]byte{}
			case "missing raw":
				delete(files, full)
				files["aa/aaaaaaaaaa/0/transcript.jsonl"] = x
			case "chunked", "missing chunk":
				files[full] = prefix(x, 3)
				idx := ".001"
				if mode == "missing chunk" {
					idx = ".002"
				}
				files[full+idx] = bytes.Join(rawLines(x)[3:], []byte{'\n'})
			case "bad offset":
				files[meta] = marshal(t, Metadata{CheckpointID: id, SessionID: "s", Agent: "Copilot CLI", Start: 100})
			case "compact offset differs":
				compact := 99
				files[meta] = marshal(t, Metadata{CheckpointID: id, SessionID: "s", Agent: "Copilot CLI", Start: 1, CompactStart: &compact})
			case "legacy offset":
				files[meta] = marshal(t, Metadata{CheckpointID: id, SessionID: "s", Agent: "Copilot CLI", LegacyStart: 1})
			case "invalid metadata":
				files[meta] = []byte(`{"checkpoint_id":"wrong","session_id":"s","agent":"Copilot CLI"}`)
			case "unknown agent":
				files[meta] = marshal(t, Metadata{CheckpointID: id, SessionID: "s", Agent: "Codex"})
			}
			r.store("refs/heads/entire/checkpoints/v1", files)
			got, err := Restore(context.Background(), g, base+"..HEAD")
			if mode == "missing checkpoint" {
				if err != nil || got.MissingCheckpoints != 1 || len(got.Events) != 1 || got.Events[0].Error == nil || got.Events[0].Error.CheckpointID != id {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			valid := mode == "chunked" || mode == "compact offset differs" || mode == "legacy offset"
			if valid {
				if err != nil || len(got.Events) != 5 {
					t.Fatalf("events=%d err=%v", len(got.Events), err)
				}
			} else if err == nil {
				t.Fatal("expected explicit failure")
			}
		})
	}
}

func TestRemoteOnlyAndCustomMetadataRef(t *testing.T) {
	for _, ref := range []string{"refs/remotes/origin/entire/checkpoints/v1", "refs/heads/custom-checkpoints"} {
		t.Run(ref, func(t *testing.T) {
			r := newRepo(t)
			base := r.git("rev-parse", "HEAD")
			r.commit("work\n\nEntire-Checkpoint: aaaaaaaaaaaa")
			r.store(ref, checkpointFiles(t, "aaaaaaaaaaaa", "aa/aaaaaaaaaa", fixtureSession{id: "s", data: fixture(t, "copilot-native.jsonl")}))
			g := GitStore{Dir: r.dir}
			if strings.Contains(ref, "custom") {
				g.ExtraRefs = []string{ref}
			}
			got, err := Restore(context.Background(), g, base+"..HEAD")
			if err != nil || len(got.Events) != 6 {
				t.Fatalf("events=%d err=%v", len(got.Events), err)
			}
		})
	}
}

func TestGitNoCheckpointAndReadOnly(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.commit("untracked prose Entire-Checkpoint: aaaaaaaaaaaa without a trailer")
	before := r.git("show-ref")
	status := r.git("status", "--porcelain")
	got, err := Restore(context.Background(), GitStore{Dir: r.dir}, base+"..HEAD")
	if err != nil || len(got.Events) != 0 {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(before, r.git("show-ref")) || status != r.git("status", "--porcelain") {
		t.Fatal("repository modified")
	}
}

func TestCheckpointIDValidation(t *testing.T) {
	for _, id := range []string{"aaaaaaaaaaaa", "01KVBJCWYA4YW6J5M9GP655HZ9"} {
		if !checkpointID.MatchString(id) {
			t.Fatal("rejected checkpoint ID", id)
		}
	}
	for _, id := range []string{"short", "AAAAAAAAAAAA", "81KVBJCWYA4YW6J5M9GP655HZ9", "01KVBJCWYA4YW6J5M9GP655HZI"} {
		if checkpointID.MatchString(id) {
			t.Fatal("accepted invalid checkpoint ID", id)
		}
	}
}
