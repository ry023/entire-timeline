package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTextAndJSONLRenderers(t *testing.T) {
	timeline := restoreFixture(t, []fixtureSession{{id: "compact", start: 1, data: prefix(fixture(t, "copilot-compact.jsonl"), 3)}, {id: "native", start: 1, data: fixture(t, "copilot-native.jsonl")}})
	var normal, detailed, exported bytes.Buffer
	if err := RenderText(&normal, timeline, TextOptions{Width: 40}); err != nil {
		t.Fatal(err)
	}
	if err := RenderText(&detailed, timeline, TextOptions{IncludeTools: true, Width: 40}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"# 認証機能\n\nAuthProviderを利用してください。\n\n```go\nfunc login() {\n    return\n}\n```", "第二のcontent block", "既存の実装を調査します。", "調査結果をもとに進めます。", "既存テストを確認する。", "テストを追加しました。"} {
		if !strings.Contains(normal.String(), body) {
			t.Fatalf("missing full message %q", body)
		}
	}
	for _, s := range []string{"Tool: Read", "Tool: bash", "Tool result:", "Session 1 = compact", "Session 2 = native", strings.Repeat("─", 40)} {
		if !strings.Contains(normal.String(), s) {
			t.Errorf("missing %q", s)
		}
	}
	if strings.Contains(normal.String(), "SECRET TOOL RESULT") {
		t.Fatal("normal mode leaked large tool results")
	}
	if !strings.Contains(detailed.String(), "SECRET TOOL RESULT") || !strings.Contains(detailed.String(), "detailedContent") {
		t.Fatal("tool detail lost")
	}
	if err := RenderJSONL(&exported, timeline); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(exported.Bytes()), []byte{'\n'})
	if len(lines) != len(timeline.Events) {
		t.Fatal("wrong JSONL count")
	}
	for i, line := range lines {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		var original, roundTrip any
		json.Unmarshal(timeline.Events[i].Raw, &original)
		json.Unmarshal(e.Raw, &roundTrip)
		ob, _ := json.Marshal(original)
		rb, _ := json.Marshal(roundTrip)
		if !bytes.Equal(ob, rb) || e.SessionID == "" || e.Agent == "" || e.Type == "" {
			t.Fatal("raw export metadata/content lost")
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }
func TestRenderWriteErrors(t *testing.T) {
	timeline := restoreFixture(t, []fixtureSession{{id: "x", data: fixture(t, "copilot-native.jsonl")}})
	if RenderText(brokenWriter{}, timeline, TextOptions{}) == nil || RenderJSONL(brokenWriter{}, timeline) == nil {
		t.Fatal("write error swallowed")
	}
}

func TestConversationTextExcludesReasoningAndTools(t *testing.T) {
	for _, raw := range []string{
		`{"type":"assistant.message","data":{"content":"本文","reasoningText":"秘密の思考","toolRequests":[{"name":"bash","arguments":{"command":"秘密のツール"}}]}}`,
		`{"v":1,"agent":"copilot-cli","type":"assistant","content":[{"type":"text","text":"本文"},{"type":"thinking","thinking":"秘密の思考"},{"type":"tool_use","name":"bash","input":{"command":"秘密のツール"}}]}`,
	} {
		got := restoreFixture(t, []fixtureSession{{id: "s", data: []byte(raw)}})
		if text := got.Events[0].ConversationText(); text != "本文" {
			t.Fatalf("unexpected conversation text: %q", text)
		}
		got.Events[0].Summary = "保存した要約"
		var text, exported bytes.Buffer
		if err := RenderText(&text, got, TextOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := RenderJSONL(&exported, got); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"本文", "秘密の思考", "保存した要約"} {
			if !strings.Contains(text.String(), want) || !strings.Contains(exported.String(), want) {
				t.Fatalf("lost %q", want)
			}
		}
	}
}

func TestStreamingFragmentsRemainRawButAreNotSummarySources(t *testing.T) {
	raw := []byte(`{"type":"assistant.message_delta","data":{"deltaContent":"streaming fragment"}}`)
	got := restoreFixture(t, []fixtureSession{{id: "s", data: raw}})
	if got.Events[0].ConversationText() != "" || !bytes.Contains(got.Events[0].Raw, []byte("streaming fragment")) {
		t.Fatal("fragment summarized or lost")
	}
}

func TestClaudeAndUnknownBlocksRemainVisible(t *testing.T) {
	raw := []byte(`{"type":"assistant","uuid":"claude-event","timestamp":"2026-01-01T00:00:00Z","message":{"content":[{"type":"text","text":"intermediate response"},{"type":"thinking","thinking":"consider the design"},{"type":"image","source":{"data":"image bytes"}},{"type":"tool_use","name":"Edit","input":{"path":"file"}}]}}`)
	got := restoreFixture(t, []fixtureSession{{id: "claude", agent: "Claude Code", data: raw}})
	var b bytes.Buffer
	RenderText(&b, got, TextOptions{})
	for _, s := range []string{"intermediate response", "consider the design", "image bytes", "Tool: Edit"} {
		if !strings.Contains(b.String(), s) {
			t.Fatal("lost block:", s)
		}
	}
	if got.Events[0].ID != "claude-event" {
		t.Fatal("lost UUID")
	}
}

func TestLongTextIsNeverTruncated(t *testing.T) {
	long := strings.Repeat("全文・Long message\n", 10000)
	got := restoreFixture(t, []fixtureSession{{id: "x", data: []byte(compactEvent("", long))}})
	var b bytes.Buffer
	if err := RenderText(&b, got, TextOptions{Width: 32}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), long) {
		t.Fatal("long message truncated")
	}
}

func TestUnexpectedPayloadsAreNotSilentlyDropped(t *testing.T) {
	for _, raw := range []string{
		`{"type":"future.message","text":"unknown native payload"}`,
		`{"v":1,"agent":"copilot-cli","type":"assistant","content":[{"type":"text","text":{"unexpected":"shape"}},{"type":"thinking","thinking":12345}]}`,
	} {
		got := restoreFixture(t, []fixtureSession{{id: "x", data: []byte(raw)}})
		var b bytes.Buffer
		if err := RenderText(&b, got, TextOptions{}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "unknown native payload") {
			if !strings.Contains(b.String(), "unknown native payload") {
				t.Fatal("unknown event payload lost")
			}
		} else if !strings.Contains(b.String(), "unexpected") || !strings.Contains(b.String(), "12345") {
			t.Fatal("malformed content blocks lost")
		}
	}
}
