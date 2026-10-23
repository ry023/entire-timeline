package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRestoreDiagnosticSerializationDifferenceStillFails(t *testing.T) {
	header := strings.Repeat("{\"type\":\"session.start\"}\n", 8)
	a := []byte(header + `{"type":"assistant.message","data":{"content":"PRIVATE body","reasoningText":"PRIVATE reasoning"}}`)
	b := []byte(header + `{"data": {"reasoningText":"PRIVATE reasoning", "content":"PRIVATE \u0062ody"}, "type":"assistant.message"}`)
	store := fixtureStore{[][]fixtureSession{{{id: "s", start: 8, data: a}}, {{id: "s", start: 8, data: b}}}}
	_, plainErr := Restore(context.Background(), store, "base..HEAD")
	var log bytes.Buffer
	_, verboseErr := RestoreWithOptions(context.Background(), store, "base..HEAD", RestoreOptions{Diagnostics: &log})
	if plainErr == nil || verboseErr == nil || plainErr.Error() != verboseErr.Error() {
		t.Fatal(plainErr, verboseErr)
	}
	for _, want := range []string{"selected_checkpoints=2", "selected_raw_range=[8,9)", "raw_line=8 physical_line=9", "different_lines=1", "decoded_json_equal=true", "possible_serialization_only_difference=true"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("missing %q: %s", want, log.String())
		}
	}
	if strings.Contains(log.String(), "PRIVATE") {
		t.Fatalf("leaked event text: %s", log.String())
	}
}

func mismatchLog(a, b string) string {
	var log bytes.Buffer
	x := snapshot{metadata: Metadata{SessionID: "s"}, checkpoint: "a", lines: rawLines([]byte(a))}
	y := snapshot{metadata: Metadata{SessionID: "s"}, checkpoint: "b", lines: rawLines([]byte(b))}
	diagnostics{&log}.mismatch(x, y, 0)
	return log.String()
}

func TestDiagnosticChangedPathsOmitValuesAndApplicationKeys(t *testing.T) {
	a := `{"type":"PRIVATE_EVENT_OLD","id":"PRIVATE_ID_OLD","timestamp":"PRIVATE_TIME_OLD","data":{"content":"PRIVATE_BODY_OLD","reasoningText":"PRIVATE_COT_OLD","toolRequests":[{"arguments":{"PRIVATE_FILE_PATH":"PRIVATE_TOKEN_OLD"}}],"PRIVATE_OBJECT_KEY":"PRIVATE_OLD","model":"PRIVATE_MODEL_OLD","outputTokens":9007199254740992},"message":{"content":[{"type":"thinking","thinking":"PRIVATE_THINKING_OLD"}]}}`
	b := strings.ReplaceAll(a, "OLD", "NEW")
	b = strings.ReplaceAll(b, "9007199254740992", "9007199254740993")
	b = strings.ReplaceAll(b, `"PRIVATE_OBJECT_KEY":"PRIVATE_NEW"`, `"PRIVATE_OTHER_KEY":"PRIVATE_NEW"`)
	log := mismatchLog(a, b)
	for _, want := range []string{"decoded_json_equal=false", "changed_path=/data/content", "changed_path=/data/reasoningText", "changed_path=/data/toolRequests/0/arguments", "changed_path=/data/<other-key>", "changed_path=/data/outputTokens", "changed_path=/message/content/0/thinking", `event_type="<other>"->"<other>" event_type_equal=false`, "field=id present=true->true equal=false"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %s", want, log)
		}
	}
	for _, secret := range []string{"PRIVATE", "9007199254740992", "9007199254740993"} {
		if strings.Contains(log, secret) {
			t.Fatalf("leaked %q: %s", secret, log)
		}
	}
}

func TestDiagnosticAdditionRemovalAndInvalidJSON(t *testing.T) {
	log := mismatchLog(`{"data":{"content":"PRIVATE","reasoningText":"PRIVATE"}}`, `{"data":{"content":null,"toolRequests":[]}}`)
	for _, want := range []string{"changed_path=/data/content change=value-or-type string(bytes=7) -> null", "changed_path=/data/reasoningText change=removed", "changed_path=/data/toolRequests change=added"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %s", want, log)
		}
	}
	log = mismatchLog(`{"data":"PRIVATE`, `{"data":"PRIVATE"}`)
	if !strings.Contains(log, "json_valid=false->true") || strings.Contains(log, "PRIVATE") {
		t.Fatal(log)
	}
}

func TestDiagnosticNearbyLineShift(t *testing.T) {
	a := "{\"type\":\"user.message\",\"id\":\"PRIVATE_A\"}\n{\"id\":\"PRIVATE_B\"}"
	b := "{\"id\":\"PRIVATE_INSERTED\"}\n" + a
	log := mismatchLog(a, b)
	if !strings.Contains(log, "other_raw_line=1 shift=+1") || strings.Contains(log, "PRIVATE") {
		t.Fatal(log)
	}
}

func TestDiagnosticBoundsAndNumberPrecision(t *testing.T) {
	var a, b []string
	for i := 0; i < 100; i++ {
		a = append(a, fmt.Sprintf("PRIVATE_OLD_%d", i))
		b = append(b, fmt.Sprintf("PRIVATE_NEW_%d", i))
	}
	x, _ := json.Marshal(map[string]any{"content": a})
	y, _ := json.Marshal(map[string]any{"content": b})
	log := mismatchLog(string(x), string(y))
	if strings.Count(log, "changed_path=") != 16 || !strings.Contains(log, "changed_paths_truncated=true") || strings.Contains(log, "PRIVATE") {
		t.Fatal(log)
	}
	log = mismatchLog(`{"data":{"outputTokens":9007199254740992}}`, `{"data":{"outputTokens":9007199254740993}}`)
	if !strings.Contains(log, "decoded_json_equal=false") {
		t.Fatal("large integers rounded: ", log)
	}
}
