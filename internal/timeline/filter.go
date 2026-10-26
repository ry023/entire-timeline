package timeline

import (
	"fmt"
	"strings"
)

// Filter selects a view of a restored timeline independently of output format.
// Keep parsing here so additional selectors can be added without teaching each
// renderer or the restoration code how to interpret them. The zero value is all.
type Filter struct{ selector string }

func ParseFilter(selector string) (Filter, error) {
	switch selector {
	case "all", "conversation":
		return Filter{selector: selector}, nil
	default:
		return Filter{}, fmt.Errorf("unsupported filter %q; use all or conversation", selector)
	}
}

// Apply preserves source order and leaves the restored timeline untouched.
// Conversation is a text projection: raw is omitted because it can contain the
// very reasoning/tool payloads being excluded. Use all for a lossless export.
func (f Filter) Apply(t Timeline) Timeline {
	if f.selector != "conversation" {
		return t
	}
	view := t
	view.Events = make([]Event, 0, len(t.Events))
	view.Sessions = make([]string, 0, len(t.Sessions))
	view.UnattributedEvents = 0
	sessions := map[string]bool{}
	for _, e := range t.Events {
		if e.Error != nil {
			view.Events = append(view.Events, e)
			continue
		}
		if strings.TrimSpace(e.ConversationText()) == "" {
			continue
		}
		parts := make([]Part, 0, len(e.Parts))
		for _, p := range e.Parts {
			if p.Kind == "text" && p.Text != "" {
				parts = append(parts, Part{Kind: "text", Text: p.Text})
			}
		}
		e.Parts = parts
		e.Raw = nil
		view.Events = append(view.Events, e)
		sessions[e.SessionID] = true
		if e.Attribution != nil {
			view.UnattributedEvents++
		}
	}
	for _, id := range t.Sessions {
		if sessions[id] {
			view.Sessions = append(view.Sessions, id)
		}
	}
	return view
}
