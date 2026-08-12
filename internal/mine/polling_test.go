package mine

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// shellEv is polling's fixture helper: a shell event carrying raw command text
// (the only kind MinePolling folds). Kept separate from misfire_test.go's ev so
// each test file's fixtures stay readable on their own.
func shellEv(session, command string) event.Event {
	return event.Event{
		Session: session, Kind: event.KindShell,
		Action: "cmd", Status: event.StatusOK, Detail: command,
	}
}

func TestMinePolling_CountsRepeatsPerSession_When_SameCommandRunsRepeatedly(t *testing.T) {
	events := []event.Event{
		shellEv("s1", "loto inbox"),
		shellEv("s1", "loto inbox"),
		shellEv("s1", "loto inbox"),
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one polled command", rep.Rows)
	}
	row := rep.Rows[0]
	if row.Command != "loto inbox" {
		t.Errorf("command = %q, want %q", row.Command, "loto inbox")
	}
	if row.TotalRepeats != 3 || row.Sessions != 1 || row.MaxPerSession != 3 {
		t.Errorf("row = %+v, want repeats=3 sessions=1 max=3", row)
	}
	if row.Score != 3 {
		t.Errorf("score = %v, want 3 (repeats × sessions)", row.Score)
	}
}

// TestMinePolling_AppliesThreshold_When_CommandRunsOnceInASession pins the
// ≥2 floor: a command run once per session is normal use, not polling, and
// must contribute nothing — neither a row nor a session count.
func TestMinePolling_AppliesThreshold_When_CommandRunsOnceInASession(t *testing.T) {
	tests := []struct {
		name     string
		events   []event.Event
		wantRows int
	}{
		{
			name:     "single run in one session",
			events:   []event.Event{shellEv("s1", "make check")},
			wantRows: 0,
		},
		{
			name: "once each across many sessions",
			events: []event.Event{
				shellEv("s1", "make check"),
				shellEv("s2", "make check"),
				shellEv("s3", "make check"),
			},
			wantRows: 0,
		},
		{
			name: "twice in one session crosses the floor",
			events: []event.Event{
				shellEv("s1", "make check"),
				shellEv("s1", "make check"),
			},
			wantRows: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := MinePolling(tt.events)
			if len(rep.Rows) != tt.wantRows {
				t.Errorf("rows = %+v, want %d row(s)", rep.Rows, tt.wantRows)
			}
		})
	}
}

// TestMinePolling_AggregatesAcrossSessions_When_CommandPollsInSeveral checks
// the corpus rollup: only polling sessions count toward Sessions/TotalRepeats,
// and MaxPerSession reports the worst single session.
func TestMinePolling_AggregatesAcrossSessions_When_CommandPollsInSeveral(t *testing.T) {
	events := []event.Event{
		// s1 polls 4×, s2 polls 2×, s3 runs it once (below the floor — excluded).
		shellEv("s1", "git status --short"), shellEv("s1", "git status --short"),
		shellEv("s1", "git status --short"), shellEv("s1", "git status --short"),
		shellEv("s2", "git status --short"), shellEv("s2", "git status --short"),
		shellEv("s3", "git status --short"),
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v, want one aggregated row", rep.Rows)
	}
	row := rep.Rows[0]
	if row.TotalRepeats != 6 {
		t.Errorf("totalRepeats = %d, want 6 (4+2; the single run in s3 does not count)", row.TotalRepeats)
	}
	if row.Sessions != 2 {
		t.Errorf("sessions = %d, want 2 (only sessions that polled)", row.Sessions)
	}
	if row.MaxPerSession != 4 {
		t.Errorf("maxPerSession = %d, want 4", row.MaxPerSession)
	}
	if rep.Sessions != 3 {
		t.Errorf("report sessions = %d, want 3 (corpus denominator counts every shell session)", rep.Sessions)
	}
}

// TestMinePolling_RanksBySpreadNotVolume_When_ScoresCompete guards the ranking
// choice: repeats × sessions, so a habit polled in many sessions outranks one
// runaway session with a slightly higher raw count.
func TestMinePolling_RanksBySpreadNotVolume_When_ScoresCompete(t *testing.T) {
	events := make([]event.Event, 0, 16)
	// "loto inbox": 3 sessions × 4 runs = 12 repeats, score 36.
	for _, s := range []string{"s1", "s2", "s3"} {
		for range 4 {
			events = append(events, shellEv(s, "loto inbox"))
		}
	}
	// "tail -f log": one session, 15 runs — more volume, score 15.
	for range 15 {
		events = append(events, shellEv("s4", "tail -f log"))
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rep.Rows)
	}
	if rep.Rows[0].Command != "loto inbox" || rep.Rows[0].Score != 36 {
		t.Errorf("top row = %+v, want loto inbox with score 36", rep.Rows[0])
	}
	if rep.Rows[1].Command != "tail -f log" || rep.Rows[1].Score != 15 {
		t.Errorf("second row = %+v, want tail -f log with score 15", rep.Rows[1])
	}
}

