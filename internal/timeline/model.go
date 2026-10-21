// Package timeline restores read-only Entire checkpoints into a shared event stream.
package timeline

import (
	"context"
	"encoding/json"
	"time"
)

type SessionPaths struct {
	Metadata   string `json:"metadata"`
	Transcript string `json:"transcript"`
}

type Summary struct {
	CheckpointID  string `json:"checkpoint_id"`
	CommitSHA     string `json:"commit_sha"`
	LinkedCommits []struct {
		SHA string `json:"sha"`
	} `json:"linked_commits"`
	Sessions []SessionPaths `json:"sessions"`
}

type Metadata struct {
	CheckpointID string `json:"checkpoint_id"`
	SessionID    string `json:"session_id"`
	Agent        string `json:"agent"`
	Start        int    `json:"checkpoint_transcript_start"`
	LegacyStart  int    `json:"transcript_lines_at_start"`
	CompactStart *int   `json:"compact_transcript_start"`
}

// StartLine matches Entire's Metadata.GetTranscriptStart. Omitted zero fields
// are valid: Entire writes these integer fields with omitempty.
func (m Metadata) StartLine() int {
	if m.Start > 0 {
		return m.Start
	}
	return m.LegacyStart
}

type Checkpoint struct {
	Summary Summary
	// Source is an immutable git commit, Prefix is the checkpoint's tree path.
	Source string
	Prefix string
}

type Store interface {
	Select(context.Context, string) ([]Checkpoint, error)
	Session(context.Context, Checkpoint, SessionPaths) (Metadata, []byte, error)
}

type Part struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Name string          `json:"name,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type Event struct {
	SessionID string     `json:"session_id"`
	Agent     string     `json:"agent"`
	Timestamp *time.Time `json:"timestamp"`
	Type      string     `json:"type"`
	Role      string     `json:"role"`
	ID        string     `json:"event_id,omitempty"`
	// Line is zero-based in full.jsonl, including blank lines.
	Line        int             `json:"source_line"`
	Checkpoints []string        `json:"checkpoints"`
	Parts       []Part          `json:"parts,omitempty"`
	Raw         json.RawMessage `json:"raw"`
	sortTime    time.Time
}

type Timeline struct {
	Range       string
	Checkpoints int
	Sessions    []string
	Events      []Event
	Warnings    []string
}
