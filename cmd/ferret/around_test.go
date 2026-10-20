package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

// writeAroundCorpus writes five sessions where bd_close is always followed by
// git_commit → git_push, so the 3-gram keeps all of the 2-gram's count and
// mine.Filter suppresses the bd_close → git_commit 2-gram.
func writeAroundCorpus(t *testing.T) string {
	t.Helper()
	return writeAroundCorpusN(t, 0)
}

// writeAroundCorpusN adds extra copies of s1's routine, so the corpus can
// clear the seqs default min-support of 20 sessions.
func writeAroundCorpusN(t *testing.T, extra int) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	routines := map[string][]string{
		"s1": {"bd_show", "bd_close", "git_commit", "git_push"},
		"s2": {"bd_show", "bd_close", "git_commit", "git_push"},
		"s3": {"git_status", "bd_close", "git_commit", "git_push"},
		"s4": {"bd_show", "bd_close", "git_commit", "git_push", "bd_list"},
		"s5": {"bd_list", "git_status"},
	}
	order := make([]string, 0, 5+extra)
	order = append(order, "s1", "s2", "s3", "s4", "s5")
	for i := range extra {
		s := fmt.Sprintf("x%d", i)
		routines[s] = routines["s1"]
		order = append(order, s)
	}
	for _, s := range order {
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

func runAround(t *testing.T, data, token string, window int) (string, error) {
	t.Helper()
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Format, CLI.Around.Limit = data, fmtText, -1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", token, window
	var runErr error
	got := captureStdout(t, func() error { runErr = cmdAround(); return nil })
	return got, runErr
}

func aroundCorpus(t *testing.T, data string) *mine.Corpus {
	t.Helper()
	c, _, err := fromLensFlags(LensFlags{Lens: "cmd"}).corpus(filepath.Join(data, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// section returns the lines between the header line starting with name and
// the next header line.
func section(out, name string) []string {
	var rows []string
	in := false
	for ln := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(ln, " ") {
			in = strings.HasPrefix(ln, name+" (")
			continue
		}
		if in {
			rows = append(rows, ln)
		}
	}
	return rows
}

// TestAround_AfterCountEqualsCountGrams_When_FilterSuppresses is the bead's
// load-bearing check: the after-table count is CountGrams' 2-gram count, even
// for the 2-gram mine.Filter hides.
func TestAround_AfterCountEqualsCountGrams_When_FilterSuppresses(t *testing.T) {
	data := writeAroundCorpus(t)
	c := aroundCorpus(t, data)
	closeTok, commitTok := "sh:bd_close", "sh:git_commit"
	var pair *mine.Gram
	for _, g := range mine.CountGrams(c, 2, 2) {
		if toks := c.Tokens(g.IDs); toks[0] == closeTok && toks[1] == commitTok {
			pair = g
		}
	}
	if pair == nil {
		t.Fatalf("fixture lacks the %s → %s 2-gram; vocab %v", closeTok, commitTok, c.Vocab)
	}
	for _, g := range mine.Filter(mine.CountGrams(c, 2, 3), 1, 2) {
		if toks := c.Tokens(g.IDs); len(toks) == 2 && toks[0] == closeTok && toks[1] == commitTok {
			t.Fatal("fixture must make mine.Filter suppress the 2-gram")
		}
	}

	got, err := runAround(t, data, closeTok, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%5dx/%-4ds d1 %s", pair.Count, pair.Sessions, commitTok)
	after := section(got, "after")
	if len(after) == 0 || after[0] != want {
		t.Errorf("after table:\n%s\nwant first row %q", strings.Join(after, "\n"), want)
	}
	before := section(got, "before")
	if len(before) != 2 || !strings.Contains(before[0], "3x/3") || !strings.HasSuffix(before[0], "sh:bd_show") {
		t.Errorf("before table sorted by sessions:\n%s", strings.Join(before, "\n"))
	}
}

// TestAround_Window2_AddsDistance2Row shows the token two steps away.
func TestAround_Window2_AddsDistance2Row(t *testing.T) {
	data := writeAroundCorpus(t)
	one, err := runAround(t, data, "sh:bd_close", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(one, " d2 ") {
		t.Fatalf("--window 1 printed a distance-2 row:\n%s", one)
	}
	two, err := runAround(t, data, "sh:bd_close", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "    4x/4   s d2 sh:git_push   via sh:bd_close → sh:git_commit → sh:git_push"
	if !strings.Contains(two, want+"\n") {
		t.Errorf("--window 2 output lacks %q:\n%s", want, two)
	}
}

// TestAround_EndsWithSeqsThroughToken checks the gapped-sequence tail.
func TestAround_EndsWithSeqsThroughToken(t *testing.T) {
	data := writeAroundCorpus(t)
	got, err := runAround(t, data, "sh:bd_close", 1)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has 5 sessions, under the seqs default min-support of 20.
	_, tail, ok := strings.Cut(got, "seqs through it")
	if !ok || !strings.Contains(tail, "0 seqs") {
		t.Errorf("want an empty seqs tail on a 5-session corpus:\n%s", got)
	}

	got, err = runAround(t, writeAroundCorpusN(t, 20), "sh:bd_close", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, tail, _ = strings.Cut(got, "seqs through it")
	_, tail, _ = strings.Cut(tail, "\n")
	rows := dataRows(tail)
	if len(rows) == 0 || len(rows) > aroundSeqTop {
		t.Fatalf("want 1..%d seqs rows, got %d:\n%s", aroundSeqTop, len(rows), tail)
	}
	for _, r := range rows {
		if !strings.Contains(r, "sh:bd_close") || !strings.Contains(r, "s ") {
			t.Errorf("seqs row lacks the token or a support count: %q", r)
		}
	}
	if !strings.HasPrefix(rows[0], "   24s ") {
		t.Errorf("top seq support: %q, want 24s", rows[0])
	}
}

func TestAround_UnknownToken_PrintsZeroMatches(t *testing.T) {
	data := writeAroundCorpus(t)
	got, err := runAround(t, data, "sh:nope", 1)
	if err != nil {
		t.Fatalf("unknown token must exit 0, got %v", err)
	}
	if got != "0 matches for sh:nope\n" {
		t.Errorf("got %q", got)
	}
}

func TestAround_AmbiguousSubstring_ErrorsNamingMatches(t *testing.T) {
	data := writeAroundCorpus(t)
	_, err := runAround(t, data, "bd_", 1)
	if !errors.Is(err, errAmbiguousToken) {
		t.Fatalf("want errAmbiguousToken, got %v", err)
	}
	if exitCodeFor(err) != exitUsage {
		t.Errorf("exit code %d, want %d", exitCodeFor(err), exitUsage)
	}
	for _, tok := range []string{"sh:bd_show", "sh:bd_close", "sh:bd_list"} {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("error %q does not name %s", err, tok)
		}
	}
}

func TestAround_UniqueSubstring_Resolves(t *testing.T) {
	data := writeAroundCorpus(t)
	got, err := runAround(t, data, "bd_close", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "around sh:bd_close ") {
		t.Errorf("header does not name the resolved token:\n%s", got)
	}
}

// TestAround_JSON_ReportsTruncation pins the AX contract: a capped section
// says so, with its total.
func TestAround_JSON_ReportsTruncation(t *testing.T) {
	data := writeAroundCorpus(t)
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Format, CLI.Around.Limit = data, fmtJSON, 1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", "sh:bd_close", 1
	var got struct {
		Before      []aroundRow `json:"before"`
		BeforeTotal int         `json:"beforeTotal"`
		Truncated   bool        `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(captureStdout(t, cmdAround)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Before) != 1 || got.BeforeTotal != 2 || !got.Truncated {
		t.Errorf("--limit 1: before %d of %d, truncated=%v; want 1 of 2, true", len(got.Before), got.BeforeTotal, got.Truncated)
	}
}
