package timeline

import (
	"sort"
)

// insertCheckpointGaps uses adjacent proven checkpoint events as display
// anchors. A missing checkpoint has no known session or conversation timestamp;
// do not invent either from a nearby transcript or the Git commit clock.
func insertCheckpointGaps(t *Timeline, cps []Checkpoint) {
	first, last := map[string]int{}, map[string]int{}
	for i, e := range t.Events {
		for _, id := range e.Checkpoints {
			if _, ok := first[id]; !ok {
				first[id] = i
			}
			last[id] = i
		}
	}
	at := map[int][]Event{}
	for i, cp := range cps {
		if cp.Unavailable == nil {
			continue
		}
		gap := *cp.Unavailable
		position, anchored := 0, false
		for j := i - 1; j >= 0; j-- {
			if cps[j].Unavailable != nil {
				continue
			}
			if gap.PreviousCheckpointID == "" {
				gap.PreviousCheckpointID = cps[j].Summary.CheckpointID
			}
			if !anchored {
				if n, ok := last[cps[j].Summary.CheckpointID]; ok {
					position, anchored = n+1, true
					gap.Placement = "after_previous_checkpoint"
				}
			}
		}
		for j := i + 1; j < len(cps); j++ {
			if cps[j].Unavailable != nil {
				continue
			}
			if gap.NextCheckpointID == "" {
				gap.NextCheckpointID = cps[j].Summary.CheckpointID
			}
			if !anchored {
				if n, ok := first[cps[j].Summary.CheckpointID]; ok {
					position, anchored = n, true
					gap.Placement = "before_next_checkpoint"
				}
			}
		}
		if !anchored {
			position = len(t.Events)
			gap.Placement = "end_without_conversation_anchor"
			if len(gap.Commits) > 0 {
				stamp := gap.Commits[0].Timestamp
				for n, e := range t.Events {
					if !stamp.IsZero() && !e.sortTime.IsZero() && !e.sortTime.Before(stamp) {
						position = n
						gap.Placement = "git_commit_time_fallback"
						break
					}
				}
			}
		}
		at[position] = append(at[position], Event{Type: "timeline.error", Role: "system", Line: -1, Error: &gap})
	}
	if len(at) == 0 {
		return
	}
	events := make([]Event, 0, len(t.Events)+t.MissingCheckpoints)
	for i := 0; i <= len(t.Events); i++ {
		events = append(events, at[i]...)
		if i < len(t.Events) {
			events = append(events, t.Events[i])
		}
	}
	t.Events = events
}

// Unattributed context is bounded by two selected, available snapshots of the
// same cumulative session, and requires a missing checkpoint between them in
// the selected commit order. These lines are not assigned to that checkpoint:
// it may belong to a different session, or the lines may concern excluded work.
func unattributedLines(snaps []snapshot, cps []Checkpoint, coverage [][]string) map[int]*Attribution {
	positions := map[string]int{}
	for i, cp := range cps {
		positions[cp.Summary.CheckpointID] = i
	}
	ordered := append([]snapshot(nil), snaps...)
	sort.SliceStable(ordered, func(i, j int) bool { return positions[ordered[i].checkpoint] < positions[ordered[j].checkpoint] })
	out := map[int]*Attribution{}
	for i := 1; i < len(ordered); i++ {
		prev, next := ordered[i-1], ordered[i]
		start, end := len(prev.lines), next.metadata.StartLine()
		if start >= end {
			continue
		}
		var missing []string
		for _, cp := range cps[positions[prev.checkpoint]+1 : positions[next.checkpoint]] {
			if cp.Unavailable != nil {
				missing = append(missing, cp.Summary.CheckpointID)
			}
		}
		if len(missing) == 0 {
			continue
		}
		attr := &Attribution{Status: "unattributed", SourceCheckpointID: next.checkpoint, PreviousCheckpointID: prev.checkpoint, NextCheckpointID: next.checkpoint, NearbyMissingCheckpointIDs: missing}
		for line := start; line < end; line++ {
			if len(coverage[line]) == 0 {
				out[line] = attr
			}
		}
	}
	return out
}
