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
			// "no, that's wrong" is a wrongness claim → v2 MoveReject (the
			// Disagree-Correct split of repair). Outcome + the ConsumedLoose/Answerable
			// tells are unchanged: reject counts as a repair via IsRepairMove.
			name: "retrieved but ignored: results, no use, reject close",
			evs: []event.Event{
				getNug(0, "how", "aaa111", "bbb222"),
				prompt(1, "no, that's wrong"),
			},
			want: Episode{Queries: 1, Results: 2, ConsumedStrict: false, ConsumedLoose: false,
				ClosingMove: dialogue.MoveReject, Outcome: dialogue.OutcomeAbandoned, Answerable: true},
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

// TestBuildEpisodesRejectRepairParity is the retrieval-side behavior-preserving
// guard for the v2 repair→reject split (bbp.7): the two finalize() booleans that
// key on the closing move — tell3 (→ConsumedLoose) and goodAbandon — read the move
// through dialogue.IsRepairMove, so a MoveReject close must yield the SAME Episode
// as the identical MoveRepair close. "that's wrong" (reject) and "no, redo it"
// (repair) are contrasted against the same retrieval prefix.
func TestBuildEpisodesRejectRepairParity(t *testing.T) {
	// tell3 → ConsumedLoose: non-empty result, no explicit consume, a pushback close.
	// Both moves are repair-class, so tell3 is false for both (a pushback is not use).
	nonEmpty := func(closing string) Episode {
		return one(t, []event.Event{getNug(0, "how", "aaa111", "bbb222"), prompt(1, closing)})
	}
	rj, rp := nonEmpty("that's wrong"), nonEmpty("no, redo it differently")
	if rj.ClosingMove != dialogue.MoveReject || rp.ClosingMove != dialogue.MoveRepair {
		t.Fatalf("setup: got moves reject=%q repair=%q", rj.ClosingMove, rp.ClosingMove)
	}
	if rj.ConsumedLoose != rp.ConsumedLoose || rj.ConsumedLoose {
		t.Errorf("ConsumedLoose parity broke: reject=%v repair=%v (both want false)", rj.ConsumedLoose, rp.ConsumedLoose)
	}
	if rj.Outcome != rp.Outcome || rj.Outcome != dialogue.OutcomeAbandoned {
		t.Errorf("Outcome parity broke: reject=%q repair=%q (both want abandoned)", rj.Outcome, rp.Outcome)
	}

	// goodAbandon: empty result, no set_nug, no self-requery, a pushback close → NOT
	// a good abandonment for either move (the human pushed back, didn't accept absence).
	empty := func(closing string) Episode {
		return one(t, []event.Event{getNug(0, "obscure"), prompt(1, closing)})
	}
	erj, erp := empty("that's wrong"), empty("no, redo it differently")
	if erj.GoodAbandon != erp.GoodAbandon || erj.GoodAbandon {
		t.Errorf("GoodAbandon parity broke: reject=%v repair=%v (both want false)", erj.GoodAbandon, erp.GoodAbandon)
	}
}

// one builds exactly one episode from evs, failing if the count isn't 1.
func one(t *testing.T, evs []event.Event) Episode {
	t.Helper()
	eps := BuildEpisodes(evs)
	if len(eps) != 1 {
		t.Fatalf("want 1 episode, got %d: %+v", len(eps), eps)
	}
	return eps[0]
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
		// v2 reject-close parity (bbp.7): a MoveReject close indicts the SAME hop a
		// MoveRepair close did — the split must not drop hop attribution.
		{"self-requery reject → interp", Episode{ClosingMove: dialogue.MoveReject, SelfRequery: true}, dialogue.HopInterp},
		{"empty-result reject → retrieval", Episode{ClosingMove: dialogue.MoveReject, EmptyResult: true}, dialogue.HopRetrieval},
		{"reject, no signal → none", Episode{ClosingMove: dialogue.MoveReject}, dialogue.HopNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ep.Hop(); got != c.want {
				t.Errorf("Hop() = %q, want %q", got, c.want)
			}
		})
	}
}

