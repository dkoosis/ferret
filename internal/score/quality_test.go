package score

import (
	"math"
	"testing"

	"github.com/dkoosis/ferret/internal/conform"
)

const eps = 1e-9

// TestAxesFor tables the per-task axes scorer: efficiency falls as calls repeat
// the same tool kinds (thrash), saturates at 1.0 when a compound call touches more
// kinds than calls, and adaptivity separates a clean task, a stuck loop, a loop the
// task moved past, and a loop the Outcome confirms shipped.
func TestAxesFor(t *testing.T) {
	cases := []struct {
		name      string
		seg       Segment
		wantAdapt float64
	}{
		{
			name:      "tight task: one fresh kind per call",
			seg:       Segment{FirstCall: 0, LastCall: 2, Shape: []string{"Read", "Edit", "sh:go_test"}},
			wantAdapt: adaptClean,
		},
		{
			name:      "thrash: six calls, two kinds",
			seg:       Segment{FirstCall: 0, LastCall: 5, Shape: []string{"Read", "sh:rg", "Read", "sh:rg", "Read", "sh:rg"}},
			wantAdapt: adaptStuck, // loop runs to the boundary (last token sh:rg recurs), not shipped
		},
		{
			name:      "compound call saturates efficiency at 1.0",
			seg:       Segment{FirstCall: 0, LastCall: 0, Shape: []string{"sh:git_add", "sh:git_commit"}},
			wantAdapt: adaptClean,
		},
		{
			name:      "loop moved past into a new kind recovers",
			seg:       Segment{FirstCall: 0, LastCall: 6, Shape: []string{"Read", "sh:rg", "Read", "sh:rg", "Read", "sh:rg", "Edit"}},
			wantAdapt: adaptRecovered, // last token Edit does not recur → moved past
		},
		{
			name:      "shipped loop recovers even ending on the looping kind",
			seg:       Segment{FirstCall: 0, LastCall: 5, Shape: []string{"sh:go_test", "Edit", "sh:go_test", "Edit", "sh:go_test", "Edit"}, Outcome: &Outcome{Positive: true, Signal: "sh:git_commit"}},
			wantAdapt: adaptRecovered, // ends on looping Edit, but Outcome confirms shipped
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := axesFor(c.seg)
			if got.Adaptivity != c.wantAdapt {
				t.Errorf("adaptivity = %v, want %v", got.Adaptivity, c.wantAdapt)
			}
			if got.Efficiency < 0 || got.Efficiency > 1 {
				t.Errorf("efficiency %v out of [0,1]", got.Efficiency)
			}
		})
	}
}

// TestAdaptivityFromConform verifies the conformance-enriched adaptivity axis:
// a clean alignment (all sync, nothing skipped/off-plan) is fully adaptive; a
// localized off-plan detour the plan otherwise replayed reads as recovered; a
// trace that skipped planned gates / churned off-plan widely reads as stuck. The
// gradations reuse the reference-free scale so the two sources are comparable.
func TestAdaptivityFromConform(t *testing.T) {
	cases := []struct {
		name string
		res  conform.Result
		want float64
	}{
		{
			name: "clean alignment: every step synced, nothing off-plan",
			res:  conform.Result{Fitness: 1.0, Sync: 4, WorstCost: 4},
			want: adaptClean,
		},
		{
			name: "localized off-plan detour, plan mostly replayed: recovered",
			res:  conform.Result{Fitness: 0.8, Sync: 4, LogMoves: 1, Cost: 1, WorstCost: 5},
			want: adaptRecovered,
		},
		{
			name: "skipped gate + heavy off-plan churn: stuck",
			res:  conform.Result{Fitness: 0.3, Sync: 1, ModelMoves: 2, LogMoves: 3, Cost: 5, WorstCost: 7},
			want: adaptStuck,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AdaptivityFromConform(c.res); got != c.want {
				t.Errorf("AdaptivityFromConform = %v, want %v", got, c.want)
			}
		})
	}
}

