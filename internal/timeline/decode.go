package timeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func canonicalAgent(agent string) string {
	switch agent {
	case "Copilot CLI", "copilot-cli":
		return "copilot-cli"
	case "Claude Code", "claude-code":
		return "claude-code"
	default:
		return agent
	}
}

func supportedAgent(agent string) bool {
	a := canonicalAgent(agent)
	return a == "copilot-cli" || a == "claude-code"
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}

func str(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }

func decode(agent string, raw []byte) (Event, error) {
	e := Event{Agent: canonicalAgent(agent), Raw: append(json.RawMessage(nil), raw...)}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return e, fmt.Errorf("invalid JSON event: %w", err)
	}
	if m == nil {
		return e, fmt.Errorf("expected a JSON event object")
	}
	e.Type = str(m["type"])
	e.ID = str(m["id"])
	if e.ID == "" {
		e.ID = str(m["uuid"])
	}
	if e.Type == "" {
		return e, fmt.Errorf("event is missing its type")
	}
	ts := str(m["timestamp"])
	if ts == "" {
		ts = str(m["ts"])
	}
	if ts != "" {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return e, fmt.Errorf("invalid timestamp %q: %w", ts, err)
		}
		e.Timestamp = &t
	} else if v := m["timestamp"]; len(v) > 0 && string(v) != "null" && string(v) != `""` {
		return e, fmt.Errorf("timestamp must be an RFC3339 string")
	}
	// Entire's compact v1 format is independently recognized. Its blocks may
	// include inline results, unlike native Copilot's separate result events.
	if _, compact := m["v"]; compact {
		if string(m["v"]) != "1" {
			return e, fmt.Errorf("unsupported compact transcript version %s", m["v"])
		}
		if a := str(m["agent"]); a != "" && canonicalAgent(a) != e.Agent {
			return e, fmt.Errorf("event agent %q disagrees with metadata %q", a, agent)
		}
		e.Role = e.Type
		e.Parts = contentParts(m["content"])
		if len(e.Parts) == 0 {
			e.Parts = []Part{{Kind: "data", Data: raw}}
		}
		return e, nil
	}
	if e.Agent == "copilot-cli" {
		decodeCopilot(&e, m)
	} else {
		decodeClaude(&e, m)
	}
	return e, nil
}

func decodeCopilot(e *Event, m map[string]json.RawMessage) {
	d := object(m["data"])
	switch e.Type {
	case "user.message", "assistant.message", "assistant.message_delta":
		e.Role = strings.Split(e.Type, ".")[0]
		e.Parts = contentParts(d["content"])
		if e.Type == "assistant.message_delta" && len(e.Parts) == 0 {
			e.Parts = contentParts(d["deltaContent"])
		}
		if text := str(d["reasoningText"]); text != "" {
			e.Parts = append(e.Parts, Part{Kind: "text", Text: text})
		}
		var requests []json.RawMessage
		if json.Unmarshal(d["toolRequests"], &requests) == nil {
			for _, req := range requests {
				e.Parts = append(e.Parts, Part{Kind: "tool_call", Name: str(object(req)["name"]), Data: req})
			}
		}
	case "tool.execution_start":
		e.Role = "tool"
		e.Parts = []Part{{Kind: "tool_call", Name: str(d["toolName"]), Data: m["data"]}}
	case "tool.execution_complete", "tool.execution_partial_result":
		e.Role = "tool"
		e.Parts = []Part{{Kind: "tool_result", Name: str(d["toolCallId"]), Data: m["data"]}}
	default:
		e.Role = "system"
		data := m["data"]
		if len(data) == 0 {
			data = e.Raw
		}
		e.Parts = []Part{{Kind: "data", Data: data}}
	}
	// An unknown payload remains visible instead of disappearing from text.
	if len(e.Parts) == 0 {
		e.Parts = []Part{{Kind: "data", Data: e.Raw}}
	}
}

func decodeClaude(e *Event, m map[string]json.RawMessage) {
	switch e.Type {
	case "user", "assistant":
		e.Role = e.Type
		e.Parts = contentParts(object(m["message"])["content"])
		if len(e.Parts) == 0 {
			e.Parts = contentParts(m["content"])
		}
	default:
		e.Role = "system"
	}
	if len(e.Parts) == 0 {
		e.Parts = []Part{{Kind: "data", Data: e.Raw}}
	}
}

func contentParts(raw json.RawMessage) []Part {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if s := str(raw); s != "" {
		return []Part{{Kind: "text", Text: s}}
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		if string(raw) == `""` {
			return nil
		}
		return []Part{{Kind: "data", Data: raw}}
	}
	var parts []Part
	for _, b := range blocks {
		m := object(b)
		kind := str(m["type"])
		switch kind {
		case "text", "input_text", "output_text", "":
			if text, ok := m["text"]; ok {
				parts = append(parts, Part{Kind: "text", Text: str(text)})
				continue
			}
		case "thinking":
			parts = append(parts, Part{Kind: "text", Text: str(m["thinking"])})
			continue
		case "tool_use", "tool_call":
			parts = append(parts, Part{Kind: "tool_call", Name: str(m["name"]), Data: b})
			continue
		case "tool_result":
			parts = append(parts, Part{Kind: "tool_result", Name: str(m["tool_use_id"]), Data: b})
			continue
		}
		parts = append(parts, Part{Kind: "data", Data: b})
	}
	return parts
}
