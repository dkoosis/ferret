package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errRemoveBoom is a static sentinel (err113: no dynamic errors in tests)
// returned by the hooked removeFile seam to force a temp-cleanup failure.
var errRemoveBoom = errors.New("boom-remove")

// hookRemoveFile swaps the package removeFile seam to return err and record
// every path it is asked to delete, restoring the real impl on cleanup.
func hookRemoveFile(t *testing.T, err error) *[]string {
	t.Helper()
	var calls []string
	orig := removeFile
	removeFile = func(p string) error {
		calls = append(calls, p)
		return err
	}
	t.Cleanup(func() { removeFile = orig })
	return &calls
}

// hookStderr swaps the package stderr seam to an in-memory buffer for the
// duration of fn and returns everything written, restoring the real stderr on
// cleanup. Unlike hijacking the process-global os.Stderr through a pipe, this
// is a plain pointer swap — no goroutine, no OS pipe, nothing global to race on.
func hookStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := stderr
	t.Cleanup(func() { stderr = orig })
	var buf bytes.Buffer
	stderr = &buf
	fn()
	return buf.String()
}

// TestWriterAbort_LogsCleanupFailure asserts Abort surfaces a temp-removal
// failure to stderr instead of silently swallowing it. A swallowed failure
// leaves an orphan events.jsonl.tmp that looks like an in-progress ingest and
// is undetectable by callers.
func TestWriterAbort_LogsCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	calls := hookRemoveFile(t, errRemoveBoom)

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	out := hookStderr(t, func() { w.Abort() })

	if len(*calls) != 1 || (*calls)[0] != path+".tmp" {
		t.Errorf("expected removeFile called once on %q, got %v", path+".tmp", *calls)
	}
	if !strings.Contains(out, "events.jsonl.tmp") || !strings.Contains(out, "boom-remove") {
		t.Errorf("expected cleanup failure logged to stderr, got %q", out)
	}
}

// TestWriterClose_LogsCleanupFailure asserts the failed-run branch of Close
// (finish() errored) surfaces a temp-removal failure to stderr too, rather
// than swallowing it under the returned finish error.
func TestWriterClose_LogsCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	calls := hookRemoveFile(t, errRemoveBoom)

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Force finish() to fail so Close takes the cleanup branch.
	w.buf = bufio.NewWriter(failWriter{})
	w.enc = json.NewEncoder(w.buf)
	_ = w.Write(&Event{}) // buffer bytes so Flush attempts a write

	var cerr error
	out := hookStderr(t, func() { cerr = w.Close() })

	if cerr == nil {
		t.Fatal("expected Close to fail on interrupted run, got nil")
	}
	if len(*calls) != 1 || (*calls)[0] != path+".tmp" {
		t.Errorf("expected removeFile called once on %q, got %v", path+".tmp", *calls)
	}
	if !strings.Contains(out, "events.jsonl.tmp") || !strings.Contains(out, "boom-remove") {
		t.Errorf("expected cleanup failure logged to stderr, got %q", out)
	}
}

// TestWriterAbort_SilentWhenTmpAlreadyGone asserts an already-absent tmp is the
// success case — nothing is orphaned, so no spurious failure is logged.
func TestWriterAbort_SilentWhenTmpAlreadyGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	hookRemoveFile(t, os.ErrNotExist)

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	out := hookStderr(t, func() { w.Abort() })

	if strings.Contains(out, "cleanup failed") {
		t.Errorf("expected no log for already-absent tmp, got %q", out)
	}
}
