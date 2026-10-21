package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// writeDatedCorpus writes the contains corpus with each session started on
// its own day: s1 10-17, s2 10-19, s3 10-20, s4 10-21 (local time, as the
// flags are read).
func writeDatedCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	routines := map[string][]string{
		"s1": {"git_status", "bd_show", "bd_close", "git_commit", "git_push"},
		"s2": {"git_status", "bd_show", "bd_close", "git_commit", "bd_list"},
		"s3": {"bd_list", "git_status", "bd_show", "bd_close", "git_commit"},
		"s4": {"git_status", "git_commit", "git_push", "bd_show", "bd_close"},
	}
	days := map[string]int{"s1": 17, "s2": 19, "s3": 20, "s4": 21}
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		for i, a := range routines[s] {
			ev := event.Event{
				Seq: i + 1, Session: s, Kind: event.KindShell, Action: a, Status: event.StatusOK,
				Time: time.Date(2026, 10, days[s], 12, i, 0, 0, time.Local),
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

// TestNgramsSinceCountsOnlyWindowStreams pins ferret-pr4's first clause:
// --since keeps only streams started on or after the date, and the header
// names the window and the kept count against the total.
func TestNgramsSinceCountsOnlyWindowStreams(t *testing.T) {
	saved := CLI.Ngrams
	t.Cleanup(func() { CLI.Ngrams = saved })
	CLI.Ngrams.Since = "2026-10-19"
	got := runNgrams(t, writeDatedCorpus(t), fmtText, "bd_close")
	if !strings.Contains(got, "since=2026-10-19 streams=3/4 untimed=0") {
		t.Errorf("header lacks the window and kept/total count:\n%s", got)
	}
	// s1 (10-17) is out, so bd_show → bd_close drops from 4 sessions to 3.
	if rows := grepRows(dataRows(got), "sh:bd_show → sh:bd_close  "); len(rows) != 1 || !strings.Contains(rows[0], "3x/3") {
		t.Errorf("bd_show → bd_close rows = %q, want one 3x/3", rows)
	}
	if strings.Contains(got, "s1@@") {
		t.Errorf("an exemplar cites s1, which started before --since:\n%s", got)
	}
}

// TestNgramsBadDateIsUsageError pins that a malformed date exits as a usage
// error that names the form it wants.
func TestNgramsBadDateIsUsageError(t *testing.T) {
	data := writeDatedCorpus(t)
	for _, tc := range []struct{ since, until string }{
		{"10/19/2026", ""},
		{"", "2026-13-01"},
		{"2026-10-21", "2026-10-19"},
	} {
		saved := CLI.Ngrams
		CLI.Ngrams.Data, CLI.Ngrams.Format, CLI.Ngrams.Lens, CLI.Ngrams.N = data, fmtText, "cmd", "2-5"
		CLI.Ngrams.Since, CLI.Ngrams.Until = tc.since, tc.until
		err := cmdNgrams()
		CLI.Ngrams = saved
		if _, ok := errors.AsType[*usageError](err); !ok {
			t.Errorf("since=%q until=%q: err = %v (%T), want *usageError", tc.since, tc.until, err, err)
			continue
		}
		if tc.since != "2026-10-21" && !strings.Contains(err.Error(), "YYYY-MM-DD") {
			t.Errorf("since=%q until=%q: err %q does not name YYYY-MM-DD", tc.since, tc.until, err)
		}
	}
}

// TestNgramsNoWindowUnchanged pins that with neither flag set the ngrams text
// is byte-identical to the output before ferret-pr4.
func TestNgramsNoWindowUnchanged(t *testing.T) {
	const want = "≡ ngrams: exact action sequences repeated verbatim (no gaps). High count across many\n" +
		"≡ sessions = a habitual routine — script/skill candidate. Nx/Ms = N occurrences in M sessions.\n" +
		"≡ tok! failed · tok? in failed chain · tok+ collapsed repeat run · ex: session@pos\n" +
		"ngrams lens=cmd n=2-5 streams=4 grams=3 (min-count=1 min-sessions=2)\n" +
		"    4x/4   s sh:bd_show → sh:bd_close  ex: s1@@2\n" +
		"    3x/3   s sh:git_status → sh:bd_show → sh:bd_close → sh:git_commit  ex: s1@@1\n" +
		"    2x/2   s sh:git_commit → sh:git_push  ex: s1@@4\n"
	if got := runNgrams(t, writeContainsCorpus(t), fmtText, ""); got != want {
		t.Errorf("ngrams output changed:\ngot  %q\nwant %q", got, want)
	}
	// A dated corpus without flags keeps every stream too.
	if got := runNgrams(t, writeDatedCorpus(t), fmtText, ""); got != want {
		t.Errorf("dated corpus, no window:\ngot  %q\nwant %q", got, want)
	}
}

// TestAroundJSONKeepsStepWindowUnderDates pins that a date window does not
// overwrite around's "window" step count: the dates go under "dateWindow".
func TestAroundJSONKeepsStepWindowUnderDates(t *testing.T) {
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Format, CLI.Around.Limit = writeDatedCorpus(t), fmtJSON, -1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", "sh:bd_close", 2
	CLI.Around.Since = "2026-10-19"
	var got struct {
		Window     int `json:"window"`
		Streams    int `json:"streams"`
		DateWindow struct {
			Since   string `json:"since"`
			Streams int    `json:"streams"`
			Total   int    `json:"total"`
		} `json:"dateWindow"`
	}
	if err := json.Unmarshal([]byte(captureStdout(t, cmdAround)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Window != 2 {
		t.Errorf("window = %d, want the step count 2", got.Window)
	}
	if d := got.DateWindow; d.Since != "2026-10-19" || d.Streams != 3 || d.Total != 4 {
		t.Errorf("dateWindow = %+v, want since=2026-10-19 streams=3 total=4", d)
	}
}

// TestReportSinceFixesRefusesWindow pins that report will not compare the
// ledger's whole-corpus baselines with a windowed count.
func TestReportSinceFixesRefusesWindow(t *testing.T) {
	saved := CLI.Report
	t.Cleanup(func() { CLI.Report = saved })
	CLI.Report.Data, CLI.Report.Format = writeDatedCorpus(t), fmtText
	CLI.Report.SinceFixes, CLI.Report.Since = true, "2026-10-19"
	if err := cmdReport(); !errors.Is(err, errSinceFixesWindow) {
		t.Errorf("err = %v, want errSinceFixesWindow", err)
	}
}

// TestTokensWindowNamesDateWindow pins that a session the window dropped is
// reported as outside the window, not as a session that does not exist.
func TestTokensWindowNamesDateWindow(t *testing.T) {
	saved := CLI.Tokens
	t.Cleanup(func() { CLI.Tokens = saved })
	CLI.Tokens.Data, CLI.Tokens.Format, CLI.Tokens.Lens = writeDatedCorpus(t), fmtText, "cmd"
	CLI.Tokens.Session, CLI.Tokens.Since = "s1", "2026-10-19"
	err := cmdTokens()
	if !errors.Is(err, errNoStreamMatch) || !strings.Contains(err.Error(), "in the date window") {
		t.Errorf("err = %v, want errNoStreamMatch naming the date window", err)
	}
}

// TestSurpriseWindowHeaderOneStreamsCount pins that a windowed surprise
// header prints one streams= value and names the scored count scored=.
func TestSurpriseWindowHeaderOneStreamsCount(t *testing.T) {
	saved := CLI.Surprise
	t.Cleanup(func() { CLI.Surprise = saved })
	CLI.Surprise.Data, CLI.Surprise.Format, CLI.Surprise.Limit = writeDatedCorpus(t), fmtText, -1
	CLI.Surprise.Lens, CLI.Surprise.Order, CLI.Surprise.MinToks = "cmd", 3, 1
	CLI.Surprise.Since = "2026-10-19"
	got := captureStdout(t, cmdSurprise)
	if n := strings.Count(got, "streams="); n != 1 || !strings.Contains(got, "streams=3/4 untimed=0 scored=3") {
		t.Errorf("header has %d streams= values, want one with scored=3:\n%s", n, got)
	}
}

// TestAroundZeroMatchNamesDateWindow pins that a token the window removed
// reads as absent from the window, not from the corpus.
func TestAroundZeroMatchNamesDateWindow(t *testing.T) {
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Since = "2026-10-22" // after every session in the fixture
	got, err := runAround(t, writeDatedCorpus(t), "sh:bd_close", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0 matches for sh:bd_close in the date window (since=2026-10-22 streams=0/4 untimed=0)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
