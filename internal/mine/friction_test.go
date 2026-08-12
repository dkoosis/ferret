package mine

import (
	"testing"
)

// burnFixture builds a BurnResult with one row per (key, renderCost, calls),
// which is all MergeWaste reads from the burn table: the per-call price and
// the gross cost it carries onto each row.
func burnFixture(rows ...BurnRow) *BurnResult {
	res := &BurnResult{Events: 100, Sessions: 10}
	for _, r := range rows {
		if r.Calls > 0 {
			r.RenderPerCall = float64(r.RenderCost) / float64(r.Calls)
		}
		res.Rows = append(res.Rows, r)
	}
	return res
}

// motifFixture is a two-token vocabulary plus the id chain a Finding carries —
// the only part of Corpus MergeWaste touches (Tokens, to render the chain).
type motifFixture struct {
	corpus *Corpus
	ids    []uint32
}

func motifCorpus(t *testing.T) motifFixture {
	t.Helper()
	c := &Corpus{Vocab: []string{"Read", "Edit"}}
	return motifFixture{corpus: c, ids: []uint32{0, 1}}
}

func TestMergeWaste_ChargesRepeatsBeyondFirstPerSession_When_CommandIsPolled(t *testing.T) {
	burn := burnFixture(BurnRow{Key: "sh:git_status", RenderCost: 1000, Calls: 10})
	poll := PollingReport{Sessions: 4, Rows: []PollingRow{
		// 12 runs over 3 polling sessions → 9 runs bought nothing, at 100/call.
		{Command: "git status --short", Key: "sh:git_status", TotalRepeats: 12, Sessions: 3},
	}}

	rep := MergeWaste(burn, MisfireReport{}, poll, nil, nil)

	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %d, want 1; got %+v", len(rep.Rows), rep.Rows)
	}
	got := rep.Rows[0]
	if got.WastedBytes != 900 {
		t.Errorf("wastedBytes = %d, want 900 ((12-3) repeats × 100/call)", got.WastedBytes)
	}
	if got.Occurrences != 9 {
		t.Errorf("occurrences = %d, want 9 (repeats beyond the first per session)", got.Occurrences)
	}
	if got.Source != WasteRepeat {
		t.Errorf("source = %q, want %q", got.Source, WasteRepeat)
	}
	if got.RenderCost != 1000 {
		t.Errorf("renderCost = %d, want the key's gross 1000 carried as context", got.RenderCost)
	}
	if got.Detail != "git status --short" {
		t.Errorf("detail = %q, want the raw command text", got.Detail)
	}
}

func TestMergeWaste_ChargesEveryFailedCall_When_KeyMisfires(t *testing.T) {
	burn := burnFixture(BurnRow{Key: "Edit", RenderCost: 2000, Calls: 20})
	mis := MisfireReport{Rows: []MisfireRow{{Key: "Edit", Fails: 5, FailSess: 2, Calls: 20}}}

	rep := MergeWaste(burn, mis, PollingReport{}, nil, nil)

	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %d, want 1; got %+v", len(rep.Rows), rep.Rows)
	}
	if got := rep.Rows[0].WastedBytes; got != 500 {
		t.Errorf("wastedBytes = %d, want 500 (5 fails × 100/call)", got)
	}
	if got := rep.Rows[0].Source; got != WasteFail {
		t.Errorf("source = %q, want %q", got, WasteFail)
	}
}

// TestMergeWaste_JoinsEveryDetectorOnOneKeySpace guards the join hazard the
// bead named: burnKey, misfireKey and pollingKey are the same "sh:"-prefixing
// rule three times, and a row whose key fails to find its burn entry silently
// prices at zero. One shell key must resolve identically from all three.
func TestMergeWaste_JoinsEveryDetectorOnOneKeySpace_When_KeyIsShell(t *testing.T) {
	burn := burnFixture(BurnRow{Key: "sh:jq", RenderCost: 800, Calls: 8})
	mis := MisfireReport{Rows: []MisfireRow{{Key: "sh:jq", Fails: 2, FailSess: 2, Calls: 8}}}
	poll := PollingReport{Rows: []PollingRow{{Command: "jq '.[0]'", Key: "sh:jq", TotalRepeats: 5, Sessions: 1}}}

	rep := MergeWaste(burn, mis, poll, nil, nil)

	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per detector, same key); got %+v", len(rep.Rows), rep.Rows)
	}
	for _, r := range rep.Rows {
		if r.RenderPerCall != 100 {
			t.Errorf("%s row priced at %v/call, want 100 — the burn join missed", r.Source, r.RenderPerCall)
		}
	}
}

func TestMergeWaste_DropsRow_When_KeyHasNoBurnEntry(t *testing.T) {
	// An unpriceable row is dropped rather than emitted at zero: burn walks
	// every non-prompt event, so a missing key means the detectors disagree
	// about the corpus, and a zero-waste row in a waste ranking is noise.
	mis := MisfireReport{Rows: []MisfireRow{{Key: "sh:ghost", Fails: 3, FailSess: 1, Calls: 3}}}

	rep := MergeWaste(burnFixture(), mis, PollingReport{}, nil, nil)

	if len(rep.Rows) != 0 {
		t.Errorf("rows = %+v, want none (no burn entry to price against)", rep.Rows)
	}
	if rep.TotalWasted != 0 {
		t.Errorf("totalWasted = %d, want 0", rep.TotalWasted)
	}
}

