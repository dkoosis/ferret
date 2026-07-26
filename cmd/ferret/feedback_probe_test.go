package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/label"
	"github.com/dkoosis/ferret/internal/transcript"
)

// writeProbeSession writes lines as a session jsonl under a temp dir and
// returns its transcript.Source — buildProbeAdjustments' input.
func writeProbeSession(t *testing.T, lines []string) transcript.Source {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	var buf []byte
	for _, ln := range lines {
		buf = append(buf, ln...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	return transcript.Source{Path: path, Project: "p", Session: "s"}
}

// TestBuildProbeAdjustments_RecognizedAnswer: a granted ask renders its
// question in an assistant text block; the next user line is a recognized
// "y - now fix the tests" answer. Both maps get an entry keyed on that user
// line's OWN timestamp — the label's already-stripped remainder as the
// adjustment, and the exclusion flag so sessionUserTurns skips it outright.
func TestBuildProbeAdjustments_RecognizedAnswer(t *testing.T) {
	const answerTS = "2026-07-26T12:00:02Z"
	lines := []string{
		`{"type":"user","timestamp":"2026-07-26T12:00:00Z","sessionId":"s","message":{"role":"user","content":"orient and assess the bead"}}`,
		`{"type":"assistant","timestamp":"2026-07-26T12:00:01Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"did this help? [y/n/s]"}]}}`,
		`{"type":"user","timestamp":"` + answerTS + `","sessionId":"s","message":{"role":"user","content":"y - now fix the tests"}}`,
	}
	labels := []label.Label{
		{Session: "s", Question: "did this help? [y/n/s]", Valence: label.ValenceYes, Text: "now fix the tests"},
	}
	adjust, exclude, err := buildProbeAdjustments(writeProbeSession(t, lines), labels)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := adjust[answerTS]; !ok || got != "now fix the tests" {
		t.Errorf("adjust[%q] = %q, ok=%v; want the label's remainder", answerTS, got, ok)
	}
	if !exclude[answerTS] {
		t.Errorf("exclude[%q] must be set for a non-ignored label", answerTS)
	}
}

// TestBuildProbeAdjustments_BareTokenGetsEmptyAdjustment: a bare "n" answer's
// remainder is "" — the adjustment map must still carry an explicit entry
// (segment.go's feed folds "" to the no-boundary carrier path), not simply
// omit the key.
func TestBuildProbeAdjustments_BareTokenGetsEmptyAdjustment(t *testing.T) {
	const answerTS = "2026-07-26T12:00:02Z"
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-26T12:00:01Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"did this help? [y/n/s]"}]}}`,
		`{"type":"user","timestamp":"` + answerTS + `","sessionId":"s","message":{"role":"user","content":"n"}}`,
	}
	labels := []label.Label{
		{Session: "s", Question: "did this help? [y/n/s]", Valence: label.ValenceNo, Text: ""},
	}
	adjust, exclude, err := buildProbeAdjustments(writeProbeSession(t, lines), labels)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := adjust[answerTS]; !ok || got != "" {
		t.Errorf("adjust[%q] = %q, ok=%v; want an explicit empty-string entry", answerTS, got, ok)
	}
	if !exclude[answerTS] {
		t.Error("exclude must still be set for a bare-token answer")
	}
}

// TestBuildProbeAdjustments_IgnoredLabelGetsNoEntry is the over-exclusion
// guard (plan §6): an Ignored label was genuine dk work, not a probe answer —
// it must get NO entry in either map, so the turn stays fully visible to
// segmentation and the dialogue tagger.
func TestBuildProbeAdjustments_IgnoredLabelGetsNoEntry(t *testing.T) {
	const answerTS = "2026-07-26T12:00:02Z"
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-26T12:00:01Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"did this help? [y/n/s]"}]}}`,
		`{"type":"user","timestamp":"` + answerTS + `","sessionId":"s","message":{"role":"user","content":"please also check the tests"}}`,
	}
	labels := []label.Label{
		{Session: "s", Question: "did this help? [y/n/s]", Valence: label.ValenceIgnored, Text: "please also check the tests"},
	}
	adjust, exclude, err := buildProbeAdjustments(writeProbeSession(t, lines), labels)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adjust[answerTS]; ok {
		t.Error("an Ignored label must not get an adjust entry")
	}
	if exclude[answerTS] {
		t.Error("an Ignored label must not get an exclude entry")
	}
}

// TestBuildProbeAdjustments_QuestionNeverRenderedNoMatch: no assistant turn
// ever carried the label's Question (e.g. the ask never actually rendered) —
// buildProbeAdjustments must not guess a turn to blame; both maps stay empty.
func TestBuildProbeAdjustments_QuestionNeverRenderedNoMatch(t *testing.T) {
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-26T12:00:01Z","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"unrelated reply text"}]}}`,
		`{"type":"user","timestamp":"2026-07-26T12:00:02Z","sessionId":"s","message":{"role":"user","content":"y - now fix the tests"}}`,
	}
	labels := []label.Label{
		{Session: "s", Question: "did this help? [y/n/s]", Valence: label.ValenceYes, Text: "now fix the tests"},
	}
	adjust, exclude, err := buildProbeAdjustments(writeProbeSession(t, lines), labels)
	if err != nil {
		t.Fatal(err)
	}
	if len(adjust) != 0 || len(exclude) != 0 {
		t.Errorf("an unrendered question must produce no adjustment, got adjust=%v exclude=%v", adjust, exclude)
	}
}
