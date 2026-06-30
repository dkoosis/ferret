package main

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// TestStreamDialogueIndexReducesPerSession guards the bbp.6 dialogue pass: each
// (project,session,agent) stream reduces to its dialogue Outcome + repair/accept
// tallies and its worst Hop2 QPP grade, keyed exactly like Corpus.StreamKeys so the
// index feeds straight into mine.AttachDialogue. The fixtures mirror the retrieval
// scorer's: a consumed-then-accepted episode grades high/success; an empty-then-
// repaired episode grades low/abandoned.
func TestStreamDialogueIndexReducesPerSession(t *testing.T) {
	getNug := func(seq int, sess, q string, ids ...string) event.Event {
		e := ev(seq, sess, event.KindTool, "mcp__trixi__get_nug")
		e.Query = q
		for _, id := range ids {
			e.Results = append(e.Results, event.NugHit{ID: id})
		}
		return e
	}
	evs := []event.Event{
		// alpha: query → consume the returned id → accept. high / success.
		getNug(0, "alpha", "lock files", "aaa111"),
		{Seq: 1, Session: "alpha", Project: "p", Kind: event.KindShell, Action: "sh", Detail: "trixi get aaa111"},
		{Seq: 2, Session: "alpha", Project: "p", Kind: event.KindPrompt, Prompt: "perfect"},
		// beta: query → empty set → repair. low / abandoned.
		getNug(3, "beta", "vague query that finds nothing"),
		{Seq: 4, Session: "beta", Project: "p", Kind: event.KindPrompt, Prompt: "no, that's wrong"},
	}
	path := writeEvents(t, evs)

	idx, err := streamDialogueIndex(path)
	if err != nil {
		t.Fatalf("streamDialogueIndex: %v", err)
	}

	alpha, ok := idx["p/alpha@"]
	if !ok {
		t.Fatalf("missing alpha stream; keys=%v", idx)
	}
	if alpha.Outcome != "success" || alpha.Hop2 != "high" || alpha.Accepts != 1 || alpha.Repairs != 0 {
		t.Errorf("alpha = %+v, want {success high accepts:1 repairs:0}", alpha)
	}

	beta, ok := idx["p/beta@"]
	if !ok {
		t.Fatalf("missing beta stream; keys=%v", idx)
	}
	if beta.Outcome != "abandoned" || beta.Hop2 != "low" || beta.Repairs != 1 {
		t.Errorf("beta = %+v, want {abandoned low repairs:1}", beta)
	}
}

// TestStreamDialogueIndexNoRetrievalLeavesHopEmpty proves a session with user turns
// but no retrieval episode still carries its dialogue Outcome while Hop2 stays "" —
// the join surfaces the human-side signal even where there's nothing to grade.
func TestStreamDialogueIndexNoRetrievalLeavesHopEmpty(t *testing.T) {
	evs := []event.Event{
		{Seq: 0, Session: "s", Project: "p", Kind: event.KindTool, Action: "Edit"},
		{Seq: 1, Session: "s", Project: "p", Kind: event.KindPrompt, Prompt: "lgtm, ship it"},
	}
	idx, err := streamDialogueIndex(writeEvents(t, evs))
	if err != nil {
		t.Fatal(err)
	}
	got := idx["p/s@"]
	if got.Outcome != "success" || got.Hop2 != "" || got.Accepts != 1 {
		t.Errorf("no-retrieval stream = %+v, want {success hop2:\"\" accepts:1}", got)
	}
}
