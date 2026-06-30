package score

import (
	"testing"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/event"
)

// --- event builders (terse, mirror the table-driven style) ---

func getNug(seq int, query string, ids ...string) event.Event {
	hits := make([]event.NugHit, len(ids))
	for i, id := range ids {
		hits[i] = event.NugHit{ID: id}
	}
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindTool, Action: toolGetNug, Query: query, Results: hits}
}

func byID(seq int) event.Event {
	// a by-id get_nug carries no query → not an episode root.
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindTool, Action: toolGetNug}
}

func setNug(seq int) event.Event {
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindTool, Action: toolSetNug}
}

func tool(seq int, action, target string) event.Event {
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindTool, Action: action, Target: target}
}

func shell(seq int, detail string) event.Event {
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindShell, Action: "sh", Detail: detail}
}

func prompt(seq int, text string) event.Event {
	return event.Event{Seq: seq, Session: "s", Project: "p", Kind: event.KindPrompt, Prompt: text}
}

func TestBuildEpisodes(t *testing.T) {
	cases := []struct {
		name string
		evs  []event.Event
		want Episode // only the fields each case asserts; checked field by field
	}{
		{
			name: "strict consume by explicit id reference, accept close",
			evs: []event.Event{
				getNug(0, "how to lock files", "aaa111", "bbb222"),
				shell(1, "trixi get bbb222"), // references rank-2 id
				prompt(2, "yes, perfect"),
			},
			want: Episode{Queries: 1, Results: 2, ConsumedStrict: true, ConsumedLoose: true,
				ConsumedID: "bbb222", ConsumedRank: 2, ClosingMove: dialogue.MoveAccept,
				Outcome: dialogue.OutcomeSuccess, Answerable: true},
		},
		{
			name: "self-requery chain folds, firsttry fails",
			evs: []event.Event{
				getNug(0, "lock", "aaa111"),
				getNug(1, "file lock coordination", "bbb222"),
				shell(2, "trixi get bbb222"),
				prompt(3, "yes"),
			},
			want: Episode{Queries: 2, Results: 1, SelfRequery: true, ConsumedStrict: true, ConsumedLoose: true,
				ConsumedID: "bbb222", ConsumedRank: 1, ClosingMove: dialogue.MoveAccept, Answerable: true},
		},
		{
			name: "loose tell2: edit follows, no requery, no explicit ref",
			evs: []event.Event{
				getNug(0, "how", "aaa111"),
				tool(1, "Edit", "x.go"),
				prompt(2, "now do the next thing"),
			},
			want: Episode{Queries: 1, Results: 1, ConsumedStrict: false, ConsumedLoose: true,
				ClosingMove: dialogue.MoveNeutral, Answerable: true},
		},
		{
			name: "loose tell3: accept close, no edit, no ref",
			evs: []event.Event{
				getNug(0, "how", "aaa111"),
				prompt(1, "great"),
			},
			want: Episode{Queries: 1, Results: 1, ConsumedStrict: false, ConsumedLoose: true,
				ClosingMove: dialogue.MoveAccept, Outcome: dialogue.OutcomeSuccess, Answerable: true},
		},
		{
			name: "retrieved but ignored: results, no use, repair close",
			evs: []event.Event{
				getNug(0, "how", "aaa111", "bbb222"),
				prompt(1, "no, that's wrong"),
			},
			want: Episode{Queries: 1, Results: 2, ConsumedStrict: false, ConsumedLoose: false,
				ClosingMove: dialogue.MoveRepair, Outcome: dialogue.OutcomeAbandoned, Answerable: true},
		},
		{
			name: "coverage gap: empty search then set_nug",
			evs: []event.Event{
				getNug(0, "obscure thing"),
				setNug(1),
				prompt(2, "ok thanks"),
			},
			want: Episode{Queries: 1, Results: 0, EmptyResult: true, CoverageGap: true, Answerable: false},
		},
		{
			name: "good abandonment: empty, no retry, no repair",
			evs: []event.Event{
				getNug(0, "does X exist"),
				prompt(1, "ok good to know"),
			},
			want: Episode{Queries: 1, Results: 0, EmptyResult: true, GoodAbandon: true, Answerable: false},
		},
		{
			name: "oversized result set flagged",
			evs: []event.Event{
				getNugN(0, "broad", 30),
				prompt(1, "yes"),
			},
			want: Episode{Queries: 1, Results: 30, Oversized: true, ConsumedLoose: true, Answerable: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eps := BuildEpisodes(c.evs)
			if len(eps) != 1 {
				t.Fatalf("want 1 episode, got %d: %+v", len(eps), eps)
			}
			got := eps[0]
			assertEpisode(t, got, c.want)
		})
	}
}

