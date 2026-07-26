package main

// cmdFeedbackCheck is the UserPromptSubmit side of the tap: read the banked
// AskCandidate (if any), spend the shared ask budget via feedback.Reserve, and
// print the granted/denied decision. Deliberately the ONLY feedback subcommand
// on the synchronous 30s-timeout hook path — no transcript parsing, no LLM
// call, just one small file read + one flock (Design: "The Stop-hook /
// UserPromptSubmit split").

import (
	"encoding/json"
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
	data, err := resolveFeedbackDataDir(cmd.Data)
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
// candidate, one-shot-consume the bank file (cleared whether the ask is
// granted or denied — a denied candidate is never retried; Reserve's own
// re-ask latch already prevents re-asking the same target, so leaving the file
// behind would only spend a Reserve call on it again next turn for no gain),
// then spend the budget and report the decision.
func runFeedbackCheck(reserve reserveAsk, budgetPath, pendingPath, session string, now time.Time) (checkResult, error) {
	cand, ok, err := feedback.LoadPending(pendingPath)
	if err != nil {
		return checkResult{}, err
	}
	if !ok {
		return checkResult{}, nil
	}

	clearErr := feedback.ClearPending(pendingPath)
	granted, rerr := reserve(budgetPath, session, cand.TargetRef, now)
	if rerr != nil {
		return checkResult{}, rerr
	}
	if clearErr != nil {
		return checkResult{}, clearErr
	}
	if !granted {
		return checkResult{}, nil
	}
	return checkResult{Ask: true, Question: cand.Question}, nil
}
