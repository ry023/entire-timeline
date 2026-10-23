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

func gapCLIRepo(t *testing.T) (string, string) {
	t.Helper()
	dir, spec := cliRepo(t)
	git := func(input []byte, index string, args ...string) string {
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
		return strings.TrimSpace(string(out))
	}
	git(nil, "", "commit", "--allow-empty", "-m", "missing middle\n\nEntire-Checkpoint: bbbbbbbbbbbb")
	git(nil, "", "commit", "--allow-empty", "-m", "available later\n\nEntire-Checkpoint: cccccccccccc")
	old := git(nil, "", "rev-parse", "refs/entire/checkpoints/aa/aaaaaaaaaaaa")
	index := filepath.Join(t.TempDir(), "index")
	git(nil, index, "read-tree", old)
	for _, name := range []string{"metadata.json", "0/metadata.json", "0/full.jsonl"} {
		data := []byte(git(nil, "", "show", old+":"+name))
		if name == "0/full.jsonl" {
			data = append(data, []byte("\n"+strings.Join([]string{
				`{"type":"user.message","timestamp":"2026-08-21T09:00:01Z","data":{"content":"UNATTRIBUTED REQUEST"}}`,
				`{"type":"assistant.message","timestamp":"2026-08-21T09:00:02Z","data":{"content":"UNATTRIBUTED RESPONSE"}}`,
				`{"type":"user.message","timestamp":"2026-08-21T09:00:03Z","data":{"content":"CONFIRMED CONTINUATION"}}`,
			}, "\n")+"\n")...)
		} else {
			data = bytes.ReplaceAll(data, []byte("aaaaaaaaaaaa"), []byte("cccccccccccc"))
			if name == "0/metadata.json" {
				data = bytes.ReplaceAll(data, []byte(`"checkpoint_transcript_start":1`), []byte(`"checkpoint_transcript_start":9`))
			}
		}
		hash := git(data, "", "hash-object", "-w", "--stdin")
		git(nil, index, "update-index", "--cacheinfo", "100644,"+hash+","+name)
	}
	tree := git(nil, index, "write-tree")
	hash := git(nil, "", "commit-tree", tree, "-m", "later checkpoint")
	git(nil, "", "update-ref", "refs/entire/checkpoints/cc/cccccccccccc", hash)
	return dir, spec
}

func TestCLIMissingCheckpointCompletesWithAnErrorRecord(t *testing.T) {
	dir, spec := gapCLIRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	for _, format := range []string{"text", "jsonl"} {
		for _, include := range []bool{false, true} {
			args := []string{spec, "--format", format, "--verbose"}
			if include {
				args = append(args, "--include-unattributed")
			}
			var out, diag bytes.Buffer
			if Run(context.Background(), args, &out, &diag) != 0 {
				t.Fatal(args, diag.String())
			}
			if !strings.Contains(diag.String(), "timeline is incomplete") || !strings.Contains(out.String(), "CONFIRMED CONTINUATION") {
				t.Fatal(out.String(), diag.String())
			}
			if strings.Contains(out.String(), "UNATTRIBUTED REQUEST") != include {
				t.Fatalf("incorrect optional context for %v: %s", args, out.String())
			}
			if format == "jsonl" {
				errors, unattributed := 0, 0
				for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
					var record map[string]any
					if err := json.Unmarshal(line, &record); err != nil {
						t.Fatal(err, string(line))
					}
					if record["type"] == "timeline.error" {
						errors++
						gap := record["error"].(map[string]any)
						if gap["code"] != "checkpoint_unavailable" || gap["checkpoint_id"] != "bbbbbbbbbbbb" {
							t.Fatal(record)
						}
					}
					if record["attribution"] != nil {
						unattributed++
						if len(record["checkpoints"].([]any)) != 0 {
							t.Fatal("invented checkpoint attribution")
						}
					}
				}
				if errors != 1 || (include && unattributed != 2) || (!include && unattributed != 0) {
					t.Fatal(errors, unattributed, out.String())
				}
			} else if !strings.Contains(out.String(), "[CHECKPOINT GAP]") || (include && !strings.Contains(out.String(), "[UNATTRIBUTED CONTEXT]")) {
				t.Fatal(out.String())
			}
		}
	}
	// Explicit false still excludes context, and successful incomplete exports
	// atomically replace an existing output with valid JSONL including the gap.
	output := filepath.Join(t.TempDir(), "timeline.jsonl")
	os.WriteFile(output, []byte("previous export"), 0600)
	var out, diag bytes.Buffer
	if Run(context.Background(), []string{"--include-unattributed=false", spec, "--format=jsonl", "--output", output}, &out, &diag) != 0 || out.Len() > 0 {
		t.Fatal(out.String(), diag.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Contains(data, []byte(`"type":"timeline.error"`)) || bytes.Contains(data, []byte("UNATTRIBUTED REQUEST")) {
		t.Fatal(string(data), err)
	}
}

func TestSummarizeSkipsGapsAndUnconfirmedContext(t *testing.T) {
	dir, spec := gapCLIRepo(t)
	bin := fakeCopilot(t, dir)
	os.WriteFile(filepath.Join(bin, "response"), []byte("{\"id\":\"m1\",\"summary\":\"依頼\"}\n{\"id\":\"m2\",\"summary\":\"準備\"}\n{\"id\":\"m3\",\"summary\":\"完了\"}\n{\"id\":\"m4\",\"summary\":\"続きの依頼\"}\n"), 0600)
	var out, diag bytes.Buffer
	if Run(context.Background(), []string{"summarize", spec}, &out, &diag) != 0 || !strings.Contains(diag.String(), "Saved 4 summaries locally") || !strings.Contains(diag.String(), "timeline is incomplete") {
		t.Fatal(diag.String())
	}
	prompt, err := os.ReadFile(filepath.Join(bin, "prompt"))
	if err != nil || bytes.Contains(prompt, []byte("UNATTRIBUTED")) || bytes.Contains(prompt, []byte("checkpoint_unavailable")) || !bytes.Contains(prompt, []byte("CONFIRMED CONTINUATION")) {
		t.Fatal(string(prompt), err)
	}
}
