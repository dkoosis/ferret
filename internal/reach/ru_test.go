package reach

import (
	"testing"
	"time"
)

func TestAdjudicateRU(t *testing.T) {
	tests := []struct {
		name      string
		result    string
		prose     string
		gotResult bool
		fellBack  bool
		want      RU
	}{
		{
			name:      "id-cited-is-used",
			result:    "nug 55596f70e4bb: agent_shell now does bg jobs, survives daemon restart",
			prose:     "Per the nug 55596f70e4bb, agent_shell already handles that — no new work.",
			gotResult: true,
			want:      RUUsed,
		},
		{
			name:      "bead-ref-cited-is-used",
			result:    "decided in ferret-p2a: reach mining is transcript-only, no telemetry",
			prose:     "Right, ferret-p2a settled this — transcript-only. Moving on.",
			gotResult: true,
			want:      RUUsed,
		},
		{
			name:      "term-overlap-above-threshold-is-used",
			result:    "loto lock format chosen: prefix bead-id colon intent, filesystem territory locks",
			prose:     "The loto lock format uses the bead-id prefix with a colon and the intent, over filesystem territory.",
			gotResult: true,
			want:      RUUsed,
		},
		{
			name:      "no-overlap-is-unused",
			result:    "landmark scorer lives in internal/score, Pereira Oren Meneguzzi citation",
			prose:     "Let me just add the flag and run the build; should compile fine.",
			gotResult: true,
			want:      RUUnused,
		},
		{
			name:      "fallback-with-low-overlap-is-unused",
			result:    "the prefix decision was cp for canapay per the beads rule",
			prose:     "Hmm, let me grep the repo to be sure.",
			gotResult: true,
			fellBack:  true,
			want:      RUUnused,
		},
		{
			name:      "empty-result-is-inconclusive",
			result:    "no results found",
			prose:     "Nothing in the store, let me check elsewhere.",
			gotResult: true,
			want:      RUInconclusive,
		},
		{
			name:      "no-result-captured-is-inconclusive",
			result:    "",
			prose:     "some prose here about many things entirely",
			gotResult: false,
			want:      RUInconclusive,
		},
		{
			name:      "result-but-no-prose-is-inconclusive",
			result:    "loto lock format details and territory rationale here",
			prose:     "   ",
			gotResult: true,
			want:      RUInconclusive,
		},
		{
			// A hyphenated dev-jargon term shared by result and prose must NOT
			// count as an id citation (regression: idRe once matched tool-use).
			name:      "shared-hyphenated-jargon-is-not-an-id-hit",
			result:    "the reach harness wraps each block in a tool-use envelope",
			prose:     "let me just wire the tool-use path and move on",
			gotResult: true,
			want:      RUUnused,
		},
		{
			// A real bead id with a no-digit suffix still reads as a decisive
			// USED citation (ferret-aay must survive the jargon filter).
			name:      "no-digit-bead-id-cited-is-used",
			result:    "filed as ferret-aay: the reach resolution-window bug",
			prose:     "yes, ferret-aay covers that one — nothing to add here",
			gotResult: true,
			want:      RUUsed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adjudicateRU(tt.result, tt.prose, tt.gotResult, tt.fellBack); got != tt.want {
				t.Errorf("adjudicateRU = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResultText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"json-string", `"the nug body"`, "the nug body"},
		{"content-block-array", `[{"type":"text","text":"first"},{"type":"text","text":"second"}]`, "first second"},
		{"empty", ``, ""},
		{"bare", `not-json`, "not-json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultText([]byte(tt.raw)); got != tt.want {
				t.Errorf("resultText = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestScanSource_RUWatch drives the full scanner watch phase: a store reach, its
// tool_result carrier, following prose, and a closing user boundary — asserting
// the store-reach opportunity carries the graded RU verdict.
func TestScanSource_RUWatch(t *testing.T) {
	win := Window{
		Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC),
	}
	tests := []struct {
		name  string
		prose string
		empty bool
		want  RU
	}{
		{"used", "The nug ferret-p2a confirms reach mining is transcript-only.", false, RUUsed},
		{"unused", "Let me just add the flag and rebuild, nothing else needed here.", false, RUUnused},
		{"inconclusive-empty", "checked, moving on to the next thing", true, RUInconclusive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := `[]`
			if !tt.empty {
				content = jsonStr("decided in ferret-p2a: reach mining is transcript-only, no telemetry join")
			}
			lines := []string{
				userLine("2026-07-04T10:00:00Z", "did we decide the reach mining approach?"),
				asstToolIDLine("2026-07-04T10:00:05Z", "mcp__trixi__get_nug", "tu_1"),
				resultLine("2026-07-04T10:00:06Z", "tu_1", content),
				asstTextLine("2026-07-04T10:00:10Z", tt.prose),
				userLine("2026-07-04T10:05:00Z", "ok, add the flag now"),
			}
			opps := scanLines(t, win, lines)
			if len(opps) != 1 {
				t.Fatalf("got %d opps, want 1: %+v", len(opps), opps)
			}
			if opps[0].Reach != ReachStore {
				t.Fatalf("reach = %q, want store", opps[0].Reach)
			}
			if opps[0].RU != tt.want {
				t.Errorf("RU = %q, want %q", opps[0].RU, tt.want)
			}
		})
	}
}

// TestScanSource_RUFallback verifies a store reach followed by a grep with no
// content overlap grades UNUSED (agent proceeded as if the store were empty).
func TestScanSource_RUFallback(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 23, 59, 59, 0, time.UTC)}
	lines := []string{
		userLine("2026-07-04T10:00:00Z", "do you remember the prefix we chose?"),
		asstToolIDLine("2026-07-04T10:00:05Z", "mcp__trixi__get_nug", "tu_1"),
		resultLine("2026-07-04T10:00:06Z", "tu_1", jsonStr("prefix policy: two or three letters")),
		asstToolLine("2026-07-04T10:00:10Z", "Grep", ""), // fallback to filesystem
		userLine("2026-07-04T10:05:00Z", "thanks"),
	}
	opps := scanLines(t, win, lines)
	if len(opps) != 1 || opps[0].RU != RUUnused {
		t.Fatalf("got %+v, want 1 store opp graded unused", opps)
	}
}

// TestRollup_RU asserts the RU tallies, denominator, and insufficient-data gate.
func TestRollup_RU(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	// 6 store reaches (≥ RUMinN): 3 used, 2 unused, 1 inconclusive.
	opps := []Opportunity{
		{Reach: ReachStore, RU: RUUsed}, {Reach: ReachStore, RU: RUUsed}, {Reach: ReachStore, RU: RUUsed},
		{Reach: ReachStore, RU: RUUnused}, {Reach: ReachStore, RU: RUUnused},
		{Reach: ReachStore, RU: RUInconclusive},
		{Reach: ReachGrep}, // non-store: no RU
	}
	r := Rollup(opps, win, 0)
	if r.Used != 3 || r.Unused != 2 || r.Inconclusive != 1 {
		t.Errorf("tally: used=%d unused=%d inconc=%d, want 3/2/1", r.Used, r.Unused, r.Inconclusive)
	}
	if r.RUDenom != 5 {
		t.Errorf("RUDenom = %d, want 5", r.RUDenom)
	}
	if r.RUInsufficient {
		t.Errorf("RUInsufficient = true, want false (6 store reaches ≥ %d)", RUMinN)
	}
	if got := r.RURate; got < 0.59 || got > 0.61 {
		t.Errorf("RURate = %v, want ~0.6 (3/5)", got)
	}
}

func TestRollup_RUInsufficient(t *testing.T) {
	win := Window{Since: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	opps := []Opportunity{{Reach: ReachStore, RU: RUUsed}} // n=1 store reach
	r := Rollup(opps, win, 0)
	if !r.RUInsufficient {
		t.Errorf("RUInsufficient = false, want true (1 < %d)", RUMinN)
	}
}