// TestScoreAxesWithConform verifies the dispatch: a segment WITH a supplied
// conform.Result gets the conformance-enriched adaptivity; a segment WITHOUT one
// falls back to the reference-free proxy unchanged. Efficiency is untouched by
// the spec (it stays the reference-free thrash ratio in both paths).
func TestScoreAxesWithConform(t *testing.T) {
	// task 1 has a conform result (clean); task 2 (a stuck reference-free loop)
	// has none and must fall back unchanged.
	stuckLoop := []string{"Read", "sh:rg", "Read", "sh:rg", "Read", "sh:rg"}
	res := Result{Segments: []Segment{
		{Index: 1, FirstCall: 0, LastCall: 3, Shape: []string{"Read", "Edit", "sh:go_test", "sh:git_commit"}},
		{Index: 2, FirstCall: 4, LastCall: 9, Shape: stuckLoop},
	}}
	specs := map[int]conform.Result{
		1: {Fitness: 1.0, Sync: 4, WorstCost: 4},
	}
	ScoreAxesWithConform(&res, specs)

	if res.Segments[0].Axes == nil {
		t.Fatal("task 1 not scored")
	}
	if got := res.Segments[0].Axes.Adaptivity; got != adaptClean {
		t.Errorf("task 1 (conform-enriched) adaptivity = %v, want %v", got, adaptClean)
	}
	if res.Segments[1].Axes == nil {
		t.Fatal("task 2 not scored")
	}
	// task 2 has no spec → reference-free proxy: a loop running to the boundary
	// unresolved scores adaptStuck.
	if got := res.Segments[1].Axes.Adaptivity; got != adaptStuck {
		t.Errorf("task 2 (reference-free fallback) adaptivity = %v, want %v", got, adaptStuck)
	}
}

// TestScoreAxesWithConformNilFallsBack verifies a nil/empty spec map degrades to
// exactly the reference-free ScoreAxes behavior (the default path is unchanged).
func TestScoreAxesWithConformNilFallsBack(t *testing.T) {
	segs := []Segment{
		{Index: 0, FirstCall: 0, LastCall: 0, Shape: []string{"Read"}}, // preamble
		{Index: 1, FirstCall: -1, LastCall: -1},                        // owns no calls
		{Index: 2, FirstCall: 1, LastCall: 2, Shape: []string{"Read", "Edit"}},
	}
	free := Result{Segments: append([]Segment(nil), segs...)}
	ScoreAxes(&free)
	enriched := Result{Segments: append([]Segment(nil), segs...)}
	ScoreAxesWithConform(&enriched, nil)
	if mustMarshal(t, free) != mustMarshal(t, enriched) {
		t.Errorf("nil-spec ScoreAxesWithConform diverged from ScoreAxes:\nfree     %s\nenriched %s",
			mustMarshal(t, free), mustMarshal(t, enriched))
	}
}

// TestEfficiencyValues pins the exact efficiency arithmetic (distinct/calls,
// clamped) separately from the bounds/adaptivity table above.
func TestEfficiencyValues(t *testing.T) {
	cases := []struct {
		name string
		seg  Segment
		want float64
	}{
		{"tight", Segment{FirstCall: 0, LastCall: 2, Shape: []string{"Read", "Edit", "sh:go_test"}}, 1.0},
		{"thrash", Segment{FirstCall: 0, LastCall: 5, Shape: []string{"Read", "sh:rg", "Read", "sh:rg", "Read", "sh:rg"}}, 2.0 / 6.0},
		{"compound clamps to 1", Segment{FirstCall: 0, LastCall: 0, Shape: []string{"sh:git_add", "sh:git_commit"}}, 1.0},
		{"no calls", Segment{FirstCall: -1, LastCall: -1, Shape: nil}, 0},
		{"calls but no shape", Segment{FirstCall: 0, LastCall: 1, Shape: nil}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := efficiency(c.seg); math.Abs(got-c.want) > eps {
				t.Errorf("efficiency = %v, want %v", got, c.want)
			}
		})
	}
}

