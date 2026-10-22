package summary

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookCoexistenceIdempotenceAndFailurePolicy(t *testing.T) {
	r := newRepo(t)
	hooks := t.TempDir()
	git(t, r, "config", "core.hooksPath", hooks)
	path := filepath.Join(hooks, "pre-push")
	original := "#!/bin/sh\n# Entire CLI hooks\ncat\nprintf 'original:%s\\n' \"$1\"\nexit 7\n"
	if err := os.WriteFile(path, []byte(original), 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "timeline 'quoted' executable")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'sync:%s,%s,%s,%s\\n' \"$1\" \"$2\" \"$3\" \"$4\" >&2\nexit 4\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Hooks(context.Background(), "install", bin); err != nil {
			t.Fatal(err)
		}
	}
	installed, _ := os.ReadFile(path)
	if strings.Count(string(installed), hookStart) != 1 || !strings.Contains(string(installed), original[len("#!/bin/sh\n"):]) {
		t.Fatal("duplicated block or modified original", string(installed))
	}
	if state, err := r.Hooks(context.Background(), "status", ""); err != nil || !strings.HasPrefix(state, "Installed:") {
		t.Fatal(state, err)
	}
	cmd := exec.Command(path, "origin", "unused-url")
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader("unchanged pre-push stdin\n")
	out, err := cmd.CombinedOutput()
	if exitCode(err) != 7 || !strings.Contains(string(out), "unchanged pre-push stdin\noriginal:origin") || !strings.Contains(string(out), "sync:remote,sync,--from-hook,origin") || !strings.Contains(string(out), "continuing push") {
		t.Fatalf("%s %v", out, err)
	}
	cmd = exec.Command(path, "origin", "unused-url")
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "ENTIRE_TIMELINE_SYNCING=1")
	out, err = cmd.CombinedOutput()
	if exitCode(err) != 7 || strings.Contains(string(out), "sync:") {
		t.Fatalf("recursed: %s %v", out, err)
	}
	if _, err := r.Hooks(context.Background(), "uninstall", ""); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != original {
		t.Fatalf("original bytes changed: %s", restored)
	}
}

func TestHookCreatedAndReplacedByEntire(t *testing.T) {
	r := newRepo(t)
	git(t, r, "config", "--unset", "core.hooksPath")
	path, err := r.hookPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "entire-timeline")
	if _, err := r.Hooks(context.Background(), "install", bin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Hooks(context.Background(), "uninstall", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created hook wasn't removed", err)
	}
	if _, err := r.Hooks(context.Background(), "install", bin); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\n# Entire CLI hooks\nprintf 'new Entire hook'\n")
	if err := os.WriteFile(path, original, 0755); err != nil {
		t.Fatal(err)
	}
	if state, err := r.Hooks(context.Background(), "status", ""); err != nil || !strings.HasPrefix(state, "Not installed:") {
		t.Fatal(state, err)
	}
	if _, err := r.Hooks(context.Background(), "install", bin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Hooks(context.Background(), "uninstall", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("replaced Entire hook wasn't preserved")
	}
}

func TestHookManagersAreNotOverwritten(t *testing.T) {
	r := newRepo(t)
	hooks := t.TempDir()
	git(t, r, "config", "core.hooksPath", hooks)
	path := filepath.Join(hooks, "pre-push")
	for _, original := range []string{"#!/usr/bin/env python3\nprint('existing')\n", "#!/bin/sh\n" + hookStart + "incomplete\n"} {
		os.WriteFile(path, []byte(original), 0755)
		if _, err := r.Hooks(context.Background(), "install", "/fake/bin/timeline"); err == nil {
			t.Fatal("unsupported hook overwritten")
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatal("changed unsupported hook")
		}
	}
}

func TestHookWorktreeUsesGitResolvedPath(t *testing.T) {
	r := newRepo(t)
	git(t, r, "config", "--unset", "core.hooksPath")
	worktree := filepath.Join(t.TempDir(), "worktree")
	git(t, r, "worktree", "add", "-b", "side", worktree)
	w, err := Open(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := r.hookPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.hookPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("worktree hook path differs: %s %s", p1, p2)
	}
	if _, err := w.Hooks(context.Background(), "install", "/fake/bin/timeline"); err != nil {
		t.Fatal(err)
	}
	if state, err := r.Hooks(context.Background(), "status", ""); err != nil || !strings.HasPrefix(state, "Installed:") {
		t.Fatal(state, err)
	}
}
