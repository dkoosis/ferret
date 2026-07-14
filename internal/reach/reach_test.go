package reach

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/transcript"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		wantCue string
		wantCls Class
		wantOK  bool
	}{
		// recall.md trigger phrasings (the autopsy's named recall questions)
		{"i-thought-we", "I thought we shifted to a global version of the Judge script. Do you see that", "i thought we", ClassRecall, true},
		{"dont-we-already", "what's capture-prompt for? don't we do that w cc logs already?", "don't we already", ClassRecall, true},
		{"where-do-we-stand", "where do we stand WRT treehouse?", "where do we stand", ClassRecall, true},
		{"do-you-remember", "do you remember the loto lock format we chose?", "do you remember", ClassRecall, true},
		{"did-we", "did we close any beads last night?", "did we", ClassRecall, true},
		{"didnt-we", "didn't we already decide to self-host?", "did we", ClassRecall, true},
		{"what-did-we-decide", "what did we decide about the prefix?", "what did we decide", ClassRecall, true},
		{"already-have", "We already decided to self-host and i thought you were running that", "already have", ClassRecall, true},
		// tx-vtea re-orientation asides
		{"where-are-we", "where are we with SM benchmark", "where are we", ClassReorient, true},
		{"i-forget", "state the finding again -- i forget", "i forget", ClassReorient, true},
		{"remind-me", "fs64 is just a random string to me. remind me of it's meaning", "remind me", ClassReorient, true},
		{"confused-where", "I am once again confused by where we are with regard to loto", "confused where", ClassReorient, true},
		// recall wins over reorient when both could match
		{"recall-beats-reorient", "remind me — where do we stand on search?", "where do we stand", ClassRecall, true},
		// non-opportunities: operational, not recall-of-knowledge
		{"plain-task", "add a --json flag to the reach command", "", "", false},
		{"empty", "", "", "", false},
		{"ship-it", "ship it, merge the PR", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cue, cls, ok := Classify(tt.prompt)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (cue=%q class=%q)", ok, tt.wantOK, cue, cls)
			}
			if !ok {
				return
			}
			if cue != tt.wantCue || cls != tt.wantCls {
				t.Errorf("got (cue=%q class=%q), want (cue=%q class=%q)", cue, cls, tt.wantCue, tt.wantCls)
			}
		})
	}
}

