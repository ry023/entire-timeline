package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GitStore deliberately uses Git rather than Entire's export envelope: the
// latter omits transcript offsets and can fetch/write refs on a local miss.
type GitStore struct {
	Dir       string
	ExtraRefs []string
}

func (g GitStore) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_NO_LAZY_FETCH=") && !strings.HasPrefix(env, "GIT_OPTIONAL_LOCKS=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (g GitStore) resolve(ctx context.Context, ref string) (string, error) {
	b, err := g.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(b)), err
}

// ResolveRange treats A...B as merge-base(A,B)..B, not Git's symmetric difference.
func (g GitStore) ResolveRange(ctx context.Context, spec string) (string, error) {
	sep := ".."
	if strings.Contains(spec, "...") {
		sep = "..."
	}
	if strings.Count(spec, sep) != 1 {
		return "", fmt.Errorf("expected A..B or A...B, got %q", spec)
	}
	a, b, _ := strings.Cut(spec, sep)
	if strings.Contains(a, "..") || strings.Contains(b, "..") {
		return "", fmt.Errorf("invalid revision range %q", spec)
	}
	if a == "" {
		a = "HEAD"
	}
	if b == "" {
		b = "HEAD"
	}
	left, err := g.resolve(ctx, a)
	if err != nil {
		return "", err
	}
	right, err := g.resolve(ctx, b)
	if err != nil {
		return "", err
	}
	if sep == "..." {
		base, err := g.git(ctx, "merge-base", "--all", left, right)
		if err != nil {
			return "", fmt.Errorf("resolve merge base: %w", err)
		}
		bases := strings.Fields(string(base))
		if len(bases) != 1 {
			return "", fmt.Errorf("range %q has %d merge bases; use an explicit A..B range", spec, len(bases))
		}
		left = bases[0]
	}
	return left + ".." + right, nil
}

var checkpointID = regexp.MustCompile(`^(?:[0-9a-f]{12}|[0-9A-HJKMNP-TV-Z]{26})$`)

func (g GitStore) Select(ctx context.Context, spec string) ([]Checkpoint, error) {
	revs, err := g.ResolveRange(ctx, spec)
	if err != nil {
		return nil, err
	}
	// Ask Git itself to parse the trailer block. Arbitrary message text is not a link.
	b, err := g.git(ctx, "log", "--reverse", "--topo-order", "--format=%H%x00%(trailers:key=Entire-Checkpoint,valueonly,separator=%x2c)%x00", revs, "--")
	if err != nil {
		return nil, err
	}
	commits := map[string]int{}
	selected := map[string]int{}
	fields := strings.Split(string(b), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		sha := strings.TrimSpace(fields[i])
		if sha == "" {
			continue
		}
		order := len(commits)
		commits[sha] = order
		for _, id := range strings.Split(strings.TrimSpace(fields[i+1]), ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if !checkpointID.MatchString(id) {
				return nil, fmt.Errorf("commit %s has invalid Entire-Checkpoint %q", sha, id)
			}
			selected[id] = order
		}
	}
	if len(commits) == 0 {
		return nil, nil
	}
	all, err := g.catalog(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Checkpoint{}
	for _, cp := range all {
		id := cp.Summary.CheckpointID
		if prev, ok := byID[id]; ok {
			// Mirrors may coexist. Never silently choose divergent checkpoint data.
			x, err := g.git(ctx, "rev-parse", prev.Source+":"+treePath(prev.Prefix))
			if err != nil {
				return nil, err
			}
			y, err := g.git(ctx, "rev-parse", cp.Source+":"+treePath(cp.Prefix))
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(x, y) {
				return nil, fmt.Errorf("checkpoint %s has conflicting local copies (%s, %s)", id, prev.Source, cp.Source)
			}
			continue
		}
		byID[id] = cp
		if n, ok := commits[cp.Summary.CommitSHA]; ok {
			if prev, found := selected[id]; !found || n > prev {
				selected[id] = n
			}
		}
		for _, link := range cp.Summary.LinkedCommits {
			if n, ok := commits[link.SHA]; ok {
				if prev, found := selected[id]; !found || n > prev {
					selected[id] = n
				}
			}
		}
	}
	var out []Checkpoint
	for id := range selected {
		cp, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("checkpoint %s is unavailable locally; fetch the Entire checkpoint refs/metadata branch first", id)
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Summary.CheckpointID, out[j].Summary.CheckpointID
		if selected[a] != selected[b] {
			return selected[a] < selected[b]
		}
		return a < b
	})
	return out, nil
}

func treePath(prefix string) string { return strings.TrimSuffix(prefix, "/") }

