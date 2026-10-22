package summary

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const hookStart = "# >>> entire-timeline sync >>>\n"
const hookEnd = "# <<< entire-timeline sync <<<\n"
const createdHook = "# entire-timeline: created-hook\n"

func (r *Repo) hookPath(ctx context.Context) (string, error) {
	b, err := r.git(ctx, nil, nil, "rev-parse", "--git-path", "hooks/pre-push")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(b))
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	return filepath.Clean(path), nil
}

func readHook(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0755, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("pre-push hook %s is not a regular file; configure its hook manager manually", path)
	}
	b, err := os.ReadFile(path)
	return b, info.Mode().Perm(), err
}

func hookBlock(data []byte) (int, int, error) {
	start, end := bytes.Index(data, []byte(hookStart)), bytes.Index(data, []byte(hookEnd))
	if start < 0 && end < 0 {
		return -1, -1, nil
	}
	if start < 0 || end < start || bytes.Count(data, []byte(hookStart)) != 1 || bytes.Count(data, []byte(hookEnd)) != 1 {
		return 0, 0, fmt.Errorf("incomplete or duplicate entire-timeline hook markers; repair the pre-push hook before changing it")
	}
	return start, end + len(hookEnd), nil
}

func shellHook(data []byte) bool {
	line, _, _ := strings.Cut(string(data), "\n")
	words := strings.Fields(strings.TrimPrefix(line, "#!"))
	if !strings.HasPrefix(line, "#!") || len(words) == 0 {
		return false
	}
	if filepath.Base(words[0]) == "env" {
		words = words[1:]
		if len(words) > 0 && words[0] == "-S" {
			words = words[1:]
		}
	}
	if len(words) == 0 {
		return false
	}
	switch filepath.Base(words[0]) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Hooks edits only a delimited shell block. Existing hook bytes (including
// Entire's .pre-entire chaining) remain intact. No backup wrapper can recurse
// if Entire later reinstalls its own hook. Such reinstalls may remove our block;
// status then reports not installed and install repairs it explicitly.
func (r *Repo) Hooks(ctx context.Context, action, binary string) (string, error) {
	path, err := r.hookPath(ctx)
	if err != nil {
		return "", err
	}
	data, mode, err := readHook(path)
	if err != nil {
		return "", err
	}
	start, end, err := hookBlock(data)
	if err != nil {
		return "", err
	}
	switch action {
	case "status":
		if start >= 0 && mode&0111 != 0 {
			return "Installed: " + path, nil
		}
		if start >= 0 {
			return "Installed but hook is not executable: " + path, nil
		}
		return "Not installed: " + path, nil
	case "uninstall":
		if start < 0 {
			return "Not installed: " + path, nil
		}
		wasCreated := bytes.Contains(data[start:end], []byte(createdHook))
		remaining := append(append([]byte(nil), data[:start]...), data[end:]...)
		if wasCreated && bytes.Equal(remaining, []byte("#!/bin/sh\n")) {
			if err := os.Remove(path); err != nil {
				return "", err
			}
		} else if err := writeHook(path, remaining, mode); err != nil {
			return "", err
		}
		return "Uninstalled: " + path, nil
	case "install":
		original := append([]byte(nil), data...)
		if !filepath.IsAbs(binary) || strings.ContainsAny(binary, "\r\n") {
			return "", fmt.Errorf("hook requires an absolute executable path")
		}
		if len(data) != 0 && !shellHook(data) {
			return "", fmt.Errorf("pre-push hook %s is not a supported shell script; add 'entire timeline remote sync' through its hook manager", path)
		}
		wasCreated := len(data) == 0
		if start >= 0 {
			wasCreated = bytes.Contains(data[start:end], []byte(createdHook))
			data = append(append([]byte(nil), data[:start]...), data[end:]...)
		}
		if len(data) == 0 {
			data = []byte("#!/bin/sh\n")
		}
		firstLine := bytes.IndexByte(data, '\n')
		if firstLine < 0 {
			return "", fmt.Errorf("hook shebang must end with a newline")
		}
		block := hookStart
		if wasCreated {
			block += createdHook
		}
		block += "if [ \"${ENTIRE_TIMELINE_SYNCING:-}\" != \"1\" ]; then\n" +
			"  if ! " + shellQuote(binary) + " remote sync --from-hook \"${1:-}\" >&2; then\n" +
			"    printf '%s\\n' 'entire-timeline: summary sync failed; local summaries remain saved; continuing push.' >&2\n" +
			"  fi\nfi\n" + hookEnd
		updated := append(append(append([]byte(nil), data[:firstLine+1]...), []byte(block)...), data[firstLine+1:]...)
		if bytes.Equal(updated, original) && mode&0100 != 0 {
			return "Installed: " + path, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return "", err
		}
		if err := writeHook(path, updated, mode|0100); err != nil {
			return "", err
		}
		return "Installed: " + path, nil
	default:
		return "", fmt.Errorf("use hooks install, uninstall, or status")
	}
}

func writeHook(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".entire-timeline-hook-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
