package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// writeShellMixCorpus writes the ferret-2mf fixture. Session s19 starts on
// 10-19: one three-segment compound (call c1), one bare Bash call (c2), and two
// native calls (Read, Grep). Session s18 starts on 10-18 with one Bash call.
// Session old predates ids: its rows carry none.
func writeShellMixCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	d19 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.Local)
	d18 := d19.AddDate(0, 0, -1)
	sh := func(sess, id string, seq, out int, at time.Time) event.Event {
		return event.Event{Seq: seq, Time: at, Session: sess, ToolUseID: id, Kind: event.KindShell,
			Action: "git", Status: event.StatusOK, OutBytes: out}
	}
	tool := func(name string, seq int) event.Event {
		return event.Event{Seq: seq, Time: d19, Session: "s19", Kind: event.KindTool, Action: name, Status: event.StatusOK}
	}
	rows := []event.Event{
		sh("s19", "c1", 1, 40, d19), sh("s19", "c1", 1, 30, d19), sh("s19", "c1", 1, 30, d19),
		sh("s19", "c2", 2, 100, d19),
		tool("Read", 3), tool("Grep", 4),
		sh("s18", "c3", 1, 500, d18),
	}
	for i := range rows {
		if err := w.Write(&rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runSummary(t *testing.T, data, format, since, until string) (string, error) {
	t.Helper()
	saved := CLI.Summary
	t.Cleanup(func() { CLI.Summary = saved })
	CLI.Summary.Data, CLI.Summary.Format, CLI.Summary.Limit = data, format, -1
	CLI.Summary.By = "corpus"
	CLI.Summary.Since, CLI.Summary.Until = since, until
	var err error
	// captureStdout fails the test on a command error; this one wants to read it.
	got := captureStdout(t, func() error { err = cmdSummary(); return nil })
	return got, err
}

func TestSummary_WindowPrintsShellLine(t *testing.T) {
	data := writeShellMixCorpus(t)
	got, err := runSummary(t, data, fmtText, "2026-10-19", "")
	if err != nil {
		t.Fatal(err)
	}
	// c1 counts once though it has three segments: 2 Bash calls, not 4.
	want := "shell bash_calls=2 native_calls=2 ratio=1.00 bash_bytes_per_session=200 · bash_bytes=200 sessions=1\n"
	if !strings.Contains(got, want) {
		t.Errorf("output lacks %q:\n%s", want, got)
	}
	if !strings.Contains(got, "summary by=corpus buckets=1 since=2026-10-19 streams=1/2 untimed=0") {
		t.Errorf("windowed output lacks the window line:\n%s", got)
	}
}

func TestSummary_NoWindowKeepsBucketsAndAddsShellLine(t *testing.T) {
	data := writeShellMixCorpus(t)
	got, err := runSummary(t, data, fmtText, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"summary by=corpus buckets=1\n",
		"7 ev     2 sess fail=0.0%",
		"shell bash_calls=3 native_calls=2 ratio=1.50 bash_bytes_per_session=350 · bash_bytes=700 sessions=2\n",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "since=") || strings.Contains(got, "streams=") {
		t.Errorf("unwindowed output carries a window tag:\n%s", got)
	}
}

func TestSummary_JSONCarriesShellMix(t *testing.T) {
	data := writeShellMixCorpus(t)
	got, err := runSummary(t, data, fmtJSON, "2026-10-19", "")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		BashCalls           *int     `json:"bashCalls"`
		NativeCalls         *int     `json:"nativeCalls"`
		Ratio               *float64 `json:"ratio"`
		BashBytesPerSession *float64 `json:"bashBytesPerSession"`
		Window              *struct {
			Kept  int `json:"streams"`
			Total int `json:"total"`
		} `json:"dateWindow"`
	}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if m.BashCalls == nil || *m.BashCalls != 2 || m.NativeCalls == nil || *m.NativeCalls != 2 ||
		m.Ratio == nil || *m.Ratio != 1 || m.BashBytesPerSession == nil || *m.BashBytesPerSession != 200 {
		t.Errorf("shell mix keys wrong in %s", got)
	}
	if m.Window == nil || m.Window.Kept != 1 || m.Window.Total != 2 {
		t.Errorf("dateWindow = %+v, want 1 of 2 streams", m.Window)
	}

	plain, err := runSummary(t, data, fmtJSON, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "dateWindow") {
		t.Errorf("unwindowed JSON carries a window key: %s", plain)
	}
	if !strings.Contains(plain, `"buckets"`) || !strings.Contains(plain, `"bashCalls": 3`) {
		t.Errorf("unwindowed JSON lost its buckets or the mix: %s", plain)
	}
}

// Events ingested before ferret-2mf carry no id; the header says so.
func TestSummary_LegacyEventsAreMarked(t *testing.T) {
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.Local)
	rows := []event.Event{
		{Seq: 1, Time: at, Session: "s", Kind: event.KindShell, Action: "a", OutBytes: 4},
		{Seq: 1, Time: at, Session: "s", Kind: event.KindShell, Action: "b", OutBytes: 4},
		{Seq: 2, Time: at, Session: "s", Kind: event.KindShell, Action: "c", OutBytes: 4},
	}
	for i := range rows {
		if err := w.Write(&rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := runSummary(t, dir, fmtText, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"bash_calls=2 native_calls=0 ratio=n/a", "2 of 2 bash calls have no tool_use id"} {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func TestSummary_BadDateIsUsageErrorNamingTheForm(t *testing.T) {
	data := writeShellMixCorpus(t)
	for _, tt := range []struct{ name, since, until string }{
		{"since", "yesterday", ""},
		{"until", "", "2026-13-40"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runSummary(t, data, fmtText, tt.since, tt.until)
			if err == nil {
				t.Fatal("want an error")
			}
			if code := exitCodeFor(err); code != exitUsage {
				t.Errorf("exit code = %d, want usage %d (err: %v)", code, exitUsage, err)
			}
			if !strings.Contains(err.Error(), "YYYY-MM-DD") {
				t.Errorf("error %q does not name YYYY-MM-DD", err)
			}
		})
	}
}
