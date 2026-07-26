package main

// cmdFeedbackCheck is the UserPromptSubmit side of the tap: read the banked
// AskCandidate (if any), spend the shared ask budget via feedback.Reserve, and
// print the granted/denied decision. Deliberately the ONLY feedback subcommand
// on the synchronous 30s-timeout hook path — no transcript parsing, no LLM
// call, just one small file read + one flock (Design: "The Stop-hook /
// UserPromptSubmit split").

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/feedback"
)

// reserveAsk is the seam runFeedbackCheck calls — feedback.Reserve in
// production, a fake in tests (mirrors cmd/ferret's newEventWriter/hop1Judge
// injection convention) so granted/denied are exercisable without racing the
// real shared budget file.
type reserveAsk func(path, session, target string, now time.Time) (bool, error)

// checkResult is feedback check's JSON stdout contract — the hand-off to the
// bash UserPromptSubmit-hook script, which wraps a granted ask into
// hookSpecificOutput.additionalContext via jq.
type checkResult struct {
	Ask      bool   `json:"ask"`
	Question string `json:"question,omitempty"`
}

// cmdFeedbackCheck wires the kong CLI flags to runFeedbackCheck and prints its
// JSON decision.
func cmdFeedbackCheck() error {
	cmd := &CLI.Feedback.Check
	if strings.TrimSpace(cmd.Session) == "" {
		return errFeedbackSessionRequired
	}
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	res, err := runFeedbackCheck(feedback.Reserve, feedback.BudgetPath(data),
		pendingBankPath(data, cmd.Session), cmd.Session, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

// runFeedbackCheck is feedback check's testable core: load the pending
// candidate, spend the budget, THEN one-shot-consume the bank file (cleared
// whether the ask is granted or denied — a denied candidate is never
// retried; Reserve's own re-ask latch already prevents re-asking the same
// target, so leaving the file behind would only spend a Reserve call on it
// again next turn for no gain).
//
// Reserve runs BEFORE the clear, deliberately: Reserve is the authoritative,
// budget-recording decision, and it must not be discarded by a later I/O
// failure. If ClearPending itself then fails, that is logged but does NOT
// invalidate an already-computed, already-recorded decision — the bank file
// is left behind as harmless orphaned scratch (the next check call would
// just find it again, call Reserve again for the same now-latched target,
// get a clean "already asked" denial, and retry the clear). The reverse
// order (clear-then-reserve) was tried first and rejected: a Reserve error
// AFTER the file is already gone loses the candidate permanently, with the
// budget never even recorded — strictly worse than a self-healing stale
// file.
func runFeedbackCheck(reserve reserveAsk, budgetPath, pendingPath, session string, now time.Time) (checkResult, error) {
	cand, ok, err := feedback.LoadPending(pendingPath)
	if err != nil {
		return checkResult{}, err
	}
	if !ok {
		return checkResult{}, nil
	}

	granted, rerr := reserve(budgetPath, session, cand.TargetRef, now)
	if rerr != nil {
		return checkResult{}, rerr
	}
	if err := feedback.ClearPending(pendingPath); err != nil {
		// Non-fatal: the decision above is already correct (and, if granted,
		// already recorded in the budget) — a failed clear must not discard it.
		fmt.Fprintf(os.Stderr, "feedback check: %s: clearing pending bank: %v\n", pendingPath, err)
	}
	if !granted {
		return checkResult{}, nil
	}
	return checkResult{Ask: true, Question: cand.Question}, nil
}