// TestMinePolling_SortsDeterministically_When_ScoresTie pins the tie-break
// chain so repeated runs over the same corpus are byte-stable despite the
// map-ordered aggregation.
func TestMinePolling_SortsDeterministically_When_ScoresTie(t *testing.T) {
	events := []event.Event{
		shellEv("s1", "zzz cmd"), shellEv("s1", "zzz cmd"),
		shellEv("s1", "aaa cmd"), shellEv("s1", "aaa cmd"),
	}
	for range 5 { // same input, five independent runs — order must not drift
		rep := MinePolling(events)
		if len(rep.Rows) != 2 {
			t.Fatalf("rows = %+v, want 2", rep.Rows)
		}
		if rep.Rows[0].Command != "aaa cmd" || rep.Rows[1].Command != "zzz cmd" {
			t.Fatalf("rows = %+v, want command-ascending tie-break", rep.Rows)
		}
	}
}

// TestMinePolling_CarriesNormalizedKey_When_RowIsRanked checks the join column:
// each row carries burn.go's "sh:"-prefixed shellnorm key, so the polling table
// can be joined back to the burn ranking.
func TestMinePolling_CarriesNormalizedKey_When_RowIsRanked(t *testing.T) {
	events := []event.Event{
		{Session: "s1", Kind: event.KindShell, Action: "git_status", Detail: "git status --short"},
		{Session: "s1", Kind: event.KindShell, Action: "git_status", Detail: "git status --short"},
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rep.Rows)
	}
	if rep.Rows[0].Key != "sh:git_status" {
		t.Errorf("key = %q, want sh:git_status (burnKey's prefix convention)", rep.Rows[0].Key)
	}
}

// TestMinePolling_MergesTruncationCollisions_When_DetailsSharePrefix documents
// the accepted imprecision: Detail is truncated raw text, so two distinct long
// commands that truncate to the same string count as one polled command. The
// output is an advisory ranking, so a prefix-sharing merge is acceptable.
func TestMinePolling_MergesTruncationCollisions_When_DetailsSharePrefix(t *testing.T) {
	// Both events reach the miner with identical (truncated) Detail even though
	// the untruncated commands differed past the cut.
	truncated := "rg --json --context 3 --glob '!vendor' --glob '!testdata' 'func Mine"
	events := []event.Event{shellEv("s1", truncated), shellEv("s1", truncated)}

	rep := MinePolling(events)
	if len(rep.Rows) != 1 || rep.Rows[0].TotalRepeats != 2 {
		t.Errorf("rows = %+v, want one merged row with 2 repeats (truncation collision is accepted)", rep.Rows)
	}
}

// TestMinePolling_IgnoresNonShellAndTextlessEvents_When_CorpusIsMixed keeps
// tool and prompt events out: they carry no raw command text, so folding them
// would pile every tool call into one empty-Detail bucket.
func TestMinePolling_IgnoresNonShellAndTextlessEvents_When_CorpusIsMixed(t *testing.T) {
	events := []event.Event{
		{Session: "s1", Kind: event.KindTool, Action: "Read", Target: "a.go"},
		{Session: "s1", Kind: event.KindTool, Action: "Read", Target: "b.go"},
		{Session: "s1", Kind: event.KindPrompt, Prompt: "do the thing"},
		{Session: "s1", Kind: event.KindPrompt, Prompt: "do the thing"},
		{Session: "s1", Kind: event.KindShell, Action: "cmd", Detail: ""},
		{Session: "s1", Kind: event.KindShell, Action: "cmd", Detail: ""},
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 0 {
		t.Errorf("rows = %+v, want none — no shell event carried raw command text", rep.Rows)
	}
	if rep.Sessions != 0 {
		t.Errorf("report sessions = %d, want 0 — the denominator counts shell events with text", rep.Sessions)
	}
}

// TestMinePolling_KeepsSessionsSeparate_When_SameCommandRunsOncePerSession is
// the cross-session negative twin of the aggregation test: per-session counting
// must not pool runs from different sessions into one qualifying count.
func TestMinePolling_KeepsSessionsSeparate_When_SameCommandRunsOncePerSession(t *testing.T) {
	events := []event.Event{
		shellEv("s1", "bd ready"),
		shellEv("s2", "bd ready"),
		shellEv("s3", "bd ready"),
		shellEv("s4", "bd ready"), shellEv("s4", "bd ready"),
	}
	rep := MinePolling(events)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rep.Rows)
	}
	row := rep.Rows[0]
	if row.TotalRepeats != 2 || row.Sessions != 1 {
		t.Errorf("row = %+v, want repeats=2 sessions=1 (only s4 polled)", row)
	}
}

// TestMinePolling_HandlesEmptyCorpus_When_NoEvents guards the zero case — an
// empty corpus yields an empty (non-nil-shaped) report, not a panic.
func TestMinePolling_HandlesEmptyCorpus_When_NoEvents(t *testing.T) {
	rep := MinePolling(nil)
	if len(rep.Rows) != 0 || rep.Sessions != 0 {
		t.Errorf("report = %+v, want empty", rep)
	}
}
