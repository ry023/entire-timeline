package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Settings struct {
	Provider, Model       string
	Timeout               time.Duration
	PushSessions          bool
	PushRemote            string
	CheckpointRemote      *CheckpointRemote
	localCheckpointRemote bool
}
type CheckpointRemote struct {
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
}

// LoadSettings reads only the Entire keys this plugin needs. Unknown keys are
// deliberately allowed. A tracked settings.local.json is ignored, like Entire.
func (r *Repo) LoadSettings(ctx context.Context) (Settings, error) {
	s := Settings{PushSessions: true}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		if name == "settings.local.json" {
			b, err := r.git(ctx, nil, nil, "ls-files", "--error-unmatch", "--", ".entire/"+name)
			if err == nil && len(b) > 0 {
				continue
			}
			if err != nil && exitCode(err) != 1 {
				return s, err
			}
		}
		file := filepath.Join(r.Dir, ".entire", name)
		b, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return s, err
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(b, &top); err != nil {
			return s, fmt.Errorf("%s: %w", file, err)
		}
		if top == nil {
			return s, fmt.Errorf("%s: expected a settings object", file)
		}
		if raw, ok := top["summary_generation"]; ok {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return s, fmt.Errorf("summary_generation: %w", err)
			}
			if raw, ok := fields["provider"]; ok {
				var provider string
				if err := json.Unmarshal(raw, &provider); err != nil {
					return s, err
				}
				if provider != s.Provider {
					if _, hasModel := fields["model"]; !hasModel {
						s.Model = ""
					}
				}
				s.Provider = provider
			}
			if raw, ok := fields["model"]; ok {
				if err := json.Unmarshal(raw, &s.Model); err != nil {
					return s, err
				}
			}
		}
		if raw, ok := top["summary_timeout_seconds"]; ok {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return s, err
			}
			if n > 0 {
				s.Timeout = time.Duration(n) * time.Second
			} else {
				s.Timeout = 0
			}
		}
		if raw, ok := top["strategy_options"]; ok {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return s, err
			}
			if raw, ok := fields["push_sessions"]; ok {
				if err := json.Unmarshal(raw, &s.PushSessions); err != nil {
					return s, err
				}
			}
			if raw, ok := fields["checkpoint_push_remote"]; ok {
				if err := json.Unmarshal(raw, &s.PushRemote); err != nil {
					return s, err
				}
			}
			if raw, ok := fields["checkpoint_remote"]; ok {
				if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
					s.CheckpointRemote = nil
				} else {
					var cp CheckpointRemote
					if err := json.Unmarshal(raw, &cp); err != nil {
						return s, err
					}
					s.CheckpointRemote = &cp
				}
				s.localCheckpointRemote = name == "settings.local.json"
			}
		}
	}
	if s.Model != "" && s.Provider == "" {
		return s, fmt.Errorf("summary_generation.model is configured without summary_generation.provider")
	}
	return s, nil
}

func (s Settings) CheckGenerator() error {
	if s.Provider == "" {
		return fmt.Errorf("summary_generation.provider is not configured; run entire configure --summarize-provider copilot-cli")
	}
	if s.Provider != "copilot-cli" {
		return fmt.Errorf("summary provider %q is unsupported; currently only copilot-cli is supported", s.Provider)
	}
	return nil
}

type Destination struct {
	Remote               string
	Push, Fetch          string
	TokenHost, TokenUser string
}

