package event

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errCallback is a static sentinel (err113: no dynamic errors in tests) used to
// assert the per-event callback's error propagates out of Read unchanged.
var errCallback = errors.New("callback-boom")

// writeArtifact lays down a raw events.jsonl body verbatim so tests can craft
// truncated or corrupt tails the Writer would never produce on its own.
func writeArtifact(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestReadToleratesTruncatedTrailingRecord is the acceptance criterion: N valid
// records followed by a truncated final object must yield exactly N events with
// no error, the truncated fragment dropped rather than poisoning the read.
func TestReadToleratesTruncatedTrailingRecord(t *testing.T) {
	const n = 3
	body := strings.Repeat(`{"i":1,"p":"proj","s":"sess","k":"tool","act":"Read"}`+"\n", n)
	// Append a trailing object cut off mid-write — no closing brace, no newline.
	body += `{"i":4,"p":"proj","s":"sess","k":"tool","act":"Wri`
	path := writeArtifact(t, body)

	var got int
	if err := Read(path, func(*Event) error { got++; return nil }); err != nil {
		t.Fatalf("Read returned error on truncated trailing record: %v", err)
	}
	if got != n {
		t.Fatalf("Read yielded %d events, want %d (truncated tail must be dropped)", got, n)
	}
}

// TestReadCleanArtifact guards the happy path: a well-formed artifact yields
// every record with no spurious drop.
func TestReadCleanArtifact(t *testing.T) {
	const n = 3
	body := strings.Repeat(`{"i":1,"p":"proj","s":"sess","k":"tool","act":"Read"}`+"\n", n)
	path := writeArtifact(t, body)

	var got int
	if err := Read(path, func(*Event) error { got++; return nil }); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != n {
		t.Fatalf("Read yielded %d events, want %d", got, n)
	}
}

// TestReadRejectsMidStreamCorruption asserts only the FINAL record is salvaged:
// a malformed object with valid records still following is a genuine corruption
// and must hard-error, not silently truncate the stream.
func TestReadRejectsMidStreamCorruption(t *testing.T) {
	body := `{"i":1,"p":"proj","s":"sess","k":"tool","act":"Read"}` + "\n"
	body += `{"i":2,` + "\n" // malformed object in the middle...
	body += `{"i":3,"p":"proj","s":"sess","k":"tool","act":"Edit"}` + "\n"
	path := writeArtifact(t, body)

	err := Read(path, func(*Event) error { return nil })
	if err == nil {
		t.Fatal("Read accepted mid-stream corruption; want hard error")
	}
}

// TestReadPropagatesCallbackError confirms a caller's own error short-circuits
// the read and surfaces unchanged (not masked as a truncation).
func TestReadPropagatesCallbackError(t *testing.T) {
	body := `{"i":1,"p":"proj","s":"sess","k":"tool","act":"Read"}` + "\n"
	path := writeArtifact(t, body)

	err := Read(path, func(*Event) error { return errCallback })
	if !errors.Is(err, errCallback) {
		t.Fatalf("Read err = %v, want %v", err, errCallback)
	}
}
