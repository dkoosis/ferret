package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type record struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TestReadLines_SkipsBlankLinesAndTrimsTrailingNewline: a ledger written with
// Append always ends each row in '\n'; ReadLines must return the row text
// without the terminator and must not emit a phantom empty final line.
func TestReadLines_SkipsBlankLinesAndTrimsTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	body := "{\"a\":1}\n\n{\"a\":2}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ReadLines(f)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	want := []string{`{"a":1}`, `{"a":2}`}
	if len(got) != len(want) {
		t.Fatalf("ReadLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestReadLines_CapturesUnterminatedFinalLine: a crash-torn append can leave the
// last row without a trailing newline. ReadLines must still surface it (the
// caller's parser decides whether it salvages or hard-errors) rather than
// silently drop it.
func TestReadLines_CapturesUnterminatedFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	body := `{"a":1}` + "\n" + `{"a":2` // torn mid-record, no trailing newline
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ReadLines(f)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(got) != 2 || got[1] != `{"a":2` {
		t.Fatalf("ReadLines = %v, want the torn trailing line captured", got)
	}
}

// TestReadLines_EmptyFile: an empty ledger file (freshly created, nothing
// appended yet) must read back as zero lines, not an error.
func TestReadLines_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ReadLines(f)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadLines(empty) = %v, want none", got)
	}
}

// TestAppendJSON_RoundTrip: a record written by AppendJSON must read back
// byte-identical through the standard JSON decoder — the durable-write spine
// every ledger caller depends on.
func TestAppendJSON_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	want := []record{{Name: "a", Count: 1}, {Name: "b", Count: 2}}
	for _, r := range want {
		if err := AppendJSON(path, r); err != nil {
			t.Fatalf("AppendJSON(%+v): %v", r, err)
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("wrote %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		var got record
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if got != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestAppendJSON_CreatesParentDir: a record can be recorded before any other
// component has materialised the data dir — AppendJSON must create the parent
// dir on first use, same as every caller's Append used to do by hand.
func TestAppendJSON_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "l.jsonl")
	if err := AppendJSON(path, record{Name: "x", Count: 1}); err != nil {
		t.Fatalf("AppendJSON into missing dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger file not created: %v", err)
	}
}

// TestAppendJSON_AppendsNotOverwrites: repeated calls must accumulate rows in
// order, never truncate — the ledger is append-only.
func TestAppendJSON_AppendsNotOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	for i := range 3 {
		if err := AppendJSON(path, record{Name: "r", Count: i}); err != nil {
			t.Fatalf("AppendJSON #%d: %v", i, err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines after 3 appends, want 3:\n%s", len(lines), b)
	}
}
