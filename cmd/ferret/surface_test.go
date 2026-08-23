package main

import (
	"errors"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
)

// TestCommandSurfacesExhaustive pins the property labelCommandHelp's comment
// promises: the map is exhaustive in BOTH directions. A grammar command with
// no entry would print "[unclassified]" (defeating the whole point — the
// dangerous default is the silent one) and dies here instead of at runtime. A
// commandSurfaces entry with no matching command is equally a bug: it names a
// path guardOffline will never see, so a rename or removal silently drops
// offline coverage for whatever replaced it.
func TestCommandSurfacesExhaustive(t *testing.T) {
	rows := commandRows(t)
	grammar := make(map[string]bool, len(rows))
	for _, r := range rows {
		grammar[r.path] = true
	}

	for path := range grammar {
		if _, ok := commandSurfaces[path]; !ok {
			t.Errorf("command %q has no commandSurfaces entry — labelCommandHelp would print [unclassified]", path)
		}
	}
	for path := range commandSurfaces {
		if !grammar[path] {
			t.Errorf("commandSurfaces has a stale entry %q — no such command in the grammar", path)
		}
	}
}

// TestNormalizeCommand pins the placeholder-stripping kong's positional args
// need: k.Command() spells out "search <query>", but the surface of a command
// never depends on its arguments.
func TestNormalizeCommand(t *testing.T) {
	tests := []struct{ in, want string }{
		{"search <query>", "search"},
		{"ingest", "ingest"},
		{"fixes add", "fixes add"},
		{"feedback judge", "feedback judge"},
		{"retrieval", "retrieval"},
	}
	for _, tt := range tests {
		if got := normalizeCommand(tt.in); got != tt.want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestGuardOffline covers the refusal itself: every surfaceModel command
// (adjudicate, over-initiative, feedback judge — retrieval escalates
// separately, see TestRetrievalEscalatesOnlyUnderHop1) is refused under both
// forms of the kill-switch, and a local command is never refused by either.
func TestGuardOffline(t *testing.T) {
	var modelCmds []string
	for path, s := range commandSurfaces {
		if s.kind == surfaceModel {
			modelCmds = append(modelCmds, path)
		}
	}
	if len(modelCmds) == 0 {
		t.Fatal("no surfaceModel commands found — this test would pass vacuously")
	}

	t.Run("via --offline flag", func(t *testing.T) {
		orig := CLI.Offline
		CLI.Offline = true
		t.Cleanup(func() { CLI.Offline = orig })

		for _, cmd := range modelCmds {
			var uerr *usageError
			if err := guardOffline(cmd); !errors.As(err, &uerr) {
				t.Errorf("guardOffline(%q) under --offline = %v (%T), want *usageError", cmd, err, err)
			}
		}
	})

	t.Run("via FERRET_OFFLINE env", func(t *testing.T) {
		t.Setenv(analyst.EnvOffline, "1")

		for _, cmd := range modelCmds {
			var uerr *usageError
			if err := guardOffline(cmd); !errors.As(err, &uerr) {
				t.Errorf("guardOffline(%q) under FERRET_OFFLINE=1 = %v (%T), want *usageError", cmd, err, err)
			}
		}
	})

	t.Run("local command is never refused", func(t *testing.T) {
		orig := CLI.Offline
		CLI.Offline = true
		t.Cleanup(func() { CLI.Offline = orig })

		if err := guardOffline("ingest"); err != nil {
			t.Errorf("guardOffline(%q) under --offline = %v, want nil (ingest is local, not model-backed)", "ingest", err)
		}
	})
}

// TestRetrievalEscalatesOnlyUnderHop1 pins the one command whose surface
// depends on a flag: `retrieval` is local until --hop1 turns on the LLM
// interp-fidelity judge, and it must be refused only in that state.
func TestRetrievalEscalatesOnlyUnderHop1(t *testing.T) {
	origHop1, origOffline := CLI.Retrieval.Hop1, CLI.Offline
	t.Cleanup(func() {
		CLI.Retrieval.Hop1 = origHop1
		CLI.Offline = origOffline
	})
	CLI.Offline = true

	CLI.Retrieval.Hop1 = false
	if escalatedToModel("retrieval") {
		t.Error("escalatedToModel(retrieval) = true with Hop1 unset")
	}
	if err := guardOffline("retrieval"); err != nil {
		t.Errorf("guardOffline(retrieval) with Hop1 unset, under --offline = %v, want nil", err)
	}

	CLI.Retrieval.Hop1 = true
	if !escalatedToModel("retrieval") {
		t.Error("escalatedToModel(retrieval) = false with Hop1 set")
	}
	var uerr *usageError
	if err := guardOffline("retrieval"); !errors.As(err, &uerr) {
		t.Errorf("guardOffline(retrieval) with Hop1 set, under --offline = %v (%T), want *usageError", err, err)
	}
}
