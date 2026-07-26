package main

// cmdFeedbackAnswer is the answer-side of the in-session feedback tap
// (ferret-j33): the UserPromptSubmit call ONE turn after `check` granted an
// ask. It reads dk's raw prompt text off stdin, consumes the armed candidate
// `check` banked (feedback.LoadArmed), confirms the ask actually rendered in
// Claude's reply (the ask-rendered check — a session that died mid-turn must
// never label a question dk never saw), classifies the leading token
// (feedback.RecognizeAnswer), and writes the gold label to the ledger.
//
// No armed candidate is the common case (most turns aren't an answer turn) —
// a single missing-file read, no transcript walk, staying well inside
// `check`'s existing "no transcript parsing on the hot path" budget.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/feedback"
	"github.com/dkoosis/ferret/internal/label"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
)

// answerExcerptCap bounds the raw-prompt excerpt recorded for an
// unrecognized (Ignored) answer-turn label — enough for a human auditing the
// ledger to see what dk actually typed, without storing an unbounded turn.
const answerExcerptCap = 200

// cmdFeedbackAnswer wires the kong CLI flags to runFeedbackAnswer, reading
// the raw prompt text from stdin (not a flag — avoids shell-quoting hazards
// for arbitrary multi-line/quoted dk text, the exact escaping trap
// feedback-stop.sh already hit once with here-strings).
func cmdFeedbackAnswer() error {
	cmd := &CLI.Feedback.Answer
	if strings.TrimSpace(cmd.Session) == "" {
		return errFeedbackSessionRequired
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	promptBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	return runFeedbackAnswer(armedBankPath(data, cmd.Session), label.Path(data), root, cmd.Session, string(promptBytes))
}

// runFeedbackAnswer is feedback answer's testable core.
//
// No armed candidate → a pure no-op (no label, no error): the one-turn TTL
// means every UserPromptSubmit invocation but the one immediately after a
// granted ask finds nothing here.
//
// An armed candidate → the ask-rendered check: resolve the session's
// transcript (resolveSpineSource, the same helper judge/helped use) and
// confirm cand.Question landed in the LAST assistant turn's text. Absent →
// dk never saw the question (the session died mid-turn, or it didn't
// render) — no label is written at all, a stderr note logs why, and the
// armed state is still cleared (nothing to retry: the render either
// happened or it didn't). Present → feedback.RecognizeAnswer classifies the
// leading token: recognized → that valence + the stripped remainder;
// unrecognized → label.ValenceIgnored + a capped excerpt of the raw prompt
// (itself signal, never a failure).
//
// ClearArmed runs UNCONDITIONALLY via defer, registered immediately after a
// successful LoadArmed — this alone IS the one-turn TTL: whatever the very
// next UserPromptSubmit invocation contains, it consumes and deletes the
// armed record.
func runFeedbackAnswer(armedPath, labelPath, root, session, prompt string) error {
	cand, ok, err := feedback.LoadArmed(armedPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil // the common case: no armed ask this turn
	}
	defer func() {
		if cerr := feedback.ClearArmed(armedPath); cerr != nil {
			fmt.Fprintf(os.Stderr, "feedback answer: %s: clearing armed bank: %v\n", armedPath, cerr)
		}
	}()

	src, _, err := resolveSpineSource(root, session)
	if err != nil {
		return err
	}
	lastText, err := lastAssistantText(src.Path)
	if err != nil {
		return err
	}
	if !strings.Contains(lastText, cand.Question) {
		fmt.Fprintf(os.Stderr,
			"feedback answer: session %q: armed question not found in the prior assistant turn — dk may never have seen it; no label recorded\n",
			session)
		return nil
	}

	l := label.Label{
		Session:   session,
		Recorded:  time.Now(),
		TargetRef: cand.TargetRef,
		Question:  cand.Question,
	}
	if valence, remainder, recognized := feedback.RecognizeAnswer(prompt); recognized {
		l.Valence = valence
		l.Text = remainder
	} else {
		l.Valence = label.ValenceIgnored
		l.Text = truncateRunes(strings.TrimSpace(prompt), answerExcerptCap)
	}
	return label.Append(labelPath, l)
}

// lastAssistantText walks path once and returns the text of the LAST
// assistant turn — the ask-rendered check's input. Mirrors
// buildProbeAdjustments' identical tracking (feedback_probe.go), kept
// separate: that walk also threads label matching per line, this one only
// needs the final value after a full pass.
func lastAssistantText(path string) (string, error) {
	var last string
	err := transcript.ReadLines(path, func(line []byte) error {
		raw, ok := decodeRaw(line)
		if !ok || raw.IsMeta || raw.Message == nil || raw.Type != roleAssistant {
			return nil // tolerant: a bad/irrelevant line is skipped, not fatal
		}
		if text := turn.PromptText(raw.Message.Content); text != "" {
			last = text
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return last, nil
}
