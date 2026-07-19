package turn

import (
	"encoding/json"
	"testing"

	"github.com/dkoosis/ferret/internal/transcript"
)

// TestPromptTextIgnoresToolResults guards the boundary signal: a user line that
// only carries a tool_result is NOT a prompt and must not open a segment.
func TestPromptTextIgnoresToolResults(t *testing.T) {
	blocks := mustBlocks(t, `[{"type":"tool_result","tool_use_id":"t1","content":"x"}]`)
	if got := PromptText(blocks); got != "" {
		t.Errorf("tool_result-only line yielded prompt %q, want empty", got)
	}
	blocks = mustBlocks(t, `[{"type":"text","text":"  do  the   thing  "}]`)
	if got := PromptText(blocks); got != "do the thing" {
		t.Errorf("promptText = %q, want collapsed %q", got, "do the thing")
	}
}

// TestClassifyBoundary is the unit table for the non-boundary filter (ferret-ajm):
// affirmations and control built-ins / system envelopes continue; real prompts and
// namespaced work commands open a boundary.
func TestClassifyBoundary(t *testing.T) {
	cmd := func(name string) string {
		return "<command-name>" + name + "</command-name> <command-message>x</command-message>"
	}
	cases := []struct {
		name     string
		prompt   string
		wantSkip bool
		wantKind string
	}{
		{"bare yes", "yes", true, "affirmation"},
		{"affirm trailing punct", "Sure.", true, "affirmation"},
		{"multiword affirm", "go ahead", true, "affirmation"},
		{"yes with more text stays a boundary", "yes, but also rename X", false, ""},
		{"real prompt", "implement the marker", false, ""},
		{"control clear", cmd("/clear"), true, "control"},
		{"control exit", cmd("/exit"), true, "control"},
		{"namespaced work command is a boundary", cmd("/wrap:wrap"), false, ""},
		{"namespaced lintbrush is a boundary", cmd("/lintbrush:clean"), false, ""},
		{"unknown bare command is a boundary", cmd("/deploy"), false, ""},
		{"local-command carrier", "<local-command-stdout>See ya!</local-command-stdout>", true, "carrier"},
		{"task-notification carrier", "<task-notification> <task-id>abc</task-id> </task-notification>", true, "carrier"},
		{"compaction-summary carrier", "This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:", true, "carrier"},
		// Phase A (bbp.7) — three new non-dialogue carrier buckets.
		{"teammate-message carrier", `<teammate-message teammate_id="team-lead"> start on X </teammate-message>`, true, "carrier"},
		{"bash-input carrier", "<bash-input>git status</bash-input>", true, "carrier"},
		{"bash-stdout carrier", "<bash-stdout>On branch main</bash-stdout>", true, "carrier"},
		{"bash-stderr carrier", "<bash-stderr>fatal: not a repo</bash-stderr>", true, "carrier"},
		{"interrupt artifact", "[Request interrupted by user]", true, "carrier"},
		{"interrupt artifact for tool use", "[Request interrupted by user for tool use]", true, "carrier"},
		// [Image #] semantics: whole-turn marker folds; embedded marker keeps the turn.
		{"whole-turn image marker", "[Image #1]", true, "carrier"},
		{"multiple whole-turn image markers", "[Image #1] [Image #2]", true, "carrier"},
		{"embedded image marker stays a boundary", "[Image #1] please refactor this function", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			skip, kind, _ := ClassifyBoundary(c.prompt)
			if skip != c.wantSkip || kind != c.wantKind {
				t.Errorf("ClassifyBoundary(%q) = (%v, %q), want (%v, %q)",
					c.prompt, skip, kind, c.wantSkip, c.wantKind)
			}
		})
	}
}

// TestClassifyBoundaryFixedLabels checks the non-<…> carriers get fixed labels
// (Phase A, bbp.7): interrupt/image markers have no envelope tag, so they must NOT
// route through carrierLabel (which would fall back to the useless "carrier").
func TestClassifyBoundaryFixedLabels(t *testing.T) {
	cases := []struct{ prompt, wantLabel string }{
		{"[Request interrupted by user]", "interrupt-artifact"},
		{"[Image #1]", "image-marker"},
		{"[Image #3] [Image #4]", "image-marker"},
	}
	for _, c := range cases {
		skip, kind, label := ClassifyBoundary(c.prompt)
		if !skip || kind != "carrier" || label != c.wantLabel {
			t.Errorf("ClassifyBoundary(%q) = (%v, %q, %q), want (true, carrier, %q)",
				c.prompt, skip, kind, label, c.wantLabel)
		}
	}
}

func mustBlocks(t *testing.T, raw string) transcript.Blocks {
	t.Helper()
	var b transcript.Blocks
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
