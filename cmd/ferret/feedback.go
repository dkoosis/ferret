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
// judge, via the shared truncateRunes helper (spine.go). Sized against
// spineTextCap (4000, spine.go) — the "a judge/reader needs to see a whole
// prose block" precedent — not overInitPromptCap (600, overinitiative.go),
// which only needs enough of an opening prompt to identify its scope. Real
// nug bodies observed in this bead's Step 18 dry run ran 4.6K-25K chars; a
// smaller cap risks truncating away the exact passage that would have made a
// long-but-relevant nug grade correctly, systematically under-grading long
// nugs as mismatch for reasons that have nothing to do with retrieval
// quality — a measurement bug, not just a cost saver. 4000 trades some
// per-call token cost (candidates run through the relevance judge on every
// settled search, not just ones that end up firing an ask) for not silently
// biasing SearchFit against long-but-good nugs; revisit if judge cost proves
// to matter more in practice than this bias risk.
const nugTextCap = 4000

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

// resolveFeedbackEventsPath resolves the --events flag (a directory; empty =
// defaultEventsDir) to one concrete retrieval-live file: the dir (default or
// explicit), then the lexically-last match within it.
func resolveFeedbackEventsPath(dir string) (string, error) {
	if dir == "" {
		d, err := defaultEventsDir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	return resolveRetrievalEventsPath(dir)
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

// armedBankPath returns the per-session armed-ask bank file path — a thin
// alias over feedback.ArmedPath, mirroring pendingBankPath (ferret-j33).
func armedBankPath(dataDir, session string) string {
	return feedback.ArmedPath(dataDir, session)
}