// getNugN builds a query-mode get_nug returning n synthetic hits.
func getNugN(seq int, query string, n int) event.Event {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "id" + string(rune('a'+i%26))
	}
	return getNug(seq, query, ids...)
}

func assertEpisode(t *testing.T, got, want Episode) {
	t.Helper()
	if got.Queries != want.Queries {
		t.Errorf("Queries = %d, want %d", got.Queries, want.Queries)
	}
	if got.Results != want.Results {
		t.Errorf("Results = %d, want %d", got.Results, want.Results)
	}
	if got.SelfRequery != want.SelfRequery {
		t.Errorf("SelfRequery = %v, want %v", got.SelfRequery, want.SelfRequery)
	}
	if got.EmptyResult != want.EmptyResult {
		t.Errorf("EmptyResult = %v, want %v", got.EmptyResult, want.EmptyResult)
	}
	if got.Oversized != want.Oversized {
		t.Errorf("Oversized = %v, want %v", got.Oversized, want.Oversized)
	}
	if got.ConsumedStrict != want.ConsumedStrict {
		t.Errorf("ConsumedStrict = %v, want %v", got.ConsumedStrict, want.ConsumedStrict)
	}
	if got.ConsumedLoose != want.ConsumedLoose {
		t.Errorf("ConsumedLoose = %v, want %v", got.ConsumedLoose, want.ConsumedLoose)
	}
	if want.ConsumedID != "" && got.ConsumedID != want.ConsumedID {
		t.Errorf("ConsumedID = %q, want %q", got.ConsumedID, want.ConsumedID)
	}
	if want.ConsumedRank != 0 && got.ConsumedRank != want.ConsumedRank {
		t.Errorf("ConsumedRank = %d, want %d", got.ConsumedRank, want.ConsumedRank)
	}
	if want.ClosingMove != "" && got.ClosingMove != want.ClosingMove {
		t.Errorf("ClosingMove = %q, want %q", got.ClosingMove, want.ClosingMove)
	}
	if want.Outcome != "" && got.Outcome != want.Outcome {
		t.Errorf("Outcome = %q, want %q", got.Outcome, want.Outcome)
	}
	if got.CoverageGap != want.CoverageGap {
		t.Errorf("CoverageGap = %v, want %v", got.CoverageGap, want.CoverageGap)
	}
	if got.GoodAbandon != want.GoodAbandon {
		t.Errorf("GoodAbandon = %v, want %v", got.GoodAbandon, want.GoodAbandon)
	}
	if got.Answerable != want.Answerable {
		t.Errorf("Answerable = %v, want %v", got.Answerable, want.Answerable)
	}
}

