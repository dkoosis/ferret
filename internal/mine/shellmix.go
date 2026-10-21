package mine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// nativeFileTools are the Claude Code tools that read or change files without a
// shell. A Bash call that did the same work (cat, sed, grep) is what the
// Bash-to-native ratio is meant to show shrinking (ferret-2mf).
var nativeFileTools = slices.Collect(strings.FieldsSeq("Read Edit MultiEdit Write NotebookEdit Grep Glob"))

// ShellMix is the Bash-against-native call mix of a window (ferret-2mf). A Bash
// call counts once however many segments shellnorm split it into: one grep |
// sed is one call on screen.
type ShellMix struct {
	BashCalls   int `json:"bashCalls"`
	NativeCalls int `json:"nativeCalls"`
	// Ratio is BashCalls per native file-tool call; nil when there were no
	// native calls, because a quotient of zero would read as "no Bash".
	Ratio *float64 `json:"ratio"`
	// BashBytes is the Bash results' total size. A compound's result is split
	// across its segments at ingest with the remainder carried, so summing the
	// segments' OutBytes counts every byte once.
	BashBytes int `json:"bashBytes"`
	// Sessions is the distinct sessions with any event in the window: the
	// divisor for BashBytesPerSession.
	Sessions            int     `json:"sessions"`
	BashBytesPerSession float64 `json:"bashBytesPerSession"`
	// UnkeyedBashCalls is how many of BashCalls had no tool_use id (events
	// ingested before ferret-2mf) and were grouped on transcript position
	// instead. Re-ingest to give them ids.
	UnkeyedBashCalls int `json:"unkeyedBashCalls"`
}

// shellTally accumulates a ShellMix over one read pass.
type shellTally struct {
	calls    map[string]struct{}
	unkeyed  map[string]struct{}
	sessions map[string]struct{}
	native   int
	bytes    int
}

func (t *shellTally) add(ev *event.Event) {
	if t.sessions == nil {
		t.calls, t.unkeyed, t.sessions = map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	}
	t.sessions[ev.Project+"/"+ev.Session] = struct{}{}
	switch ev.Kind {
	case event.KindShell:
		t.bytes += ev.OutBytes
		key, keyed := callKey(ev)
		t.calls[key] = struct{}{}
		if !keyed {
			t.unkeyed[key] = struct{}{}
		}
	case event.KindTool:
		if slices.Contains(nativeFileTools, ev.Action) {
			t.native++
		}
	}
}

// callKey names the Bash call a shell event came from. With a tool_use id that
// is the id. Without one (an event written before ferret-2mf) every segment of
// a call still shares the transcript line it was split from, so session, agent,
// Seq and time pick the call out; grouping on session and timestamp alone, as
// the first hand count did, merges two calls made in the same second.
func callKey(ev *event.Event) (key string, keyed bool) {
	if ev.ToolUseID != "" {
		return "id:" + ev.ToolUseID, true
	}
	return fmt.Sprintf("pos:%s|%d|%d", streamName(ev), ev.Seq, ev.Time.UnixNano()), false
}

func (t *shellTally) result() ShellMix {
	m := ShellMix{
		BashCalls: len(t.calls), NativeCalls: t.native, BashBytes: t.bytes,
		Sessions: len(t.sessions), UnkeyedBashCalls: len(t.unkeyed),
	}
	if t.native > 0 {
		r := float64(m.BashCalls) / float64(t.native)
		m.Ratio = &r
	}
	if m.Sessions > 0 {
		m.BashBytesPerSession = float64(m.BashBytes) / float64(m.Sessions)
	}
	return m
}
