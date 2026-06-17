package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/out"
)

var errAdjSessionRequired = errors.New("adjudicate: --session PREFIX required")

// cmdAdjudicate runs the LLM analyst over one session's spine. It is the
// precision half of the loop: the deterministic spine (recall — every
// prompt+reasoning+call) is the input; the model judges each call against the
// task it served and flags tool-for-intent mismatches; dk validates a sample of
// the verdicts. ferret runs the analyst as a built-in step but is NOT the final
// arbiter — the human stays the validator (AgentRewardBench: LLM judges
// over-credit success).
//
// --emit-prompt assembles and prints the prompt without a network call, so the
// pipeline is exercisable before an API key is wired up.
func cmdAdjudicate() error {
	cmd := &CLI.Adjudicate
	if cmd.Session == "" {
		return errAdjSessionRequired
	}
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	root := cmd.Root
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}

	// Resolve the canonical session id for labeling, then capture the spine the
	// same builder the `spine` subcommand emits — the analyst reads exactly what
	// a human would see.
	src, _, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := spine(&buf, root, cmd.Session); err != nil {
		return err
	}

	if cmd.EmitPrompt {
		system, user := analyst.BuildPrompt(buf.String())
		bw := os.Stdout
		fmt.Fprintln(bw, "=== SYSTEM ===")
		fmt.Fprintln(bw, system)
		fmt.Fprintln(bw, "\n=== USER ===")
		fmt.Fprintln(bw, user)
		return nil
	}

	cfg := analyst.Config{Model: cmd.Model}
	if !cfg.HasAPIKey() {
		return analyst.ErrNoAPIKey
	}
	res, err := analyst.Run(context.Background(), cfg, src.Session, buf.String())
	if err != nil {
		return err
	}

	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeAdjudicateText(os.Stdout, res)
}

// writeAdjudicateText renders the verdicts densely: mismatches first (the
// actionable subset dk validates), then a count of served calls. Each mismatch
// shows the task it failed, the tool chosen, the better fit, and the one-line
// reasoning + confidence.
func writeAdjudicateText(w io.Writer, res analyst.Result) error {
	sink := out.NewSink(w, 0, 0)
	defer sink.Close()
	about(sink,
		"≡ adjudicate: LLM analyst over the session spine — judges each call against the task it served.",
		"≡ mismatch = a better-fitting tool was available but not chosen (e.g. snipe-shaped query → rg). dk validates.")
	mism := res.Mismatches()
	sink.Head("adjudicate session=%s model=%s findings=%d mismatches=%d", res.Session, res.Model, len(res.Findings), len(mism))
	if len(mism) == 0 {
		sink.Head("no tool-for-intent mismatches flagged")
		return nil
	}
	for _, f := range mism {
		sink.Row("✗ %-6s → %-8s [%s]  task: %s", f.ToolUsed, f.Better, f.Confidence, f.Task)
		sink.Row("    why: %s", f.Why)
		sink.Row("    call: %s", f.Call)
	}
	return nil
}
