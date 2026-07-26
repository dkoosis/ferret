package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/feedback"
	"github.com/dkoosis/ferret/internal/label"
	"github.com/dkoosis/ferret/internal/score"
)

// answerFixtureQuestion is the rendered ask text every test below arms and
// renders identically, so the ask-rendered substring check passes unless a
// test deliberately omits it (the render-check-failure case).
const answerFixtureQuestion = "did this help? [y/n/s]"

// writeAnswerSession writes a session transcript at root/proj/sess-a.jsonl:
// a user prompt opening one segment, a tool_use call, an assistant text
// block (optionally carrying the armed question — renderQuestion controls
// this, for the render-check-failure case), and a second tool_use call so
// the segment's [FirstTS,LastTS] interval comfortably contains the
// candidate's TS (answerCandTS below). Returns root.
func writeAnswerSession(t *testing.T, renderQuestion bool) string {
	t.Helper()
	root := t.TempDir()
	sessDir := filepath.Join(root, "proj")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "unrelated reply text"
	if renderQuestion {
		text = answerFixtureQuestion
	}
	lines := []string{
		`{"type":"user","timestamp":"2026-07-25T15:00:00Z","sessionId":"sess-a","message":{"role":"user","content":"find the backend design notes"}}`,
		`{"type":"assistant","timestamp":"2026-07-25T15:01:00Z","sessionId":"sess-a","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"x"}}]}}`,
		`{"type":"assistant","timestamp":"2026-07-25T15:02:00Z","sessionId":"sess-a","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"` + text + `"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-25T15:03:00Z","sessionId":"sess-a","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"y"}}]}}`,
	}
	var buf []byte
	for _, ln := range lines {
		buf = append(buf, ln...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(filepath.Join(sessDir, "sess-a.jsonl"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// answerFixtureCand is the armed candidate every test below banks: TS sits
// inside the fixture session's single segment interval [15:01:00,15:03:00].
func answerFixtureCand() feedback.AskCandidate {
	return feedback.AskCandidate{
		TargetRef: "evt-1",
		SegmentID: "sess-a#1",
		TS:        "2026-07-25T15:02:30Z",
		Question:  answerFixtureQuestion,
		Reason:    "test",
	}
}

// TestRunFeedbackAnswer_RecognizedAnswerJoinsAndLabels is test plan item 1: a
// recognized "y - now fix the tests" answer to a rendered, armed ask writes
// the label with the right valence/remainder/TargetRef, clears the armed
// state, and — independently, exercising bbp.16's own join helpers rather
// than trusting the write path — re-resolves the SAME SegmentID the ask was
// raised for via a fresh score.SegmentSource + score.OwningSegment pass.
func TestRunFeedbackAnswer_RecognizedAnswerJoinsAndLabels(t *testing.T) {
	root := writeAnswerSession(t, true)
	data := t.TempDir()
	cand := answerFixtureCand()
	armedPath := feedback.ArmedPath(data, "sess-a")
	if err := feedback.SaveArmed(armedPath, cand); err != nil {
		t.Fatal(err)
	}

	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "y - now fix the tests"); err != nil {
		t.Fatalf("runFeedbackAnswer: %v", err)
	}

	labels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 {
		t.Fatalf("want exactly 1 label, got %d: %+v", len(labels), labels)
	}
	got := labels[0]
	if got.Valence != label.ValenceYes || got.Text != "now fix the tests" || got.TargetRef != cand.TargetRef || got.Question != cand.Question {
		t.Errorf("label = %+v, want valence=yes text=%q targetRef=%q question=%q",
			got, "now fix the tests", cand.TargetRef, cand.Question)
	}

	if _, ok, _ := feedback.LoadArmed(armedPath); ok {
		t.Error("armed state must be cleared after a recognized answer")
	}

	// Independent re-verification: re-run the join from scratch and confirm
	// it lands on the exact segment the ask was raised for.
	src, _, err := resolveSpineSource(root, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	res, err := score.SegmentSource(src)
	if err != nil {
		t.Fatal(err)
	}
	idx := score.OwningSegment(res.Segments, cand.TS)
	if idx < 0 {
		t.Fatalf("OwningSegment: cand.TS %q resolved to no segment in %+v", cand.TS, res.Segments)
	}
	gotSegID := fmt.Sprintf("%s#%d", res.Session, res.Segments[idx].Index)
	if gotSegID != cand.SegmentID {
		t.Errorf("re-verified SegmentID = %q, want the ask's own %q", gotSegID, cand.SegmentID)
	}
}

// TestRunFeedbackAnswer_IgnoredTurn is test plan item 2: an ordinary work
// turn after an armed, rendered ask is NOT presumed to be the answer — it
// records label.ValenceIgnored with a capped excerpt of the raw prompt, and
// still clears the armed state.
func TestRunFeedbackAnswer_IgnoredTurn(t *testing.T) {
	root := writeAnswerSession(t, true)
	data := t.TempDir()
	cand := answerFixtureCand()
	armedPath := feedback.ArmedPath(data, "sess-a")
	if err := feedback.SaveArmed(armedPath, cand); err != nil {
		t.Fatal(err)
	}

	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "please also check the tests"); err != nil {
		t.Fatalf("runFeedbackAnswer: %v", err)
	}

	labels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 {
		t.Fatalf("want exactly 1 label, got %d: %+v", len(labels), labels)
	}
	if labels[0].Valence != label.ValenceIgnored || labels[0].Text != "please also check the tests" {
		t.Errorf("label = %+v, want valence=ignored text=%q", labels[0], "please also check the tests")
	}
	if _, ok, _ := feedback.LoadArmed(armedPath); ok {
		t.Error("armed state must be cleared after an ignored turn")
	}
}

// TestRunFeedbackAnswer_NoArmedCandidateIsNoOp is (half of) test plan item 3:
// no armed file at all — the common case — is a pure no-op: no label, no
// error, nothing to clear.
func TestRunFeedbackAnswer_NoArmedCandidateIsNoOp(t *testing.T) {
	root := writeAnswerSession(t, true)
	data := t.TempDir()
	armedPath := feedback.ArmedPath(data, "sess-a")

	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "y"); err != nil {
		t.Fatalf("runFeedbackAnswer: %v", err)
	}
	labels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 0 {
		t.Errorf("no armed candidate must write no label, got %+v", labels)
	}
}

// TestRunFeedbackAnswer_TTLNoDoubleFire is the other half of test plan item
// 3: after a first call consumes the armed candidate, a SECOND call on the
// same session (simulating the turn after) must be a pure no-op too — one-
// shot consumption, proving the one-turn TTL.
func TestRunFeedbackAnswer_TTLNoDoubleFire(t *testing.T) {
	root := writeAnswerSession(t, true)
	data := t.TempDir()
	cand := answerFixtureCand()
	armedPath := feedback.ArmedPath(data, "sess-a")
	if err := feedback.SaveArmed(armedPath, cand); err != nil {
		t.Fatal(err)
	}
	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "y"); err != nil {
		t.Fatalf("first runFeedbackAnswer: %v", err)
	}
	firstLabels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(firstLabels) != 1 {
		t.Fatalf("first call: want exactly 1 label, got %d", len(firstLabels))
	}

	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "n - not this time either"); err != nil {
		t.Fatalf("second runFeedbackAnswer: %v", err)
	}
	secondLabels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(secondLabels) != 1 {
		t.Errorf("second call (no armed candidate left) must write NO additional label, got %d: %+v",
			len(secondLabels), secondLabels)
	}
}

// TestRunFeedbackAnswer_RenderCheckFailureWritesNoLabel is test plan item 3's
// companion: the armed question never actually rendered in the prior
// assistant turn (session died mid-turn, or Claude didn't render it) — no
// label at all is written (not even Ignored), but the armed state is still
// cleared (nothing to retry).
func TestRunFeedbackAnswer_RenderCheckFailureWritesNoLabel(t *testing.T) {
	root := writeAnswerSession(t, false) // assistant text does NOT carry the question
	data := t.TempDir()
	cand := answerFixtureCand()
	armedPath := feedback.ArmedPath(data, "sess-a")
	if err := feedback.SaveArmed(armedPath, cand); err != nil {
		t.Fatal(err)
	}

	if err := runFeedbackAnswer(armedPath, label.Path(data), root, "sess-a", "y - now fix the tests"); err != nil {
		t.Fatalf("runFeedbackAnswer: %v", err)
	}

	labels, err := label.Load(label.Path(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 0 {
		t.Errorf("a failed render check must write NO label, got %+v", labels)
	}
	if _, ok, _ := feedback.LoadArmed(armedPath); ok {
		t.Error("armed state must still be cleared even when the render check fails")
	}
}
