package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// parallelResult builds a minimal two-profile result for the render tests —
// the sweep itself is pinned in internal/mine/parallel_test.go; these pin the
// CLI render layer.
func parallelResult() *mine.ParallelResult {
	return &mine.ParallelResult{
		Sessions: 3, Events: 9, Untimed: 2, Bytes: 1000,
		SpanHours: 4, ActiveHours: 2.5, IdleGapMin: 5,
		Window: mine.ConcurrencyProfile{
			Max: 2, TimeWeightedMean: 1.4, ByteWeightedMean: 1.8,
			Levels: []mine.LevelRow{
				{N: 1, Hours: 1.5, HoursPct: 60, Events: 4, Bytes: 200, BytesPct: 20, BytesPctAtOrAbove: 100},
				{N: 2, Hours: 1.0, HoursPct: 40, Events: 5, Bytes: 800, BytesPct: 80, BytesPctAtOrAbove: 80},
			},
		},
		Fanout: mine.ConcurrencyProfile{
			Max: 1, TimeWeightedMean: 1, ByteWeightedMean: 1,
			Levels: []mine.LevelRow{{N: 1, Hours: 2.5, HoursPct: 100, Events: 9, Bytes: 1000, BytesPct: 100, BytesPctAtOrAbove: 100}},
		},
	}
}

// TestWriteParallelText_RendersBothProfiles_When_ResultHasLevels pins that the
// two axes are reported separately and labeled — a merged number cannot say
// whether the cost belongs to overlapping windows or to subagent fan-out, which
// is the only question the command answers.
func TestWriteParallelText_RendersBothProfiles_When_ResultHasLevels(t *testing.T) {
	var buf bytes.Buffer
	if err := writeParallelText(&buf, parallelResult(), 0, 0); err != nil {
		t.Fatalf("writeParallelText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"sessions=3", "events=9", "untimed=2", "span=4.0h", "active=2.5h", "idle-gap=5m",
		"window (distinct sessions", "fan-out (distinct agents",
		"max=2", "N=1", "N=2", "80.0% bytes@N+",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	if wIdx, fIdx := strings.Index(out, "window (distinct"), strings.Index(out, "fan-out (distinct"); wIdx > fIdx {
		t.Errorf("window profile must render before fan-out (%d > %d)\n---\n%s", wIdx, fIdx, out)
	}
}

// TestWriteParallelText_ReportsEmptyState_When_NoTimedEvents pins the DK-AXI
// "0 results" rule: an empty corpus says so instead of printing a bare header a
// caller would retry against.
func TestWriteParallelText_ReportsEmptyState_When_NoTimedEvents(t *testing.T) {
	var buf bytes.Buffer
	if err := writeParallelText(&buf, &mine.ParallelResult{IdleGapMin: 5}, 0, 0); err != nil {
		t.Fatalf("writeParallelText: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "0 timed events") {
		t.Errorf("empty result missing the explicit empty state\n---\n%s", out)
	}
}

// TestParallelResult_MarshalsBothProfiles_When_EncodedAsJSON pins the JSON
// contract: both axes present under stable keys, including the cumulative
// column downstream readers rank on.
func TestParallelResult_MarshalsBothProfiles_When_EncodedAsJSON(t *testing.T) {
	raw, err := json.Marshal(parallelResult())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"window", "fanout", "sessions", "untimed", "activeHours", "idleGapMinutes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q: %s", key, raw)
		}
	}
	if !strings.Contains(string(raw), "bytesPctAtOrAbove") {
		t.Errorf("JSON missing the cumulative column: %s", raw)
	}
}
