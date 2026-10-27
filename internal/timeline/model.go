// Package timeline restores read-only Entire checkpoints into a shared event stream.
package timeline

import (
	"context"
	"encoding/json"
	"strings"
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
	Source      string
	Prefix      string
	Commits     []CommitReference
	Unavailable *MissingCheckpointError
}

type CommitReference struct {
	SHA       string    `json:"sha"`
	Timestamp time.Time `json:"timestamp"` // Git committer time, not conversation time
}

// MissingCheckpointError describes unavailable local data, not a confirmed
// absence on any remote. It is carried in the stream so restoration can finish.
type MissingCheckpointError struct {
	Code                 string            `json:"code"`
	CheckpointID         string            `json:"checkpoint_id"`
	Message              string            `json:"message"`
	Commits              []CommitReference `json:"commits"`
	PreviousCheckpointID string            `json:"previous_checkpoint_id,omitempty"`
	NextCheckpointID     string            `json:"next_checkpoint_id,omitempty"`
	Placement            string            `json:"placement"`
}

func (e *MissingCheckpointError) Error() string {
	return "checkpoint " + e.CheckpointID + ": " + e.Message
}

type Attribution struct {
	Status                     string   `json:"status"`
	SourceCheckpointID         string   `json:"source_checkpoint_id"`
	PreviousCheckpointID       string   `json:"previous_checkpoint_id"`
	NextCheckpointID           string   `json:"next_checkpoint_id"`
	NearbyMissingCheckpointIDs []string `json:"nearby_missing_checkpoint_ids"`
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
	CheckpointID string     `json:"checkpoint_id,omitempty"`
	SessionID    string     `json:"session_id"`
	Agent        string     `json:"agent"`
	Timestamp    *time.Time `json:"timestamp"`
	Type         string     `json:"type"`
	Role         string     `json:"role"`
	ID           string     `json:"event_id,omitempty"`
	// Line is zero-based in this checkpoint's full.jsonl, including blank lines.
	Line        int                     `json:"source_line"`
	Checkpoints []string                `json:"checkpoints"`
	Parts       []Part                  `json:"parts,omitempty"`
	Raw         json.RawMessage         `json:"raw,omitempty"`
	Summary     string                  `json:"summary,omitempty"`
	Error       *MissingCheckpointError `json:"error,omitempty"`
	Attribution *Attribution            `json:"attribution,omitempty"`
	sortTime    time.Time
}

// ConversationText excludes tools, reasoning and system data from model input.
// Raw events and the other Parts remain available to renderers and exporters.
func (e Event) ConversationText() string {
	// Streaming fragments are not independent utterances. The completed
	// assistant.message is the summary source; raw fragments remain exportable.
	if e.Type == "assistant.message_delta" {
		return ""
	}
	if e.Role != "user" && e.Role != "assistant" {
		return ""
	}
	var blocks []string
	for _, part := range e.Parts {
		if part.Kind == "text" && part.Text != "" {
			blocks = append(blocks, part.Text)
		}
	}
	return strings.Join(blocks, "\n\n")
}

type Timeline struct {
	Range              string
	Checkpoints        int
	Sessions           []string
	Events             []Event
	Warnings           []string
	MissingCheckpoints int
	UnattributedEvents int
}