func TestClassifyShellCmd(t *testing.T) {
	tests := []struct {
		cmd  string
		want Reach
	}{
		{"trixi_search", ReachStore},
		{"trixi_get", ReachStore},
		{"trixi_set", ReachNone}, // a write is not a reach
		{"bd_show", ReachBeads},
		{"bd_query", ReachBeads},
		{"bd_update", ReachNone}, // a write is not a reach
		{"rg", ReachGrep},
		{"grep", ReachGrep},
		{"fd", ReachGrep},
		{"snipe", ReachGrep},
		{"snipe_refs", ReachGrep},
		{"gh_pr", ReachGh},
		{"gh_api", ReachGh},
		{"git_log", ReachGh},
		{"git_grep", ReachGh},
		{"git_commit", ReachNone},
		{"make", ReachNone},
		{"go_build", ReachNone},
	}
	for _, tt := range tests {
		if got, _ := classifyShellCmd(tt.cmd); got != tt.want {
			t.Errorf("classifyShellCmd(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestClassifyReachBlock(t *testing.T) {
	bash := func(cmd string) *transcript.Block {
		return newBlock("tool_use", "Bash", `{"command":`+jsonStr(cmd)+`}`)
	}
	tests := []struct {
		name string
		blk  *transcript.Block
		want Reach
	}{
		{"get_nug", newBlock("tool_use", "mcp__trixi__get_nug", `{}`), ReachStore},
		{"set_nug-not-reach", newBlock("tool_use", "mcp__trixi__set_nug", `{}`), ReachNone},
		{"grep-tool", newBlock("tool_use", "Grep", `{}`), ReachGrep},
		{"read-tool", newBlock("tool_use", "Read", `{}`), ReachGrep},
		{"edit-not-reach", newBlock("tool_use", "Edit", `{}`), ReachNone},
		{"web-search", newBlock("tool_use", "WebSearch", `{}`), ReachGh},
		{"bash-trixi-search", bash(`trixi search "loto lock"`), ReachStore},
		{"bash-rg", bash(`rg -n foo internal/`), ReachGrep},
		{"bash-compound-first-retrieval", bash(`cd /tmp && bd show x-1`), ReachBeads},
		{"bash-build-not-reach", bash(`make check`), ReachNone},
		{"text-block-not-reach", newBlock("text", "", ``), ReachNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := classifyReachBlock(tt.blk); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestScanSource_ReachAndMiss builds a tiny transcript and asserts the scanner
// pairs each recall opportunity with the first following retrieval action, and
// closes an unanswered opportunity as ReachNone.
func TestScanSource_ReachAndMiss(t *testing.T) {
	win := Window{
		Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC),
	}
	lines := []string{
		// opp 1: recall question → assistant reaches get_nug (store HIT)
		userLine("2026-07-04T10:00:00Z", "do you remember the loto lock format?"),
		asstToolLine("2026-07-04T10:00:05Z", "mcp__trixi__get_nug", ""),
		// opp 2: recall question → assistant greps first (MISS)
		userLine("2026-07-04T11:00:00Z", "I thought we shipped the judge already?"),
		asstToolLine("2026-07-04T11:00:05Z", "Grep", ""),
		// opp 3: recall question → no retrieval before next user turn (none)
		userLine("2026-07-04T12:00:00Z", "where do we stand on search?"),
		asstToolLine("2026-07-04T12:00:05Z", "Edit", ""), // not a retrieval
		userLine("2026-07-04T12:05:00Z", "ok thanks, add a flag"),
		// out-of-window opportunity: ignored
		userLine("2026-07-09T09:00:00Z", "do you remember the plan?"),
		asstToolLine("2026-07-09T09:00:05Z", "Grep", ""),
	}
	opps := scanLines(t, win, lines)
	if len(opps) != 3 {
		t.Fatalf("got %d opportunities, want 3: %+v", len(opps), opps)
	}
	if opps[0].Reach != ReachStore || !opps[0].Reached() {
		t.Errorf("opp0 reach = %q, want store", opps[0].Reach)
	}
	if opps[1].Reach != ReachGrep {
		t.Errorf("opp1 reach = %q, want grep", opps[1].Reach)
	}
	if opps[2].Reach != ReachNone {
		t.Errorf("opp2 reach = %q, want none", opps[2].Reach)
	}
}

// TestScanSource_CarrierDoesNotCloseArc verifies a tool_result carrier and an
// affirmation between the question and the reach do not close the opportunity.
func TestScanSource_CarrierDoesNotCloseArc(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC)}
	lines := []string{
		userLine("2026-07-04T10:00:00Z", "did we decide on the prefix?"),
		asstToolLine("2026-07-04T10:00:05Z", "Read", ""), // a first retrieval action
	}
	// Insert a tool_result carrier line (user role, no text) before the assistant.
	carrier := `{"type":"user","timestamp":"2026-07-04T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"[]"}]}}`
	lines = []string{lines[0], carrier, lines[1]}
	opps := scanLines(t, win, lines)
	if len(opps) != 1 || opps[0].Reach != ReachGrep {
		t.Fatalf("got %+v, want 1 opp with grep reach", opps)
	}
}

func TestRollup(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	opps := []Opportunity{
		{Project: "a", Session: "s1", Class: ClassRecall, Reach: ReachStore},
		{Project: "a", Session: "s1", Class: ClassRecall, Reach: ReachGrep},
		{Project: "b", Session: "s2", Class: ClassReorient, Reach: ReachBeads},
		{Project: "b", Session: "s2", Class: ClassRecall, Reach: ReachNone},
	}
	r := Rollup(opps, win, 2)
	if r.N != 4 || r.Reached != 1 || r.Beads != 1 || r.Grep != 1 || r.None != 1 {
		t.Errorf("channel counts wrong: %+v", r)
	}
	if r.Recall != 3 || r.Reorient != 1 {
		t.Errorf("class counts wrong: recall=%d reorient=%d", r.Recall, r.Reorient)
	}
	if r.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", r.Sessions)
	}
	if r.Failures != 3 {
		t.Errorf("failures = %d, want 3", r.Failures)
	}
	if got := r.ReachRate; got < 0.24 || got > 0.26 {
		t.Errorf("reachRate = %v, want ~0.25", got)
	}
	if got := r.KgRate; got < 0.49 || got > 0.51 {
		t.Errorf("kgRate = %v, want ~0.5 (store+beads)", got)
	}
}

// --- test helpers -----------------------------------------------------------

func newBlock(typ, name, input string) *transcript.Block {
	b := &transcript.Block{Type: typ, Name: name}
	if input != "" {
		b.Input = json.RawMessage(input)
	}
	return b
}

func sourceFor(path string) transcript.Source {
	return transcript.Source{Path: path, Project: "proj", Session: "sess"}
}

// TestScanSource_SidechainTurnsIgnored — an inlined subagent (isSidechain) turn
// is not dk: a recall-shaped sidechain prompt opens no opportunity and a
// sidechain retrieval resolves none (ferret-48l).
func TestScanSource_SidechainTurnsIgnored(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC)}
	sidechainRecall := `{"type":"user","isSidechain":true,"timestamp":"2026-07-04T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"did we decide on the prefix?"}]}}`
	lines := []string{
		sidechainRecall,
		asstToolLine("2026-07-04T10:00:05Z", "Read", ""),
	}
	if opps := scanLines(t, win, lines); len(opps) != 0 {
		t.Fatalf("sidechain recall must open no opportunity, got %+v", opps)
	}
}

