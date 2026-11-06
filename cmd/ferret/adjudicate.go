package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

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

var errAdjSessionRequired = usage("adjudicate: --session PREFIX required")

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
	// a human would see, except every tool-arg value is placeholder-mapped
	// (ferret-5c0): a repeated volatile value (an absolute path, a URL) costs its
	// full string once and a short token on every repeat within this prompt.
	src, distinct, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	warnAmbiguousSession(cmd.Session, distinct, src.Session, "judging")
	ph := newPlaceholderTable()
	var buf bytes.Buffer
	if err := spine(&buf, root, cmd.Session, ph); err != nil {
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

	res, err := runAdjudicateAnalyst(cmd.Model, cmd.Timeout, src.Session, buf.String(), ph)
	if err != nil {
		return err
	}

	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeAdjudicateText(os.Stdout, res)
}

// runAdjudicateAnalyst runs the LLM analyst call and expands any placeholder
// token it echoes back — split out of cmdAdjudicate to keep it under the
// statement-count lint ceiling.
func runAdjudicateAnalyst(model string, timeout time.Duration, session, spineText string, ph *placeholderTable) (analyst.Result, error) {
	ctx, cfg, stop, err := newAnalystRun(model, timeout)
	if err != nil {
		return analyst.Result{}, err
	}
	defer stop()
	res, err := analyst.Run(ctx, cfg, session, spineText)
	if err != nil {
		return analyst.Result{}, err
	}
	// The model only ever saw placeholder tokens for volatile values; expand any
	// it echoes back into its own findings before dk sees them — a raw "[P1]"
	// must never reach human-facing output.
	expandFindings(res.Findings, ph)
	return res, nil
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

// expandFindings restores any placeholder token (ferret-5c0) an LLM finding
// echoes back — the model's own words about a tool call may quote a path or
// URL it only ever saw as a token — to its real value, in place. A nil ph (no
// mapping happened) or one with nothing registered is a no-op via
// placeholderTable.expand.
func expandFindings(findings []analyst.Finding, ph *placeholderTable) {
	for i := range findings {
		f := &findings[i]
		f.Task = ph.expand(f.Task)
		f.Call = ph.expand(f.Call)
		f.ToolUsed = ph.expand(f.ToolUsed)
		f.Better = ph.expand(f.Better)
		f.Why = ph.expand(f.Why)
	}
}

// expandProposals is expandFindings' propose-mode twin: restores any
// placeholder token echoed into a proposal's fix text or rationale.
func expandProposals(proposals []analyst.Proposal, ph *placeholderTable) {
	for i := range proposals {
		p := &proposals[i]
		p.Proposal = ph.expand(p.Proposal)
		p.Why = ph.expand(p.Why)
	}
}

// runPropose is the --propose branch (ferret-567 step 5): instead of judging
// calls already made, it feeds the deterministic cost-leak ranking ('ferret
// candidates') plus the spine to the analyst and returns one fix per task —
// automate the routine, or de-context the output. The candidate bundle is
// captured as JSON (the analyst feed) and the spine as the human-readable call
// detail; --emit-prompt assembles both without a network call.
func runPropose(root string) error {
	cmd := &CLI.Adjudicate
	src, distinct, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	warnAmbiguousSession(cmd.Session, distinct, src.Session, "proposing for")
	var bundle bytes.Buffer
	if err := candidates(sessionRun{w: &bundle, root: root, session: cmd.Session, format: fmtJSON}, cmd.Top, ""); err != nil {
		return err
	}
	// Same placeholder mapping as cmdAdjudicate (ferret-5c0): the propose prompt's
	// spine half gets a fresh table per render pass.
	ph := newPlaceholderTable()
	var spineBuf bytes.Buffer
	if err := spine(&spineBuf, root, cmd.Session, ph); err != nil {
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

	ctx, cfg, stop, err := newAnalystRun(cmd.Model, cmd.Timeout)
	if err != nil {
		return err
	}
	defer stop()
	res, err := analyst.RunPropose(ctx, cfg, src.Session, bundle.String(), spineBuf.String())
	if err != nil {
		return err
	}
	// Same reverse-mapping guarantee as cmdAdjudicate: dk never sees a raw token.
	expandProposals(res.Proposals, ph)

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
	// AXI #9 (contextual disclosure): chain the natural next stage — record the
	// acted-on fix in the ledger. Head-style so the hint survives row truncation
	// (AXI #8, data first). Only reached when actionable proposals exist.
	// Unlike the candidates→propose hint, this one is a fill-in template, not a
	// runnable line: Proposal carries free-text, not discrete motif/fix tokens, so
	// dk translates a proposal above into the two flags. Bare (unquoted) angle
	// brackets keep it from pasting cleanly into a shell and recording literal
	// placeholders in the ledger.
	sink.NextHead(`(template — fill from a proposal above) ferret fixes add --motif <tokens> --fix <action>`)
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
