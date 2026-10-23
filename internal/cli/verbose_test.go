package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerboseJSONLStaysClean(t *testing.T) {
	dir, spec := cliRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	var plain, plainDiag bytes.Buffer
	if Run(context.Background(), []string{spec, "--format=jsonl"}, &plain, &plainDiag) != 0 {
		t.Fatal(plainDiag.String())
	}
	for _, args := range [][]string{{spec, "--verbose", "--format=jsonl"}, {"--verbose", "--format=jsonl", spec}, {spec, "--verbose=false", "--format=jsonl"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 0 || !bytes.Equal(out.Bytes(), plain.Bytes()) {
			t.Fatal(args, out.String(), diag.String())
		}
		if strings.Contains(strings.Join(args, " "), "verbose=false") {
			if bytes.Contains(diag.Bytes(), []byte("verbose:")) {
				t.Fatal(diag.String())
			}
		} else if !strings.Contains(diag.String(), "restored checkpoints=1 sessions=1 events=6") {
			t.Fatal(diag.String())
		}
		for _, secret := range []string{"並行してテストを書いて。", "既存テストを確認する。", "SECRET TOOL RESULT", "go test ./..."} {
			if strings.Contains(diag.String(), secret) {
				t.Fatalf("leaked %q: %s", secret, diag.String())
			}
		}
	}
}

func TestVerboseExplainsRealCheckpointMismatch(t *testing.T) {
	dir, spec := cliRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	git := func(input []byte, index string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = bytes.NewReader(input)
		if index != "" {
			cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	old := strings.TrimSpace(string(git(nil, "", "rev-parse", "refs/entire/checkpoints/aa/aaaaaaaaaaaa")))
	index := filepath.Join(t.TempDir(), "index")
	git(nil, index, "read-tree", old)
	for _, name := range []string{"metadata.json", "0/metadata.json", "0/full.jsonl"} {
		data := git(nil, "", "show", old+":"+name)
		data = bytes.ReplaceAll(data, []byte("aaaaaaaaaaaa"), []byte("bbbbbbbbbbbb"))
		data = bytes.ReplaceAll(data, []byte("既存テストを確認する。"), []byte("PRIVATE_REWRITTEN_REASONING"))
		hash := strings.TrimSpace(string(git(data, "", "hash-object", "-w", "--stdin")))
		git(nil, index, "update-index", "--cacheinfo", "100644,"+hash+","+name)
	}
	tree := strings.TrimSpace(string(git(nil, index, "write-tree")))
	hash := strings.TrimSpace(string(git(nil, "", "commit-tree", tree, "-m", "checkpoint")))
	git(nil, "", "update-ref", "refs/entire/checkpoints/bb/bbbbbbbbbbbb", hash)
	git(nil, "", "commit", "--allow-empty", "-m", "more work\n\nEntire-Checkpoint: bbbbbbbbbbbb")
	// Both viewing and generation diagnose restore failure before any output or
	// provider execution. A private output file is never replaced on failure.
	os.MkdirAll(filepath.Join(dir, ".entire"), 0700)
	os.WriteFile(filepath.Join(dir, ".entire/settings.json"), []byte(`{"summary_generation":{"provider":"copilot-cli"}}`), 0600)
	output := filepath.Join(t.TempDir(), "existing.jsonl")
	os.WriteFile(output, []byte("preserve existing export"), 0600)
	for _, args := range [][]string{{spec, "--verbose", "--format=jsonl", "--output", output}, {"summarize", spec, "--verbose"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 1 || out.Len() != 0 {
			t.Fatalf("%v: %s %s", args, out.String(), diag.String())
		}
		for _, want := range []string{"prefix_mismatch", "raw_line=2 physical_line=3", "decoded_json_equal=false", "changed_path=/data/reasoningText", "cannot safely merge offsets"} {
			if !strings.Contains(diag.String(), want) {
				t.Fatalf("missing %q: %s", want, diag.String())
			}
		}
		for _, secret := range []string{"PRIVATE_REWRITTEN_REASONING", "既存テストを確認する。", "SECRET TOOL RESULT"} {
			if strings.Contains(diag.String(), secret) {
				t.Fatalf("leaked %q: %s", secret, diag.String())
			}
		}
	}
	data, _ := os.ReadFile(output)
	if string(data) != "preserve existing export" {
		t.Fatal("failed verbose restore replaced export")
	}
}
