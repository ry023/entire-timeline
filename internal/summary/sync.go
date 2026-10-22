package summary

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sync transfers only the timeline ref. Internal pushes bypass all pre-push
// hooks; the user's ordinary push and Entire's checkpoint hooks stay separate.
func (r *Repo) Sync(ctx context.Context, s Settings, fromHook string) (string, error) {
	if !s.PushSessions {
		return "Summary sync disabled by Entire's push_sessions setting", nil
	}
	d, err := r.Destination(ctx, s)
	if err != nil {
		return "", err
	}
	if fromHook != "" && fromHook != d.Remote && fromHook != d.Push && fromHook != d.Fetch {
		return "Skipping a push to a different remote", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	hooks, err := os.MkdirTemp("", "entire-timeline-sync-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(hooks)
	env := []string{"ENTIRE_TIMELINE_SYNCING=1", "GIT_TERMINAL_PROMPT=0"}
	if d.TokenHost != "" {
		// Pass credentials through Git's environment config, never URL or argv.
		n := 0
		if raw := os.Getenv("GIT_CONFIG_COUNT"); raw != "" {
			n, err = strconv.Atoi(raw)
			if err != nil || n < 0 {
				return "", fmt.Errorf("invalid GIT_CONFIG_COUNT")
			}
		}
		header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte(d.TokenUser+":"+os.Getenv("ENTIRE_CHECKPOINT_TOKEN")))
		env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1), fmt.Sprintf("GIT_CONFIG_KEY_%d=http.https://%s/.extraheader", n, d.TokenHost), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, header))
	}
	network := func(args ...string) ([]byte, error) {
		out, err := r.git(ctx, nil, env, append([]string{"-c", "core.hooksPath=" + hooks}, args...)...)
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) {
				copyErr := *ge
				copyErr.args = append([]string(nil), ge.args...)
				for i, arg := range copyErr.args {
					clean := cleanURL(arg)
					copyErr.output = strings.ReplaceAll(copyErr.output, arg, clean)
					copyErr.args[i] = clean
				}
				if token := os.Getenv("ENTIRE_CHECKPOINT_TOKEN"); token != "" {
					copyErr.output = strings.ReplaceAll(copyErr.output, token, "[redacted]")
					copyErr.output = strings.ReplaceAll(copyErr.output, base64.StdEncoding.EncodeToString([]byte(d.TokenUser+":"+token)), "[redacted]")
				}
				err = &copyErr
			}
		}
		return out, err
	}
	// A unique ref prevents concurrent fetches from changing the snapshot being
	// reconciled. FETCH_HEAD is deliberately not used or overwritten.
	fetchRef := "refs/entire-timeline/fetch/" + filepath.Base(hooks)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r.git(cleanup, nil, nil, "update-ref", "-d", fetchRef)
	}()
	for attempt := 0; attempt < 4; attempt++ {
		local, err := r.tip(ctx, MetadataRef)
		if err != nil {
			return "", err
		}
		if local != "" {
			_, pushErr := network("push", "--porcelain", "--", d.Push, local+":"+MetadataRef)
			if pushErr == nil {
				return "Summary metadata synced", nil
			}
			var ge *gitError
			if !errors.As(pushErr, &ge) || !(strings.Contains(ge.output, "[rejected]") && (strings.Contains(ge.output, "fetch first") || strings.Contains(ge.output, "non-fast-forward"))) {
				return "", pushErr
			}
		} else {
			_, err := network("ls-remote", "--exit-code", "--", d.Fetch, MetadataRef)
			if exitCode(err) == 2 {
				return "No summary metadata to sync", nil
			}
			if err != nil {
				return "", err
			}
		}
		if _, err := network("fetch", "--no-tags", "--no-write-fetch-head", "--", d.Fetch, "+"+MetadataRef+":"+fetchRef); err != nil {
			return "", err
		}
		remote, err := r.tip(ctx, fetchRef)
		if err != nil {
			return "", err
		}
		if remote == "" {
			return "", fmt.Errorf("remote summary branch disappeared during sync; retry")
		}
		next := remote
		if local != "" {
			baseBytes, err := r.git(ctx, nil, nil, "merge-base", local, remote)
			if err != nil && exitCode(err) != 1 {
				return "", err
			}
			base := strings.TrimSpace(string(baseBytes))
			if base == remote {
				// Remote changed between rejection and fetch; retry our intact tip.
				continue
			}
			if base != local {
				updates, err := r.localChanges(ctx, base, local)
				if err != nil {
					return "", err
				}
				next, err = r.buildCommit(ctx, remote, updates, "Reapply local conversation summaries")
				if err != nil {
					return "", err
				}
			}
		}
		if err := r.updateRef(ctx, MetadataRef, next, local); err != nil {
			current, readErr := r.tip(ctx, MetadataRef)
			if readErr != nil {
				return "", readErr
			}
			if current == local {
				return "", err
			}
			continue
		}
		if local == "" {
			return "Summary metadata fetched", nil
		}
	}
	return "", fmt.Errorf("summary branch kept changing during sync; local data is saved, retry remote sync")
}

// Collect paths touched in local-only commits, including a file reverted to its
// old value. Apply the final local blob for each path, preserving remote-only
// sessions and giving local session files the agreed last-writer behavior.
func (r *Repo) localChanges(ctx context.Context, base, local string) (map[string][]byte, error) {
	revs := local
	if base != "" {
		revs = base + ".." + local
	}
	b, err := r.git(ctx, nil, nil, "log", "--format=", "--name-only", "-z", "--no-renames", revs, "--", "summaries/")
	if err != nil {
		return nil, err
	}
	updates := map[string][]byte{}
	for _, path := range strings.Split(string(b), "\x00") {
		path = strings.TrimLeft(path, "\n")
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "summaries/") || strings.Contains(path, "..") || strings.ContainsAny(path, "\r\n") {
			return nil, fmt.Errorf("invalid metadata path")
		}
		if _, seen := updates[path]; seen {
			continue
		}
		listed, err := r.git(ctx, nil, nil, "ls-tree", "-r", "--name-only", local, "--", path)
		if err != nil {
			return nil, err
		}
		if len(listed) == 0 {
			updates[path] = nil
			continue
		}
		blob, err := r.git(ctx, nil, nil, "cat-file", "blob", local+":"+path)
		if err != nil {
			return nil, err
		}
		updates[path] = blob
	}
	return updates, nil
}
