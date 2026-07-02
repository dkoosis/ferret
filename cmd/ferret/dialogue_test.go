package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/dialogue"
)

// TestRunDialogueClarifyWiring is the ferret-bbp.9 integration check: the
// transcript-walking caller populates MoveContext.PriorAgentQuestion from the
// preceding assistant turn, so a genuine answer-after-an-agent-question tags
// MoveClarify — while an answer-shaped turn with NO preceding agent question stays
// neutral, and the first turn (no predecessor) is unaffected.
func TestRunDialogueClarifyWiring(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-cq.jsonl", []string{
		// turn 0: opening user request — first turn, no predecessor.
		`{"type":"user","sessionId":"sess-cq","message":{"role":"user","content":"add retry logic to the client"}}`,
		// assistant asks a disambiguating question (ends in "?").
		`{"type":"assistant","sessionId":"sess-cq","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Which client — the HTTP one or the gRPC one?"}]}}`,
		// turn 1: the human answers it → clarify (matches no lexical cue on its own).
		`{"type":"user","sessionId":"sess-cq","message":{"role":"user","content":"the http one"}}`,
		// assistant makes a plain statement (no question).
		`{"type":"assistant","sessionId":"sess-cq","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Done, I added exponential backoff."}]}}`,
		// turn 2: same answer-shaped turn but NO preceding question → neutral.
		`{"type":"user","sessionId":"sess-cq","message":{"role":"user","content":"the second one"}}`,
	})

	var buf bytes.Buffer
	if err := runDialogue(&buf, root, "sess-cq", fmtText); err != nil {
		t.Fatalf("runDialogue: %v", err)
	}
	out := buf.String()

	if got := strings.Count(out, string(dialogue.MoveClarify)); got != 1 {
		t.Errorf("expected exactly one clarify turn, got %d\n%s", got, out)
	}
	// The answer with a preceding question is clarify; the identical-shaped turn
	// with no preceding question must NOT be clarify (it lands neutral/other).
	if !strings.Contains(out, "[turn 1] "+string(dialogue.MoveClarify)) {
		t.Errorf("turn 1 (answer after agent question) should be clarify\n%s", out)
	}
	if strings.Contains(out, "[turn 2] "+string(dialogue.MoveClarify)) {
		t.Errorf("turn 2 (no preceding agent question) must not be clarify\n%s", out)
	}
}

// TestRunDialogueFirstTurnNoPredecessor guards the boundary: the very first user
// turn has no preceding assistant turn, so the pending-question flag is false and it
// is never spuriously tagged clarify.
func TestRunDialogueFirstTurnNoPredecessor(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-first.jsonl", []string{
		`{"type":"user","sessionId":"sess-first","message":{"role":"user","content":"just do the thing"}}`,
	})

	var buf bytes.Buffer
	if err := runDialogue(&buf, root, "sess-first", fmtText); err != nil {
		t.Fatalf("runDialogue: %v", err)
	}
	if out := buf.String(); strings.Contains(out, string(dialogue.MoveClarify)) {
		t.Errorf("first turn with no predecessor must not be clarify\n%s", out)
	}
}
