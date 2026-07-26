//go:build unix

package feedback

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestLockSerializesConcurrentWriters: Lock is the guard feedback prep's cursor
// read-modify-write leans on (ferret-efc). Each goroutine reads a counter from a
// FILE, increments it, and writes it back — all under the same lock. The shared
// state is on disk (invisible to the race detector, like Reserve's budget file),
// so a wrong final total is unambiguous evidence of a lost update, i.e. that Lock
// failed to serialize the critical sections.
func TestLockSerializesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "x.lock")
	counterPath := filepath.Join(dir, "counter")
	if err := os.WriteFile(counterPath, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	const goroutines, perGoroutine = 8, 50

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				release, err := Lock(lockPath)
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				b, _ := os.ReadFile(counterPath)
				n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				_ = os.WriteFile(counterPath, []byte(strconv.Itoa(n+1)), 0o644)
				release()
			}
		})
	}
	wg.Wait()

	b, _ := os.ReadFile(counterPath)
	got, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if want := goroutines * perGoroutine; got != want {
		t.Errorf("counter = %d, want %d — a lost update means Lock did not serialize", got, want)
	}
}
