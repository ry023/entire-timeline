package summary

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ry023/entire-timeline/internal/timeline"
)

const MetadataRef = "refs/heads/entire-timeline/metadata/v1"

type Repo struct{ Dir, CommonDir string }

type gitError struct {
	args   []string
	output string
	err    error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, strings.TrimSpace(e.output))
}
func (e *gitError) Unwrap() error { return e.err }

func Open(ctx context.Context, dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	b, err := r.git(ctx, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	r.Dir = strings.TrimSpace(string(b))
	b, err = r.git(ctx, nil, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	r.CommonDir = strings.TrimSpace(string(b))
	if !filepath.IsAbs(r.CommonDir) {
		r.CommonDir = filepath.Join(r.Dir, r.CommonDir)
	}
	return r, nil
}

// git always keeps the user's index out of metadata operations. A private index
// is supplied explicitly only for tree construction. No shell evaluates argv.
func (r *Repo) git(ctx context.Context, input []byte, extra []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = 2 * time.Second
	overrides := map[string]bool{"GIT_INDEX_FILE": true, "GIT_OPTIONAL_LOCKS": true, "GIT_NO_LAZY_FETCH": true}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		overrides[k] = true
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !overrides[k] {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
	cmd.Env = append(cmd.Env, extra...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &gitError{args, stderr.String() + string(out), err}
	}
	return out, nil
}

func (r *Repo) tip(ctx context.Context, ref string) (string, error) {
	_, err := r.git(ctx, nil, nil, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		var ge *gitError
		var ee *exec.ExitError
		if errors.As(err, &ge) && errors.As(ge.err, &ee) && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	b, err := r.git(ctx, nil, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(b)), err
}

// escapeID keeps ordinary UUID/date IDs readable, but encodes every unsafe byte
// (including percent, slash and dots) reversibly rather than normalizing names.
func escapeID(id string) string {
	var b strings.Builder
	for _, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
func sessionPath(agent, session string) string {
	return "summaries/" + escapeID(agent) + "/" + escapeID(session) + ".jsonl"
}

func decodeRecords(data []byte, agent, session string) (map[string]Record, error) {
	rows := map[string]Record{}
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row Record
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("summary record line %d: %w", i+1, err)
		}
		hash, hashErr := hex.DecodeString(row.TextSHA256)
		validSchema := (row.SchemaVersion == 1 && row.CheckpointID == "") || (row.SchemaVersion == 2 && row.CheckpointID != "")
		if !validSchema || row.Agent != agent || agent == "" || row.SessionID != session || session == "" || row.SourceLine < 0 || hashErr != nil || len(hash) != 32 || strings.TrimSpace(row.Summary) == "" || strings.ContainsAny(row.Summary, "\r\n") {
			return nil, fmt.Errorf("invalid or unsupported summary record at line %d", i+1)
		}
		if _, ok := rows[row.Key()]; ok {
			return nil, fmt.Errorf("duplicate summary record at line %d", i+1)
		}
		rows[row.Key()] = row
	}
	return rows, nil
}

func (r *Repo) sessionAt(ctx context.Context, tip, agent, session string) (map[string]Record, error) {
	if tip == "" {
		return map[string]Record{}, nil
	}
	name := sessionPath(agent, session)
	files, err := r.git(ctx, nil, nil, "ls-tree", "-r", "--name-only", tip, "--", name)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return map[string]Record{}, nil
	}
	data, err := r.git(ctx, nil, nil, "cat-file", "blob", tip+":"+name)
	if err != nil {
		return nil, err
	}
	return decodeRecords(data, agent, session)
}

func (r *Repo) Load(ctx context.Context, events []timeline.Event) (map[string]Record, error) {
	tip, err := r.tip(ctx, MetadataRef)
	if err != nil {
		return nil, err
	}
	rows := map[string]Record{}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Error != nil {
			continue
		}
		name := sessionPath(e.Agent, e.SessionID)
		if seen[name] {
			continue
		}
		seen[name] = true
		got, err := r.sessionAt(ctx, tip, e.Agent, e.SessionID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for k, v := range got {
			rows[k] = v
		}
	}
	return rows, nil
}

func Attach(t *timeline.Timeline, records map[string]Record) {
	for i := range t.Events {
		e := &t.Events[i]
		e.Summary = ""
		if r, ok := lookupRecord(records, *e); ok {
			e.Summary = r.Summary
		}
	}
}

func encodeRecords(rows map[string]Record) ([]byte, error) {
	var records []Record
	for _, row := range rows {
		records = append(records, row)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CheckpointID != records[j].CheckpointID {
			return records[i].CheckpointID < records[j].CheckpointID
		}
		return records[i].SourceLine < records[j].SourceLine
	})
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, row := range records {
		if err := enc.Encode(row); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

// Save merges validated results with existing session records, including rows
// outside the current range. CAS retries preserve concurrent local writes.
func (r *Repo) Save(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	if err := r.CheckMetadataCheckout(ctx); err != nil {
		return err
	}
	for _, row := range records {
		if err := row.validate(); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 5; attempt++ {
		old, err := r.tip(ctx, MetadataRef)
		if err != nil {
			return err
		}
		grouped := map[string]map[string]Record{}
		for _, row := range records {
			name := sessionPath(row.Agent, row.SessionID)
			if grouped[name] == nil {
				rows, err := r.sessionAt(ctx, old, row.Agent, row.SessionID)
				if err != nil {
					return err
				}
				grouped[name] = rows
			}
			grouped[name][row.Key()] = row
		}
		files := map[string][]byte{}
		for name, rows := range grouped {
			b, err := encodeRecords(rows)
			if err != nil {
				return err
			}
			files[name] = b
		}
		commit, err := r.buildCommit(ctx, old, files, "Update conversation summaries")
		if err != nil {
			return err
		}
		if commit == old {
			return nil
		}
		if err := r.updateRef(ctx, MetadataRef, commit, old); err == nil {
			return nil
		} else {
			now, readErr := r.tip(ctx, MetadataRef)
			if readErr != nil {
				return readErr
			}
			if now == old {
				return err
			}
		}
	}
	return fmt.Errorf("summary branch kept changing; retry the command")
}

// CheckMetadataCheckout rejects a metadata branch checked out in any worktree.
// update-ref itself permits changing checked-out branches. Reject that case
// explicitly so a manual metadata checkout (including another worktree) cannot
// have its HEAD moved underneath its index/worktree by a metadata operation.
func (r *Repo) CheckMetadataCheckout(ctx context.Context) error {
	b, err := r.git(ctx, nil, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "branch "+MetadataRef {
			return fmt.Errorf("summary metadata branch is checked out in a worktree; switch that worktree to another branch before saving or syncing summaries")
		}
	}
	return nil
}

func (r *Repo) updateRef(ctx context.Context, ref, value, old string) error {
	if old == "" {
		format, err := r.git(ctx, nil, nil, "rev-parse", "--show-object-format")
		if err != nil {
			return err
		}
		n := 40
		if strings.TrimSpace(string(format)) == "sha256" {
			n = 64
		}
		old = strings.Repeat("0", n)
	}
	_, err := r.git(ctx, nil, nil, "update-ref", ref, value, old)
	return err
}

// buildCommit uses a disposable index, never the user's index or a checkout.
// base is a Git commit, and updates contains only paths this writer changed.
func (r *Repo) buildCommit(ctx context.Context, base string, updates map[string][]byte, message string) (string, error) {
	dir, err := os.MkdirTemp("", "entire-timeline-index-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	args := []string{"read-tree", "--empty"}
	if base != "" {
		args = []string{"read-tree", base}
	}
	if _, err := r.git(ctx, nil, env, args...); err != nil {
		return "", err
	}
	var names []string
	for name := range updates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "summaries/") || strings.Contains(name, "..") {
			return "", fmt.Errorf("unexpected metadata path %q", name)
		}
		var args []string
		if updates[name] == nil {
			args = []string{"update-index", "--force-remove", "--", name}
		} else {
			hash, err := r.git(ctx, updates[name], nil, "hash-object", "-w", "--stdin")
			if err != nil {
				return "", err
			}
			args = []string{"update-index", "--add", "--cacheinfo", "100644," + strings.TrimSpace(string(hash)) + "," + name}
		}
		if _, err := r.git(ctx, nil, env, args...); err != nil {
			return "", err
		}
	}
	tree, err := r.git(ctx, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	treeID := strings.TrimSpace(string(tree))
	if base != "" {
		prev, err := r.git(ctx, nil, nil, "rev-parse", base+"^{tree}")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(prev)) == treeID {
			return base, nil
		}
	}
	args = []string{"-c", "commit.gpgsign=false", "commit-tree", treeID, "-m", message}
	if base != "" {
		args = append(args, "-p", base)
	}
	commit, err := r.git(ctx, nil, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(commit)), nil
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
func cleanURL(s string) string {
	u, err := url.Parse(s)
	if err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return s
}
func (r *Repo) config(ctx context.Context, key string) (string, error) {
	b, err := r.git(ctx, nil, nil, "config", "--get", key)
	if err != nil && exitCode(err) != 1 {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (r Record) validate() error {
	_, err := decodeRecords(mustJSON(r), r.Agent, r.SessionID)
	return err
}
func mustJSON(r Record) []byte { b, _ := json.Marshal(r); return b }
