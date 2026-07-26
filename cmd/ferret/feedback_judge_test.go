package main

import (
	"context"
	"errors"
	"math/rand"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/retrievalevent"
	"github.com/dkoosis/ferret/internal/score"
)

// judgeFixture builds a minimal, self-consistent scenario for
// runFeedbackJudge: one segment spanning the search event's ts, the search
// event itself (2 returned nugs), and a linked kind:read row (so the
// deterministic lattice lands on VerdictHelped — Adjudicate: linked, no
// repair-adjacency → helped).
func judgeFixture() (score.Result, []retrievalevent.Event) {
	res := score.Result{
		Session: "s1",
		Segments: []score.Segment{
			{Index: 0, Prompt: "an earlier task", FirstTS: "2026-07-25T14:00:00Z", LastTS: "2026-07-25T14:05:00Z"},
			{Index: 1, Prompt: "find the backend design notes", FirstTS: "2026-07-25T15:00:00Z", LastTS: "2026-07-25T15:05:00Z"},
		},
	}
	search := retrievalevent.Event{
		SchemaVersion: retrievalevent.SchemaVersion,
		Kind:          retrievalevent.KindSearch,
		EventID:       "evt-1",
		TS:            "2026-07-25T15:02:00Z",
		SessionID:     "s1",
		Query:         "backend design",
		Returned: []retrievalevent.Returned{
			{NugID: "n1", Score: 0.9},
			{NugID: "n2", Score: 0.8},
		},
	}
	read := retrievalevent.Event{
		SchemaVersion: retrievalevent.SchemaVersion,
		Kind:          retrievalevent.KindRead,
		EventID:       "evt-2",
		TS:            "2026-07-25T15:03:00Z",
		SessionID:     "s1",
		NugID:         "n1",
		SearchRef:     &retrievalevent.SearchRef{EventID: "evt-1", SessionID: "s1", TS: search.TS},
	}
	return res, []retrievalevent.Event{search, read}
}

func fixedRNG() *rand.Rand { return rand.New(rand.NewSource(42)) } //nolint:gosec // test determinism, not security

// TestRunFeedbackJudge_DisagreementFires: lattice=helped (linked, no
// repair-adjacency) but every returned nug grades below relevantThreshold
// (mismatch) — feedback.Disagree's helped-vs-mismatch case must fire.
func TestRunFeedbackJudge_DisagreementFires(t *testing.T) {
	res, events := judgeFixture()
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		return analyst.RelevanceResult{Judgments: []analyst.NugJudgment{
			{NugID: "n1", Grade: analyst.GradeMarginal},
			{NugID: "n2", Grade: analyst.GradeIrrelevant},
		}}, nil
	}
	got, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake, fixedRNG(),
		"evt-1", res, events, nil, []analyst.NugCandidate{{ID: "n1", Text: "t1"}, {ID: "n2", Text: "t2"}})
	if err != nil {
		t.Fatalf("runFeedbackJudge: %v", err)
	}
	if !got.Fired {
		t.Fatal("helped-vs-mismatch must fire")
	}
	if got.Ask.TargetRef != "evt-1" {
		t.Errorf("Ask.TargetRef = %q, want evt-1", got.Ask.TargetRef)
	}
	if got.Ask.Question == "" {
		t.Error("a fired candidate must carry a non-empty question")
	}
}

// TestRunFeedbackJudge_AgreementSilent: lattice=helped and a returned nug
// grades at/above relevantThreshold (served) — the signals agree, must stay
// silent.
func TestRunFeedbackJudge_AgreementSilent(t *testing.T) {
	res, events := judgeFixture()
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		return analyst.RelevanceResult{Judgments: []analyst.NugJudgment{
			{NugID: "n1", Grade: analyst.GradeRelevant},
			{NugID: "n2", Grade: analyst.GradeIrrelevant},
		}}, nil
	}
	got, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake, fixedRNG(),
		"evt-1", res, events, nil, []analyst.NugCandidate{{ID: "n1", Text: "t1"}, {ID: "n2", Text: "t2"}})
	if err != nil {
		t.Fatalf("runFeedbackJudge: %v", err)
	}
	if got.Fired {
		t.Errorf("helped-vs-served must stay silent, got %+v", got)
	}
}

