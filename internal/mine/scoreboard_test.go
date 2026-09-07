package mine

import (
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
)

// testCorpus builds a minimal Corpus whose Vocab is just the token strings —
// enough for Tokens() to resolve IDs, without a real events.jsonl.
func testCorpus(vocab ...string) *Corpus {
	return &Corpus{Vocab: vocab}
}

func TestBuildScoreboard_CapsRoutinesAndCountsBelowCut_When_MoreFindingsThanCap(t *testing.T) {
	corpus := testCorpus("Read", "Edit", "Write")
	findings := []*Finding{
		{IDs: []uint32{0, 1}, Burn: 300, Count: 5, Sessions: 3},
		{IDs: []uint32{1, 2}, Burn: 200, Count: 4, Sessions: 2},
		{IDs: []uint32{0, 2}, Burn: 100, Count: 3, Sessions: 1},
		{IDs: []uint32{2, 0}, Burn: 50, Count: 2, Sessions: 1},
	}
	sb := BuildScoreboard(corpus, findings, WasteReport{}, nil, 3)
	if len(sb.Routines) != 3 {
		t.Fatalf("routines = %d, want 3 (capped)", len(sb.Routines))
	}
	if sb.Routines[0].Key != "Read ⇝ Edit" || sb.Routines[0].Burn != 300 {
		t.Errorf("top routine = %+v, want Read ⇝ Edit burn=300", sb.Routines[0])
	}
	if sb.BelowCut != 1 {
		t.Errorf("belowCut = %d, want 1 (the 4th finding)", sb.BelowCut)
	}
}

func TestBuildScoreboard_RemovesLedgerCoveredMotifFromRoutines_When_AnyDispositionRecorded(t *testing.T) {
	corpus := testCorpus("Read", "Edit")
	findings := []*Finding{
		{IDs: []uint32{0, 1}, Burn: 300, Count: 5, Sessions: 3}, // "Read ⇝ Edit"
	}
	for _, disp := range []string{fixes.DispositionFix, fixes.DispositionWatch, fixes.DispositionWontfix} {
		ledger := []fixes.Entry{{Motif: fixes.MotifKey([]string{"Read", "Edit"}), Fix: "x", Disposition: disp, BaselineBurn: 300}}
		sb := BuildScoreboard(corpus, findings, WasteReport{}, ledger, 3)
		if len(sb.Routines) != 0 {
			t.Errorf("disposition=%s: routines = %+v, want empty (ledger-covered motif must leave the ranking)", disp, sb.Routines)
		}
	}
}

func TestBuildScoreboard_CapsWasteAndFiltersByLedgerKey_When_SingleKeyIsFixed(t *testing.T) {
	corpus := testCorpus()
	waste := WasteReport{Rows: []WasteRow{
		{Key: "SendMessage", Source: WasteFail, WastedBytes: 900},
		{Key: "sh:git_status", Source: WasteRepeat, WastedBytes: 800},
		{Key: "Read", Source: WasteFail, WastedBytes: 700},
		{Key: "Edit", Source: WasteFail, WastedBytes: 600},
		{Key: "Write", Source: WasteFail, WastedBytes: 500},
	}}
	ledger := []fixes.Entry{{Motif: fixes.MotifKey([]string{"sh:git_status"}), Fix: "aliased", Disposition: fixes.DispositionWontfix}}
	sb := BuildScoreboard(corpus, nil, waste, ledger, 3)
	if len(sb.Waste) != 3 {
		t.Fatalf("waste rows = %d, want 3 (capped, sh:git_status excluded)", len(sb.Waste))
	}
	for _, r := range sb.Waste {
		if r.Key == "sh:git_status" {
			t.Errorf("sh:git_status is ledger-covered (wontfix) and must not appear: %+v", sb.Waste)
		}
	}
	if sb.Waste[0].Key != "SendMessage" {
		t.Errorf("waste order not preserved from the merge: got %+v", sb.Waste)
	}
	if sb.BelowCut != 1 {
		t.Errorf("belowCut = %d, want 1 (5 waste rows, minus 1 ledger-covered, minus cap 3)", sb.BelowCut)
	}
}

