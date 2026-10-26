package timeline

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestConversationFilterAcrossTranscriptFormats(t *testing.T) {
	got := restoreFixture(t, []fixtureSession{
		{id: "compact", start: 1, data: prefix(fixture(t, "copilot-compact.jsonl"), 3)},
		{id: "native", start: 1, data: fixture(t, "copilot-native.jsonl")},
		{id: "claude", agent: "Claude Code", data: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Claudeの本文"},{"type":"thinking","thinking":"PRIVATE CLAUDE REASONING"},{"type":"tool_use","name":"Edit","input":{"path":"PRIVATE PATH"}}]}}`)},
	})
	before, _ := json.Marshal(got)
	filter, err := ParseFilter("conversation")
	if err != nil {
		t.Fatal(err)
	}
	view := filter.Apply(got)
	if len(view.Events) != 6 {
		t.Fatalf("got %d conversation events", len(view.Events))
	}
	var text, exported bytes.Buffer
	if err := RenderText(&text, view, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RenderJSONL(&exported, view); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# 認証機能\n\nAuthProviderを利用してください。", "第二のcontent block", "既存の実装を調査します。", "調査結果をもとに進めます。", "並行してテストを書いて。", "テストを準備します。", "テストを追加しました。", "Claudeの本文"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing full text %q", want)
		}
	}
	for _, excluded := range []string{"既存テストを確認する。", "SECRET TOOL RESULT", "PRIVATE CLAUDE REASONING", "PRIVATE PATH", "go test ./...", "↳ Tool", "[Reasoning]"} {
		if strings.Contains(text.String(), excluded) || strings.Contains(exported.String(), excluded) {
			t.Fatalf("leaked %q", excluded)
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'}) {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if _, ok := e["raw"]; ok {
			t.Fatal("raw reintroduces filtered content")
		}
		if e["role"] != "user" && e["role"] != "assistant" {
			t.Fatal("retained a non-conversation event")
		}
	}
	after, _ := json.Marshal(got)
	if !bytes.Equal(before, after) {
		t.Fatal("filter mutated the restored timeline")
	}
	all, _ := ParseFilter("all")
	if !reflect.DeepEqual(all.Apply(got), got) || !reflect.DeepEqual((Filter{}).Apply(got), got) {
		t.Fatal("all/zero-value filter changed events")
	}
}

func TestConversationFilterKeepsGapAndUnattributedMarkers(t *testing.T) {
	body := "# 本文\n\n```go\n    return\n```\n" + strings.Repeat("全文\n", 10000)
	attr := &Attribution{Status: "unattributed", SourceCheckpointID: "c"}
	gap := Event{Type: "timeline.error", Error: &MissingCheckpointError{Code: "checkpoint_unavailable", CheckpointID: "b", Message: "missing", Placement: "after_previous_checkpoint"}}
	source := Timeline{Checkpoints: 3, MissingCheckpoints: 1, UnattributedEvents: 2, Sessions: []string{"hidden", "shown"}, Events: []Event{
		{SessionID: "hidden", Role: "assistant", Type: "assistant.message", Parts: []Part{{Kind: "reasoning", Text: "PRIVATE COT ONLY"}}, Attribution: attr},
		gap,
		{SessionID: "shown", Role: "assistant", Type: "assistant.message", Parts: []Part{{Kind: "text", Text: body, Data: json.RawMessage(`"PRIVATE BLOCK METADATA"`)}, {Kind: "tool_call", Data: json.RawMessage(`"PRIVATE TOOL"`)}}, Raw: json.RawMessage(`{"reasoningText":"PRIVATE RAW"}`), Summary: "保存済み要約", Attribution: attr},
		{SessionID: "shown", Role: "assistant", Type: "assistant.message_delta", Parts: []Part{{Kind: "text", Text: "PRIVATE STREAM FRAGMENT"}}},
		{SessionID: "shown", Role: "user", Type: "user.message", Parts: []Part{{Kind: "text", Text: " \n\t"}}},
		{SessionID: "shown", Role: "system", Type: "system", Parts: []Part{{Kind: "text", Text: "PRIVATE SYSTEM"}}},
	}}
	filter, _ := ParseFilter("conversation")
	view := filter.Apply(source)
	if len(view.Events) != 2 || view.Events[0].Error != gap.Error || view.Events[1].Attribution != attr || view.UnattributedEvents != 1 || view.MissingCheckpoints != 1 || !reflect.DeepEqual(view.Sessions, []string{"shown"}) {
		t.Fatalf("%+v", view)
	}
	var text, exported bytes.Buffer
	RenderText(&text, view, TextOptions{})
	RenderJSONL(&exported, view)
	for _, want := range []string{body, "[CHECKPOINT GAP]", "[UNATTRIBUTED CONTEXT]", "保存済み要約"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("lost %q", want[:min(len(want), 50)])
		}
	}
	if strings.Contains(text.String(), "PRIVATE") || strings.Contains(exported.String(), "PRIVATE") {
		t.Fatal("excluded data escaped through metadata or raw")
	}
	if !reflect.DeepEqual(filter.Apply(view), view) {
		t.Fatal("filter is not idempotent")
	}
}

func TestFilterRejectsUnknownSelectors(t *testing.T) {
	for _, input := range []string{"", "chat", "pretty", "role=user"} {
		if _, err := ParseFilter(input); err == nil {
			t.Fatalf("accepted unsupported filter %q", input)
		}
	}
}
