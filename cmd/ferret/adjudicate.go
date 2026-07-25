package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
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

var errAdjSessionRequired = errors.New("adjudicate: --session PREFIX required")

// errNoRecallRuns is the empty-honest sentinel: a recall-trace with no runs is a
// hard error, never a silent zero-finding green (the downstream metric must not
// read a false baseline off an empty batch). Wrapped with the path at the call.
var errNoRecallRuns = errors.New("adjudicate: recall-trace has no runs")

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
	if cmd.RecallTrace != "" {
		return runRecallTrace()
	}
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
	src, _, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
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
	cfg := analyst.Config{Model: model, Timeout: timeout}
	ok, err := cfg.HasAPIKey()
	if err != nil {
		return analyst.Result{}, err
	}
	if !ok {
		return analyst.Result{}, analyst.ErrNoAPIKey
	}
	ctx, stop := analystContext()
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
	src, _, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	var bundle bytes.Buffer
	if err := candidates(&bundle, root, cmd.Session, fmtJSON, cmd.Top, ""); err != nil {
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

	cfg := analyst.Config{Model: cmd.Model, Timeout: cmd.Timeout}
	ok, err := cfg.HasAPIKey()
	if err != nil {
		return err
	}
	if !ok {
		return analyst.ErrNoAPIKey
	}
	ctx, stop := analystContext()
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
	sink.Head(`next (template — fill from a proposal above): ferret fixes add --motif <tokens> --fix <action>`)
	return nil
}

// recallRun mirrors one row of trixi-bot's recall-trace.jsonl (its
// agent.RecallTrace): the fragments memory surfaced at run start paired with the
// final answer they may or may not have shaped. Field tags match the emitter
// (trixi-bot internal/core/agent/trace.go) so the JSONL parses verbatim; the
// timestamp/chat fields the judge doesn't need are ignored on decode.
type recallRun struct {
	RunID     string             `json:"run_id"`
	Recalled  []recalledFragment `json:"recalled"`
	FinalText string             `json:"final_text"`
	Outcome   string             `json:"outcome"`
}

type recalledFragment struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// items projects the row's fragments onto the analyst's input shape.
func (r recallRun) items() []analyst.RecalledItem {
	out := make([]analyst.RecalledItem, len(r.Recalled))
	for i, f := range r.Recalled {
		out[i] = analyst.RecalledItem{Source: f.Source, Text: f.Text}
	}
	return out
}

// runRecallTrace is the memory-use branch (ferret-izo): instead of a transcript
// session it reads trixi-bot's recall-trace.jsonl — recalled fragments + the
// final answer per run — and judges, per run, whether each fragment shaped the
// answer. Findings across all runs aggregate into ONE Result whose findings shape
// trixi-bot's eval/adjudicate mapper (gg-8rq.16.2) consumes unchanged. The CLI
// seam is the contract: subprocess, --format json, keychain key — the judge lives
// here, never reimplemented downstream.
func runRecallTrace() error {
	cmd := &CLI.Adjudicate
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	runs, err := readRecallTrace(cmd.RecallTrace)
	if err != nil {
		return err
	}
	// The downstream metric (gg-8rq.16.3) must not read a false baseline off an
	// empty batch; the consumer surfaces this error through its --trace seam.
	if len(runs) == 0 {
		return fmt.Errorf("%w: %q", errNoRecallRuns, cmd.RecallTrace)
	}

	if cmd.EmitPrompt {
		emitRecallPrompts(os.Stdout, runs)
		return nil
	}

	cfg := analyst.Config{Model: cmd.Model, Timeout: cmd.Timeout}
	ok, err := cfg.HasAPIKey()
	if err != nil {
		return err
	}
	if !ok {
		return analyst.ErrNoAPIKey
	}
	ctx, stop := analystContext()
	defer stop()

	judge := func(jctx context.Context, r recallRun) ([]analyst.Finding, string, error) {
		return analyst.RunRecallTrace(jctx, cfg, r.FinalText, r.Outcome, r.items())
	}
	res, err := judgeRecallRuns(ctx, runs, judge)
	if err != nil {
		return err
	}
	res.Session = recallTraceLabel(cmd.RecallTrace)

	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeAdjudicateText(os.Stdout, res)
}

