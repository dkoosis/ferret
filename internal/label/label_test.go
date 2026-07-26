package label

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookSyncDir swaps the package syncDir seam for the duration of a test,
// recording every directory path it is asked to fsync, and restores the real
// implementation on cleanup. Mirrors internal/fixes' sync_test.go.
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

// TestAppendLoadRoundTrip: a label written through Append must come back from
// Load with every field intact and in append order — including an ignored row
// (a non-answer is signal) and a say-more Text. This is the ledger's core
// contract: a harvested label has to survive to the calibration read or the
// feedback loop never closes.
func TestAppendLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	at := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	want := []Label{
		{Session: "s1", Recorded: at, TargetRef: "seg:42", Question: "get_nug retry worth it?", Valence: ValenceYes},
		{Session: "s1", Recorded: at.Add(time.Minute), TargetRef: "seg:57", Question: "that search help?", Valence: ValenceNo, Text: "wrong package entirely"},
		{Session: "s1", Recorded: at.Add(2 * time.Minute), TargetRef: "seg:61", Question: "worth it?", Valence: ValenceIgnored},
	}
	for _, l := range want {
		if err := Append(path, l); err != nil {
			t.Fatalf("Append(%q): %v", l.TargetRef, err)
		}
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load returned %d labels, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Session != want[i].Session || got[i].TargetRef != want[i].TargetRef ||
			got[i].Question != want[i].Question || got[i].Valence != want[i].Valence ||
			got[i].Text != want[i].Text || !got[i].Recorded.Equal(want[i].Recorded) {
			t.Errorf("label %d round-trip mismatch:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// TestAppendRejectsInvalidValence: every label must carry an explicit,
// recognized verdict. A blank or garbage valence is a writer bug — reject it
// before any write so a silent bad row never pollutes the gold-label set.
func TestAppendRejectsInvalidValence(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	for _, bad := range []string{"", "maybe", "YES", "1"} {
		if err := Append(path, Label{Session: "s", TargetRef: "seg:1", Valence: bad}); err == nil {
			t.Errorf("Append with valence %q must error", bad)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a rejected append must not create the ledger file")
	}
}

// TestAppendCreatesLedgerDir: Append must create the data dir on first use, so a
// label can be recorded before any ingest has materialised ~/.ferret.
func TestAppendCreatesLedgerDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	if err := Append(path, Label{Session: "s", TargetRef: "seg:1", Valence: ValenceYes}); err != nil {
		t.Fatalf("Append into missing dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger not created: %v", err)
	}
}

// TestAppendSyncsParentDir asserts Append fsyncs the ledger's parent directory
// on first-create, so the new file's directory entry survives a crash before the
// dir inode flushes.
func TestAppendSyncsParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	dirs := hookSyncDir(t)

	if err := Append(path, Label{Session: "s", TargetRef: "seg:1", Valence: ValenceYes}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !slices.Contains(*dirs, dir) {
		t.Errorf("expected parent dir %q fsync'd on first-create, got dirs=%v", dir, *dirs)
	}
}

// TestLoadMissingLedgerIsEmpty: a missing ledger is an empty ledger, not an
// error — a calibration reader must join unconditionally before any ask has been
// answered.
func TestLoadMissingLedgerIsEmpty(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("Load(missing) must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load(missing) = %d labels, want 0", len(got))
	}
}

// TestLoadCorruptMidLineErrors: a malformed line with valid labels AFTER it is
// genuine mid-ledger corruption — a hard error, not a silent skip. A corrupt
// ledger that joined as "no labels" would erase harvested ground truth silently.
func TestLoadCorruptMidLineErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := `{"session":"s","ts":"2026-07-25T15:00:00Z","target_ref":"seg:1","valence":"yes"}
{not json}
{"session":"s","ts":"2026-07-25T15:01:00Z","target_ref":"seg:2","valence":"no"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("corrupt mid-ledger line must surface an error, not parse as partial")
	}
}

// TestLoadSalvagesCorruptTrailingLine: a single corrupt TRAILING line (a torn
// append) is dropped with a stderr warning while every prior label still loads —
// one bad append must not brick all future ledger reads.
func TestLoadSalvagesCorruptTrailingLine(t *testing.T) {
	var buf strings.Builder
	old := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = old })

	path := filepath.Join(t.TempDir(), FileName)
	body := `{"session":"s","ts":"2026-07-25T15:00:00Z","target_ref":"seg:1","valence":"yes"}
{"session":"s","ts":"2026-07-25T15:01:00Z","target_ref":` // truncated mid-record, no newline
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load salvageable ledger: %v", err)
	}
	if len(got) != 1 || got[0].TargetRef != "seg:1" {
		t.Errorf("expected the one prior label to survive, got %+v", got)
	}
	if !strings.Contains(buf.String(), "corrupt trailing ledger line dropped") {
		t.Errorf("expected a salvage warning on stderr, got %q", buf.String())
	}
}

// TestConcurrentAppendNoTornLines: the flock must serialize concurrent appends
// so every line stays a whole, parseable label — the write-safety guarantee the
// answer-side relies on when a label lands while another session is also writing.
func TestConcurrentAppendNoTornLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	const n = 20
	var wg sync.WaitGroup
	at := time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := Label{Session: "s", Recorded: at, TargetRef: "seg:" + string(rune('A'+i%26)), Valence: ValenceYes}
			if err := Append(path, l); err != nil {
				t.Errorf("concurrent Append: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load after concurrent appends: %v", err)
	}
	if len(got) != n {
		t.Errorf("expected %d intact labels after concurrent appends, got %d", n, len(got))
	}
}
