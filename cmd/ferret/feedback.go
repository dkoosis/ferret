package main

// Shared plumbing for the ferret feedback subcommands (prep/judge/check) — the
// live-join half of the in-session feedback tap (ferret-wf9.1). See
// internal/feedback for the pure ask-side selector this CLI layer orchestrates
// against a real kind:search retrieval event; see cmd/ferret/helped.go for the
// segment+join+lattice wiring this package mirrors.

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/dkoosis/ferret/internal/feedback"
)

// errFeedbackSessionRequired guards every feedback subcommand — none of them
// makes sense unscoped: the cursor, the pending bank, and the budget's
// per-session cap are all keyed by session.
var errFeedbackSessionRequired = errors.New("feedback: --session PREFIX required")

// nugTextCap bounds each candidate nug's body text sent to the relevance
// judge, via the shared truncateRunes helper (spine.go) — same mechanism
// overinitiative.go's overInitPromptCap uses. Nug bodies carry the actual
// content the judge grades (not just a prompt), so this cap is looser than
// overInitPromptCap's 600.
const nugTextCap = 2000

// retrievalLiveGlob is the live retrieval-event feed's monthly-rotated
// filename pattern under the events dir (Design: "Cursor/offset state for the
// retrieval-live JSONL tail") — the filename embeds YYYY-MM, so lexical order
// is chronological order.
const retrievalLiveGlob = "retrieval-live-*.jsonl"

// errNoRetrievalEvents means the events dir holds no retrieval-live file yet —
// a session before trixi's producer has ever fired.
var errNoRetrievalEvents = errors.New("feedback: no retrieval-live-*.jsonl found")

// defaultEventsDir returns ~/.trixi/telemetry, the trixi sidecar's live
// retrieval-event feed directory — the producer side of the trixi<->ferret
// contract (bead design findings; confirmed live: retrieval-live-2026-07.jsonl).
func defaultEventsDir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoHomeDir, err)
	}
	return filepath.Join(home, ".trixi", "telemetry"), nil
}

// resolveRetrievalEventsPath picks "the current file" out of dir: the
// monthly-rotated retrieval-live-YYYY-MM.jsonl files, lexically last. Sorting
// lexically is sound because the filename embeds YYYY-MM.
func resolveRetrievalEventsPath(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, retrievalLiveGlob))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: %s", errNoRetrievalEvents, dir)
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}

// resolveFeedbackEventsDir resolves the --events flag (a directory; empty =
// defaultEventsDir) — split from resolveRetrievalEventsPath so a caller that
// wants the resolved DIR (not yet the one file within it) can stop here.
func resolveFeedbackEventsDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return defaultEventsDir()
}

// resolveFeedbackEventsPath resolves --events all the way to one concrete
// retrieval-live file: the dir (default or explicit), then the lexically-last
// match within it.
func resolveFeedbackEventsPath(dir string) (string, error) {
	d, err := resolveFeedbackEventsDir(dir)
	if err != nil {
		return "", err
	}
	return resolveRetrievalEventsPath(d)
}

// cursorPath returns the per-session retrieval-tail cursor file path under a
// ferret data dir. Session-scoped like pendingBankPath (unlike the shared
// BudgetPath): each session tails the live feed independently.
func cursorPath(dataDir, session string) string {
	return filepath.Join(dataDir, "feedback-cursor-"+session+".json")
}

// pendingBankPath returns the per-session pending-ask bank file path — a thin
// alias over feedback.PendingPath so cmd/ferret's path helpers read as one
// family alongside cursorPath, without re-deriving the bank's filename scheme
// (single source of truth stays internal/feedback/bank.go).
func pendingBankPath(dataDir, session string) string {
	return feedback.PendingPath(dataDir, session)
}
