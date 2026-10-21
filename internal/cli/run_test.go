package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build a real Git repository using public checkpoint v1 files. No Entire
// installation, agent session, credentials or network is needed by these tests.
func cliRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(input []byte, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Stdin = bytes.NewReader(input)
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git(nil, "init", "--initial-branch=main")
	git(nil, "config", "user.name", "Timeline Test")
	git(nil, "config", "user.email", "test@example.invalid")
	git(nil, "config", "core.hooksPath", "/dev/null")
	git(nil, "config", "commit.gpgsign", "false")
	git(nil, "commit", "--allow-empty", "-m", "base")
	base := git(nil, "rev-parse", "HEAD")
	git(nil, "commit", "--allow-empty", "-m", "work\n\nEntire-Checkpoint: aaaaaaaaaaaa")
	raw, err := os.ReadFile("../timeline/testdata/copilot-native.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("{\"type\":\"future.event\",\"data\":{\"text\":\"preserve future events\"}}\n")...)
	// This commit's tree contains only checkpoint metadata; HEAD remains code.
	files := map[string][]byte{
		"metadata.json":   []byte(`{"checkpoint_id":"aaaaaaaaaaaa","sessions":[{"metadata":"/0/metadata.json","transcript":"/0/full.jsonl"}]}`),
		"0/metadata.json": []byte(`{"checkpoint_id":"aaaaaaaaaaaa","session_id":"test-session","agent":"Copilot CLI","checkpoint_transcript_start":1}`),
		"0/full.jsonl":    raw,
	}
	index := filepath.Join(t.TempDir(), "index")
	indexed := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	indexed("read-tree", "--empty")
	for name, data := range files {
		hash := git(data, "hash-object", "-w", "--stdin")
		indexed("update-index", "--add", "--cacheinfo", "100644,"+hash+","+name)
	}
	tree := indexed("write-tree")
	hash := git(nil, "commit-tree", tree, "-m", "checkpoint")
	git(nil, "update-ref", "refs/entire/checkpoints/aa/aaaaaaaaaaaa", hash)
	return dir, base + "..HEAD"
}

func TestCLIOutputAndArguments(t *testing.T) {
	dir, spec := cliRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	for _, args := range [][]string{{spec}, {spec, "--include-tools"}, {spec, "--format", "jsonl"}, {"--format=jsonl", spec}, {spec, "--width", "32"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "have no timestamp") {
			t.Fatal("missing timestamp diagnostic")
		}
		if strings.Contains(strings.Join(args, " "), "jsonl") {
			lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte{'\n'})
			if len(lines) != 6 {
				t.Fatalf("events=%d", len(lines))
			}
			for _, line := range lines {
				if !json.Valid(line) {
					t.Fatalf("non-JSON on stdout: %s", line)
				}
			}
		} else {
			if !strings.Contains(stdout.String(), "テストを追加しました。") {
				t.Fatal("missing conversation")
			}
			if strings.Contains(strings.Join(args, " "), "include-tools") && !strings.Contains(stdout.String(), "SECRET TOOL RESULT") {
				t.Fatal("missing tool detail")
			}
		}
	}
	var stdout, stderr bytes.Buffer
	output := filepath.Join(t.TempDir(), "timeline.jsonl")
	if Run(context.Background(), []string{spec, "--format", "jsonl", "--output", output}, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	b, err := os.ReadFile(output)
	if err != nil || !bytes.Contains(b, []byte(`"session_id":"test-session"`)) || stdout.Len() != 0 {
		t.Fatalf("output=%s stdout=%s err=%v", b, stdout.String(), err)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("export not private")
	}
}

func TestCLIHelpAndErrors(t *testing.T) {
	dir, spec := cliRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	var out, diag bytes.Buffer
	if Run(context.Background(), []string{"--help"}, &out, &diag) != 0 || !strings.Contains(out.String(), "Usage:") || diag.Len() != 0 {
		t.Fatal("help failed")
	}
	for _, args := range [][]string{nil, {spec, "extra"}, {spec, "--format", "xml"}, {spec, "--width", "0"}, {spec, "--unknown"}, {spec, "--output"}, {"missing..HEAD"}, {"HEAD"}, {spec, "--output", "/nonexistent/directory/export"}} {
		out.Reset()
		diag.Reset()
		if Run(context.Background(), args, &out, &diag) == 0 || out.Len() != 0 || diag.Len() == 0 {
			t.Fatalf("bad CLI result for %v: %q %q", args, out.String(), diag.String())
		}
	}
	output := filepath.Join(t.TempDir(), "existing.jsonl")
	os.WriteFile(output, []byte("keep existing export"), 0600)
	Run(context.Background(), []string{"missing..HEAD", "--output", output}, &out, &diag)
	b, _ := os.ReadFile(output)
	if string(b) != "keep existing export" {
		t.Fatal("failed restoration overwrote output")
	}
}

func TestEntirePluginInvocation(t *testing.T) {
	entire, err := exec.LookPath("entire")
	if err != nil {
		t.Skip("optional plugin smoke test: Entire CLI is not installed")
	}
	dir, spec := cliRepo(t)
	bin := t.TempDir()
	binary := filepath.Join(bin, "entire-timeline")
	build := exec.Command("go", "build", "-o", binary, "./cmd/entire-timeline")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v %s", err, out)
	}
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	var want, diagnostics bytes.Buffer
	if Run(context.Background(), []string{spec, "--format", "jsonl"}, &want, &diagnostics) != 0 {
		t.Fatal(diagnostics.String())
	}
	// Invoke from a nested directory to exercise Entire's environment contract.
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(entire, "timeline", spec, "--format", "jsonl")
	cmd.Dir = nested
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("Entire plugin: %v %s", err, stderr.String())
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("plugin output differs:\n%s\n%s", got, want.Bytes())
	}
}
