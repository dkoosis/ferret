package feedback

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/dkoosis/ferret/internal/durable"
)

// The feedback tap must never nag. Two caps and a latch bound how often it
// interrupts dk, and — because dk runs many sessions at once — the daily cap is
// SHARED state a per-session counter can't provide: 2 asks/session across 5
// parallel sessions would be 10 asks/day, exactly the pestering the tap exists
// to avoid.
const (
	// SessionCap is the most asks a single session may spend.
	SessionCap = 2
	// DailyCap is the most asks all concurrent sessions may spend in a day —
	// the ceiling that actually governs the felt frequency.
	DailyCap = 3
)

const dateLayout = "2006-01-02"

// stderr is the diagnostic-log seam (mirrors internal/label). A corrupt budget
// file self-heals to empty with a warning here rather than hard-blocking asks
// forever — the budget is transient rate-limit scratch, not durable data.
var stderr io.Writer = os.Stderr

// syncDir fsyncs a directory so a publish rename is crash-durable (see
// durable.SyncDir). Package var so a test can observe the write path invokes it.
var syncDir = durable.SyncDir

// BudgetFileName is the shared budget state's basename under the ferret data dir.
const BudgetFileName = "feedback-budget.json"

// BudgetPath returns the shared budget state path for a ferret data dir.
func BudgetPath(dataDir string) string { return filepath.Join(dataDir, BudgetFileName) }

// budgetState is the shared ask-budget, rewritten atomically under an flock. The
// counts reset when the date rolls; Asked is the re-ask latch (a target already
// asked is never asked again, even under cap, even from another session).
type budgetState struct {
	Date       string         `json:"date"`
	Daily      int            `json:"daily"`
	PerSession map[string]int `json:"per_session"`
	Asked      []string       `json:"asked"`
}

// Reserve atomically decides whether the tap may spend an ask on target from
// session right now, and — if granted — records the spend so a concurrent
// session sees it. It grants iff the target has not already been asked, the day
// is under DailyCap, and the session is under SessionCap; otherwise it refuses
// without mutating state. Refusal is the common case and is not an error.
//
// The whole read-check-write runs under one flock so two concurrent sessions
// cannot both read "2 spent" and each grant a third — the exact race a shared
// cap exists to close.
func Reserve(path, session, target string, now time.Time) (granted bool, err error) {
	release, lerr := lockBudget(path)
	if lerr != nil {
		return false, lerr
	}
	defer release()

	st := loadState(path)
	today := now.UTC().Format(dateLayout)
	if st.Date != today {
		st = budgetState{Date: today, PerSession: map[string]int{}}
	}

	switch {
	case slices.Contains(st.Asked, target): // latch: never re-ask a target
		return false, nil
	case st.Daily >= DailyCap:
		return false, nil
	case st.PerSession[session] >= SessionCap:
		return false, nil
	}

	st.Daily++
	st.PerSession[session]++
	st.Asked = append(st.Asked, target)
	if err := writeState(path, st); err != nil {
		return false, err
	}
	return true, nil
}

// loadState reads the budget file. A missing file is empty state (the first ask
// of the tap's life). A corrupt file also reads as empty — with a warning —
// rather than hard-erroring: a permanently-unparseable rate-limit file must not
// block every future ask, and the next grant rewrites it clean. This is safe
// because the worst case is a few extra asks after corruption, never lost gold
// data (the labels live in the durable ledger, not here).
func loadState(path string) budgetState {
	b, err := os.ReadFile(path)
	if err != nil {
		return budgetState{PerSession: map[string]int{}} // missing == empty
	}
	var st budgetState
	if err := json.Unmarshal(b, &st); err != nil {
		fmt.Fprintf(stderr, "feedback: %s: corrupt budget file reset to empty (%v)\n", path, err)
		return budgetState{PerSession: map[string]int{}}
	}
	if st.PerSession == nil {
		st.PerSession = map[string]int{}
	}
	return st
}

// writeState publishes the budget atomically: durable.WriteTempRename writes a
// unique temp, fsyncs it, and renames it over the target; syncDir then fsyncs the
// parent dir so the rename is crash-durable. An atomic rewrite, not an append,
// because the budget is read-modify-write state, not a log.
func writeState(path string, st budgetState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := durable.WriteTempRename(path, b); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
