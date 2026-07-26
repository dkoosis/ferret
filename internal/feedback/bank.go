package feedback

// The pending-ask bank is the transient one-turn handoff between `judge` (the
// Stop-hook writer, on a Select disagreement) and `check` (the
// UserPromptSubmit reader, on the next human turn) — NOT the durable gold
// label store (that's the oz4/j33 ledger, D11: one home per fact). One file
// per session, at most one candidate at a time: judge overwrites, check
// consumes-and-deletes.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PendingFilePrefix names the per-session pending-ask file under the ferret
// data dir.
const pendingFilePrefix = "feedback-pending-"

// PendingPath returns the per-session pending-ask bank file path for a ferret
// data dir. Keyed by session (unlike the shared BudgetPath) because the bank
// holds at most one in-flight candidate per session, never merged across.
func PendingPath(dataDir, session string) string {
	return filepath.Join(dataDir, pendingFilePrefix+session+".json")
}

// SavePending publishes the one pending AskCandidate for a session,
// atomically (temp+fsync+rename — mirrors budget.go's writeState). No flock:
// NOT because writer/reader never overlap — with async:true a backgrounded
// judge CAN genuinely overlap a check call (or another judge, if two Stop
// hooks settle in quick succession), so "never concurrent" would be false.
// Safety instead comes from the rename itself being atomic: a reader
// (LoadPending) always sees either the old file or the new one whole, never
// a torn write, and if two SavePending calls race, the last rename simply
// wins. The worst case under overlap is one benign lost candidate (an
// earlier one silently overwritten before check ever read it) — acceptable
// for a budget-capped, at-most-a-few-asks-per-day feature; a flock would
// only guard against a harm this design already tolerates.
func SavePending(path string, c AskCandidate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	bw := bufio.NewWriter(tmp)
	b, merr := json.Marshal(c)
	if merr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return merr
	}
	if _, werr := bw.Write(b); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return werr
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// LoadPending reads the pending AskCandidate at path. A missing file reports
// (zero, false, nil) — no pending ask is the common case (most turns bank
// nothing), not an error. A corrupt file self-heals the same way budget.go's
// loadState does: warn to the package's stderr seam and treat it as absent,
// rather than hard-blocking the tap forever on one bad write — the worst case
// is one missed ask, never a crash loop.
func LoadPending(path string) (AskCandidate, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AskCandidate{}, false, nil
		}
		return AskCandidate{}, false, err
	}
	var c AskCandidate
	if err := json.Unmarshal(b, &c); err != nil {
		fmt.Fprintf(stderr, "feedback: %s: corrupt pending file treated as absent (%v)\n", path, err)
		return AskCandidate{}, false, nil
	}
	return c, true, nil
}

// ClearPending removes the pending file. An already-absent file is not an
// error: `check` consumes the bank one-shot either way (granted or denied by
// Reserve), so clearing must be idempotent against a file that was never
// written or was already cleared.
func ClearPending(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// armedFilePrefix names the per-session ARMED-ask file — the second hop in
// the relay, after `check` grants an ask (feedback_check.go). `judge` banks
// to PendingPath; `check` consumes-and-clears that pending bank one-shot AND
// arms this file, so the candidate survives the render turn; `answer`
// (ferret-j33), one turn later, consumes-and-clears THIS file. Two separate
// files, not one, because `judge` can overwrite the pending file with a NEW
// candidate on a later turn before `answer` has consumed the OLD armed one —
// conflating them would let a fresh candidate clobber an in-flight armed
// target.
const armedFilePrefix = "feedback-armed-"

// ArmedPath returns the per-session armed-ask file path for a ferret data
// dir. Mirrors PendingPath's naming/keying scheme.
func ArmedPath(dataDir, session string) string {
	return filepath.Join(dataDir, armedFilePrefix+session+".json")
}

// SaveArmed publishes the one armed AskCandidate for a session — the exact
// atomic temp+fsync+rename write SavePending performs (see its doc comment
// for the overlap-tolerance rationale, which applies identically here: the
// worst case under a race is one benign lost candidate).
func SaveArmed(path string, c AskCandidate) error {
	return SavePending(path, c)
}

// LoadArmed reads the armed AskCandidate at path — the same absent-is-not-
// an-error, corrupt-self-heals contract as LoadPending.
func LoadArmed(path string) (AskCandidate, bool, error) {
	return LoadPending(path)
}

// ClearArmed removes the armed file — the same idempotent-against-absent
// contract as ClearPending. `answer` calls this unconditionally (defer): the
// one-turn TTL is enforced entirely by "the very next UserPromptSubmit
// invocation consumes and deletes the armed record" — nothing more elaborate
// is needed.
func ClearArmed(path string) error {
	return ClearPending(path)
}
