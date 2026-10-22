package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ry023/entire-timeline/internal/summary"
)

func TestAdditionalCommandHelpAndErrors(t *testing.T) {
	for _, args := range [][]string{{"summarize", "--help"}, {"remote", "--help"}, {"remote", "sync", "--help"}, {"hooks", "--help"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 0 || !strings.Contains(out.String(), "Usage:") || diag.Len() > 0 {
			t.Fatalf("%v: %s %s", args, out.String(), diag.String())
		}
	}
	for _, args := range [][]string{{"summarize"}, {"summarize", "HEAD", "--force", "--batch-bytes=0"}, {"summarize", "--unknown"}, {"remote"}, {"remote", "push"}, {"remote", "sync", "extra"}, {"remote", "sync", "--unknown"}, {"hooks"}, {"hooks", "enable"}, {"hooks", "status", "extra"}} {
		var out, diag bytes.Buffer
		if Run(context.Background(), args, &out, &diag) != 1 || out.Len() > 0 || diag.Len() == 0 {
			t.Fatalf("%v: %s %s", args, out.String(), diag.String())
		}
	}
}

func TestInstalledHookSyncsOnRealGitPush(t *testing.T) {
	dir, spec := cliRepo(t)
	fake := fakeCopilot(t, dir)
	os.WriteFile(filepath.Join(fake, "response"), []byte("{\"id\":\"m1\",\"summary\":\"依頼\"}\n{\"id\":\"m2\",\"summary\":\"準備\"}\n{\"id\":\"m3\",\"summary\":\"完了\"}\n"), 0600)
	binary := filepath.Join(t.TempDir(), "entire-timeline")
	build := exec.Command("go", "build", "-o", binary, "./cmd/entire-timeline")
	build.Dir = "../.."
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	command := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
		return string(out)
	}
	remote := t.TempDir()
	command("git", "init", "--bare", remote)
	command("git", "remote", "add", "origin", remote)
	hooks := t.TempDir()
	command("git", "config", "core.hooksPath", hooks)
	original := []byte("#!/bin/sh\n# Entire CLI hooks\ncat > /dev/null\nprintf 'original-hook-preserved\\n' >&2\n")
	os.WriteFile(filepath.Join(hooks, "pre-push"), original, 0755)
	command(binary, "summarize", spec)
	command(binary, "hooks", "install")
	if state := command(binary, "hooks", "status"); !strings.Contains(state, "Installed:") {
		t.Fatal(state)
	}
	push := command("git", "push", "origin", "HEAD:refs/heads/main")
	if !strings.Contains(push, "original-hook-preserved") || !strings.Contains(push, "Summary metadata synced") {
		t.Fatal(push)
	}
	cmd := exec.Command("git", "--git-dir="+remote, "show", summary.MetadataRef+":summaries/copilot-cli/test-session.jsonl")
	rows, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(rows, []byte("完了")) {
		t.Fatal(string(rows), err)
	}
	calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
	if string(calls) != "called\n" {
		t.Fatalf("push generated summaries: %s", calls)
	}
	command(binary, "hooks", "uninstall")
	restored, _ := os.ReadFile(filepath.Join(hooks, "pre-push"))
	if string(restored) != string(original) {
		t.Fatal("uninstall modified original hook")
	}
	// Manual sync also bootstraps a machine with no local summary branch.
	command("git", "update-ref", "-d", summary.MetadataRef)
	if state := command(binary, "remote", "sync"); !strings.Contains(state, "fetched") {
		t.Fatal(state)
	}
	view := command(binary, spec)
	if !strings.Contains(view, "Summary: 完了") {
		t.Fatal(view)
	}
}