// TestScanSource_WindowBoundsResolution — a recall question answered from context
// (no retrieval), then an affirmation, then an unrelated retrieval past the
// window: the late retrieval must NOT be attributed; the opportunity resolves
// ReachNone (ferret-aay: bound the once-unbounded resolution window).
func TestScanSource_WindowBoundsResolution(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC)}
	lines := []string{
		userLine("2026-07-04T10:00:00Z", "did we decide on the prefix?"),
		asstTextLine("2026-07-04T10:00:05Z", "Yes, we settled on cp earlier."), // turn 1, no retrieval
		userLine("2026-07-04T10:00:10Z", "go"),                                 // affirmation, arc stays open
		asstTextLine("2026-07-04T10:00:15Z", "On it, wiring the flag now."),    // turn 2, no retrieval → close ReachNone
		asstToolLine("2026-07-04T10:00:20Z", "Read", ""),                       // turn 3 retrieval, unrelated
	}
	opps := scanLines(t, win, lines)
	if len(opps) != 1 || opps[0].Reach != ReachNone {
		t.Fatalf("late retrieval must not attribute; got %+v, want 1 opp ReachNone", opps)
	}
}

// TestScanSource_RetrievalWithinWindowAttributes — a retrieval that arrives inside
// the window (turn 2 here, after a no-retrieval turn 1) still resolves the
// opportunity, so the bound doesn't drop prompt reaches.
func TestScanSource_RetrievalWithinWindowAttributes(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC)}
	lines := []string{
		userLine("2026-07-04T10:00:00Z", "did we decide on the prefix?"),
		asstTextLine("2026-07-04T10:00:05Z", "Let me check."), // turn 1, no retrieval
		asstToolLine("2026-07-04T10:00:10Z", "Read", ""),      // turn 2 retrieval, within window
	}
	opps := scanLines(t, win, lines)
	if len(opps) != 1 || opps[0].Reach != ReachGrep {
		t.Fatalf("within-window retrieval must attribute; got %+v, want 1 opp ReachGrep", opps)
	}
}

func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func userLine(ts, text string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"text","text":` + jsonStr(text) + `}]}}`
}

func asstToolLine(ts, name, input string) string {
	if input == "" {
		input = "{}"
	}
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}`
}

// asstToolIDLine is asstToolLine with an explicit tool_use id (so a following
// tool_result carrier can be matched to it in RU-watch tests).
func asstToolIDLine(ts, name, id string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":{}}]}}`
}

// asstTextLine is an assistant turn carrying prose (the RU "was it used?" surface).
func asstTextLine(ts, text string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":` + jsonStr(text) + `}]}}`
}

// resultLine is a user-role tool_result carrier bearing the retrieved content.
func resultLine(ts, toolUseID, content string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + toolUseID + `","content":` + content + `}]}}`
}

func scanLines(t *testing.T, win Window, lines []string) []Opportunity {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "proj", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opps, _, err := ScanSource(sourceFor(p), win)
	if err != nil {
		t.Fatal(err)
	}
	return opps
}
