package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ry023/entire-timeline/internal/summary"
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

func TestVerboseRestoresIndependentCheckpoints(t *testing.T) {
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
	// The different reasoning payloads must not stop either viewing or summary
	// generation. Six per-checkpoint conversation records are requested.
	bin := fakeCopilot(t, dir)
	var response bytes.Buffer
	for i := 1; i <= 6; i++ {
		json.NewEncoder(&response).Encode(map[string]string{"id": fmt.Sprintf("m%d", i), "summary": "要約"})
	}
	os.WriteFile(filepath.Join(bin, "response"), response.Bytes(), 0600)
	output := filepath.Join(t.TempDir(), "existing.jsonl")
	os.WriteFile(output, []byte("preserve existing export"), 0600)
	for _, args := range [][]string{{spec, "--verbose", "--format=jsonl", "--output", output}, {spec, "--verbose", "--filter=conversation", "--format=jsonl"}, {"summarize", spec, "--verbose"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 0 {
			t.Fatalf("%v: %s %s", args, out.String(), diag.String())
		}
		for _, want := range []string{"restore_mode=per_checkpoint", `restored_snapshot checkpoint="aaaaaaaaaaaa"`, `restored_snapshot checkpoint="bbbbbbbbbbbb"`, "restored checkpoints=2 sessions=1 events=12"} {
			if !strings.Contains(diag.String(), want) {
				t.Fatalf("missing %q: %s", want, diag.String())
			}
		}
		if strings.Contains(diag.String(), "cannot safely merge offsets") || strings.Contains(diag.String(), "prefix_mismatch") {
			t.Fatal(diag.String())
		}
		for _, secret := range []string{"PRIVATE_REWRITTEN_REASONING", "既存テストを確認する。", "SECRET TOOL RESULT"} {
			if strings.Contains(diag.String(), secret) {
				t.Fatalf("leaked %q: %s", secret, diag.String())
			}
		}
	}
	data, _ := os.ReadFile(output)
	if len(bytes.Split(bytes.TrimSpace(data), []byte{'\n'})) != 12 || !bytes.Contains(data, []byte(`"checkpoint_id":"aaaaaaaaaaaa"`)) || !bytes.Contains(data, []byte(`"checkpoint_id":"bbbbbbbbbbbb"`)) {
		t.Fatal("independent checkpoint export is incomplete")
	}
	stored := git(nil, "", "show", summary.MetadataRef+":summaries/copilot-cli/test-session.jsonl")
	lines := bytes.Split(bytes.TrimSpace(stored), []byte{'\n'})
	if len(lines) != 6 {
		t.Fatalf("same source lines overwrote summaries across checkpoints: %s", stored)
	}
	for _, line := range lines {
		var row summary.Record
		if err := json.Unmarshal(line, &row); err != nil || row.SchemaVersion != 2 || row.CheckpointID == "" {
			t.Fatal(string(line), err)
		}
	}
	var view, diag bytes.Buffer
	if Run(context.Background(), []string{spec, "--filter=conversation", "--format=jsonl"}, &view, &diag) != 0 || strings.Count(view.String(), `"summary":"要約"`) != 6 {
		t.Fatal(view.String(), diag.String())
	}
}
