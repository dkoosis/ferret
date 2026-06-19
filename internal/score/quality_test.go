package score

import (
	"math"
	"testing"
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
