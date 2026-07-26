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
// this file has exactly one writer (`judge`, on a Select disagreement) and
// exactly one reader-and-deleter (`check`, on the next UserPromptSubmit),
// never concurrent within a session — PendingPath is keyed by session, so two
// sessions never share a file, and within one session the Stop hook and the
// UserPromptSubmit hook never run at the same instant (they gate opposite
// ends of a turn boundary).
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