func (g GitStore) catalog(ctx context.Context) ([]Checkpoint, error) {
	b, err := g.git(ctx, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/entire/checkpoints/", "refs/remotes/", "refs/entire/checkpoints/")
	if err != nil {
		return nil, err
	}
	type source struct{ hash, prefix, id string }
	var branches, remotes, refs []source
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		ref, hash, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		switch {
		case ref == "refs/heads/entire/checkpoints/v1":
			branches = append(branches, source{hash: hash})
		case strings.HasPrefix(ref, "refs/remotes/") && strings.HasSuffix(ref, "/entire/checkpoints/v1"):
			remotes = append(remotes, source{hash: hash})
		case strings.HasPrefix(ref, "refs/entire/checkpoints/"):
			id := path.Base(ref)
			if !checkpointID.MatchString(id) || !strings.EqualFold(path.Base(path.Dir(ref)), id[len(id)-2:]) {
				return nil, fmt.Errorf("invalid checkpoint ref %q", ref)
			}
			refs = append(refs, source{hash: hash, id: id})
		}
	}
	if len(branches) == 0 {
		branches = remotes
	}
	for _, ref := range g.ExtraRefs {
		hash, err := g.resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		branches = append(branches, source{hash: hash})
	}
	for _, src := range branches {
		files, err := g.files(ctx, src.hash, "")
		if err != nil {
			return nil, err
		}
		for _, name := range files {
			parts := strings.Split(name, "/")
			if len(parts) != 3 || parts[2] != "metadata.json" {
				continue
			}
			id := parts[0] + parts[1]
			if !checkpointID.MatchString(id) {
				continue
			}
			refs = append(refs, source{src.hash, strings.Join(parts[:2], "/"), id})
		}
	}
	var out []Checkpoint
	for _, src := range refs {
		data, err := g.blob(ctx, src.hash, path.Join(src.prefix, "metadata.json"))
		if err != nil {
			return nil, err
		}
		var summary Summary
		if err := json.Unmarshal(data, &summary); err != nil {
			return nil, fmt.Errorf("checkpoint %s metadata: %w", src.id, err)
		}
		if summary.CheckpointID != src.id {
			return nil, fmt.Errorf("checkpoint ID mismatch: path %s, metadata %s", src.id, summary.CheckpointID)
		}
		out = append(out, Checkpoint{summary, src.hash, src.prefix})
	}
	return out, nil
}

func (g GitStore) files(ctx context.Context, source, prefix string) ([]string, error) {
	args := []string{"ls-tree", "-r", "--name-only", "-z", source, "--"}
	if prefix != "" {
		args = append(args, prefix)
	}
	b, err := g.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range strings.Split(string(b), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

func (g GitStore) blob(ctx context.Context, source, name string) ([]byte, error) {
	return g.git(ctx, "cat-file", "blob", source+":"+name)
}

func checkpointPath(cp Checkpoint, name string) (string, error) {
	name = strings.TrimPrefix(name, "/")
	if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00\\") {
		return "", fmt.Errorf("invalid checkpoint file path %q", name)
	}
	if cp.Prefix != "" && !strings.HasPrefix(name, cp.Prefix+"/") {
		return "", fmt.Errorf("file %q is outside checkpoint %s", name, cp.Summary.CheckpointID)
	}
	return name, nil
}

func (g GitStore) Session(ctx context.Context, cp Checkpoint, p SessionPaths) (Metadata, []byte, error) {
	var m Metadata
	name, err := checkpointPath(cp, p.Metadata)
	if err != nil {
		return m, nil, err
	}
	b, err := g.blob(ctx, cp.Source, name)
	if err != nil {
		return m, nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, nil, fmt.Errorf("session metadata %s: %w", name, err)
	}
	if m.CheckpointID != cp.Summary.CheckpointID || m.SessionID == "" {
		return m, nil, fmt.Errorf("invalid session metadata %s: missing session ID or mismatched checkpoint ID", name)
	}
	if !supportedAgent(m.Agent) {
		return m, nil, fmt.Errorf("unsupported agent %q in session %s (supported: Copilot CLI, Claude Code); cannot guarantee raw offset semantics", m.Agent, m.SessionID)
	}
	dir := path.Dir(name)
	if p.Transcript != "" {
		tname, err := checkpointPath(cp, p.Transcript)
		if err != nil {
			return m, nil, err
		}
		if path.Dir(tname) != dir || path.Base(tname) != "full.jsonl" {
			return m, nil, fmt.Errorf("session %s: transcript must point to raw full.jsonl, got %q", m.SessionID, p.Transcript)
		}
	}
	files, err := g.files(ctx, cp.Source, dir+"/")
	if err != nil {
		return m, nil, err
	}
	chunks := map[int]string{}
	for _, f := range files {
		if path.Dir(f) != dir {
			continue
		}
		base := path.Base(f)
		if base == "full.jsonl" {
			chunks[0] = f
			continue
		}
		if strings.HasPrefix(base, "full.jsonl.") {
			n, err := strconv.Atoi(strings.TrimPrefix(base, "full.jsonl."))
			if err != nil || n < 1 || chunks[n] != "" {
				return m, nil, fmt.Errorf("invalid transcript chunk %s", f)
			}
			chunks[n] = f
		}
	}
	if len(chunks) == 0 {
		return m, nil, fmt.Errorf("session %s: raw full.jsonl is missing; compact transcript cannot substitute raw offsets", m.SessionID)
	}
	var data []byte
	for i := 0; i < len(chunks); i++ {
		f, ok := chunks[i]
		if !ok {
			return m, nil, fmt.Errorf("session %s: missing transcript chunk %d", m.SessionID, i)
		}
		b, err := g.blob(ctx, cp.Source, f)
		if err != nil {
			return m, nil, err
		}
		// Entire's ReassembleJSONL joins chunks with exactly one newline.
		if i > 0 {
			data = append(data, '\n')
		}
		data = append(data, b...)
	}
	return m, data, nil
}
