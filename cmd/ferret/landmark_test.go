package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/score"
)

func TestReadLandmarkSpecFromFile(t *testing.T) {
	spec := `{"task":"ship","milestones":[{"id":"read","tools":["Read"],"weight":1}],"shape":["Read","Edit"]}`
	f := t.TempDir() + "/spec.json"
	if err := writeFile(t, f, spec); err != nil {
		t.Fatal(err)
	}
	got, err := readLandmarkSpec(f)
	if err != nil {
		t.Fatalf("readLandmarkSpec(file): %v", err)
	}
	if got.Task != "ship" || len(got.Milestones) != 1 || len(got.Shape) != 2 {
		t.Fatalf("parsed spec wrong: %+v", got)
	}
}

func TestReadLandmarkSpecBadJSON(t *testing.T) {
	f := t.TempDir() + "/spec.json"
	if err := writeFile(t, f, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandmarkSpec(f); !errors.Is(err, errLandmarkBadSpec) {
		t.Fatalf("err = %v; want errLandmarkBadSpec", err)
	}
}

func TestWriteLandmarkTextRowsAndProgress(t *testing.T) {
	spec := landmarkSpec{
		Task: "ship feature",
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}, Weight: 1},
			{ID: "edit", Tools: []string{"Edit"}, Weight: 1},
			{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
		},
		Shape: []string{"Read", "Edit"}, // commit missed
	}
	res := score.ScoreLandmarks(spec.Milestones, spec.Shape)
	var buf bytes.Buffer
	if err := writeLandmarkText(&buf, spec, res); err != nil {
		t.Fatalf("writeLandmarkText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`task="ship feature"`,
		"milestones: 3",
		"progress=0.67",
		"✓ read",
		"✓ edit",
		"✗ commit",
		"missed: 1 milestone(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

func TestWriteLandmarkTextMissingNecessaryWarning(t *testing.T) {
	// One rare (high-weight) milestone missed → the "goal likely not reached" warn.
	spec := landmarkSpec{
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}, Weight: 1},
			{ID: "deploy", Tools: []string{"sh:deploy"}, Weight: landmarkHighWeightCap + 1},
		},
		Shape: []string{"Read"}, // deploy missed
	}
	res := score.ScoreLandmarks(spec.Milestones, spec.Shape)
	if !missingNecessary(res) {
		t.Fatalf("expected missingNecessary trip; res=%+v", res)
	}
	var buf bytes.Buffer
	if err := writeLandmarkText(&buf, spec, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "⚠ missing necessary milestone") {
		t.Errorf("missing necessary-milestone warning\n---\n%s", buf.String())
	}
}

func TestWriteLandmarkJSONShape(t *testing.T) {
	spec := landmarkSpec{
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}, Weight: 1},
			{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
		},
		Shape: []string{"Read"},
	}
	res := score.ScoreLandmarks(spec.Milestones, spec.Shape)
	var buf bytes.Buffer
	if err := writeLandmarkJSON(&buf, spec, res); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{
		"task", "milestones", "observed", "progress", "hitCount",
		"total", "hitWeight", "totalWeight", "missingNecessary", "hits",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	if p, ok := got["progress"].(float64); !ok || p != 0.5 {
		t.Errorf("progress=%v; want 0.5 (1 of 2 equal-weight milestones)", got["progress"])
	}
}

// TestWeighMilestonesUniformFallback: with no corpus at an empty data dir, every
// unweighted milestone gets the uniform fallback weight 1, and a spec-supplied
// weight is preserved.
func TestWeighMilestonesUniformFallback(t *testing.T) {
	ms := []score.Milestone{
		{ID: "a", Tools: []string{"Read"}},            // unweighted → fallback 1
		{ID: "b", Tools: []string{"Edit"}, Weight: 7}, // override preserved
	}
	weighMilestones(ms, t.TempDir()) // empty dir → no events.jsonl → no corpus
	if ms[0].Weight != 1 {
		t.Errorf("fallback weight = %v; want 1", ms[0].Weight)
	}
	if ms[1].Weight != 7 {
		t.Errorf("override clobbered: %v; want 7", ms[1].Weight)
	}
}
