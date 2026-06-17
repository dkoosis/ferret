package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/conform"
)

func TestReadConformSpecFromStdinAndFile(t *testing.T) {
	spec := `{"task":"t","reference":["a","b"],"observed":[{"call":0,"tool":"Bash","step":"a"}]}`

	// file path
	f := t.TempDir() + "/spec.json"
	if err := writeFile(t, f, spec); err != nil {
		t.Fatal(err)
	}
	got, err := readConformSpec(f)
	if err != nil {
		t.Fatalf("readConformSpec(file): %v", err)
	}
	if got.Task != "t" || len(got.Reference) != 2 || len(got.Observed) != 1 {
		t.Fatalf("parsed spec wrong: %+v", got)
	}
}

func TestReadConformSpecBadJSON(t *testing.T) {
	f := t.TempDir() + "/spec.json"
	if err := writeFile(t, f, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := readConformSpec(f); !errors.Is(err, errConformBadSpec) {
		t.Fatalf("err = %v; want errConformBadSpec", err)
	}
}

func TestWriteConformanceTextLocalizesSkippedGate(t *testing.T) {
	spec := conformSpec{
		Task:      "assess PRs, merge IFF green, clean up",
		Reference: []string{"list", "review", "wait-green", "merge", "cleanup"},
		Observed: []conform.ObsCall{
			{Call: 0, Tool: "Bash", Step: "list"},
			{Call: 1, Tool: "Bash", Step: "review"},
			{Call: 2, Tool: "Bash", Step: "merge"},
			{Call: 3, Tool: "Bash", Step: "cleanup"},
		},
	}
	res := conform.Align(spec.Reference, spec.Observed)
	var buf bytes.Buffer
	if err := writeConformanceText(&buf, spec, res); err != nil {
		t.Fatalf("writeConformanceText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`task="assess PRs, merge IFF green, clean up"`,
		"reference (5 steps): list → review → wait-green → merge → cleanup",
		"observed: 4 calls (4 on-plan)",
		"✓ list",
		"✗ wait-green",          // the skipped gate, localized
		"deviations: 1 skipped", // roll-up
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

func TestWriteConformanceTextFlowerWarning(t *testing.T) {
	// One planned step, many off-plan calls that all happen to replay → high
	// fitness, low precision → flower-model warning.
	spec := conformSpec{
		Reference: []string{"a"},
		Observed: []conform.ObsCall{
			{Call: 0, Step: "a"},
			{Call: 1}, {Call: 2}, {Call: 3}, {Call: 4},
		},
	}
	res := conform.Align(spec.Reference, spec.Observed)
	var buf bytes.Buffer
	if err := writeConformanceText(&buf, spec, res); err != nil {
		t.Fatal(err)
	}
	if !flowerModel(res) {
		t.Fatalf("expected flower-model trip: fitness=%.2f precision=%.2f", res.Fitness, res.Precision)
	}
	if !strings.Contains(buf.String(), "⚠ invoked != served") {
		t.Errorf("missing flower-model warning\n---\n%s", buf.String())
	}
}

func TestWriteConformanceTextReportsDroppedNoise(t *testing.T) {
	// Noise interspersed in a step's calls must be dropped + reported, not folded
	// into off-plan: both "build" calls sync once de-noised.
	spec := conformSpec{
		Task:      "build then learn",
		Reference: []string{"build", "learn"},
		Observed: []conform.ObsCall{
			{Call: 0, Tool: "Bash", Step: "build"},
			{Call: 1, Tool: "Bash", Noise: true},
			{Call: 2, Tool: "Bash", Step: "build"},
			{Call: 3, Tool: "Bash", Step: "learn"},
		},
	}
	res := conform.Align(spec.Reference, spec.Observed)
	var buf bytes.Buffer
	if err := writeConformanceText(&buf, spec, res); err != nil {
		t.Fatalf("writeConformanceText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"observed: 4 calls (3 logical scored, 1 noise dropped) — 3 on-plan",
		"deviations: 0 skipped step(s), 0 off-plan call(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

func TestWriteConformanceJSONShape(t *testing.T) {
	spec := conformSpec{
		Reference: []string{"a", "b"},
		Observed:  []conform.ObsCall{{Call: 0, Step: "a"}},
	}
	res := conform.Align(spec.Reference, spec.Observed)
	var buf bytes.Buffer
	if err := writeConformanceJSON(&buf, spec, res); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, key := range []string{"fitness", "precision", "skipped", "offPlan", "noise", "flowerModel", "moves"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
	skipped, ok := got["skipped"].(float64)
	if !ok || skipped != 1 {
		t.Errorf("skipped=%v; want 1 (step b never executed)", got["skipped"])
	}
}

func writeFile(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o600)
}
