package main

import (
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
