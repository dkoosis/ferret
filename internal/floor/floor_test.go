package floor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

// writeFloorFixture drops a *.jsonl under a project-slug dir (transcript.Walk
// requires ≥2 path segments) and returns a transcript.Source pointing at it.
func writeFloorFixture(t *testing.T, root, slug, name string, lines []string) transcript.Source {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcript.Source{Path: p, Project: slug, Session: strings.TrimSuffix(name, ".jsonl")}
}

// TestScanSource_ClassifiesHookAndRosterBeforeFirstAssistantResponse pins the
// shape a real session actually has (verified against a live corpus,
// 2026-09-07): the roster attachments (deferred-tool/agent/mcp/skill) land
// AFTER the user's own real prompt line, not before it — CC assembles the
// roster once it has a real prompt to respond to — and only THEN does the
// model produce its first "assistant" line. So the floor window is "before
// the first assistant line", and it must span both the pre-prompt hook
// attachments AND the post-prompt roster attachments; nothing at or after
// the first response may count.
func TestScanSource_ClassifiesHookAndRosterBeforeFirstAssistantResponse(t *testing.T) {
	root := t.TempDir()
	src := writeFloorFixture(t, root, "-Users-dev-proj", "sess-a.jsonl", []string{
		`{"type":"mode","mode":"normal"}`,
		`{"type":"permission-mode","permissionMode":"auto"}`,
		`{"type":"attachment","attachment":{"type":"hook_success","stdout":"banner text"}}`,
		`{"type":"attachment","attachment":{"type":"hook_success","stdout":"<itzy-context>fresh recall block</itzy-context>"}}`,
		`{"type":"attachment","attachment":{"type":"hook_system_message","content":"printed banner"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>Caveat</local-command-caveat>"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":"do the actual thing dk wants"}}`,
		`{"type":"attachment","attachment":{"type":"deferred_tools_delta","addedNames":["Monitor"]}}`,
		`{"type":"attachment","attachment":{"type":"skill_listing","content":"skills: a, b, c"}}`,
		`{"type":"attachment","attachment":{"type":"auto_mode"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}`,
		`{"type":"attachment","attachment":{"type":"skill_listing","content":"a mid-session skill load must NOT count as floor"}}`,
	})

	sess, err := ScanSource(src)
	if err != nil {
		t.Fatalf("ScanSource: %v", err)
	}

	wantHook := len(`{"type":"hook_success","stdout":"banner text"}`) +
		len(`{"type":"hook_success","stdout":"<itzy-context>fresh recall block</itzy-context>"}`) +
		len(`{"type":"hook_system_message","content":"printed banner"}`)
	if sess.Cat.HookBytes != wantHook {
		t.Errorf("HookBytes = %d, want %d", sess.Cat.HookBytes, wantHook)
	}
	wantItzy := len("<itzy-context>fresh recall block</itzy-context>")
	if sess.Cat.ItzyBytes != wantItzy {
		t.Errorf("ItzyBytes = %d, want %d", sess.Cat.ItzyBytes, wantItzy)
	}
	wantRoster := len(`{"type":"deferred_tools_delta","addedNames":["Monitor"]}`) +
		len(`{"type":"skill_listing","content":"skills: a, b, c"}`)
	if sess.Cat.RosterBytes != wantRoster {
		t.Errorf("RosterBytes = %d, want %d (a post-response skill_listing must not be folded in)", sess.Cat.RosterBytes, wantRoster)
	}
	// auto_mode is a real attachment class but not one of the two named
	// buckets — it must contribute to neither, and must not blow up the scan.
	if sess.Cat.Sum() != wantHook+wantRoster {
		t.Errorf("Sum() = %d, want %d", sess.Cat.Sum(), wantHook+wantRoster)
	}
}

// TestScanSource_SidechainAssistantLineDoesNotCloseTheWindow guards against a
// subagent turn inlined into the parent transcript (isSidechain:true) being
// mistaken for the main thread's first response — it is not: a sidechain is
// spawned BY the main thread's own first turn, so treating it as the
// boundary would truncate the floor window before the roster ever loads.
func TestScanSource_SidechainAssistantLineDoesNotCloseTheWindow(t *testing.T) {
	root := t.TempDir()
	src := writeFloorFixture(t, root, "-Users-dev-proj", "sess-b.jsonl", []string{
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"stray inlined sidechain turn"}]}}`,
		`{"type":"attachment","attachment":{"type":"skill_listing","content":"still before the real response"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the real first response"}]}}`,
		`{"type":"attachment","attachment":{"type":"skill_listing","content":"after — must not count"}}`,
	})

	sess, err := ScanSource(src)
	if err != nil {
		t.Fatalf("ScanSource: %v", err)
	}
	want := len(`{"type":"skill_listing","content":"still before the real response"}`)
	if sess.Cat.RosterBytes != want {
		t.Errorf("RosterBytes = %d, want %d — a sidechain assistant line must not close the floor window", sess.Cat.RosterBytes, want)
	}
}