// TestScoreAxes verifies the in-place pass annotates scored tasks, skips the
// preamble and call-less segments (nil Axes = not scored), and is idempotent.
func TestScoreAxes(t *testing.T) {
	res := Result{Segments: []Segment{
		{Index: 0, FirstCall: 0, LastCall: 0, Shape: []string{"Read"}}, // preamble
		{Index: 1, FirstCall: -1, LastCall: -1},                        // owns no calls
		{Index: 2, FirstCall: 1, LastCall: 2, Shape: []string{"Read", "Edit"}},
	}}
	ScoreAxes(&res)

	if res.Segments[0].Axes != nil {
		t.Errorf("preamble scored: %+v", res.Segments[0].Axes)
	}
	if res.Segments[1].Axes != nil {
		t.Errorf("call-less task scored: %+v", res.Segments[1].Axes)
	}
	if res.Segments[2].Axes == nil {
		t.Fatal("task 2 not scored")
	}

	first := mustMarshal(t, res)
	ScoreAxes(&res)
	if second := mustMarshal(t, res); second != first {
		t.Errorf("ScoreAxes not idempotent:\nfirst  %s\nsecond %s", first, second)
	}
}

// TestClusterByShape verifies identical ordered shapes group, different shapes
// split, input order doesn't change the sorted output, and the preamble plus
// shapeless tasks are excluded.
func TestClusterByShape(t *testing.T) {
	segs := []Segment{
		{Index: 1, InBytes: 100, Shape: []string{"Read", "Edit"}},
		{Index: 2, InBytes: 120, Shape: []string{"Read", "Edit"}}, // same shape as #1
		{Index: 3, InBytes: 50, Shape: []string{"sh:go_test"}},    // distinct shape
		{Index: 0, InBytes: 999, Shape: []string{"Read", "Edit"}}, // preamble — excluded
		{Index: 4, InBytes: 10, Shape: nil},                       // shapeless — excluded
	}
	got := ClusterByShape(segs)
	if len(got) != 2 {
		t.Fatalf("clusters = %d, want 2", len(got))
	}
	// sorted by Key: "Read Edit" < "sh:go_test"
	if got[0].Key != "Read Edit" || got[0].K != 2 {
		t.Errorf("cluster[0] = %+v, want key=%q k=2", got[0], "Read Edit")
	}
	if got[1].Key != "sh:go_test" || got[1].K != 1 {
		t.Errorf("cluster[1] = %+v, want key=%q k=1", got[1], "sh:go_test")
	}

	// input order does not change the sorted output
	shuffled := []Segment{segs[2], segs[4], segs[1], segs[3], segs[0]}
	reordered := ClusterByShape(shuffled)
	if mustMarshal(t, reordered) != mustMarshal(t, got) {
		t.Errorf("ClusterByShape not order-stable")
	}
}

// TestConsistency verifies k identical costs score max consistency / zero spread,
// an outlier widens spread, and k==1 returns the sentinel, never a spurious 1.0.
func TestConsistency(t *testing.T) {
	if score, spread := consistency([]float64{100, 100, 100}); math.Abs(score-1) > eps || spread != 0 {
		t.Errorf("identical costs: (%v,%v), want (1,0)", score, spread)
	}
	if score, _ := consistency([]float64{50}); score != clusterNoSignal {
		t.Errorf("k==1: score = %v, want sentinel %v", score, clusterNoSignal)
	}
	tight, _ := consistency([]float64{100, 110, 90})
	loose, _ := consistency([]float64{10, 500, 50})
	if loose >= tight {
		t.Errorf("outlier cluster (%v) should be less consistent than tight (%v)", loose, tight)
	}
	if score, _ := consistency([]float64{0, 0}); math.Abs(score-1) > eps {
		t.Errorf("all-zero cost: score = %v, want 1", score)
	}
}
