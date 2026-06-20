package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/score"
)

// landmarkSessionFixtureLines is a synthetic session with two recognizable tasks:
// a ship-feature task (prompt "implement …", Shape Read+Edit → partial progress)
// and an investigate task (prompt "audit …", Shape Read+Grep → full progress).
func landmarkSessionFixtureLines() []string {
	return []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"implement the landmark session mode"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}},` +
			`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"investigate how the gates package works"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"g.go"}},` +
			`{"type":"tool_use","id":"t4","name":"Grep","input":{"pattern":"foo"}}]}}`,
	}
}

func runLandmarkSession(t *testing.T, lines []string, format string) string {
	t.Helper()
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "s.jsonl", lines)
	var buf bytes.Buffer
	// Empty data dir → no corpus → uniform default weights (the bare path).
	if err := landmarkSession(&buf, root, "s", t.TempDir(), format); err != nil {
		t.Fatalf("landmarkSession: %v", err)
	}
	return buf.String()
}

// TestLandmarkSessionPerTaskJSON is the load-bearing acceptance: segment a
// session, map each task's stated goal to its milestone set, score the task's
// Shape, and emit one progress row per recognized task.
func TestLandmarkSessionPerTaskJSON(t *testing.T) {
	out := runLandmarkSession(t, landmarkSessionFixtureLines(), fmtJSON)
	var sp analyst.SessionProgress
	if err := json.Unmarshal([]byte(out), &sp); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	if sp.Total != 2 || sp.Recognized != 2 {
		t.Fatalf("total=%d recognized=%d; want 2/2\n%s", sp.Total, sp.Recognized, out)
	}
	if sp.Tasks[0].GoalKind != "ship-feature" {
		t.Errorf("task1 kind = %q; want ship-feature", sp.Tasks[0].GoalKind)
	}
	// ship-feature: read+edit of read/edit/test/commit → 0.5.
	if sp.Tasks[0].Progress.Score != 0.5 {
		t.Errorf("task1 progress = %v; want 0.5", sp.Tasks[0].Progress.Score)
	}
	if sp.Tasks[1].GoalKind != "investigate" || sp.Tasks[1].Progress.Score != 1.0 {
		t.Errorf("task2 = %+v; want investigate progress 1.0", sp.Tasks[1])
	}
}

// TestLandmarkSessionText asserts the human rendering carries the per-task rows,
// the recognized goal kind, the weighted progress, and the rollup.
func TestLandmarkSessionText(t *testing.T) {
	out := runLandmarkSession(t, landmarkSessionFixtureLines(), fmtText)
	for _, want := range []string{
		"landmark session=s",
		"ship-feature",
		"investigate",
		"progress=",
		"tasks=2",
		"recognized=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdLandmarkSessionRoutes pins that --session routes to the session scope
// (not the spec scope): cmdLandmark with Session set and no spec must not error
// on a missing spec, and must emit a SessionProgress JSON shape.
func TestCmdLandmarkSessionRoutes(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "s.jsonl", landmarkSessionFixtureLines())
	CLI.Landmark.Session = "s"
	CLI.Landmark.Root = root
	CLI.Landmark.Data = t.TempDir()
	CLI.Landmark.Format = fmtJSON
	t.Cleanup(func() {
		CLI.Landmark.Session, CLI.Landmark.Root, CLI.Landmark.Data, CLI.Landmark.Format = "", "", "", ""
	})
	// cmdLandmark writes to os.Stdout; capture it so the routing assertion does not
	// spill the session JSON into test output (the rendering itself is covered by
	// runLandmarkSession above). We assert it does not error and routed to session.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	cmdErr := cmdLandmark()
	os.Stdout = orig
	_ = w.Close()
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()
	if cmdErr != nil {
		t.Fatalf("cmdLandmark session mode: %v", cmdErr)
	}
}

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

// TestCmdLandmarkNegativeWeightRejected pins that a negative milestone weight is
// rejected: it would otherwise survive as an "override" and push the [0,1] progress
// score out of range (a missed negative milestone shrinks totalWeight below hitWeight).
func TestCmdLandmarkNegativeWeightRejected(t *testing.T) {
	specPath := t.TempDir() + "/spec.json"
	spec := `{"milestones":[{"id":"bad","tools":["Read"],"weight":-1}],"shape":["Read"]}`
	if err := writeFile(t, specPath, spec); err != nil {
		t.Fatal(err)
	}
	CLI.Landmark.Spec = specPath
	CLI.Landmark.Format = fmtJSON
	CLI.Landmark.Data = t.TempDir() // empty dir → no corpus, but guard fires first
	t.Cleanup(func() { CLI.Landmark.Spec, CLI.Landmark.Format, CLI.Landmark.Data = "", "", "" })
	if err := cmdLandmark(); !errors.Is(err, errLandmarkNegWeight) {
		t.Fatalf("err = %v; want errLandmarkNegWeight", err)
	}
}