func TestMergeWaste_RanksByWasteThenSpread_When_RowsTie(t *testing.T) {
	burn := burnFixture(
		BurnRow{Key: "sh:a", RenderCost: 1000, Calls: 10}, // 100/call
		BurnRow{Key: "sh:b", RenderCost: 1000, Calls: 10}, // 100/call
		BurnRow{Key: "sh:c", RenderCost: 5000, Calls: 10}, // 500/call
	)
	mis := MisfireReport{Rows: []MisfireRow{
		{Key: "sh:a", Fails: 2, FailSess: 1, Calls: 10}, // 200 waste, 1 session
		{Key: "sh:b", Fails: 2, FailSess: 5, Calls: 10}, // 200 waste, 5 sessions — spread wins the tie
		{Key: "sh:c", Fails: 2, FailSess: 1, Calls: 10}, // 1000 waste — top
	}}

	rep := MergeWaste(burn, mis, PollingReport{}, nil, nil)

	want := []string{"sh:c", "sh:b", "sh:a"}
	for i, key := range want {
		if rep.Rows[i].Key != key {
			t.Errorf("row %d = %q, want %q (waste desc, then session spread desc)", i, rep.Rows[i].Key, key)
		}
	}
	if rep.TotalWasted != 1400 {
		t.Errorf("totalWasted = %d, want 1400 (sum over all rows)", rep.TotalWasted)
	}
}

func TestMergeWaste_ReportsCorpusTotals_When_BurnTableIsPopulated(t *testing.T) {
	burn := burnFixture(
		BurnRow{Key: "sh:a", RenderCost: 1000, Calls: 10},
		BurnRow{Key: "sh:b", RenderCost: 2500, Calls: 10},
	)

	rep := MergeWaste(burn, MisfireReport{}, PollingReport{}, nil, nil)

	if rep.TotalRender != 3500 {
		t.Errorf("totalRender = %d, want 3500 (the corpus denominator)", rep.TotalRender)
	}
	if rep.Events != 100 || rep.Sessions != 10 {
		t.Errorf("events/sessions = %d/%d, want 100/10 carried from the burn result", rep.Events, rep.Sessions)
	}
}

func TestMergeWaste_ExcludesSwallowedRows_When_SwallowTableIsPopulated(t *testing.T) {
	// A swallow count is a FLOOR on hidden failures, not a count of them
	// (misfire.go) — pricing it would manufacture a number the corpus cannot
	// support, so the swallow table contributes nothing here.
	burn := burnFixture(BurnRow{Key: "sh:guess", RenderCost: 1000, Calls: 10})
	mis := MisfireReport{Swallowed: []SwallowRow{{Key: "sh:guess", Swallows: 9, SwallowSess: 3, Calls: 10}}}

	rep := MergeWaste(burn, mis, PollingReport{}, nil, nil)

	if len(rep.Rows) != 0 {
		t.Errorf("rows = %+v, want none — swallows are a floor, not a priceable count", rep.Rows)
	}
}

func TestMergeWaste_ChargesRedundantMotifOccurrences_When_MotifIsFriction(t *testing.T) {
	corpus := motifCorpus(t)
	// 10 occurrences over 4 sessions → 6 redundant; burn 400 tokens = 1600
	// bytes, of which 6/10 is charged.
	motifs := []*Finding{{
		IDs: corpus.ids, Kind: KindFriction, Count: 10, Sessions: 4, Burn: 400,
	}}

	rep := MergeWaste(burnFixture(), MisfireReport{}, PollingReport{}, corpus.corpus, motifs)

	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %d, want 1; got %+v", len(rep.Rows), rep.Rows)
	}
	got := rep.Rows[0]
	if got.WastedBytes != 960 {
		t.Errorf("wastedBytes = %d, want 960 (400 tok × 4 B/tok × 6/10 redundant)", got.WastedBytes)
	}
	if got.Source != WasteMotif {
		t.Errorf("source = %q, want %q", got.Source, WasteMotif)
	}
	if got.Key != "Read ⇝ Edit" {
		t.Errorf("key = %q, want the rendered motif chain %q", got.Key, "Read ⇝ Edit")
	}
}

func TestMergeWaste_SkipsRoutineAndNoiseMotifs_When_MotifRecursCheaply(t *testing.T) {
	corpus := motifCorpus(t)
	for _, kind := range []FindingKind{KindRoutine, KindNoise} {
		motifs := []*Finding{{IDs: corpus.ids, Kind: kind, Count: 10, Sessions: 2, Burn: 400}}
		rep := MergeWaste(burnFixture(), MisfireReport{}, PollingReport{}, corpus.corpus, motifs)
		if len(rep.Rows) != 0 {
			t.Errorf("kind %q produced %+v, want no rows — a recurring routine is the thing to script, not waste", kind, rep.Rows)
		}
	}
}
