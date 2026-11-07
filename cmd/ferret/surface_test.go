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
	}
	for _, tt := range tests {
		if got := normalizeCommand(tt.in); got != tt.want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestGuardOffline covers the refusal itself: every surfaceModel command
// (adjudicate, over-initiative) is refused under both
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

// TestGuardOfflineExemptsEmitPrompt pins the case that regresses if
// guardOffline ever stops consulting emitPromptOnly: adjudicate,
// and over-initiative each return before calling the analyst when
// --emit-prompt is set (adjudicate.go:82, overinitiative.go:285), so --offline / FERRET_OFFLINE must not refuse them.
func TestGuardOfflineExemptsEmitPrompt(t *testing.T) {
	origOffline := CLI.Offline
	origAdjEmit := CLI.Adjudicate.EmitPrompt
	origOIEmit := CLI.OverInitiative.EmitPrompt
	t.Cleanup(func() {
		CLI.Offline = origOffline
		CLI.Adjudicate.EmitPrompt = origAdjEmit
		CLI.OverInitiative.EmitPrompt = origOIEmit
	})
	CLI.Offline = true
	CLI.Adjudicate.EmitPrompt = true
	CLI.OverInitiative.EmitPrompt = true

	for _, cmd := range []string{"adjudicate", "over-initiative"} {
		if err := guardOffline(cmd); err != nil {
			t.Errorf("guardOffline(%q) under --offline with --emit-prompt = %v, want nil (dry run never calls the model)", cmd, err)
		}
	}

	// Without --emit-prompt, the same commands must still be refused.
	CLI.Adjudicate.EmitPrompt = false
	CLI.OverInitiative.EmitPrompt = false
	for _, cmd := range []string{"adjudicate", "over-initiative"} {
		var uerr *usageError
		if err := guardOffline(cmd); !errors.As(err, &uerr) {
			t.Errorf("guardOffline(%q) under --offline without --emit-prompt = %v (%T), want *usageError", cmd, err, err)
		}
	}
}