// TestSession_ShareAndBytesSplit pins the pure arithmetic: FloorBytes folds
// in the caller-supplied rule bytes, TurnBytes excludes them, and Share puts
// ruleBytes in the denominator too (real cost the transcript never records
// as a line of its own).
func TestSession_ShareAndBytesSplit(t *testing.T) {
	s := Session{
		TranscriptBytes: 1000,
		Cat:             Category{HookBytes: 200, RosterBytes: 100},
	}
	if got := s.FloorBytes(50); got != 350 {
		t.Errorf("FloorBytes(50) = %d, want 350", got)
	}
	if got := s.TurnBytes(); got != 700 {
		t.Errorf("TurnBytes() = %d, want 700", got)
	}
	// share = (300 floor + 50 rule) / (1000 transcript + 50 rule) = 350/1050
	want := 350.0 / 1050.0
	if got := s.Share(50); got < want-1e-9 || got > want+1e-9 {
		t.Errorf("Share(50) = %v, want %v", got, want)
	}
}

// TestSession_ShareZeroTotal guards the degenerate empty-transcript case: a
// zero-byte session with zero rule bytes must report 0, not divide by zero.
func TestSession_ShareZeroTotal(t *testing.T) {
	var s Session
	if got := s.Share(0); got != 0 {
		t.Errorf("Share(0) on an empty session = %v, want 0", got)
	}
}

// TestRuleFiles_MeasuresRealDiskBytes pins the disclosed constraint: rule
// bytes are read straight off disk, not guessed — and an absent tier (no
// match) is 0, not an error.
func TestRuleFiles_MeasuresRealDiskBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, bytes, err := RuleFiles(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("RuleFiles: %v", err)
	}
	if files != 2 || bytes != 15 {
		t.Errorf("RuleFiles = (%d, %d), want (2, 15)", files, bytes)
	}

	files, bytes, err = RuleFiles(filepath.Join(dir, "no-such", "*.md"))
	if err != nil {
		t.Fatalf("RuleFiles on an absent tier must not error: %v", err)
	}
	if files != 0 || bytes != 0 {
		t.Errorf("RuleFiles on an absent tier = (%d, %d), want (0, 0)", files, bytes)
	}
}

// TestRollup_WorstShareSurfacesTheShortSession is the bead's own headline
// claim: a short session with the same fixed floor shows a WORSE ratio than
// a long one, and Rollup must surface it rather than average it away.
func TestRollup_WorstShareSurfacesTheShortSession(t *testing.T) {
	short := Session{Session: "short", TranscriptBytes: 100, Cat: Category{HookBytes: 40}}
	mid := Session{Session: "mid", TranscriptBytes: 10000, Cat: Category{HookBytes: 40}}
	long := Session{Session: "long", TranscriptBytes: 100000, Cat: Category{HookBytes: 40}}

	tot := Rollup([]Session{short, mid, long}, 0)
	if tot.WorstSession != "short" {
		t.Errorf("WorstSession = %q, want %q", tot.WorstSession, "short")
	}
	wantWorst := 40.0 / 100.0 * 100
	if tot.WorstSharePct < wantWorst-1e-6 || tot.WorstSharePct > wantWorst+1e-6 {
		t.Errorf("WorstSharePct = %v, want %v", tot.WorstSharePct, wantWorst)
	}
	if tot.MedianSharePct >= tot.WorstSharePct {
		t.Errorf("MedianSharePct (%v) should sit below the worst outlier (%v)", tot.MedianSharePct, tot.WorstSharePct)
	}
}

// TestRollup_Empty guards the 0-sessions case: no divide-by-zero, no panic.
func TestRollup_Empty(t *testing.T) {
	tot := Rollup(nil, 500)
	if tot.Sessions != 0 || tot.SharePct != 0 {
		t.Errorf("Rollup(nil) = %+v, want a zero Totals", tot)
	}
}
