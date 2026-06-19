package analyst

import (
	"math"
	"math/rand"
	"testing"
)

// --- pool building: returned ∪ sampled-unreturned, shuffled, rank-blind --------

func TestBuildPoolUnionsReturnedAndSampledUnreturned(t *testing.T) {
	ep := Episode{
		Name:     "e1",
		Prompt:   "p",
		Query:    "q",
		Returned: []NugCandidate{{ID: "r1", Text: "a"}, {ID: "r2", Text: "b"}},
	}
	store := []NugCandidate{
		{ID: "r1", Text: "a"}, // already returned — must not be double-counted
		{ID: "u1", Text: "c"},
		{ID: "u2", Text: "d"},
		{ID: "u3", Text: "e"},
	}
	// sampleN large enough to take all unreturned.
	pool := BuildPool(ep, store, 10, rand.New(rand.NewSource(1)))
	ids := map[string]int{}
	for _, c := range pool {
		ids[c.ID]++
	}
	for _, want := range []string{"r1", "r2", "u1", "u2", "u3"} {
		if ids[want] != 1 {
			t.Errorf("pool should contain %q exactly once, got %d", want, ids[want])
		}
	}
	if len(pool) != 5 {
		t.Errorf("pool size = %d, want 5 (no dupes of r1)", len(pool))
	}
}

func TestBuildPoolCapsSampledUnreturned(t *testing.T) {
	ep := Episode{Name: "e", Returned: []NugCandidate{{ID: "r1"}}}
	store := []NugCandidate{{ID: "r1"}, {ID: "u1"}, {ID: "u2"}, {ID: "u3"}, {ID: "u4"}}
	pool := BuildPool(ep, store, 2, rand.New(rand.NewSource(1)))
	// 1 returned + 2 sampled unreturned = 3.
	if len(pool) != 3 {
		t.Fatalf("pool size = %d, want 3 (returned + 2 sampled)", len(pool))
	}
}

func TestBuildPoolIsRankBlind(t *testing.T) {
	// The pool the judge sees must carry no rank/score field — NugCandidate has
	// only ID+Text by construction. This test pins that the returned items are
	// not handed out in their retrieval order (shuffled), so the judge can't
	// anchor on position. With a fixed seed we get a deterministic permutation;
	// assert it is not the identity order for a pool big enough to shuffle.
	ep := Episode{Name: "e"}
	var store []NugCandidate
	for i := 0; i < 12; i++ {
		store = append(store, NugCandidate{ID: string(rune('a' + i))})
	}
	pool := BuildPool(ep, store, 100, rand.New(rand.NewSource(42)))
	identity := true
	for i, c := range pool {
		if c.ID != store[i].ID {
			identity = false
			break
		}
	}
	if identity {
		t.Errorf("pool was not shuffled (identity order): %v", pool)
	}
}

// --- R4 precision@k -----------------------------------------------------------

func TestPrecisionAtKCountsRelevantInTopK(t *testing.T) {
	// Returned order r1,r2,r3,r4. grades: r1=3 r2=0 r3=2 r4=1.
	// relevant = grade>=2 → r1,r3. precision@4 = 2/4 = 0.5.
	returned := []string{"r1", "r2", "r3", "r4"}
	grades := map[string]RelGrade{"r1": 3, "r2": 0, "r3": 2, "r4": 1}
	got := PrecisionAtK(returned, grades, 4)
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("PrecisionAtK@4 = %v, want 0.5", got)
	}
	// precision@2 = r1 relevant, r2 not → 1/2 = 0.5
	if got2 := PrecisionAtK(returned, grades, 2); math.Abs(got2-0.5) > 1e-9 {
		t.Errorf("PrecisionAtK@2 = %v, want 0.5", got2)
	}
	// precision@1 = r1 relevant → 1.0
	if got1 := PrecisionAtK(returned, grades, 1); math.Abs(got1-1.0) > 1e-9 {
		t.Errorf("PrecisionAtK@1 = %v, want 1.0", got1)
	}
}

func TestPrecisionAtKEmptyReturnedIsZero(t *testing.T) {
	if got := PrecisionAtK(nil, map[string]RelGrade{}, 5); got != 0 {
		t.Errorf("PrecisionAtK on empty = %v, want 0", got)
	}
}

// --- R5 recall ----------------------------------------------------------------