// --- ferret-bbp.4: deterministic Hop2 (query→result) QPP retrieval-quality grade ---

// TestEpisodeQPP_GradesResultSetQuality walks each rubric branch of the
// reference-free QPP grade: empty→low, oversized→low-clarity (mid, charged to
// retrieval), result-used→high, results-but-no-use→mid. The grade is a pure
// function of the served result-set signals — scored even when nothing broke.
func TestEpisodeQPP_GradesResultSetQuality(t *testing.T) {
	cases := []struct {
		name      string
		ep        Episode
		wantGrade QPPGrade
		wantHop   dialogue.Hop
	}{
		{
			name:      "empty/error result → low, charged to retrieval",
			ep:        Episode{EmptyResult: true},
			wantGrade: QPPLow, wantHop: dialogue.HopRetrieval,
		},
		{
			name:      "oversized set → low-clarity (mid), charged to retrieval",
			ep:        Episode{Results: 30, Oversized: true},
			wantGrade: QPPMid, wantHop: dialogue.HopRetrieval,
		},
		{
			name:      "oversized beats a downstream accept: clarity defect is not masked",
			ep:        Episode{Results: 30, Oversized: true, ConsumedLoose: true},
			wantGrade: QPPMid, wantHop: dialogue.HopRetrieval,
		},
		{
			name:      "result used → high",
			ep:        Episode{Results: 3, ConsumedStrict: true, ConsumedLoose: true},
			wantGrade: QPPHigh, wantHop: dialogue.HopNone,
		},
		{
			name:      "results returned, no use signal → mid",
			ep:        Episode{Results: 3},
			wantGrade: QPPMid, wantHop: dialogue.HopNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.ep.QPP()
			if got.Grade != c.wantGrade {
				t.Errorf("Grade = %q, want %q", got.Grade, c.wantGrade)
			}
			if got.Hop != c.wantHop {
				t.Errorf("Hop = %q, want %q", got.Hop, c.wantHop)
			}
		})
	}
}

// TestEpisodeQPP_DataDiagnosisChargesRetrievalNotQuery is the critical bbp.4
// nuance: a human DATA-diagnosis reaction ("it moved the file") after an empty
// search is a Hop2 (retrieval) FAIL — the data/index was stale, the query was
// faithful. The QPP grade is LOW and the failure is charged to the RETRIEVAL hop,
// NEVER the query/interp hop. A data diagnosis is not lexically a repair, so the
// dialogue tagger reads it Neutral; the grade keys off the empty result-set, not
// the move, so the query is never blamed for a data failure.
func TestEpisodeQPP_DataDiagnosisChargesRetrievalNotQuery(t *testing.T) {
	evs := []event.Event{
		getNug(0, "where is landmark.go"),                             // empty: the file moved, index stale
		prompt(1, "it moved the file, it's under internal/score now"), // data diagnosis, NOT a query correction
	}
	eps := BuildEpisodes(evs)
	if len(eps) != 1 {
		t.Fatalf("want 1 episode, got %d", len(eps))
	}
	ep := eps[0]
	// A data diagnosis is Neutral, not a repair cue — the human's words never
	// indict the query.
	if ep.ClosingMove != dialogue.MoveNeutral {
		t.Fatalf("ClosingMove = %q, want neutral (a data diagnosis is not a repair cue)", ep.ClosingMove)
	}
	q := ep.QPP()
	if q.Grade != QPPLow {
		t.Errorf("Grade = %q, want low (an empty served set is a Hop2 fail)", q.Grade)
	}
	if q.Hop != dialogue.HopRetrieval {
		t.Errorf("Hop = %q, want retrieval (the data/index failed, not the query)", q.Hop)
	}
	if q.Hop == dialogue.HopInterp {
		t.Error("a Hop2 data-diagnosis fail must NOT be charged to the query/interp hop")
	}
}

