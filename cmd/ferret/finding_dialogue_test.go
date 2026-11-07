package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
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

// TestStreamDialogueIndexReducesPerSession guards the bbp.6 dialogue pass: each
// (project,session,agent) stream reduces to its dialogue Outcome + repair/accept
// tallies, keyed exactly like Corpus.StreamKeys so the index feeds straight into
// mine.AttachDialogue. An accepted session grades success; a repaired one,
// abandoned.
func TestStreamDialogueIndexReducesPerSession(t *testing.T) {
	evs := []event.Event{
		{Seq: 0, Session: "alpha", Project: "p", Kind: event.KindTool, Action: "Edit"},
		{Seq: 1, Session: "alpha", Project: "p", Kind: event.KindPrompt, Prompt: "perfect"},
		{Seq: 2, Session: "beta", Project: "p", Kind: event.KindTool, Action: "Edit"},
		{Seq: 3, Session: "beta", Project: "p", Kind: event.KindPrompt, Prompt: "no, that's wrong"},
	}
	idx, err := streamDialogueIndex(writeEvents(t, evs))
	if err != nil {
		t.Fatalf("streamDialogueIndex: %v", err)
	}

	alpha, ok := idx["p/alpha@"]
	if !ok {
		t.Fatalf("missing alpha stream; keys=%v", idx)
	}
	if alpha.Outcome != "success" || alpha.Accepts != 1 || alpha.Repairs != 0 {
		t.Errorf("alpha = %+v, want {success accepts:1 repairs:0}", alpha)
	}

	beta, ok := idx["p/beta@"]
	if !ok {
		t.Fatalf("missing beta stream; keys=%v", idx)
	}
	if beta.Outcome != "abandoned" || beta.Repairs != 1 {
		t.Errorf("beta = %+v, want {abandoned repairs:1}", beta)
	}
}
