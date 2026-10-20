package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// writeVocabCorpus writes three sessions: bd_close --reason runs four times in
// two of them, and one git_push fails, so the cmd lens marks it with !.
func writeVocabCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEraManifest(t, dir, event.SchemaVersion)
	w, err := event.NewWriter(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		action string
		status string
	}
	ok, fail := event.StatusOK, event.StatusFail
	sessions := []struct {
		id    string
		steps []step
	}{
		{"s1", []step{{"bd_show", ok}, {"bd_close --reason", ok}, {"git_commit", ok}, {"bd_close --reason", ok}}},
		{"s2", []step{{"bd_close --reason", ok}, {"git_push", fail}, {"bd_show", ok}, {"bd_close --reason", ok}}},
		{"s3", []step{{"git_status", ok}}},
	}
	for _, s := range sessions {
		for i, st := range s.steps {
			ev := event.Event{Seq: i + 1, Session: s.id, Kind: event.KindShell, Action: st.action, Status: st.status}
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

func runVocab(t *testing.T, data, format, sub string) string {
	t.Helper()
	saved := CLI.Vocab
	t.Cleanup(func() { CLI.Vocab = saved })
	CLI.Vocab.Data, CLI.Vocab.Format, CLI.Vocab.Lens, CLI.Vocab.Substring = data, format, "cmd", sub
	return captureStdout(t, cmdVocab)
}

func TestVocab_Substring_PrintsRowWithCounts(t *testing.T) {
	got := runVocab(t, writeVocabCorpus(t), fmtText, "bd_close")
	if !strings.Contains(got, "\n  sh:bd_close --reason  4 occ / 2 sessions\n") {
		t.Errorf("want the bd_close row with 4 occ / 2 sessions:\n%s", got)
	}
	if strings.Contains(got, "sh:bd_show") {
		t.Errorf("substring filter let sh:bd_show through:\n%s", got)
	}
	if !strings.Contains(got, `vocab lens=cmd contains="bd_close" streams=3 tokens=1`) {
		t.Errorf("header:\n%s", got)
	}
}

func TestVocab_NoSubstring_ListsEveryTokenSorted(t *testing.T) {
	got := runVocab(t, writeVocabCorpus(t), fmtText, "")
	if !strings.Contains(got, "vocab lens=cmd streams=3 tokens=5\n") {
		t.Errorf("header must name lens, streams and tokens:\n%s", got)
	}
	want := []string{
		"  sh:bd_close --reason  4 occ / 2 sessions",
		"  sh:bd_show  2 occ / 2 sessions",
		"  sh:git_commit  1 occ / 1 sessions",
		"  sh:git_push!  1 occ / 1 sessions",
		"  sh:git_status  1 occ / 1 sessions",
	}
	if rows := dataRows(got); strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
}

func TestVocab_JSON_Shape(t *testing.T) {
	data := writeVocabCorpus(t)
	saved := CLI.Vocab
	t.Cleanup(func() { CLI.Vocab = saved })
	CLI.Vocab.Data, CLI.Vocab.Format, CLI.Vocab.Lens, CLI.Vocab.Limit = data, fmtJSON, "cmd", 2
	var got struct {
		Lens      string     `json:"lens"`
		Tokens    []vocabRow `json:"tokens"`
		Total     int        `json:"total"`
		Truncated bool       `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(captureStdout(t, cmdVocab)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Lens != "cmd" || got.Total != 5 || !got.Truncated || len(got.Tokens) != 2 {
		t.Fatalf("got %+v; want lens cmd, 2 of 5 tokens, truncated", got)
	}
	if got.Tokens[0] != (vocabRow{"sh:bd_close --reason", 4, 2}) {
		t.Errorf("first token %+v", got.Tokens[0])
	}
}

func TestVocab_NoMatch_PrintsZeroTokens(t *testing.T) {
	got := runVocab(t, writeVocabCorpus(t), fmtText, "nope")
	if !strings.Contains(got, "tokens=0\n0 tokens\n") {
		t.Errorf("want an explicit 0 tokens:\n%s", got)
	}
}

func TestVocab_NoMatch_JSONTokensIsEmptyArray(t *testing.T) {
	got := runVocab(t, writeVocabCorpus(t), fmtJSON, "nope")
	if !strings.Contains(got, `"tokens": []`) {
		t.Errorf("want \"tokens\": [] on no match:\n%s", got)
	}
}
