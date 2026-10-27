package timeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"time"
)

type snapshot struct {
	metadata   Metadata
	checkpoint string
	lines      [][]byte
}

// rawLines counts physical JSONL lines, including blank lines, and counts a
// final unterminated line. It never treats the trailing newline as another line.
func rawLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type RestoreOptions struct {
	// Diagnostics receives metadata and redacted structural differences only.
	// A nil writer disables diagnostics without changing restore validation.
	Diagnostics         io.Writer
	IncludeUnattributed bool
}

func Restore(ctx context.Context, store Store, spec string) (Timeline, error) {
	return RestoreWithOptions(ctx, store, spec, RestoreOptions{})
}

func RestoreWithOptions(ctx context.Context, store Store, spec string, opt RestoreOptions) (Timeline, error) {
	diag := diagnostics{opt.Diagnostics}
	diag.printf("selecting checkpoints from the local Git range")
	diag.printf("restore_mode=per_checkpoint")
	t := Timeline{Range: spec}
	cps, err := store.Select(ctx, spec)
	if err != nil {
		return t, err
	}
	t.Checkpoints = len(cps)
	diag.printf("selected_checkpoints=%d", len(cps))
	groups := map[string][]snapshot{}
	for _, cp := range cps {
		if cp.Unavailable != nil {
			t.MissingCheckpoints++
			diag.printf("checkpoint_unavailable checkpoint=%q referencing_commits=%d", cp.Summary.CheckpointID, len(cp.Commits))
			continue
		}
		if len(cp.Summary.Sessions) == 0 {
			return t, fmt.Errorf("checkpoint %s has no session metadata", cp.Summary.CheckpointID)
		}
		seen := map[string]bool{}
		for _, paths := range cp.Summary.Sessions {
			m, data, err := store.Session(ctx, cp, paths)
			if err != nil {
				return t, fmt.Errorf("checkpoint %s: %w", cp.Summary.CheckpointID, err)
			}
			if m.SessionID == "" || seen[m.SessionID] {
				return t, fmt.Errorf("checkpoint %s: empty or duplicate session ID %q", cp.Summary.CheckpointID, m.SessionID)
			}
			seen[m.SessionID] = true
			lines := rawLines(data)
			diag.printf("snapshot session=%q checkpoint=%q agent=%q raw_lines=%d selected_raw_range=[%d,%d) checkpoint_transcript_start=%d transcript_lines_at_start=%d", m.SessionID, cp.Summary.CheckpointID, canonicalAgent(m.Agent), len(lines), m.StartLine(), len(lines), m.Start, m.LegacyStart)
			if m.Start < 0 || m.LegacyStart < 0 {
				return t, fmt.Errorf("checkpoint %s session %s: negative transcript offset (checkpoint_transcript_start=%d, transcript_lines_at_start=%d)", cp.Summary.CheckpointID, m.SessionID, m.Start, m.LegacyStart)
			}
			if m.StartLine() > len(lines) {
				return t, fmt.Errorf("checkpoint %s session %s: raw transcript offset %d is outside [0,%d]", cp.Summary.CheckpointID, m.SessionID, m.StartLine(), len(lines))
			}
			if !supportedAgent(m.Agent) {
				return t, fmt.Errorf("session %s: unsupported agent %q", m.SessionID, m.Agent)
			}
			groups[m.SessionID] = append(groups[m.SessionID], snapshot{m, cp.Summary.CheckpointID, lines})
		}
	}
	if t.MissingCheckpoints > 0 {
		t.Warnings = append(t.Warnings, fmt.Sprintf("%d checkpoint(s) unavailable locally; timeline is incomplete and contains explicit gap events (remote availability not checked)", t.MissingCheckpoints))
	}
	for id := range groups {
		t.Sessions = append(t.Sessions, id)
	}
	sort.Strings(t.Sessions)
	for _, id := range t.Sessions {
		snaps := groups[id]
		var contexts map[string]contextRange
		if opt.IncludeUnattributed {
			var warnings []string
			contexts, warnings = unattributedContexts(snaps, cps, diag)
			t.Warnings = append(t.Warnings, warnings...)
		}
		for _, s := range snaps {
			events, warnings, err := restoreSnapshot(ctx, s, contexts[s.checkpoint])
			if err != nil {
				return t, err
			}
			for _, e := range events {
				if e.Attribution != nil {
					t.UnattributedEvents++
				}
			}
			t.Events = append(t.Events, events...)
			t.Warnings = append(t.Warnings, warnings...)
			diag.printf("restored_snapshot checkpoint=%q session=%q events=%d", s.checkpoint, id, len(events))
		}
	}
	checkpointOrder := map[string]int{}
	for i, cp := range cps {
		checkpointOrder[cp.Summary.CheckpointID] = i
	}
	sourceCheckpoint := func(e Event) string {
		if e.Attribution != nil {
			return e.Attribution.SourceCheckpointID
		}
		return e.CheckpointID
	}
	sort.SliceStable(t.Events, func(i, j int) bool {
		a, b := t.Events[i], t.Events[j]
		if a.sortTime.IsZero() != b.sortTime.IsZero() {
			return !a.sortTime.IsZero()
		}
		if !a.sortTime.Equal(b.sortTime) {
			return a.sortTime.Before(b.sortTime)
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if x, y := checkpointOrder[sourceCheckpoint(a)], checkpointOrder[sourceCheckpoint(b)]; x != y {
			return x < y
		}
		return a.Line < b.Line
	})
	insertCheckpointGaps(&t, cps)
	diag.printf("restored checkpoints=%d sessions=%d events=%d", t.Checkpoints, len(t.Sessions), len(t.Events))
	if t.MissingCheckpoints > 0 || t.UnattributedEvents > 0 {
		diag.printf("incomplete_timeline missing_checkpoints=%d unattributed_events=%d", t.MissingCheckpoints, t.UnattributedEvents)
	}
	return t, nil
}

// Each checkpoint owns its offset coordinate system. A changed, inserted or
// removed line in another snapshot cannot change what is extracted here.
func restoreSnapshot(ctx context.Context, s snapshot, extra contextRange) ([]Event, []string, error) {
	start := s.metadata.StartLine()
	if extra.attribution != nil {
		start = extra.start
	}
	var events []Event
	for i := start; i < len(s.lines); i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if len(bytes.TrimSpace(s.lines[i])) == 0 {
			continue
		}
		e, err := decode(s.metadata.Agent, s.lines[i])
		if err != nil {
			return nil, nil, fmt.Errorf("checkpoint %s session %s raw line %d: %w", s.checkpoint, s.metadata.SessionID, i, err)
		}
		e.SessionID, e.Line = s.metadata.SessionID, i
		if i < s.metadata.StartLine() {
			e.Attribution, e.Checkpoints = extra.attribution, []string{}
		} else {
			e.CheckpointID, e.Checkpoints = s.checkpoint, []string{s.checkpoint}
		}
		events = append(events, e)
	}
	var clock time.Time
	for _, e := range events {
		if e.Timestamp != nil {
			clock = *e.Timestamp
			break
		}
	}
	missing, regressed := 0, 0
	for i := range events {
		e := &events[i]
		if e.Timestamp == nil {
			missing++
		} else if e.Timestamp.Before(clock) {
			regressed++
		} else {
			clock = *e.Timestamp
		}
		e.sortTime = clock
	}
	var warnings []string
	if missing > 0 {
		warnings = append(warnings, fmt.Sprintf("session %s checkpoint %s: %d event(s) have no timestamp; source order retained using adjacent known times", s.metadata.SessionID, s.checkpoint, missing))
	}
	if regressed > 0 {
		warnings = append(warnings, fmt.Sprintf("session %s checkpoint %s: %d timestamp(s) regress; source order takes precedence over wall-clock order", s.metadata.SessionID, s.checkpoint, regressed))
	}
	return events, warnings, nil
}
