package analyst

import (
	"testing"

	"github.com/dkoosis/ferret/internal/score"
)

// TestScoreSessionLandmarksPerTask is the load-bearing contract: each real task's
// stated goal maps to a milestone set, and the task's Shape scores against THAT
// set. The preamble is skipped; an unrecognized goal is surfaced unscored.
func TestScoreSessionLandmarksPerTask(t *testing.T) {
	res := score.Result{
		Session: "s", Project: "p",
		Segments: []score.Segment{
			// preamble (Index 0): no stated goal → skipped.
			{Index: 0, Prompt: "", Shape: []string{"Read"}},
			// ship-feature ("implement"): read+edit+test+commit milestones.
			// Shape hits read+edit, misses test+commit → partial progress.
			{Index: 1, Prompt: "implement the marker", Shape: []string{"Read", "Edit"}},
			// investigate ("audit"): read+search. Shape hits both → full progress.
			{Index: 2, Prompt: "audit the gates package", Shape: []string{"Read", "Grep"}},
			// unrecognized goal: no cue matches → Recognized=false, no score.
			{Index: 3, Prompt: "zxqv frobnicate the widget", Shape: []string{"Read"}},
		},
	}

	sp := ScoreSessionLandmarks(res, nil) // nil corpus → uniform default weights

	if sp.Total != 3 {
		t.Fatalf("Total = %d; want 3 (preamble excluded)", sp.Total)
	}
	if sp.Recognized != 2 {
		t.Fatalf("Recognized = %d; want 2 (two goals matched a set)", sp.Recognized)
	}

	// Task 1: ship-feature, 2 of 4 equal-weight milestones hit → 0.5.
	t1 := sp.Tasks[0]
	if t1.Index != 1 || !t1.Recognized || t1.GoalKind != "ship-feature" {
		t.Fatalf("task1 = %+v; want index 1, recognized ship-feature", t1)
	}
	if t1.Progress.Score != 0.5 {
		t.Errorf("task1 progress = %v; want 0.5 (read+edit of read/edit/test/commit)", t1.Progress.Score)
	}

	// Task 2: investigate, both milestones hit → 1.0.
	t2 := sp.Tasks[1]
	if t2.GoalKind != "investigate" || t2.Progress.Score != 1.0 {
		t.Errorf("task2 = %+v; want investigate, progress 1.0", t2)
	}

	// Task 3: unrecognized → no set, no score.
	t3 := sp.Tasks[2]
	if t3.Recognized || t3.GoalKind != "" {
		t.Errorf("task3 = %+v; want unrecognized (no goal kind, no progress)", t3)
	}
	if len(t3.Progress.Hits) != 0 {
		t.Errorf("task3 carried %d hits; want 0 (unrecognized goal is unscored)", len(t3.Progress.Hits))
	}
}

// TestScoreSessionLandmarksWeightsFromDefault pins that with a nil corpus the
// uniform default weight stands in (every milestone counts equally), so a fully
// covered set scores exactly 1.0 — the path is usable without an events corpus.
func TestScoreSessionLandmarksWeightsFromDefault(t *testing.T) {
	res := score.Result{
		Segments: []score.Segment{
			// refactor: read+edit. Shape hits both → 1.0 under uniform weights.
			{Index: 1, Prompt: "refactor the segmenter", Shape: []string{"Read", "Edit"}},
		},
	}
	sp := ScoreSessionLandmarks(res, nil)
	if len(sp.Tasks) != 1 || sp.Tasks[0].GoalKind != "refactor" {
		t.Fatalf("tasks = %+v; want one refactor task", sp.Tasks)
	}
	if got := sp.Tasks[0].Progress.Score; got != 1.0 {
		t.Errorf("progress = %v; want 1.0 (both milestones hit, uniform weights)", got)
	}
	// Every milestone weight is the uniform default (positive), so totalWeight > 0.
	if sp.Tasks[0].Progress.TotalWeight <= 0 {
		t.Errorf("totalWeight = %v; want > 0 (uniform default fills weights)", sp.Tasks[0].Progress.TotalWeight)
	}
}
