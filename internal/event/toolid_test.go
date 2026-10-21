package event

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestShellEventsCarryTheirBashCallID pins ferret-2mf: every segment of one
// compound or pipe carries the id of the Bash call it was split from, a bare
// call carries its own, and a non-shell tool carries none.
func TestShellEventsCarryTheirBashCallID(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "call-compound", "Bash", `{"command":"a && b && c"}`),
		toolResultContent("r1", "call-compound", `"out"`),
		toolUse("u2", "call-bare", "Bash", `{"command":"ls"}`),
		toolResultContent("r2", "call-bare", `"out"`),
		toolUse("u3", "call-read", "Read", `{"file_path":"a.go"}`),
		toolResultContent("r3", "call-read", `"out"`),
	)
	evs := ingest(t, src)
	got := map[string]int{}
	for _, ev := range evs {
		switch ev.Kind {
		case KindShell:
			got[ev.ToolUseID]++
		case KindTool:
			if ev.ToolUseID != "" {
				t.Errorf("tool event %q carries ToolUseID %q, want none", ev.Action, ev.ToolUseID)
			}
		}
	}
	if got["call-compound"] != 3 || got["call-bare"] != 1 || len(got) != 2 {
		t.Errorf("segments per call id = %v, want call-compound 3, call-bare 1, nothing else", got)
	}
}

// TestToolUseIDIsOmittedWhenEmpty keeps old rows and non-shell rows lean: no
// "tu" key unless the id is set.
func TestToolUseIDIsOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(&Event{Kind: KindTool, Action: "Read"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"tu"`) {
		t.Errorf("empty ToolUseID serialized: %s", b)
	}
	b, err = json.Marshal(&Event{Kind: KindShell, ToolUseID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tu":"x"`) {
		t.Errorf("ToolUseID missing from %s", b)
	}
}