// recallJudgeFunc judges one run's recalled fragments against its final answer,
// returning the findings and the model that produced them. Abstracts
// analyst.RunRecallTrace so tests can inject a fake judge without a network call.
type recallJudgeFunc func(ctx context.Context, r recallRun) ([]analyst.Finding, string, error)

// judgeRecallRuns fans the per-run judge calls out across an 8-wide semaphore
// (ferret-nv3: sequential judging was O(n) wall-clock on large batches). Each
// run's result lands in its own slot of an index-aligned slice, so the
// concurrent fan-out cannot scramble finding order — callers downstream
// (gg-8rq.16.2) assume findings arrive in run order. The first error from any
// run cancels the shared context (cooperative fail-fast, mirroring the SIGINT
// wiring in analystContext) and is returned once every in-flight call has
// drained; it is never swallowed.
//
// A run with no recalled fragments has nothing to judge and is skipped — same
// as the prior sequential loop, it costs no paid call and leaves its result
// slot at its zero value.
func judgeRecallRuns(ctx context.Context, runs []recallRun, judge recallJudgeFunc) (analyst.Result, error) {
	const maxConcurrency = 8

	type runOutcome struct {
		findings []analyst.Finding
		model    string
	}
	results := make([]runOutcome, len(runs))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error

	for i, r := range runs {
		if ctx.Err() != nil {
			break
		}
		if len(r.Recalled) == 0 {
			continue
		}
		sem <- struct{}{}
		// sem<-struct{}{} can block on a full semaphore and only unblock once
		// ctx is already canceled (a slot freed by another run's early exit) —
		// re-check here so that race doesn't still launch one extra judge call
		// (Codex adversarial review of #95).
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			findings, model, err := judge(ctx, r)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("adjudicate recall-trace run %s: %w", r.RunID, err)
					cancel()
				})
				return
			}
			results[i] = runOutcome{findings: findings, model: model}
		})
	}
	wg.Wait()

	if firstErr != nil {
		return analyst.Result{}, firstErr
	}

	var res analyst.Result
	for _, o := range results {
		if res.Model == "" && o.model != "" {
			res.Model = o.model
		}
		res.Findings = append(res.Findings, o.findings...)
	}
	return res, nil
}

// readRecallTrace parses a recall-trace.jsonl into its runs. One JSON object per
// line; blank lines are skipped; a malformed line fails loud with its line number
// (a truncated or corrupt batch must not be half-scored, mirroring the consumer's
// trailing-JSON rejection). The scanner buffer is raised because a run's final
// answer plus recalled fragments can exceed the 64KiB default.
func readRecallTrace(path string) ([]recallRun, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var runs []recallRun
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for line := 1; sc.Scan(); line++ {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var r recallRun
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("recall-trace %s line %d: %w", path, line, err)
		}
		runs = append(runs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("recall-trace %s: %w", path, err)
	}
	return runs, nil
}

// recallTraceLabel names the batch in the Result the way a session id names a
// spine adjudication — the trace file's basename, prefixed so it reads as a
// recall-trace batch rather than a Claude Code session.
func recallTraceLabel(path string) string {
	return "recall-trace:" + filepath.Base(path)
}

// emitRecallPrompts is the --emit-prompt escape hatch for the recall-trace mode:
// assemble and print each run's prompt without a network call, so the pipeline is
// exercisable before an API key is wired up (same convention as cmdAdjudicate).
func emitRecallPrompts(w io.Writer, runs []recallRun) {
	for i, r := range runs {
		if len(r.Recalled) == 0 {
			continue
		}
		system, user := analyst.BuildRecallPrompt(r.FinalText, r.Outcome, r.items())
		fmt.Fprintf(w, "=== RUN %d (%s) SYSTEM ===\n", i+1, r.RunID)
		fmt.Fprintln(w, system)
		fmt.Fprintf(w, "\n=== RUN %d (%s) USER ===\n", i+1, r.RunID)
		fmt.Fprintln(w, user)
		fmt.Fprintln(w)
	}
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
