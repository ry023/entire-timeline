package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIConversationFilterAndDefaultCompatibility(t *testing.T) {
	dir, spec := cliRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	for _, format := range []string{"text", "jsonl"} {
		var original, originalDiag, all, allDiag bytes.Buffer
		if Run(context.Background(), []string{spec, "--format", format}, &original, &originalDiag) != 0 {
			t.Fatal(originalDiag.String())
		}
		if Run(context.Background(), []string{spec, "--filter=all", "--format", format}, &all, &allDiag) != 0 || !bytes.Equal(original.Bytes(), all.Bytes()) {
			t.Fatal("default/all output changed", allDiag.String())
		}
		for _, args := range [][]string{{spec, "--filter", "conversation", "--format", format}, {"--filter=conversation", "--format", format, spec}} {
			var out, diag bytes.Buffer
			if Run(context.Background(), args, &out, &diag) != 0 {
				t.Fatal(diag.String())
			}
			for _, want := range []string{"並行してテストを書いて。", "テストを準備します。", "テストを追加しました。"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q", want)
				}
			}
			for _, excluded := range []string{"既存テストを確認する。", "SECRET TOOL RESULT", "go test ./...", "preserve future events"} {
				if strings.Contains(out.String(), excluded) {
					t.Fatalf("leaked %q", excluded)
				}
			}
			if format == "jsonl" {
				lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
				if len(lines) != 3 {
					t.Fatalf("events=%d", len(lines))
				}
				for _, line := range lines {
					var e map[string]any
					if err := json.Unmarshal(line, &e); err != nil {
						t.Fatal(err)
					}
					if _, ok := e["raw"]; ok {
						t.Fatal("raw should be omitted from conversation projection")
					}
				}
			}
		}
	}
	for _, args := range [][]string{{spec, "--filter"}, {spec, "--filter", "unknown"}, {spec, "--filter=conversation", "--include-tools"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 1 || out.Len() != 0 || diag.Len() == 0 {
			t.Fatalf("%v: %s %s", args, out.String(), diag.String())
		}
	}
}

func TestCLIConversationFilterPreservesGapsAndOptionalContext(t *testing.T) {
	dir, spec := gapCLIRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	for _, format := range []string{"text", "jsonl"} {
		for _, include := range []bool{false, true} {
			args := []string{spec, "--filter=conversation", "--format", format}
			if include {
				args = append(args, "--include-unattributed")
			}
			var out, diag bytes.Buffer
			if Run(context.Background(), args, &out, &diag) != 0 {
				t.Fatal(diag.String())
			}
			if !strings.Contains(out.String(), "checkpoint_unavailable") || !strings.Contains(diag.String(), "incomplete") || strings.Contains(out.String(), "UNATTRIBUTED REQUEST") != include {
				t.Fatal(out.String(), diag.String())
			}
			if include && !strings.Contains(out.String(), map[string]string{"text": "[UNATTRIBUTED CONTEXT]", "jsonl": `"status":"unattributed"`}[format]) {
				t.Fatal("lost attribution marker")
			}
		}
	}
	output := filepath.Join(t.TempDir(), "conversation.jsonl")
	var out, diag bytes.Buffer
	if Run(context.Background(), []string{spec, "--filter=conversation", "--format=jsonl", "--output", output}, &out, &diag) != 0 || out.Len() != 0 {
		t.Fatal(diag.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Contains(data, []byte(`"type":"timeline.error"`)) || bytes.Contains(data, []byte("SECRET TOOL RESULT")) {
		t.Fatal(string(data), err)
	}
}
