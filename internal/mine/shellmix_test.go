package mine

import (
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// seg is one shell segment of a Bash call: the call id, the transcript line
// (Seq) the segment was split from, and its share of the result bytes.
func seg(session, id string, seq, out int, at time.Time) event.Event {
	return event.Event{Seq: seq, Time: at, Project: "p", Session: session, ToolUseID: id,
		Kind: event.KindShell, Action: "x", OutBytes: out}
}

func native(session, name string, seq int, at time.Time) event.Event {
	return event.Event{Seq: seq, Time: at, Project: "p", Session: session, Kind: event.KindTool, Action: name}
}

var mixDay = time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)

// TestSummarize_ShellMix_CountsACallOnceNotPerSegment is the bead's fixture:
// one three-segment compound and one bare Bash call are 2 calls, not 4.
func TestSummarize_ShellMix_CountsACallOnceNotPerSegment(t *testing.T) {
	evs := []event.Event{
		seg("s1", "c1", 1, 4, mixDay), seg("s1", "c1", 1, 3, mixDay), seg("s1", "c1", 1, 3, mixDay),
		seg("s1", "c2", 2, 7, mixDay),
		native("s1", "Read", 3, mixDay), native("s1", "Grep", 4, mixDay), native("s1", "Task", 5, mixDay),
	}
	s, err := Summarize(writeEventsFile(t, evs), "corpus", Window{})
	if err != nil {
		t.Fatal(err)
	}
	m := s.Shell
	if m.BashCalls != 2 {
		t.Errorf("BashCalls = %d, want 2 (not 4 segments)", m.BashCalls)
	}
	if m.NativeCalls != 2 {
		t.Errorf("NativeCalls = %d, want 2 (Read, Grep; Task is not a file tool)", m.NativeCalls)
	}
	if m.Ratio == nil || *m.Ratio != 1.0 {
		t.Errorf("Ratio = %v, want 1.0", m.Ratio)
	}
	if m.BashBytes != 17 {
		t.Errorf("BashBytes = %d, want 17 (each segment's share counted once)", m.BashBytes)
	}
	if m.Sessions != 1 || m.BashBytesPerSession != 17 {
		t.Errorf("sessions = %d, per session = %v, want 1 and 17", m.Sessions, m.BashBytesPerSession)
	}
	if m.UnkeyedBashCalls != 0 {
		t.Errorf("UnkeyedBashCalls = %d, want 0", m.UnkeyedBashCalls)
	}
}

func TestSummarize_ShellMix_NativeToolSet(t *testing.T) {
	evs := make([]event.Event, 0, 10)
	for i, n := range []string{"Read", "Edit", "MultiEdit", "Write", "NotebookEdit", "Grep", "Glob", "WebFetch", "Task", "mcp__x__y"} {
		evs = append(evs, native("s1", n, i+1, mixDay))
	}
	s, err := Summarize(writeEventsFile(t, evs), "corpus", Window{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Shell.NativeCalls != 7 {
		t.Errorf("NativeCalls = %d, want 7", s.Shell.NativeCalls)
	}
	if s.Shell.Ratio == nil || *s.Shell.Ratio != 0 {
		t.Errorf("Ratio = %v, want 0 (no Bash calls over 7 native)", s.Shell.Ratio)
	}
}

// TestSummarize_ShellMix_NoNativeCallsHasNoRatio: dividing by zero would print
// a number that means nothing.
func TestSummarize_ShellMix_NoNativeCallsHasNoRatio(t *testing.T) {
	s, err := Summarize(writeEventsFile(t, []event.Event{seg("s1", "c1", 1, 1, mixDay)}), "corpus", Window{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Shell.Ratio != nil {
		t.Errorf("Ratio = %v, want nil with no native calls", *s.Shell.Ratio)
	}
}

// TestSummarize_ShellMix_LegacyEventsGroupByTranscriptLine: rows written before
// ToolUseID existed group on session+Seq+time, so a compound is still one call
// and two calls at the same instant (different lines) stay two.
func TestSummarize_ShellMix_LegacyEventsGroupByTranscriptLine(t *testing.T) {
	evs := []event.Event{
		seg("s1", "", 10, 2, mixDay), seg("s1", "", 10, 2, mixDay), seg("s1", "", 10, 2, mixDay), // one compound
		seg("s1", "", 11, 5, mixDay), // a second call, same timestamp
		seg("s2", "", 10, 1, mixDay), // same Seq in another session
		seg("s1", "keyed", 12, 1, mixDay),
	}
	s, err := Summarize(writeEventsFile(t, evs), "corpus", Window{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Shell.BashCalls != 4 {
		t.Errorf("BashCalls = %d, want 4", s.Shell.BashCalls)
	}
	if s.Shell.UnkeyedBashCalls != 3 {
		t.Errorf("UnkeyedBashCalls = %d, want 3 (the calls grouped without an id)", s.Shell.UnkeyedBashCalls)
	}
}

// TestSummarize_ShellMix_WindowKeepsStreamsStartingInside: a stream is judged
// by its start, as mine.Build does, so a stream that begins 10-18 and runs on
// into 10-19 is out of a --since 2026-10-19 window.
func TestSummarize_ShellMix_WindowKeepsStreamsStartingInside(t *testing.T) {
	old := mixDay.AddDate(0, 0, -1)
	evs := []event.Event{
		seg("old", "o1", 1, 100, old), seg("old", "o2", 2, 100, mixDay), // starts 10-18
		seg("new", "n1", 1, 6, mixDay), native("new", "Read", 2, mixDay),
		{Seq: 1, Project: "p", Session: "untimed", Kind: event.KindShell, Action: "x", ToolUseID: "u1", OutBytes: 50},
	}
	path := writeEventsFile(t, evs)
	since := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	s, err := Summarize(path, "corpus", Window{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	if s.Shell.BashCalls != 1 || s.Shell.NativeCalls != 1 || s.Shell.BashBytes != 6 {
		t.Errorf("windowed mix = %+v, want 1 bash call, 1 native, 6 bytes (only session new)", s.Shell)
	}
	w := s.Window
	if w.Total != 3 || s.Kept != 1 || w.Untimed != 1 {
		t.Errorf("Window = %+v, want kept 1 of 3, 1 no-timestamp", w)
	}
	if len(s.Buckets) != 1 || s.Buckets[0].Events != 2 {
		t.Errorf("buckets = %+v, want one bucket of the 2 in-window events", s.Buckets)
	}

	s, err = Summarize(path, "corpus", Window{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Window.Set() || s.Shell.BashCalls != 4 {
		t.Errorf("unwindowed: Window=%+v BashCalls=%d, want unset and 4", s.Window, s.Shell.BashCalls)
	}
}
