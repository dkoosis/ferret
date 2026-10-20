package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// writeWindowCorpus writes four sessions that run the same bd/git routine but
// start on different days: s1 on 10-18, s2 on 10-19, s3 on 10-20, and s4 with
// no timestamps at all.
func writeWindowCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	routine := []string{"git_status", "bd_show", "bd_close", "git_commit", "git_push"}
	start := map[string]time.Time{
		"s1": time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local),
		"s2": time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local),
		"s3": time.Date(2026, 10, 20, 23, 30, 0, 0, time.Local), // late on the until day
	}
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		for i, a := range routine {
			ev := event.Event{Seq: i + 1, Session: s, Kind: event.KindShell, Action: a, Status: event.StatusOK}
			if st, ok := start[s]; ok {
				ev.Time = st.Add(time.Duration(i) * time.Minute)
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

// ngramsWindow runs `ferret ngrams --lens cmd --contains bd_close` with a
// window and returns what it printed, or the command's error.
func ngramsWindow(t *testing.T, data, format, since, until string) (string, error) {
	t.Helper()
	saved := CLI.Ngrams
	t.Cleanup(func() { CLI.Ngrams = saved })
	CLI.Ngrams.Data, CLI.Ngrams.Format, CLI.Ngrams.Limit = data, format, -1
	CLI.Ngrams.Lens, CLI.Ngrams.N = "cmd", "2-5"
	CLI.Ngrams.MinCount, CLI.Ngrams.MinSessions = 1, 2
	CLI.Ngrams.Contains = "bd_close"
	CLI.Ngrams.Since, CLI.Ngrams.Until = since, until

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	runErr := cmdNgrams()
	os.Stdout = orig
	_ = w.Close()
	got := <-done
	_ = r.Close()
	return got, runErr
}

func TestNgramsWindow_Header_ShowsSinceAndKeptAgainstTotal(t *testing.T) {
	data := writeWindowCorpus(t)
	tests := []struct {
		name         string
		since, until string
		want         []string
	}{
		{"since", "2026-10-19", "",
			[]string{"streams=2 ", "window since=2026-10-19 kept=2 of 4 streams · dropped no-timestamp=1\n"}},
		{"until is inclusive of its whole day", "", "2026-10-20",
			[]string{"streams=3 ", "window until=2026-10-20 kept=3 of 4 streams · dropped no-timestamp=1\n"}},
		{"until before the last start", "", "2026-10-19",
			[]string{"streams=2 ", "window until=2026-10-19 kept=2 of 4 streams · dropped no-timestamp=1\n"}},
		{"both", "2026-10-19", "2026-10-19",
			[]string{"streams=1 ", "window since=2026-10-19 until=2026-10-19 kept=1 of 4 streams · dropped no-timestamp=1\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ngramsWindow(t, data, fmtText, tt.since, tt.until)
			if err != nil {
				t.Fatalf("cmdNgrams: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("output lacks %q:\n%s", w, got)
				}
			}
		})
	}
}

// Counts follow the window: the routine runs once per kept session.
func TestNgramsWindow_CountsOnlyKeptStreams(t *testing.T) {
	data := writeWindowCorpus(t)
	all, err := ngramsWindow(t, data, fmtText, "", "")
	if err != nil {
		t.Fatal(err)
	}
	windowed, err := ngramsWindow(t, data, fmtText, "2026-10-19", "")
	if err != nil {
		t.Fatal(err)
	}
	// The routine runs once in each session: 4x/4 sessions, or 2x/2 once the
	// window drops s1 and the untimestamped s4.
	if !strings.Contains(all, "4x/4") {
		t.Fatalf("unwindowed output should count the routine 4x in 4 sessions:\n%s", all)
	}
	if !strings.Contains(windowed, "2x/2") {
		t.Fatalf("windowed output should count the routine 2x in 2 sessions:\n%s", windowed)
	}
	if strings.Contains(windowed, "4x/4") {
		t.Fatalf("windowed output still counts a dropped session:\n%s", windowed)
	}
}

func TestNgramsWindow_JSON_CarriesWindowOnlyWhenSet(t *testing.T) {
	data := writeWindowCorpus(t)
	plain, err := ngramsWindow(t, data, fmtJSON, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(plain), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["streamWindow"]; ok {
		t.Errorf("unwindowed JSON carries a window key: %s", plain)
	}

	win, err := ngramsWindow(t, data, fmtJSON, "2026-10-19", "")
	if err != nil {
		t.Fatal(err)
	}
	var wm struct {
		Window struct {
			Since   string `json:"since"`
			Kept    int    `json:"kept"`
			Total   int    `json:"total"`
			Dropped int    `json:"dropped_no_timestamp"`
		} `json:"streamWindow"`
	}
	if err := json.Unmarshal([]byte(win), &wm); err != nil {
		t.Fatal(err)
	}
	if got := wm.Window; got.Since != "2026-10-19" || got.Kept != 2 || got.Total != 4 || got.Dropped != 1 {
		t.Errorf("window = %+v, want since 2026-10-19 kept 2 total 4 dropped 1", got)
	}
}

func TestNgramsWindow_BadDate_IsUsageErrorNamingTheForm(t *testing.T) {
	data := writeWindowCorpus(t)
	tests := []struct {
		name         string
		since, until string
		wantForm     bool
	}{
		{"since not a date", "yesterday", "", true},
		{"since wrong order", "19-10-2026", "", true},
		{"until not a date", "", "2026-13-40", true},
		{"since after until", "2026-10-20", "2026-10-19", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ngramsWindow(t, data, fmtText, tt.since, tt.until)
			if err == nil {
				t.Fatalf("want an error, got output:\n%s", got)
			}
			if code := exitCodeFor(err); code != exitUsage {
				t.Errorf("exit code = %d, want usage %d (err: %v)", code, exitUsage, err)
			}
			if tt.wantForm && !strings.Contains(err.Error(), "YYYY-MM-DD") {
				t.Errorf("error %q does not name the YYYY-MM-DD form", err)
			}
			if got != "" {
				t.Errorf("a bad window printed output:\n%s", got)
			}
		})
	}
}

// TestNgramsWindow_NoFlags_OutputIsByteIdenticalToToday pins the unwindowed
// text output against testdata/ngrams_nowindow.golden, which was captured
// before --since/--until existed. Regenerate it only on purpose:
// FERRET_UPDATE_GOLDEN=1 go test ./cmd/ferret -run NoFlags.
func TestNgramsWindow_NoFlags_OutputIsByteIdenticalToToday(t *testing.T) {
	got, err := ngramsWindow(t, writeWindowCorpus(t), fmtText, "", "")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "ngrams_nowindow.golden")
	if os.Getenv("FERRET_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("unwindowed ngrams output changed.\n got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "window") {
		t.Errorf("unwindowed output mentions a window:\n%s", got)
	}
}

// A fix's baseline burn is a whole-corpus number; --since-fixes over a window
// would compare a windowed burn against it and show a false down-arrow.
func TestReportSinceFixes_WithWindow_IsUsageError(t *testing.T) {
	for _, set := range []func(){
		func() { CLI.Report.Since = "2026-10-19" },
		func() { CLI.Report.Until = "2026-10-19" },
	} {
		saved := CLI.Report
		CLI.Report.Data, CLI.Report.Format, CLI.Report.Lens = writeWindowCorpus(t), fmtText, "cmd"
		CLI.Report.SinceFixes = true
		set()
		err := cmdReport()
		CLI.Report = saved
		if !errors.Is(err, errSinceFixesWindow) || exitCodeFor(err) != exitUsage {
			t.Errorf("report --since-fixes with a window: err = %v, want errSinceFixesWindow (usage)", err)
		}
	}
}

// --since-fixes alone, and a window alone, stay allowed.
func TestReportWindow_WithoutSinceFixes_IsNotRefused(t *testing.T) {
	saved := CLI.Report
	t.Cleanup(func() { CLI.Report = saved })
	CLI.Report.Data, CLI.Report.Format, CLI.Report.Lens = writeWindowCorpus(t), fmtText, "cmd"
	CLI.Report.Since = "2026-10-19"
	CLI.Report.MinSupport, CLI.Report.MaxGap, CLI.Report.MaxLen = 2, 3, 8
	CLI.Report.Order, CLI.Report.Top = 3, 10
	CLI.Report.Limit = -1
	got := captureStdout(t, cmdReport)
	if !strings.Contains(got, "window since=2026-10-19 kept=2 of 4 streams") {
		t.Errorf("report header lacks the window line:\n%s", got)
	}
}
