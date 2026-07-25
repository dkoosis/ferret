package fixes

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// hookSyncDir swaps the package syncDir seam for the duration of a test,
// recording every directory path it is asked to fsync, and restores the real
// implementation on cleanup. Mirrors internal/event's codec_atomic_test.go.
func hookSyncDir(t *testing.T) *[]string {
	t.Helper()
	var dirs []string
	orig := syncDir
	syncDir = func(dir string) error {
		dirs = append(dirs, dir)
		return orig(dir)
	}
	t.Cleanup(func() { syncDir = orig })
	return &dirs
}

// TestRecordSub_SyncsParentDir asserts writeSubs fsyncs the ledger's parent
// directory after the publish rename, so a just-recorded substitution rule
// survives a crash in the dirty-inode window.
func TestRecordSub_SyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SubFileName)
	dirs := hookSyncDir(t)

	if _, _, err := RecordSub(path, subFixture("symbol-callers", "rg", "snipe callers <sym>"), time.Now()); err != nil {
		t.Fatalf("RecordSub: %v", err)
	}

	if !slices.Contains(*dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd after rename, got dirs=%v", dir, *dirs)
	}
}

// TestAppend_SyncsParentDir asserts Append fsyncs the ledger's parent
// directory on first-create, so the new file's directory entry survives a
// crash before the dir inode flushes.
func TestAppend_SyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	dirs := hookSyncDir(t)

	if err := Append(path, Entry{Motif: "a,b", Fix: "x"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if !slices.Contains(*dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd on first-create, got dirs=%v", dir, *dirs)
	}
}
