package main

import (
	"errors"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/feedback"
)

// errCheckReserveBoom is a static sentinel for the faked Reserve failure
// (err113: no dynamic errors).
var errCheckReserveBoom = errors.New("boom")

var checkNow = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

// TestRunFeedbackCheck_NoPendingFile: nothing banked → {"ask":false}, no
// Reserve call spent (nothing to spend a budget slot on).
func TestRunFeedbackCheck_NoPendingFile(t *testing.T) {
	dir := t.TempDir()
	called := false
	reserve := func(path, session, target string, now time.Time) (bool, error) {
		called = true
		return true, nil
	}
	got, err := runFeedbackCheck(reserve, feedback.BudgetPath(dir), feedback.PendingPath(dir, "s1"), "s1", checkNow)
	if err != nil {
		t.Fatalf("runFeedbackCheck: %v", err)
	}
	if got.Ask {
		t.Errorf("no pending file must yield ask=false, got %+v", got)
	}
	if called {
		t.Error("Reserve must not be called when there is nothing banked")
	}
}

// TestRunFeedbackCheck_GrantedClearsAndReturnsQuestion: a banked candidate +
// a granting Reserve yields {"ask":true,"question":...} and the bank file is
// gone afterward (one-shot consumption).
func TestRunFeedbackCheck_GrantedClearsAndReturnsQuestion(t *testing.T) {
	dir := t.TempDir()
	pendingPath := feedback.PendingPath(dir, "s1")
	cand := feedback.AskCandidate{TargetRef: "evt-1", Question: "did it help? [y/n]"}
	if err := feedback.SavePending(pendingPath, cand); err != nil {
		t.Fatal(err)
	}
	reserve := func(path, session, target string, now time.Time) (bool, error) {
		if target != "evt-1" || session != "s1" {
			t.Errorf("Reserve called with session=%q target=%q, want s1/evt-1", session, target)
		}
		return true, nil
	}
	got, err := runFeedbackCheck(reserve, feedback.BudgetPath(dir), pendingPath, "s1", checkNow)
	if err != nil {
		t.Fatalf("runFeedbackCheck: %v", err)
	}
	if !got.Ask || got.Question != cand.Question {
		t.Errorf("got %+v, want ask=true with the banked question", got)
	}
	if _, ok, _ := feedback.LoadPending(pendingPath); ok {
		t.Error("bank file must be gone after a granted check")
	}
}

// TestRunFeedbackCheck_DeniedStillClears: a banked candidate + a denying
// Reserve yields {"ask":false}, and the bank file is STILL gone afterward —
// one-shot consumption regardless of the budget verdict.
func TestRunFeedbackCheck_DeniedStillClears(t *testing.T) {
	dir := t.TempDir()
	pendingPath := feedback.PendingPath(dir, "s1")
	if err := feedback.SavePending(pendingPath, feedback.AskCandidate{TargetRef: "evt-1", Question: "q"}); err != nil {
		t.Fatal(err)
	}
	reserve := func(path, session, target string, now time.Time) (bool, error) { return false, nil }
	got, err := runFeedbackCheck(reserve, feedback.BudgetPath(dir), pendingPath, "s1", checkNow)
	if err != nil {
		t.Fatalf("runFeedbackCheck: %v", err)
	}
	if got.Ask {
		t.Errorf("a denied Reserve must yield ask=false, got %+v", got)
	}
	if _, ok, _ := feedback.LoadPending(pendingPath); ok {
		t.Error("bank file must be gone even when the ask is denied")
	}
}

// TestRunFeedbackCheck_ReserveErrorLeavesBankForRetry: when Reserve itself
// errors (e.g. a transient I/O failure on the budget file), the bank file
// must NOT be cleared — clearing before a confirmed decision would lose the
// candidate permanently with the budget never even recorded. Reserve runs
// before the clear specifically so this failure is retryable, not silent
// data loss (self-review finding).
func TestRunFeedbackCheck_ReserveErrorLeavesBankForRetry(t *testing.T) {
	dir := t.TempDir()
	pendingPath := feedback.PendingPath(dir, "s1")
	if err := feedback.SavePending(pendingPath, feedback.AskCandidate{TargetRef: "evt-1", Question: "q"}); err != nil {
		t.Fatal(err)
	}
	reserve := func(path, session, target string, now time.Time) (bool, error) {
		return false, errCheckReserveBoom
	}
	_, err := runFeedbackCheck(reserve, feedback.BudgetPath(dir), pendingPath, "s1", checkNow)
	if err == nil {
		t.Fatal("a Reserve error must surface, not be swallowed")
	}
	if _, ok, loadErr := feedback.LoadPending(pendingPath); loadErr != nil || !ok {
		t.Errorf("bank file must survive a Reserve error for a future retry, ok=%v err=%v", ok, loadErr)
	}
}

// TestCursorAndPendingPathsDoNotCollide: sanity that the two per-session
// scratch files (cursor, bank) live at distinct paths under the same data dir
// — a naming collision would have one silently clobber the other.
func TestCursorAndPendingPathsDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	if cursorPath(dir, "s1") == pendingBankPath(dir, "s1") {
		t.Error("cursor and pending-bank paths must not collide")
	}
}
