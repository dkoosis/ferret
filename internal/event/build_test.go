package event

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

// writeTranscript drops lines into a temp .jsonl and returns its Source.
func writeTranscript(t *testing.T, lines ...string) transcript.Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sess.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return transcript.Source{Path: p, Project: "proj", Session: "sess"}
}

func toolUse(uuid, id, name, inputJSON string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":"2026-06-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`,
		uuid, id, name, inputJSON)
}

func toolResult(uuid, id string, isError bool) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-06-10T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":%v}]}}`,
		uuid, id, isError)
}

func ingest(t *testing.T, src transcript.Source) []*Event {
	t.Helper()
	b := NewBuilder()
	var evs []*Event
	if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestPairingAndStatus(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`),
		toolResult("u2", "t1", false),
		toolUse("u3", "t2", "Edit", `{"file_path":"a.go"}`),
		toolResult("u4", "t2", true),
		toolUse("u5", "t3", "Grep", `{"pattern":"x"}`), // never resolved
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	for i, want := range []string{StatusOK, StatusFail, StatusNone} {
		if evs[i].Status != want {
			t.Errorf("event %d (%s) status = %q, want %q", i, evs[i].Action, evs[i].Status, want)
		}
	}
	if evs[0].Target != "a.go" || evs[2].Target != "x" {
		t.Errorf("targets = %q, %q; want a.go, x", evs[0].Target, evs[2].Target)
	}
}

func TestCompoundFailureIsCFailNotFail(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./... && go build ./..."}`),
		toolResult("u2", "t1", true),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (split compound)", len(evs))
	}
	for _, ev := range evs {
		if !ev.Compound {
			t.Errorf("%s: Compound = false, want true", ev.Action)
		}
		if ev.Status != StatusCFail {
			t.Errorf("%s: status = %q, want %q — failing segment is unknown", ev.Action, ev.Status, StatusCFail)
		}
	}
}

func TestSingleSegmentFailureIsFail(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./..."}`),
		toolResult("u2", "t1", true),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Status != StatusFail {
		t.Errorf("status = %q, want %q", evs[0].Status, StatusFail)
	}
}

func TestDuplicateUUIDDedup(t *testing.T) {
	// Resumed sessions copy history: the same uuid in a second file must not double-count.
	line1 := toolUse("dup", "t1", "Read", `{"file_path":"a.go"}`)
	src1 := writeTranscript(t, line1, toolResult("r1", "t1", false))
	src2 := writeTranscript(t, line1) // copied history

	b := NewBuilder()
	var evs []*Event
	for _, src := range []transcript.Source{src1, src2} {
		if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (uuid dedup across files)", len(evs))
	}
	if b.Stats.Deduped != 1 {
		t.Errorf("Stats.Deduped = %d, want 1", b.Stats.Deduped)
	}
}

func TestPromptDetection(t *testing.T) {
	src := writeTranscript(t,
		`{"type":"user","uuid":"p1","timestamp":"2026-06-10T10:00:00Z","message":{"role":"user","content":"fix the bug"}}`,
		`{"type":"user","uuid":"p2","isMeta":true,"message":{"role":"user","content":"injected context"}}`,
		toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`),
		toolResult("u2", "t1", false), // tool_result user line: not a prompt
	)
	evs := ingest(t, src)
	prompts := 0
	for _, ev := range evs {
		if ev.Kind == KindPrompt {
			prompts++
		}
	}
	if prompts != 1 {
		t.Errorf("prompts = %d, want 1 (meta and tool_result lines are not prompts)", prompts)
	}
}

func TestRetryAttribution(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./..."}`),
		toolResult("u2", "t1", true),
		toolUse("u3", "t2", "Bash", `{"command":"go test ./..."}`),
		toolResult("u4", "t2", false),
		toolUse("u5", "t3", "Bash", `{"command":"go test ./..."}`),
		toolResult("u6", "t3", false),
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	if !evs[1].Retry {
		t.Error("second go_test follows a failure — Retry should be true")
	}
	if evs[2].Retry {
		t.Error("third go_test follows a success — the retry window is closed")
	}
}

func TestSidechainFlag(t *testing.T) {
	src := writeTranscript(t,
		`{"type":"assistant","uuid":"s1","isSidechain":true,"timestamp":"2026-06-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}`,
	)
	evs := ingest(t, src)
	if len(evs) != 1 || !evs[0].Sidechain {
		t.Fatalf("want 1 sidechain event, got %+v", evs)
	}
}