func TestRecallIsReturnedRelevantOverAllRelevantInPool(t *testing.T) {
	// Pool grades: r1=3,r2=0 (returned) ; u1=2,u2=2,u3=0 (unreturned).
	// relevant in pool (grade>=2): r1,u1,u2 → 3. returned-relevant: r1 → 1.
	// recall = 1/3.
	returned := []string{"r1", "r2"}
	poolGrades := map[string]RelGrade{"r1": 3, "r2": 0, "u1": 2, "u2": 2, "u3": 0}
	got := Recall(returned, poolGrades)
	if math.Abs(got-1.0/3.0) > 1e-9 {
		t.Errorf("Recall = %v, want 1/3", got)
	}
}

func TestRecallNoRelevantInPoolIsZero(t *testing.T) {
	// No relevant nug exists → recall undefined; report 0 (a coverage gap, not a
	// recall success). The episode should be routed to coverage analysis, not
	// counted as recall=1.
	got := Recall([]string{"r1"}, map[string]RelGrade{"r1": 0, "u1": 1})
	if got != 0 {
		t.Errorf("Recall with no relevant nug = %v, want 0", got)
	}
}

// --- R6 nDCG@k ----------------------------------------------------------------

func TestNDCGPerfectRankingIsOne(t *testing.T) {
	// Returned in the ideal order (grades descending) → nDCG = 1.
	returned := []string{"a", "b", "c"}
	grades := map[string]RelGrade{"a": 3, "b": 2, "c": 1}
	got := NDCGAtK(returned, grades, 3)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("NDCG perfect = %v, want 1.0", got)
	}
}

func TestNDCGImperfectRankingBelowOne(t *testing.T) {
	// Same grades, worst order (ascending) → nDCG < 1.
	returned := []string{"c", "b", "a"}
	grades := map[string]RelGrade{"a": 3, "b": 2, "c": 1}
	got := NDCGAtK(returned, grades, 3)
	if got >= 1.0 || got <= 0.0 {
		t.Errorf("NDCG worst order = %v, want in (0,1)", got)
	}
}

func TestNDCGKnownValue(t *testing.T) {
	// grades returned in order: 2, 0, 1 (positions 1..3).
	// gain = 2^g - 1 → 3, 0, 1. discount = 1/log2(i+1).
	// DCG = 3/log2(2) + 0/log2(3) + 1/log2(4) = 3/1 + 0 + 1/2 = 3.5
	// ideal order grades 2,1,0 → gains 3,1,0:
	// IDCG = 3/1 + 1/log2(3) + 0 = 3 + 1/1.5849625 = 3.6309298
	// nDCG = 3.5 / 3.6309298 = 0.963947...
	returned := []string{"x", "y", "z"}
	grades := map[string]RelGrade{"x": 2, "y": 0, "z": 1}
	got := NDCGAtK(returned, grades, 3)
	want := 3.5 / (3.0 + 1.0/math.Log2(3))
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("NDCG = %v, want %v", got, want)
	}
}

func TestNDCGAllZeroIsZero(t *testing.T) {
	// No graded relevance anywhere → IDCG=0 → nDCG defined as 0, not NaN.
	got := NDCGAtK([]string{"a", "b"}, map[string]RelGrade{"a": 0, "b": 0}, 2)
	if got != 0 {
		t.Errorf("NDCG all-zero = %v, want 0 (not NaN)", got)
	}
}

// --- ScoreEpisode: rejoin judge grades to retrieval order, compute R4/R5/R6 ----

func TestScoreEpisodeComputesAllThree(t *testing.T) {
	// returned order r1,r2 ; pool also has unreturned u1.
	// grades: r1=3, r2=0, u1=2.
	// precision@2 = 1/2 (r1 relevant) = 0.5
	// recall = returned-relevant{r1} / all-relevant{r1,u1} = 1/2 = 0.5
	// nDCG@2 over returned r1=3,r2=0: DCG = 7/1 + 0 = 7 (gain 2^3-1=7).
	//   ideal order of returned grades {3,0}: 7/1 + 0 = 7 → nDCG = 1.0
	ep := Episode{
		Name:     "ep",
		Returned: []NugCandidate{{ID: "r1"}, {ID: "r2"}},
	}
	grades := map[string]RelGrade{"r1": 3, "r2": 0, "u1": 2}
	sc := ScoreEpisode(ep, grades, 2)
	if math.Abs(sc.PrecisionAtK-0.5) > 1e-9 {
		t.Errorf("PrecisionAtK = %v, want 0.5", sc.PrecisionAtK)
	}
	if math.Abs(sc.Recall-0.5) > 1e-9 {
		t.Errorf("Recall = %v, want 0.5", sc.Recall)
	}
	if math.Abs(sc.NDCGAtK-1.0) > 1e-9 {
		t.Errorf("NDCGAtK = %v, want 1.0", sc.NDCGAtK)
	}
	if sc.Episode != "ep" {
		t.Errorf("Episode = %q, want ep", sc.Episode)
	}
}
