package main

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/retrievalevent"
	"github.com/dkoosis/ferret/internal/score"
)

// errJudgeBoom is a static sentinel for the faked judge failure (err113: no
// dynamic errors), mirrors retrieval_hop1_test.go's errTestJudge.
var errJudgeBoom = errors.New("boom")

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

func fixedRNG() *rand.Rand { return rand.New(rand.NewSource(42)) }

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
	fake := func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
		return analyst.RelevanceResult{}, errJudgeBoom
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
			rand.New(rand.NewSource(seed)), "evt-1", res, events, nil, cands)
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

// TestReadEventsTolerantSkipsSchemaMismatch: a line with a schema_version
// that doesn't match retrievalevent.SchemaVersion (a future producer bump, or
// a corrupt row), and an unparseable line, are both skipped — not fatal, the
// same tolerance feedback_prep.go's scanNewLines already has. Without this
// symmetry, a future schema bump would leave prep working (it already
// tolerates the same way) while judge died on every settled event handed to
// it — a silent, permanent feature outage (specialist review finding).
func TestReadEventsTolerantSkipsSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	writeJSONL(t, path, searchEvent("evt-good", "s1", "n1"))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema_version":99,"kind":"search","event_id":"evt-future","session_id":"s1"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json at all\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, searchEvent("evt-good-2", "s1", "n2"))

	events, err := readEventsTolerant(path)
	if err != nil {
		t.Fatalf("readEventsTolerant: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("readEventsTolerant returned %d events, want 2 (schema-mismatch and unparseable lines skipped), got %+v", len(events), events)
	}
	if events[0].EventID != "evt-good" || events[1].EventID != "evt-good-2" {
		t.Errorf("readEventsTolerant events = %+v, want evt-good then evt-good-2 in order", events)
	}
}
