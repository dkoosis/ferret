package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// around already has a "window" JSON key (its --window step count); the
// stream window must not overwrite it.
func TestAroundWindow_JSON_KeepsStepCountBesideStreamWindow(t *testing.T) {
	data := writeWindowCorpus(t)
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Format, CLI.Around.Limit = data, fmtJSON, -1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", "bd_close", 2
	CLI.Around.Since = "2026-10-19"
	got := captureStdout(t, cmdAround)
	var m struct {
		Window       int `json:"window"`
		StreamWindow struct {
			Kept  int `json:"kept"`
			Total int `json:"total"`
		} `json:"streamWindow"`
	}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("around JSON: %v\n%s", err, got)
	}
	if m.Window != 2 {
		t.Errorf("window = %d, want the --window step count 2:\n%s", m.Window, got)
	}
	if m.StreamWindow.Kept != 2 || m.StreamWindow.Total != 4 {
		t.Errorf("streamWindow = %+v, want kept 2 of 4:\n%s", m.StreamWindow, got)
	}
}

// writeOldTokenCorpus writes two sessions: "old" (10-18) holds a token the
// "new" session (10-20) never uses.
func writeOldTokenCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := []struct {
		id      string
		day     int
		actions []string
	}{
		{"old", 18, []string{"git_status", "old_only_tool"}},
		{"new", 20, []string{"git_status", "bd_show"}},
	}
	for _, s := range sessions {
		for i, a := range s.actions {
			ev := event.Event{
				Seq: i + 1, Session: s.id, Kind: event.KindShell, Action: a, Status: event.StatusOK,
				Time: time.Date(2026, 10, s.day, 12, i, 0, 0, time.Local),
			}
			if err := w.Write(&ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A token seen only in dropped streams is gone from the windowed vocab, so
// around finds no match; the window must still be reported, or "0 matches"
// reads as "this token never occurs".
func TestAroundWindow_ZeroMatch_StillCarriesWindow(t *testing.T) {
	data := writeOldTokenCorpus(t)
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Limit = data, -1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", "old_only_tool", 1
	CLI.Around.Since = "2026-10-19"

	CLI.Around.Format = fmtText
	text := captureStdout(t, cmdAround)
	if !strings.Contains(text, "0 matches for old_only_tool\n") ||
		!strings.Contains(text, "window since=2026-10-19 kept=1 of 2 streams · dropped no-timestamp=0\n") {
		t.Errorf("zero-match text lacks the match note or the window line:\n%s", text)
	}

	CLI.Around.Format = fmtJSON
	var m struct {
		Matches      int `json:"matches"`
		StreamWindow struct {
			Since string `json:"since"`
			Kept  int    `json:"kept"`
			Total int    `json:"total"`
		} `json:"streamWindow"`
	}
	got := captureStdout(t, cmdAround)
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("around JSON: %v\n%s", err, got)
	}
	if m.Matches != 0 || m.StreamWindow.Since != "2026-10-19" || m.StreamWindow.Kept != 1 || m.StreamWindow.Total != 2 {
		t.Errorf("zero-match JSON = %+v, want matches 0 and streamWindow since 2026-10-19 kept 1 of 2:\n%s", m, got)
	}

	// Unwindowed, the token is found, so the zero-match path is the window's doing.
	CLI.Around.Since, CLI.Around.Format = "", fmtText
	if all := captureStdout(t, cmdAround); strings.Contains(all, "0 matches") {
		t.Errorf("unwindowed around should find old_only_tool:\n%s", all)
	}
}