// Destination follows Entire's explicit > captured > origin > config-order
// election, then applies checkpoint_remote's separate repository URL.
func (r *Repo) Destination(ctx context.Context, s Settings) (Destination, error) {
	remotes, err := r.remoteNames(ctx)
	if err != nil {
		return Destination{}, err
	}
	name := s.PushRemote
	if name != "" && !contains(remotes, name) {
		return Destination{}, fmt.Errorf("checkpoint_push_remote %q has no configured fetch URL", name)
	}
	if name == "" {
		var captured struct {
			Remotes []string `json:"remotes"`
		}
		if b, err := os.ReadFile(filepath.Join(r.CommonDir, "entire-checkpoint-sync-remotes.json")); err == nil && json.Unmarshal(b, &captured) == nil {
			for _, candidate := range captured.Remotes {
				if contains(remotes, candidate) {
					name = candidate
					break
				}
			}
		}
	}
	if name == "" && contains(remotes, "origin") {
		name = "origin"
	}
	if name == "" && len(remotes) > 0 {
		name = remotes[0]
	}
	if name == "" {
		return Destination{}, fmt.Errorf("no checkpoint sync remote is configured")
	}
	fetchURLs, err := r.remoteURLs(ctx, name, false)
	if err != nil {
		return Destination{}, err
	}
	pushURLs, err := r.remoteURLs(ctx, name, true)
	if err != nil {
		return Destination{}, err
	}
	if len(pushURLs) != 1 {
		return Destination{}, fmt.Errorf("remote %q has multiple push URLs; configure a single checkpoint_push_remote for timeline sync", name)
	}
	d := Destination{Remote: name, Push: pushURLs[0], Fetch: fetchURLs[0]}
	if s.CheckpointRemote == nil {
		return d, nil
	}
	cp := s.CheckpointRemote
	if !validRepoSlug.MatchString(cp.Repo) || contains(strings.Split(cp.Repo, "/"), ".") || contains(strings.Split(cp.Repo, "/"), "..") {
		return d, fmt.Errorf("invalid checkpoint_remote.repo %q", cp.Repo)
	}
	if cp.Provider != "github" && cp.Provider != "gitlab" {
		return d, fmt.Errorf("unsupported checkpoint_remote.provider %q", cp.Provider)
	}
	// A committed checkpoint remote inherited from another project owner is
	// ignored by Entire. A developer-owned local override is authoritative.
	if !s.localCheckpointRemote {
		identities := append([]string{}, pushURLs...)
		if contains(remotes, "origin") {
			urls, err := r.remoteURLs(ctx, "origin", false)
			if err != nil {
				return d, err
			}
			identities = append(identities, urls...)
		}
		owner := strings.Split(cp.Repo, "/")[0]
		for _, identity := range identities {
			u, err := parseRemote(identity)
			if err != nil || !strings.EqualFold(strings.Split(u.repo, "/")[0], owner) || (u.host == "github.com" && cp.Provider != "github") || (u.host == "gitlab.com" && cp.Provider != "gitlab") {
				return d, nil
			}
		}
	}
	push, err := deriveURL(pushURLs[0], *cp)
	if err != nil {
		return d, err
	}
	fetch, err := deriveURL(fetchURLs[0], *cp)
	if err != nil {
		return d, err
	}
	d.Push = push
	d.Fetch = fetch
	if os.Getenv("ENTIRE_CHECKPOINT_TOKEN") != "" {
		u, err := parseRemote(push)
		if err != nil {
			return d, err
		}
		d.Push = "https://" + u.host + "/" + cp.Repo + ".git"
		d.Fetch = d.Push
		d.TokenHost = u.host
		d.TokenUser = "x-access-token"
		if cp.Provider == "gitlab" {
			d.TokenUser = "oauth2"
		}
	}
	return d, nil
}

var validRepoSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`)

type remoteInfo struct{ scheme, host, repo, user string }

func parseRemote(raw string) (remoteInfo, error) {
	if strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "\r\n") {
		return remoteInfo{}, fmt.Errorf("invalid remote URL")
	}
	if !strings.Contains(raw, "://") && strings.Contains(raw, ":") {
		left, right, _ := strings.Cut(raw, ":")
		user, host, ok := strings.Cut(left, "@")
		if !ok {
			host = left
			user = "git"
		}
		if strings.ContainsAny(host, "/\\") {
			return remoteInfo{}, fmt.Errorf("invalid SSH remote")
		}
		return remoteInfo{"scp", host, strings.TrimSuffix(right, ".git"), user}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") {
		return remoteInfo{}, fmt.Errorf("cannot derive checkpoint URL from this remote transport")
	}
	user := "git"
	if u.User != nil && u.User.Username() != "" {
		user = u.User.Username()
	}
	return remoteInfo{u.Scheme, u.Host, strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), user}, nil
}
func deriveURL(base string, cp CheckpointRemote) (string, error) {
	u, err := parseRemote(base)
	if err != nil {
		return "", err
	}
	if u.host == "github.com" && cp.Provider != "github" || u.host == "gitlab.com" && cp.Provider != "gitlab" {
		u.scheme = "https"
		u.host = "github.com"
		if cp.Provider == "gitlab" {
			u.host = "gitlab.com"
		}
	}
	switch u.scheme {
	case "scp":
		return "git@" + u.host + ":" + cp.Repo + ".git", nil
	case "ssh":
		return "ssh://git@" + u.host + "/" + cp.Repo + ".git", nil
	default:
		return "https://" + u.host + "/" + cp.Repo + ".git", nil
	}
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func (r *Repo) remoteURLs(ctx context.Context, name string, push bool) ([]string, error) {
	args := []string{"remote", "get-url", "--all"}
	if push {
		args = append(args, "--push")
	}
	args = append(args, "--", name)
	b, err := r.git(ctx, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	urls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(urls) == 0 || urls[0] == "" {
		return nil, fmt.Errorf("remote %q has no URL", name)
	}
	return urls, nil
}
func (r *Repo) remoteNames(ctx context.Context) ([]string, error) {
	b, err := r.git(ctx, nil, nil, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil && exitCode(err) != 1 {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		key, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}