// TestEpisodeQPP_SelfRequeryNotChargedToRetrieval is the inverse guard: a
// self-requery that ends in a used, clean result is QUERY (Hop1/interp) churn,
// not a retrieval defect. The retrieval delivered, so the grade is HIGH and no
// retrieval hop is charged — a query failure is never charged to Hop2.
func TestEpisodeQPP_SelfRequeryNotChargedToRetrieval(t *testing.T) {
	evs := []event.Event{
		getNug(0, "lock", "aaa111"),
		getNug(1, "file lock coordination", "bbb222"), // reformulation: self-requery (Hop1 churn)
		shell(2, "trixi get bbb222"),                  // the returned nug was used
		prompt(3, "yes"),
	}
	ep := BuildEpisodes(evs)[0]
	if !ep.SelfRequery {
		t.Fatal("setup: expected a self-requery chain")
	}
	q := ep.QPP()
	if q.Grade != QPPHigh {
		t.Errorf("Grade = %q, want high (the result was used; the requery is Hop1 churn)", q.Grade)
	}
	if q.Hop != dialogue.HopNone {
		t.Errorf("Hop = %q, want none (no retrieval-hop defect)", q.Hop)
	}
}

// TestEpisodeQPP_Deterministic is the byte-stability gate (doc.go contract):
// identical episodes grade identically across repeated calls.
func TestEpisodeQPP_Deterministic(t *testing.T) {
	eps := []Episode{
		{EmptyResult: true},
		{Results: 30, Oversized: true},
		{Results: 3, ConsumedLoose: true},
		{Results: 3},
	}
	for i := range eps {
		first := eps[i].QPP()
		for run := range 5 {
			if got := eps[i].QPP(); got != first {
				t.Fatalf("episode %d run %d not stable: got %+v, want %+v", i, run, got, first)
			}
		}
	}
}

// TestBuildEpisodesCapturesRootPrompt pins Episode.Prompt to the human turn that
// PRECEDED the episode's root query (the Hop1 judge's input), and to "" when no
// prompt preceded it — mirroring ClosingMove, which reads the FOLLOWING turn.
func TestBuildEpisodesCapturesRootPrompt(t *testing.T) {
	withPrompt := BuildEpisodes([]event.Event{
		prompt(0, "find x"),
		getNug(1, "x query", "id1"),
		prompt(2, "yes"),
	})
	if len(withPrompt) != 1 {
		t.Fatalf("want 1 episode, got %d", len(withPrompt))
	}
	if withPrompt[0].Prompt != "find x" {
		t.Errorf("Prompt = %q, want %q", withPrompt[0].Prompt, "find x")
	}

	noPrompt := BuildEpisodes([]event.Event{
		getNug(0, "x query", "id1"),
		prompt(1, "yes"),
	})
	if len(noPrompt) != 1 {
		t.Fatalf("want 1 episode, got %d", len(noPrompt))
	}
	if noPrompt[0].Prompt != "" {
		t.Errorf("Prompt = %q, want empty (no prompt preceded the root query)", noPrompt[0].Prompt)
	}
}

// TestBuildEpisodesSharesPromptAcrossSameTurnEpisodes pins the intended (not
// buggy) behavior when one human turn roots more than one episode: a consuming
// Edit closes episode 1 without a new prompt, then a later get_nug roots episode
// 2 in the same turn — both carry the one prompt that opened the turn. Prompt is
// never reset on episode flush.
func TestBuildEpisodesSharesPromptAcrossSameTurnEpisodes(t *testing.T) {
	eps := BuildEpisodes([]event.Event{
		prompt(0, "find x"),
		getNug(1, "q1", "id1"),
		tool(2, "Edit", "x.go"), // consumes → closes episode 1 without a new prompt
		getNug(3, "q2", "id2"),  // roots episode 2 in the SAME human turn
	})
	if len(eps) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(eps))
	}
	if eps[0].Prompt != "find x" || eps[1].Prompt != "find x" {
		t.Errorf("Prompt = (%q, %q), want both %q", eps[0].Prompt, eps[1].Prompt, "find x")
	}
}
