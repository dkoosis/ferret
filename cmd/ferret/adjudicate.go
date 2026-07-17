package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/out"
)

// analystContext derives a cancellable context from a SIGINT handler so a wedged
// analyst call cancels cooperatively on the first Ctrl-C (the SDK threads ctx
// down to the HTTP request) rather than requiring a hard kill (ferret-c71). The
// caller defers stop() to restore default signal handling.
func analystContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

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
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}

	if cmd.Propose {
		return runPropose(root)
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

	cfg := analyst.Config{Model: cmd.Model, Timeout: cmd.Timeout}
	if !cfg.HasAPIKey() {
		return analyst.ErrNoAPIKey
	}
	ctx, stop := analystContext()
	defer stop()
	res, err := analyst.Run(ctx, cfg, src.Session, buf.String())
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

// runPropose is the --propose branch (ferret-567 step 5): instead of judging
// calls already made, it feeds the deterministic cost-leak ranking ('ferret
// candidates') plus the spine to the analyst and returns one fix per task —
// automate the routine, or de-context the output. The candidate bundle is
// captured as JSON (the analyst feed) and the spine as the human-readable call
// detail; --emit-prompt assembles both without a network call.
func runPropose(root string) error {
	cmd := &CLI.Adjudicate
	src, _, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	var bundle bytes.Buffer
	if err := candidates(&bundle, root, cmd.Session, fmtJSON, cmd.Top); err != nil {
		return err
	}
	var spineBuf bytes.Buffer
	if err := spine(&spineBuf, root, cmd.Session); err != nil {
		return err
	}

	if cmd.EmitPrompt {
		system, user := analyst.BuildProposePrompt(bundle.String(), spineBuf.String())
		bw := os.Stdout
		fmt.Fprintln(bw, "=== SYSTEM ===")
		fmt.Fprintln(bw, system)
		fmt.Fprintln(bw, "\n=== USER ===")
		fmt.Fprintln(bw, user)
		return nil
	}

	cfg := analyst.Config{Model: cmd.Model, Timeout: cmd.Timeout}
	if !cfg.HasAPIKey() {
		return analyst.ErrNoAPIKey
	}
	ctx, stop := analystContext()
	defer stop()
	res, err := analyst.RunPropose(ctx, cfg, src.Session, bundle.String(), spineBuf.String())
	if err != nil {
		return err
	}

	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeProposeText(os.Stdout, res)
}

// writeProposeText renders the proposals densely: actionable fixes first (what dk
// acts on), grouped by lever, then a count of declined tasks. Each fix shows the
// task it targets, the proposal, and the one-line rationale + confidence.
func writeProposeText(w io.Writer, res analyst.ProposeResult) error {
	sink := out.NewSink(w, 0, 0)
	defer sink.Close()
	about(sink,
		"≡ propose: LLM analyst over the session's cost-leak candidates — one fix per task. dk validates.",
		"≡ ⚙ automate = script a recurring routine · ✂ de-context = shrink output-heavy IO before it enters context.")
	act := res.Actionable()
	sink.Head("propose session=%s model=%s proposals=%d actionable=%d", res.Session, res.Model, len(res.Proposals), len(act))
	if len(act) == 0 {
		sink.Head("no actionable fixes proposed")
		return nil
	}
	for _, p := range act {
		sink.Row("%s %-10s [%s] task %d: %s", proposeMark(p.Kind), p.Kind, p.Confidence, p.Task, p.Proposal)
		sink.Row("    why: %s", p.Why)
	}
	return nil
}

// proposeMark maps a fix lever to its glyph (css symbol vocabulary).
func proposeMark(k analyst.ProposeKind) string {
	switch k {
	case analyst.ProposeAutomate:
		return "⚙"
	case analyst.ProposeDeContext:
		return "✂"
	case analyst.ProposeNone:
		return "·"
	default:
		return "·"
	}
}
