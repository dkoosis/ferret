package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// errBoomFlush is the static sentinel errWriter returns so the flush-error
// path is identifiable via errors.Is (and satisfies err113: no dynamic errors).
var errBoomFlush = errors.New("boom-flush")

// errWriter always fails on Write, forcing bufio.Writer.Flush to error.
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errBoomFlush }

func TestWriterCloseIdempotent(t *testing.T) {
	w, err := NewWriter(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close should be nil, got: %v", err)
	}
}

func TestWriterCloseJoinsFlushAndCloseError(t *testing.T) {
	// Real file we close early so w.f.Close() returns os.ErrClosed,
	// and a buf over a failing writer so Flush() errors. Close must
	// return both, joined.
	f, err := os.Create(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("pre-close: %v", err)
	}
	buf := bufio.NewWriter(errWriter{})
	w := &Writer{f: f, buf: buf, enc: json.NewEncoder(buf)}
	// Force buffered bytes so Flush actually attempts a write.
	_ = w.Write(&Event{})

	cerr := w.Close()
	if cerr == nil {
		t.Fatal("expected joined error, got nil")
	}
	if !containsErr(cerr.Error(), "boom-flush") {
		t.Errorf("expected flush error in %q", cerr)
	}
	if !errors.Is(cerr, os.ErrClosed) {
		t.Errorf("expected close error (os.ErrClosed) joined in %q", cerr)
	}
}

func containsErr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