// TestBuildEpisodesBoundaries covers multi-episode segmentation: a by-id fetch
// and pre-search calls are not episodes, and a new query after consumption opens
// a fresh episode rather than folding.
func TestBuildEpisodesBoundaries(t *testing.T) {
	evs := []event.Event{
		tool(0, "Read", "boot.md"), // pre-search call: not an episode
		byID(1),                    // by-id fetch: not an episode root
		getNug(2, "first intent", "aaa111"),
		shell(3, "trixi get aaa111"), // consumes → next query is a NEW episode
		getNug(4, "second intent", "bbb222"),
		prompt(5, "no"),
	}
	eps := BuildEpisodes(evs)
	if len(eps) != 2 {
		t.Fatalf("want 2 episodes, got %d: %+v", len(eps), eps)
	}
	if eps[0].Query != "first intent" || !eps[0].ConsumedStrict || eps[0].SelfRequery {
		t.Errorf("episode 0 = %+v, want first intent consumed, no self-requery", eps[0])
	}
	if eps[1].Query != "second intent" || eps[1].ConsumedStrict || eps[1].ClosingMove != dialogue.MoveRepair {
		t.Errorf("episode 1 = %+v, want second intent unconsumed, repair close", eps[1])
	}
}

// TestBuildEpisodesDeterministic is the acceptance gate: identical input yields
// identical episodes across repeated runs (no time / map-order / randomness).
func TestBuildEpisodesDeterministic(t *testing.T) {
	evs := []event.Event{
		getNug(0, "lock", "aaa111"),
		getNug(1, "file lock", "bbb222"),
		shell(2, "trixi get bbb222"),
		prompt(3, "yes"),
		getNug(4, "next", "ccc333"),
		setNug(5),
		prompt(6, "ok"),
	}
	first := BuildEpisodes(evs)
	for i := range 5 {
		got := BuildEpisodes(evs)
		if len(got) != len(first) {
			t.Fatalf("run %d: len %d != %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d episode %d not stable:\n got %+v\nwant %+v", i, j, got[j], first[j])
			}
		}
	}
}

func TestAggregate(t *testing.T) {
	// 4 episodes: 1 clean strict win, 1 self-requery (firsttry fails),
	// 1 coverage gap (excluded), 1 good abandon (excluded).
	eps := []Episode{
		{ConsumedStrict: true, ConsumedLoose: true, ConsumedRank: 1, Results: 3, Queries: 1,
			Outcome: dialogue.OutcomeSuccess, Answerable: true},
		{ConsumedStrict: true, ConsumedLoose: true, ConsumedRank: 2, Results: 2, Queries: 2,
			SelfRequery: true, Outcome: dialogue.OutcomeSuccess, Answerable: true},
		{EmptyResult: true, CoverageGap: true, Queries: 1, Answerable: false},
		{EmptyResult: true, GoodAbandon: true, Queries: 1, Answerable: false},
	}
	r := Aggregate(eps)
	if r.Episodes != 4 || r.Answerable != 2 {
		t.Fatalf("episodes/answerable = %d/%d, want 4/2", r.Episodes, r.Answerable)
	}
	// served_strict: only episode 0 (ep1 fails firsttry). RU = 1/2.
	if r.RUStrict != 0.5 {
		t.Errorf("RUStrict = %v, want 0.5", r.RUStrict)
	}
	if r.ConsumedRateStrict != 1.0 {
		t.Errorf("ConsumedRateStrict = %v, want 1.0", r.ConsumedRateStrict)
	}
	if r.FirstTryRate != 0.5 {
		t.Errorf("FirstTryRate = %v, want 0.5", r.FirstTryRate)
	}
	if r.NonAbandonRate != 1.0 {
		t.Errorf("NonAbandonRate = %v, want 1.0", r.NonAbandonRate)
	}
	// Q1 over all 4 episodes: 1 self-requery.
	if r.Q1SelfRequeryRate != 0.25 {
		t.Errorf("Q1SelfRequeryRate = %v, want 0.25", r.Q1SelfRequeryRate)
	}
	// MRR over ranked episodes: (1/1 + 1/2)/2 = 0.75.
	if r.R2MRR != 0.75 || r.R2Ranked != 2 {
		t.Errorf("R2MRR/ranked = %v/%d, want 0.75/2", r.R2MRR, r.R2Ranked)
	}
	// grounding: 2 retrieving episodes, both strict-consumed.
	if r.R7Retrieving != 2 || r.R7GroundingRate != 1.0 {
		t.Errorf("R7 retrieving/rate = %d/%v, want 2/1.0", r.R7Retrieving, r.R7GroundingRate)
	}
	if r.C1CoverageGap != 1 || r.C2GoodAbandon != 1 {
		t.Errorf("C1/C2 = %d/%d, want 1/1", r.C1CoverageGap, r.C2GoodAbandon)
	}
	if r.R3aEmpty != 2 {
		t.Errorf("R3aEmpty = %d, want 2", r.R3aEmpty)
	}
}