func TestBuildScoreboard_EmitsDeltaOnlyForFixDisposition_When_LedgerMixesVerdicts(t *testing.T) {
	corpus := testCorpus("Read", "Edit")
	findings := []*Finding{
		{IDs: []uint32{0, 1}, Burn: 100, Count: 5, Sessions: 3}, // "Read ⇝ Edit", now 100 tok
	}
	ledger := []fixes.Entry{
		{Motif: fixes.MotifKey([]string{"Read", "Edit"}), Fix: "hookified", Disposition: fixes.DispositionFix,
			BaselineBurn: 300, AddedAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		{Motif: fixes.MotifKey([]string{"Edit"}), Fix: "not fixable", Disposition: fixes.DispositionWontfix, BaselineBurn: 0},
	}
	sb := BuildScoreboard(corpus, findings, WasteReport{}, ledger, 3)
	if len(sb.Delta) != 1 {
		t.Fatalf("delta rows = %d, want 1 (only the fix entry deltas)", len(sb.Delta))
	}
	d := sb.Delta[0]
	if d.Key != "Read ⇝ Edit" || d.Fix != "hookified" || d.FixedAt != "2026-08-12" {
		t.Errorf("delta row = %+v", d)
	}
	if d.BeforeBytes != 300*bytesPerToken || d.AfterBytes != 100*bytesPerToken {
		t.Errorf("delta bytes = before=%d after=%d, want before=%d after=%d",
			d.BeforeBytes, d.AfterBytes, 300*bytesPerToken, 100*bytesPerToken)
	}
}

func TestBuildScoreboard_DeltaAfterIsZero_When_MotifNoLongerRecurs(t *testing.T) {
	corpus := testCorpus("Read", "Edit")
	ledger := []fixes.Entry{
		{Motif: fixes.MotifKey([]string{"Read", "Edit"}), Fix: "hookified", Disposition: fixes.DispositionFix,
			BaselineBurn: 300, AddedAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
	}
	// No findings at all — the fix eliminated the motif entirely.
	sb := BuildScoreboard(corpus, nil, WasteReport{}, ledger, 3)
	if len(sb.Delta) != 1 || sb.Delta[0].AfterBytes != 0 {
		t.Fatalf("delta = %+v, want one row with afterBytes=0", sb.Delta)
	}
	if sb.Delta[0].Key != fixes.MotifKey([]string{"Read", "Edit"}) {
		t.Errorf("delta key = %q, want the raw ledger key as a fallback display (no matching finding to resolve tokens from)", sb.Delta[0].Key)
	}
}

func TestBuildScoreboard_DeltaRowsAreByteStable_When_MultipleFixesRecorded(t *testing.T) {
	corpus := testCorpus("A", "B", "C")
	ledger := []fixes.Entry{
		{Motif: fixes.MotifKey([]string{"C"}), Fix: "z", Disposition: fixes.DispositionFix, BaselineBurn: 10},
		{Motif: fixes.MotifKey([]string{"A"}), Fix: "x", Disposition: fixes.DispositionFix, BaselineBurn: 10},
		{Motif: fixes.MotifKey([]string{"B"}), Fix: "y", Disposition: fixes.DispositionFix, BaselineBurn: 10},
	}
	sb := BuildScoreboard(corpus, nil, WasteReport{}, ledger, 3)
	if len(sb.Delta) != 3 {
		t.Fatalf("delta rows = %d, want 3", len(sb.Delta))
	}
	want := []string{fixes.MotifKey([]string{"A"}), fixes.MotifKey([]string{"B"}), fixes.MotifKey([]string{"C"})}
	for i, w := range want {
		if sb.Delta[i].Key != w {
			t.Errorf("delta[%d].Key = %q, want %q (sorted key order)", i, sb.Delta[i].Key, w)
		}
	}
}

func TestBuildScoreboard_JSONShape_When_EmptyLedger(t *testing.T) {
	corpus := testCorpus("Read")
	findings := []*Finding{{IDs: []uint32{0}, Burn: 50, Count: 1, Sessions: 1}}
	waste := WasteReport{Rows: []WasteRow{{Key: "Read", Source: WasteFail, WastedBytes: 10}}}
	sb := BuildScoreboard(corpus, findings, waste, nil, 3)
	if len(sb.Routines) != 1 || len(sb.Waste) != 1 || len(sb.Delta) != 0 || sb.BelowCut != 0 {
		t.Errorf("scoreboard with an empty ledger = %+v, want everything through uncapped/unfiltered", sb)
	}
}
