package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ry023/entire-timeline/internal/summary"
)

func fakeCopilot(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".entire"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".entire/settings.json"), []byte(`{"summary_generation":{"provider":"copilot-cli","model":"configured-model"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TIMELINE_TEST_DIR/args"
pwd > "$TIMELINE_TEST_DIR/cwd"
printf '%s' "${ENTIRE_REPO_ROOT:-}${GIT_INDEX_FILE:-}${COPILOT_ALLOW_ALL:-}" > "$TIMELINE_TEST_DIR/inherited"
cat > "$TIMELINE_TEST_DIR/prompt"
printf 'called\n' >> "$TIMELINE_TEST_DIR/calls"
cat "$TIMELINE_TEST_DIR/response"
exit "${TIMELINE_TEST_EXIT:-0}"
`
	if err := os.WriteFile(filepath.Join(bin, "copilot"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TIMELINE_TEST_DIR", bin)
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	t.Setenv("COPILOT_ALLOW_ALL", "true")
	return bin
}

func TestSummarizeAndViewEndToEnd(t *testing.T) {
	dir, spec := cliRepo(t)
	bin := fakeCopilot(t, dir)
	response := "{\"id\":\"m3\",\"summary\":\"テスト追加完了\"}\n{\"id\":\"m1\",\"summary\":\"テスト作成の依頼\"}\n{\"id\":\"m2\",\"summary\":\"テストを準備\"}\n"
	os.WriteFile(filepath.Join(bin, "response"), []byte(response), 0600)
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"summarize", spec}, &out, &diag); code != 0 || out.Len() != 0 {
		t.Fatalf("%d %s %s", code, out.String(), diag.String())
	}
	for _, name := range []string{"prompt", "args", "cwd", "inherited"} {
		b, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "prompt":
			for _, want := range []string{"並行してテストを書いて。", "テストを準備します。", "テストを追加しました。", `"role":"assistant"`, `"session_id":"test-session"`} {
				if !bytes.Contains(b, []byte(want)) {
					t.Fatalf("missing %q", want)
				}
			}
			for _, excluded := range []string{"既存テストを確認する。", "SECRET TOOL RESULT", "go test ./...", "preserve future events"} {
				if bytes.Contains(b, []byte(excluded)) {
					t.Fatalf("sent excluded text %q", excluded)
				}
			}
		case "args":
			if !bytes.Contains(b, []byte("--model\nconfigured-model")) || !bytes.Contains(b, []byte("--deny-tool")) || !bytes.Contains(b, []byte("--disable-builtin-mcps")) || bytes.Contains(b, []byte("--allow-all")) {
				t.Fatalf("unexpected argv %s", b)
			}
		case "cwd":
			if strings.TrimSpace(string(b)) == dir {
				t.Fatal("generation cwd is source repository")
			}
		case "inherited":
			if len(b) != 0 {
				t.Fatalf("unsafe inherited environment %s", b)
			}
		}
	}
	out.Reset()
	diag.Reset()
	if Run(context.Background(), []string{spec, "--format", "jsonl"}, &out, &diag) != 0 {
		t.Fatal(diag.String())
	}
	count := 0
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if _, ok := e["summary"]; ok {
			count++
		}
	}
	if count != 3 || !bytes.Contains(out.Bytes(), []byte("既存テストを確認する。")) || !bytes.Contains(out.Bytes(), []byte("SECRET TOOL RESULT")) {
		t.Fatalf("unexpected view %s", out.String())
	}
	out.Reset()
	diag.Reset()
	if Run(context.Background(), []string{spec}, &out, &diag) != 0 || !strings.Contains(out.String(), "Summary: テスト追加完了") || !strings.Contains(out.String(), "テストを追加しました。") {
		t.Fatalf("%s %s", out.String(), diag.String())
	}
	out.Reset()
	diag.Reset()
	if Run(context.Background(), []string{"summarize", spec}, &out, &diag) != 0 || !strings.Contains(diag.String(), "reused 3") {
		t.Fatal(diag.String())
	}
	calls, _ := os.ReadFile(filepath.Join(bin, "calls"))
	if string(calls) != "called\n" {
		t.Fatalf("reused summaries triggered generation: %s", calls)
	}
	out.Reset()
	diag.Reset()
	if Run(context.Background(), []string{"summarize", spec, "--force"}, &out, &diag) != 0 {
		t.Fatal(diag.String())
	}
	calls, _ = os.ReadFile(filepath.Join(bin, "calls"))
	if string(calls) != "called\ncalled\n" {
		t.Fatalf("force didn't regenerate: %s", calls)
	}
}

func TestSummarizePartialFailureSaved(t *testing.T) {
	dir, spec := cliRepo(t)
	bin := fakeCopilot(t, dir)
	os.WriteFile(filepath.Join(bin, "response"), []byte("{\"id\":\"m1\",\"summary\":\"保存する成功分\"}\n{\"id\":"), 0600)
	t.Setenv("TIMELINE_TEST_EXIT", "3")
	var out, diag bytes.Buffer
	if Run(context.Background(), []string{"summarize", spec}, &out, &diag) != 1 || !strings.Contains(diag.String(), "Saved 1 summaries locally") || !strings.Contains(diag.String(), "missing 2") {
		t.Fatal(diag.String())
	}
	out.Reset()
	diag.Reset()
	if Run(context.Background(), []string{spec}, &out, &diag) != 0 || !strings.Contains(out.String(), "保存する成功分") {
		t.Fatal(out.String(), diag.String())
	}
	r, err := summary.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Destination(context.Background(), summary.Settings{}); err == nil {
		t.Fatal("test unexpectedly has a remote; generation must not require one")
	}
}