func TestAggregateEmpty(t *testing.T) {
	r := Aggregate(nil)
	if r.Episodes != 0 || r.RUStrict != 0 || r.R2MRR != 0 || r.Q2MeanDepth != 0 {
		t.Errorf("empty rollup not cleanly zeroed: %+v", r)
	}
}

// TestEpisode_FoldsRetryMotifIntoSelfRequery_When_FollowingActionRetries proves
// the event.Retry self-requery motif (a following bd_show!->bd_show /
// retry-after-failure, distinct from a get_nug reformulation chain) reaches the
// hop-attribution TurnContext. A repair after the agent silently retried its own
// action indicts the interpretation hop — the substrate bbp.4/bbp.5 read.
func TestEpisode_FoldsRetryMotifIntoSelfRequery_When_FollowingActionRetries(t *testing.T) {
	retry := tool(2, "Bash", "bd show x")
	retry.Retry = true // event builder flagged a retry-after-failure
	evs := []event.Event{
		getNug(0, "where is x", "aaa111"), // single query, NOT reformulated
		tool(1, "Bash", "bd show x"),      // first attempt
		retry,                             // self-retry motif
		prompt(3, "no, that's the wrong x"),
	}
	eps := BuildEpisodes(evs)
	if len(eps) != 1 {
		t.Fatalf("want 1 episode, got %d", len(eps))
	}
	ep := eps[0]
	// The get_nug was not reformulated, so the chain-based SelfRequery stays
	// false — RU's FirstTry leg must not be polluted by an action-level retry.
	if ep.SelfRequery {
		t.Error("get_nug chain SelfRequery should stay false: the query was not reformulated")
	}
	// But the retry motif must surface in the episode and in the attribution
	// TurnContext...
	if !ep.RetryMotif {
		t.Error("RetryMotif should be true: a following action carried event.Retry")
	}
	if tc := ep.TurnContext(); !tc.SelfRequery {
		t.Errorf("TurnContext.SelfRequery should be true via the event.Retry motif; got %+v", tc)
	}
	// ...and place the repair on the interpretation hop.
	if got := ep.Hop(); got != dialogue.HopInterp {
		t.Errorf("Hop() = %q, want %q (a self-retry before a repair indicts interp)", got, dialogue.HopInterp)
	}
}

// TestEpisodeHopWiring proves the attribute.TurnContext stub is now filled: the
// episode's signals flow into AttributeHop and place a repair on the right hop.
func TestEpisodeHopWiring(t *testing.T) {
	cases := []struct {
		name string
		ep   Episode
		want dialogue.Hop
	}{
		{"self-requery repair → interp", Episode{ClosingMove: dialogue.MoveRepair, SelfRequery: true}, dialogue.HopInterp},
		{"empty-result repair → retrieval", Episode{ClosingMove: dialogue.MoveRepair, EmptyResult: true}, dialogue.HopRetrieval},
		{"oversized repair → retrieval", Episode{ClosingMove: dialogue.MoveRepair, Oversized: true}, dialogue.HopRetrieval},
		{"accept → none", Episode{ClosingMove: dialogue.MoveAccept, SelfRequery: true}, dialogue.HopNone},
		{"repair, no signal → none", Episode{ClosingMove: dialogue.MoveRepair}, dialogue.HopNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ep.Hop(); got != c.want {
				t.Errorf("Hop() = %q, want %q", got, c.want)
			}
		})
	}
}
