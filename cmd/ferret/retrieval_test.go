package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

// writeEvents marshals events as a json stream (the events.jsonl shape event.Read
// consumes) under a temp dir, returning the path.
func writeEvents(t *testing.T, evs []event.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range evs {
		if err := enc.Encode(&evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ev(seq int, session string, kind, action string) event.Event {
	return event.Event{Seq: seq, Session: session, Project: "p", Kind: kind, Action: action}
}

// TestBuildRetrievalEpisodesGroupsBySession proves episodes don't bleed across
// sessions and that the --session filter scopes the read.
func TestBuildRetrievalEpisodesGroupsBySession(t *testing.T) {
	mk := func(seq int, sess, q string, ids ...string) event.Event {
		e := ev(seq, sess, event.KindTool, "mcp__trixi__get_nug")
		e.Query = q
		for _, id := range ids {
			e.Results = append(e.Results, event.NugHit{ID: id})
		}
		return e
	}
	evs := []event.Event{
		mk(0, "alpha", "lock files", "aaa111"),
		{Seq: 1, Session: "alpha", Project: "p", Kind: event.KindShell, Action: "sh", Detail: "trixi get aaa111"},
		{Seq: 2, Session: "alpha", Project: "p", Kind: event.KindPrompt, Prompt: "perfect"},
		mk(3, "beta", "other thing", "bbb222"),
		{Seq: 4, Session: "beta", Project: "p", Kind: event.KindPrompt, Prompt: "no, wrong"},
	}
	path := writeEvents(t, evs)

	all, err := buildRetrievalEpisodes(path, "")
	if err != nil {
		t.Fatalf("buildRetrievalEpisodes: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 episodes across 2 sessions, got %d", len(all))
	}

	only, err := buildRetrievalEpisodes(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Session != "alpha" || !only[0].ConsumedStrict {
		t.Fatalf("session filter wrong: %+v", only)
	}
}

func TestWriteRetrievalTextScorecard(t *testing.T) {
	eps := []score.Episode{
		{Session: "s", Query: "how to lock files", Queries: 1, Results: 3, ConsumedStrict: true,
			ConsumedLoose: true, ConsumedRank: 1, Outcome: "success", Answerable: true},
		{Session: "s", Query: "vague", Queries: 2, Results: 0, SelfRequery: true, EmptyResult: true,
			CoverageGap: true, Answerable: false},
	}
	roll := score.Aggregate(eps)
	var buf bytes.Buffer
	sink := out.NewSink(&buf, 0, 0)
	writeRetrievalText(sink, "", roll, eps)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"retrieval corpus episodes=2 answerable=1",
		"RU      strict=1.00 loose=1.00",
		"R2 MRR=1.00",
		"C1 coverage-gap=1",
		"how to lock files",
		"coverage-gap",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("scorecard missing %q\n---\n%s", want, got)
		}
	}
}

func TestWriteRetrievalJSONSchema(t *testing.T) {
	eps := []score.Episode{
		{Session: "s", Query: "q", Queries: 1, Results: 2, ConsumedStrict: true, ConsumedRank: 2,
			ConsumedLoose: true, Outcome: "success", Answerable: true},
	}
	roll := score.Aggregate(eps)
	var buf bytes.Buffer
	if err := writeRetrievalJSON(&buf, "", roll, eps, 0, nil); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scope  string `json:"scope"`
		Rollup struct {
			Episodes int     `json:"episodes"`
			RUStrict float64 `json:"ruStrict"`
			R2MRR    float64 `json:"r2Mrr"`
		} `json:"rollup"`
		Episodes  []score.Episode `json:"episodes"`
		Total     int             `json:"total"`
		Truncated bool            `json:"truncated"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, buf.String())
	}
	if doc.Scope != "corpus" || doc.Rollup.Episodes != 1 || doc.Total != 1 || doc.Truncated {
		t.Errorf("doc meta wrong: %+v", doc)
	}
	if doc.Rollup.R2MRR != 0.5 { // consumed at rank 2 → 1/2
		t.Errorf("R2MRR = %v, want 0.5", doc.Rollup.R2MRR)
	}
	if len(doc.Episodes) != 1 || doc.Episodes[0].Query != "q" {
		t.Errorf("episodes round-trip wrong: %+v", doc.Episodes)
	}
}
