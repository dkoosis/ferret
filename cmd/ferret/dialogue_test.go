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

// TestRunDialogueSkippedTurnClearsPendingQuestion guards the leak Gemini caught on
// #59: an agent question answered by a SKIPPED genuine user turn (an affirmation)
// must consume the pending-question flag, so the next unrelated request is not
// spuriously tagged clarify. A carrier envelope in the same slot would NOT consume
// it — but an affirmation/control is real user intent that supersedes the question.
func TestRunDialogueSkippedTurnClearsPendingQuestion(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-leak.jsonl", []string{
		`{"type":"user","sessionId":"sess-leak","message":{"role":"user","content":"add retry logic"}}`,
		`{"type":"assistant","sessionId":"sess-leak","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Should I proceed?"}]}}`,
		// affirmation answers the question but is skipped as a fold-in.
		`{"type":"user","sessionId":"sess-leak","message":{"role":"user","content":"yes"}}`,
		// fresh, unrelated request — must NOT inherit the consumed question.
		`{"type":"user","sessionId":"sess-leak","message":{"role":"user","content":"the second one"}}`,
	})

	var buf bytes.Buffer
	if err := runDialogue(&buf, root, "sess-leak", fmtText); err != nil {
		t.Fatalf("runDialogue: %v", err)
	}
	if out := buf.String(); strings.Contains(out, string(dialogue.MoveClarify)) {
		t.Errorf("skipped affirmation must clear pending question; no turn should be clarify\n%s", out)
	}
}

// TestRunDialogueShippedTellUpgradesUnknown is the ferret-bbp.21 join check: a
// session whose moves never signal accept or friction (unknown by the moves-only
// read) but whose final segment closes on a terminal-tracker call (bd_create)
// renders shipped(sh:bd_create) instead of a bare "outcome=unknown" — the
// 43ad3b27 evidence case (recall bug diagnosed, tx-0455 filed, clean exit, no
// accept-shaped human turn anywhere).
func TestRunDialogueShippedTellUpgradesUnknown(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-shipped.jsonl", []string{
		`{"type":"user","sessionId":"sess-shipped","message":{"role":"user","content":"reproduce the recall bug and file a tracking bead for it"}}`,
		`{"type":"assistant","sessionId":"sess-shipped","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"bd create --title recall-bug"}}]}}`,
	})

	var buf bytes.Buffer
	if err := runDialogue(&buf, root, "sess-shipped", fmtText); err != nil {
		t.Fatalf("runDialogue: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "outcome=shipped(sh:bd_create)") {
		t.Errorf("want outcome=shipped(sh:bd_create), got:\n%s", out)
	}
	if strings.Contains(out, "outcome=unknown") {
		t.Errorf("bare outcome=unknown must not survive the shipped-tell join\n%s", out)
	}
}

// TestRunDialogueShippedTellDoesNotUpgradeRepair guards the precedence rule
// (ferret-bbp.21): a session with a repair move outranks the shipped tell even
// when the final segment closes on bd_create — repair moves push the
// moves-outcome off Unknown (to abandoned/repair-heavy), so the gate on
// OutcomeUnknown structurally blocks the upgrade. A session that thrashed then
// filed a bead is not a clean ship.
func TestRunDialogueShippedTellDoesNotUpgradeRepair(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-repair.jsonl", []string{
		`{"type":"user","sessionId":"sess-repair","message":{"role":"user","content":"no, try again with the correct fix"}}`,
		`{"type":"assistant","sessionId":"sess-repair","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"bd create --title recall-bug"}}]}}`,
	})

	var buf bytes.Buffer
	if err := runDialogue(&buf, root, "sess-repair", fmtText); err != nil {
		t.Fatalf("runDialogue: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "shipped(") {
		t.Errorf("repair-carrying session must not be upgraded to shipped\n%s", out)
	}
}
