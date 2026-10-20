package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// writeContainsCorpus writes a current-era corpus of four sessions whose
// shell routines mix bd and git steps, so some mined rows hold bd_close and
// some do not.
func writeContainsCorpus(t *testing.T) string {
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
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		for i, a := range routines[s] {
			ev := event.Event{Seq: i + 1, Session: s, Kind: event.KindShell, Action: a, Status: event.StatusOK}
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

// captureStdout runs fn with os.Stdout piped and returns what it printed.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	runErr := fn()
	os.Stdout = orig
	_ = w.Close()
	got := <-done
	_ = r.Close()
	if runErr != nil {
		t.Fatalf("command: %v", runErr)
	}
	return string(got)
}

// dataRows keeps a text table's data rows, which start with padded counts,
// and drops its header lines.
func dataRows(out string) []string {
	var rows []string
	for ln := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(ln, " ") {
			rows = append(rows, ln)
		}
	}
	return rows
}

func grepRows(rows []string, sub string) []string {
	var kept []string
	for _, r := range rows {
		if strings.Contains(r, sub) {
			kept = append(kept, r)
		}
	}
	return kept
}

func runNgrams(t *testing.T, data, format, contains string) string {
	t.Helper()
	saved := CLI.Ngrams
	t.Cleanup(func() { CLI.Ngrams = saved })
	CLI.Ngrams.Data, CLI.Ngrams.Format, CLI.Ngrams.Limit = data, format, -1
	CLI.Ngrams.Lens, CLI.Ngrams.N = "cmd", "2-5"
	CLI.Ngrams.MinCount, CLI.Ngrams.MinSessions = 1, 2
	CLI.Ngrams.Contains = contains
	return captureStdout(t, cmdNgrams)
}

func runSeqs(t *testing.T, data, contains string) string {
	t.Helper()
	saved := CLI.Seqs
	t.Cleanup(func() { CLI.Seqs = saved })
	CLI.Seqs.Data, CLI.Seqs.Format, CLI.Seqs.Limit = data, fmtText, -1
	CLI.Seqs.Lens = "cmd"
	CLI.Seqs.MinSupport, CLI.Seqs.MaxGap, CLI.Seqs.MaxLen = 2, 3, 5
	CLI.Seqs.Contains = contains
	return captureStdout(t, cmdSeqs)
}

func runGraph(t *testing.T, data, contains string) string {
	t.Helper()
	saved := CLI.Graph
	t.Cleanup(func() { CLI.Graph = saved })
	CLI.Graph.Data, CLI.Graph.Format, CLI.Graph.Limit = data, fmtText, -1
	CLI.Graph.Lens = "cmd"
	CLI.Graph.MinCount, CLI.Graph.Loops = 1, true
	CLI.Graph.Contains = contains
	return captureStdout(t, cmdGraph)
}

// TestNgramsContains_MatchesGrep_When_FilterSet is ferret-f6i's load-bearing
// check: --contains prints exactly the rows grep would keep, in order.
func TestNgramsContains_MatchesGrep_When_FilterSet(t *testing.T) {
	data := writeContainsCorpus(t)
	all := dataRows(runNgrams(t, data, fmtText, ""))
	got := dataRows(runNgrams(t, data, fmtText, "bd_close"))
	want := grepRows(all, "bd_close")
	if len(want) == 0 || len(want) == len(all) {
		t.Fatalf("fixture must mine rows with and without bd_close; got %d of %d", len(want), len(all))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("--contains rows:\n%s\nwant grep rows:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestContainsHeader_NamesFilter_OnlyWhen_Set pins the header tag on all
// three commands.
func TestContainsHeader_NamesFilter_OnlyWhen_Set(t *testing.T) {
	data := writeContainsCorpus(t)
	for name, run := range map[string]func(string) string{
		"ngrams": func(c string) string { return runNgrams(t, data, fmtText, c) },
		"seqs":   func(c string) string { return runSeqs(t, data, c) },
		"graph":  func(c string) string { return runGraph(t, data, c) },
	} {
		if out := run("bd_close"); !strings.Contains(out, "contains=bd_close") {
			t.Errorf("%s --contains header lacks contains=bd_close:\n%s", name, out)
		}
		if out := run(""); strings.Contains(out, "contains=") {
			t.Errorf("%s without --contains prints contains=:\n%s", name, out)
		}
	}
}

// assertFilteredSubset checks the filtered rows drop at least one row that
// lacks sub and keep the rest byte-identical (so counts are unchanged).
func assertFilteredSubset(t *testing.T, name string, all, got []string, sub string) {
	t.Helper()
	if len(grepRows(all, sub)) == len(all) {
		t.Fatalf("%s fixture has no row without %s", name, sub)
	}
	if strings.Join(got, "\n") != strings.Join(grepRows(all, sub), "\n") {
		t.Errorf("%s --contains rows:\n%s\nwant (unfiltered rows holding %s):\n%s",
			name, strings.Join(got, "\n"), sub, strings.Join(grepRows(all, sub), "\n"))
	}
}

func TestSeqsContains_DropsNonMatchingRows_When_FilterSet(t *testing.T) {
	data := writeContainsCorpus(t)
	assertFilteredSubset(t, "seqs",
		dataRows(runSeqs(t, data, "")), dataRows(runSeqs(t, data, "bd_close")), "bd_close")
}

func TestGraphContains_DropsNonMatchingRows_When_FilterSet(t *testing.T) {
	data := writeContainsCorpus(t)
	assertFilteredSubset(t, "graph",
		dataRows(runGraph(t, data, "")), dataRows(runGraph(t, data, "bd_close")), "bd_close")
}

// TestNgramsContainsJSON_TotalCountsFilteredRows pins that total reports the
// rows after the filter, so truncated stays true to what --limit cut.
func TestNgramsContainsJSON_TotalCountsFilteredRows(t *testing.T) {
	data := writeContainsCorpus(t)
	var all, got struct {
		Total int `json:"total"`
		Grams []struct {
			Tokens []string `json:"tokens"`
		} `json:"grams"`
	}
	if err := json.Unmarshal([]byte(runNgrams(t, data, fmtJSON, "")), &all); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runNgrams(t, data, fmtJSON, "bd_close")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != len(got.Grams) || got.Total >= all.Total {
		t.Errorf("filtered total=%d grams=%d unfiltered total=%d; want total = filtered rows < unfiltered",
			got.Total, len(got.Grams), all.Total)
	}
}
