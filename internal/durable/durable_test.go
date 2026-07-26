//go:build unix

package durable

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteTempRename_PublishesContent asserts the data lands at path and no
// temp artifact is left behind on success.
func TestWriteTempRename_PublishesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	want := []byte(`{"n":1}`)

	if err := WriteTempRename(path, want); err != nil {
		t.Fatalf("WriteTempRename: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("content = %q, want %q", got, want)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("leftover artifact after publish: %s", e.Name())
		}
	}
}

// TestWriteTempRename_CreatesParentDir asserts a first write into a not-yet-
// existent dir succeeds — callers rely on this to record before any ingest has
// materialised the data dir.
func TestWriteTempRename_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "nested", "state.json")
	if err := WriteTempRename(path, []byte("x")); err != nil {
		t.Fatalf("WriteTempRename into fresh dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

// TestWriteTempRename_Overwrites asserts a second publish replaces the first
// whole (read-modify-write state, not an append).
func TestWriteTempRename_Overwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteTempRename(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteTempRename(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
}

// TestSyncDir_Missing asserts a non-existent dir surfaces the open error rather
// than silently succeeding.
func TestSyncDir_Missing(t *testing.T) {
	if err := SyncDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("SyncDir on missing dir: want error, got nil")
	}
}

// TestSyncDir_Existing asserts fsyncing a real dir succeeds.
func TestSyncDir_Existing(t *testing.T) {
	if err := SyncDir(t.TempDir()); err != nil {
		t.Errorf("SyncDir on real dir: %v", err)
	}
}

// TestLock_MutualExclusion asserts a second acquire on the same sentinel blocks
// until the first releases — the core serialization guarantee callers depend on.
func TestLock_MutualExclusion(t *testing.T) {
	lock := filepath.Join(t.TempDir(), ".sentinel.lock")

	release1, err := Lock(lock)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	got := make(chan func(), 1)
	go func() {
		release2, err := Lock(lock)
		if err != nil {
			t.Errorf("second Lock: %v", err)
			got <- func() {}
			return
		}
		got <- release2
	}()

	select {
	case <-got:
		t.Fatal("second Lock acquired while first still held")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	release1()

	select {
	case release2 := <-got:
		release2()
	case <-time.After(2 * time.Second):
		t.Fatal("second Lock never acquired after release")
	}
}

// TestLock_ReleaseThenReacquire asserts release frees the lock for the next
// caller without a lingering hold.
func TestLock_ReleaseThenReacquire(t *testing.T) {
	lock := filepath.Join(t.TempDir(), ".sentinel.lock")
	release, err := Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	release()

	done := make(chan struct{})
	go func() {
		r, err := Lock(lock)
		if err != nil {
			t.Errorf("re-acquire: %v", err)
		} else {
			r()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-acquire blocked after release")
	}
}