// TestRunFeedbackJudge_UnclassifiableNoOp: no returned nug gets a grade at all
// (the judge only graded IDs outside the returned set) — SearchFit ok=false,
// must be a silent no-op, never a manufactured disagreement.
func TestRunFeedbackJudge_UnclassifiableNoOp(t *testing.T) {
	res, events := judgeFixture()
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		return analyst.RelevanceResult{Judgments: []analyst.NugJudgment{
			{NugID: "some-unreturned-id", Grade: analyst.GradeExact},
		}}, nil
	}
	got, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake, fixedRNG(),
		"evt-1", res, events, nil, []analyst.NugCandidate{{ID: "n1", Text: "t1"}})
	if err != nil {
		t.Fatalf("runFeedbackJudge: %v", err)
	}
	if got.Fired {
		t.Errorf("no graded returned nug must be a silent no-op, got %+v", got)
	}
}

// TestRunFeedbackJudge_JudgeErrorSurfaces: an injected judge error must
// surface as an error, write nothing, and not crash.
func TestRunFeedbackJudge_JudgeErrorSurfaces(t *testing.T) {
	res, events := judgeFixture()
	boom := errors.New("boom")
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		return analyst.RelevanceResult{}, boom
	}
	got, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake, fixedRNG(),
		"evt-1", res, events, nil, []analyst.NugCandidate{{ID: "n1", Text: "t1"}})
	if err == nil {
		t.Fatal("a judge error must surface, not be swallowed")
	}
	if got.Fired {
		t.Error("a judge error must never fire an ask")
	}
}

// TestRunFeedbackJudge_NoMatchingRecordNoOp: a search-event id with no
// matching HelpedRecord (e.g. filtered by AdjudicateEvents, or simply absent)
// is a silent no-op — and, since there is nothing to judge, the (paid) judge
// must never even be called.
func TestRunFeedbackJudge_NoMatchingRecordNoOp(t *testing.T) {
	res, events := judgeFixture()
	called := false
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		called = true
		return analyst.RelevanceResult{}, nil
	}
	got, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake, fixedRNG(),
		"evt-does-not-exist", res, events, nil, nil)
	if err != nil {
		t.Fatalf("runFeedbackJudge: %v", err)
	}
	if got.Fired {
		t.Error("no matching record must never fire")
	}
	if called {
		t.Error("the paid judge must never be called when there is no record to judge against")
	}
}

// TestRunFeedbackJudge_ShuffleIsDeterministicPerSeed: the same seeded rng
// produces the same candidate order every run; two different seeds produce
// (almost certainly) different orders — the rank-blinding contract
// (analyst.NugCandidate) must be reproducible for tests, not literally random.
func TestRunFeedbackJudge_ShuffleIsDeterministicPerSeed(t *testing.T) {
	res, events := judgeFixture()
	cands := []analyst.NugCandidate{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}, {ID: "n4"}, {ID: "n5"}}

	captureOrder := func(seed int64) []string {
		var order []string
		fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
			for _, c := range candidates {
				order = append(order, c.ID)
			}
			return analyst.RelevanceResult{}, nil
		}
		_, err := runFeedbackJudge(context.Background(), analyst.Config{}, fake,
			rand.New(rand.NewSource(seed)), "evt-1", res, events, nil, cands) //nolint:gosec
		if err != nil {
			t.Fatalf("runFeedbackJudge: %v", err)
		}
		return order
	}

	a1 := captureOrder(42)
	a2 := captureOrder(42)
	if len(a1) != len(a2) {
		t.Fatalf("order lengths differ: %v vs %v", a1, a2)
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("same seed produced different order: %v vs %v", a1, a2)
		}
	}

	b := captureOrder(7)
	same := len(a1) == len(b)
	if same {
		for i := range a1 {
			if a1[i] != b[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Error("different seeds produced the same order — shuffle seam looks unwired")
	}
}

// TestTurnsBack: turnsBack counts turns relative to the session's LAST
// segment; the owning segment itself is 0 turns back only when it IS the last
// one, and an out-of-range index degrades to 0 rather than negative.
func TestTurnsBack(t *testing.T) {
	segs := make([]score.Segment, 4) // indices 0..3
	cases := []struct {
		owner int
		want  int
	}{
		{3, 0}, // the latest segment: 0 turns back
		{2, 1},
		{0, 3},
		{-1, 0}, // no owning segment
		{99, 0}, // out of range
	}
	for _, c := range cases {
		if got := turnsBack(segs, c.owner); got != c.want {
			t.Errorf("turnsBack(segs, %d) = %d, want %d", c.owner, got, c.want)
		}
	}
}
