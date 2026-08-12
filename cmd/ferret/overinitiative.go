package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
)

// over-initiative (ferret-bbp.18) is the standalone LLM leg the deterministic
// dialogue floor cannot cover: the NO-PUSHBACK case. The floor (CauseOverInitiative)
// fires only when the human reverses an unrequested action in their own words; when
// the agent acts beyond the ask and the human lets it stand, there is no reaction to
// read. This command walks a session, gates to episodes where the agent took a
// MUTATING action and the following human turn did NOT push back (so the floor did
// not already own it), and asks the analyst to judge the opening prompt's SCOPE
// against the action — results-blind. dk validates a sample before the score is
// trusted in a loop.

const overInitPromptCap = 600 // opening prompt chars sent to the judge / rendered

var errOverInitSessionRequired = usage("over-initiative: --session PREFIX required")

// mutatingTools is the precision-first v1 set: the built-in tools that
// unambiguously mutate durable state. Bash is deliberately EXCLUDED — it is
// read-capable (ls/cat/rg dominate real transcripts), so gating on it would flood
// the judge with read-only episodes; catching git/rm-via-Bash over-initiative is a
// follow-up. Anchor for treating these as the irreversible surface: OWASP LLM06
// Excessive Agency.
var mutatingTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// isMutatingTool reports whether a tool_use name is in the mutating set the
// no-pushback gate keys on.
func isMutatingTool(name string) bool { return mutatingTools[name] }

// actionDetail pulls a short target out of a mutating tool's input — the path it
// wrote or edited — so the judge weighs WHAT was mutated, not just that something
// was. Best-effort: a tool whose input doesn't carry a path (or won't decode)
// contributes a bare tool name, which still lists cleanly.
func actionDetail(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	if fields.FilePath != "" {
		return fields.FilePath
	}
	return fields.NotebookPath
}

// overInitCandidate is one no-pushback episode: the opening prompt and the mutating
// actions the agent took before the next (non-pushback) human turn. The LLM leg
// judges scope-vs-action per candidate.
type overInitCandidate struct {
	Prompt  string
	Actions []analyst.AgentAction
}

// isPushbackTurn reports whether a genuine human turn objected to what the agent
// did — a repair/reject move, or an explicit over-initiative reversal. Those cases
// belong to the deterministic floor (dialogue.CauseOverInitiative), so this LLM leg
// skips them: it exists only for the episodes the floor cannot read.
func isPushbackTurn(prompt string) bool {
	move, _ := dialogue.TagMove(prompt)
	if dialogue.IsRepairMove(move) {
		return true
	}
	if cause, _, ok := dialogue.TagAgentCause(prompt); ok && cause == dialogue.CauseOverInitiative {
		return true
	}
	return false
}

// overInitAction is one mutating tool_use recorded into the open window, kept
// alongside its tool_use_id so a later tool_result on the same id can drop it —
// and marked dropped rather than deleted so call order survives the resolution.
type overInitAction struct {
	analyst.AgentAction
	id      string
	dropped bool
}

// overInitState accumulates one window: the opening prompt and the mutating actions
// the agent took after it, pending the next human turn's verdict.
type overInitState struct {
	prompt string
	raw    []overInitAction
	open   bool
}

// applyAssistant folds an assistant turn's mutating tool calls into the open window,
// optimistically — a tool_use is recorded before its tool_result is known, and
// applyToolResult drops it if the result later reports failure/rejection.
func (st *overInitState) applyAssistant(blocks transcript.Blocks) {
	if !st.open {
		return
	}
	for i := range blocks {
		b := &blocks[i]
		if b.Type == "tool_use" && isMutatingTool(b.Name) {
			st.raw = append(st.raw, overInitAction{
				AgentAction: analyst.AgentAction{Tool: b.Name, Detail: actionDetail(b.Input)},
				id:          b.ID,
			})
		}
	}
}

// applyToolResult drops the mutating action matching this tool_result's id when the
// result reports is_error. CC surfaces a user-rejected permission prompt the same
// way as an execution failure — both mean the mutation never landed, so neither may
// reach the no-pushback judge as evidence of an unrequested action (ferret-xg4). A
// tool_result with no matching pending id (already resolved, or for a non-mutating
// tool this window never recorded) is a no-op.
func (st *overInitState) applyToolResult(blk *transcript.Block) {
	if !st.open || blk.ToolUseID == "" || blk.IsError == nil || !*blk.IsError {
		return
	}
	for i := range st.raw {
		if st.raw[i].id == blk.ToolUseID && !st.raw[i].dropped {
			st.raw[i].dropped = true
			return
		}
	}
}

// landedActions returns the window's mutating actions whose tool_use was never
// dropped by a failed/rejected tool_result, in call order. An action whose
// tool_result never arrived (transcript ended, or the result line was skipped)
// stays landed — absence of a verdict is not evidence of failure.
func (st *overInitState) landedActions() []analyst.AgentAction {
	if len(st.raw) == 0 {
		return nil
	}
	out := make([]analyst.AgentAction, 0, len(st.raw))
	for _, a := range st.raw {
		if !a.dropped {
			out = append(out, a.AgentAction)
		}
	}
	return out
}

// applyUser processes a genuine or folded human turn, closing the prior window via
// emit and opening the next. Carriers leave the window standing (system envelopes,
// not intent); folded affirmations/controls close it WITHOUT emitting — the human
// engaged, and an affirmation ("go ahead") authorizes the action rather than
// reversing it. A genuine turn closes the prior window, emitting a candidate only
// when the human did NOT push back (the floor owns the pushback case). Mirrors
// runDialogue's fold handling.
func (st *overInitState) applyUser(prompt string, emit func()) {
	if skip, kind, _ := turn.ClassifyBoundary(prompt); skip {
		if kind == "carrier" {
			return
		}
		*st = overInitState{}
		return
	}
	if !isPushbackTurn(prompt) {
		emit()
	}
	*st = overInitState{prompt: prompt, open: true}
}

// applyLine decodes one transcript line and routes it into the walk. Decode failures
// bump the counter and drop the line (the walk tolerates a corrupt line, mirroring
// tagUserTurn); the bare return keeps the ReadLines callback's error path clean.
func (st *overInitState) applyLine(line []byte, decodeErrs *int, emit func()) {
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		*decodeErrs++
		return
	}
	if raw.IsMeta || raw.Message == nil {
		return
	}
	switch raw.Type {
	case roleAssistant:
		st.applyAssistant(raw.Message.Content)
	case roleUser:
		// tool_result blocks land on user-role lines and must resolve pending
		// mutations even on a tool_result-only line, where turn.PromptText returns
		// "" and applyUser below never fires — checking both independently is what
		// makes those lines visible to the gate.
		for i := range raw.Message.Content {
			if b := &raw.Message.Content[i]; b.Type == "tool_result" {
				st.applyToolResult(b)
			}
		}
		if prompt := turn.PromptText(raw.Message.Content); prompt != "" {
			st.applyUser(prompt, emit)
		}
	}
}

// collectOverInitiativeCandidates walks a transcript and returns the no-pushback
// episodes worth judging: the agent took ≥1 mutating action after an opening prompt,
// and the human turn that closed the window did NOT push back (or the transcript
// ended, so the human let it stand).
func collectOverInitiativeCandidates(path string) (cands []overInitCandidate, decodeErrs int, err error) {
	cands = []overInitCandidate{}
	var st overInitState
	emit := func() {
		if !st.open {
			return
		}
		if acts := st.landedActions(); len(acts) > 0 {
			cands = append(cands, overInitCandidate{Prompt: st.prompt, Actions: acts})
		}
	}
	if rerr := transcript.ReadLines(path, func(line []byte) error {
		st.applyLine(line, &decodeErrs, emit)
		return nil
	}); rerr != nil {
		return nil, decodeErrs, rerr
	}
	emit() // transcript ended: the human never followed, so the action stood
	return cands, decodeErrs, nil
}

// oiFinding is one flagged over-initiative episode in the emission.
type oiFinding struct {
	Prompt     string   `json:"prompt"`
	Actions    []string `json:"actions"`
	Scope      string   `json:"scope"`
	Why        string   `json:"why"`
	Confidence string   `json:"confidence"`
}

// oiResult is the whole over-initiative pass for one session.
type oiResult struct {
	Session    string      `json:"session"`
	Model      string      `json:"model,omitempty"`
	Episodes   int         `json:"episodes"` // no-pushback candidates judged
	Flagged    []oiFinding `json:"flagged"`
	DecodeErrs int         `json:"decodeErrs,omitempty"`
}

// cmdOverInitiative runs the no-pushback over-initiative judge over one session.
// --emit-prompt assembles and prints each candidate's judge prompt without a network
// call, so the pipeline (and dk's validation sample) is exercisable before a key is
// wired up. Mirrors cmdAdjudicate's shape.
func cmdOverInitiative() error {
	cmd := &CLI.OverInitiative
	if cmd.Session == "" {
		return errOverInitSessionRequired
	}
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	src, distinct, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	warnAmbiguousSession(cmd.Session, distinct, src.Session, "scoring")
	cands, decodeErrs, err := collectOverInitiativeCandidates(src.Path)
	if err != nil {
		return err
	}

	if cmd.EmitPrompt {
		return emitOverInitPrompts(os.Stdout, cands)
	}

	ctx, cfg, stop, err := newAnalystRun(cmd.Model, cmd.Timeout)
	if err != nil {
		return err
	}
	defer stop()

	res, err := judgeOverInitiative(ctx, cfg, src.Session, cands, decodeErrs)
	if err != nil {
		return err
	}
	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeOverInitText(os.Stdout, res)
}

// runOverInitJudge is the per-candidate judge seam — analyst.RunOverInitiative in
// production, a fake in tests so fan-out order/fail-fast are exercisable without
// a live API (same shape as hop1Judge).
var runOverInitJudge = analyst.RunOverInitiative

// judgeOverInitiative runs the analyst over each no-pushback candidate and collects
// the flagged verdicts (the actionable subset dk validates). A single episode error
// fails the pass — a half-scored batch would read as a false baseline.
//
// Judge calls fan out across an 8-wide semaphore (ferret-fk8, mirroring
// judgeRecallRuns): verdicts land in index-aligned slots and are collected in
// candidate order, so concurrency changes neither Flagged order nor which
// episode's model is reported. The first error cancels the shared context
// (fail-fast, same as the sequential loop's abort) and is returned once every
// in-flight call has drained.
func judgeOverInitiative(ctx context.Context, cfg analyst.Config, session string, cands []overInitCandidate, decodeErrs int) (oiResult, error) {
	const maxConcurrency = 8

	type oiOutcome struct {
		verdict analyst.OverInitiativeVerdict
		model   string
	}
	outcomes := make([]oiOutcome, len(cands))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error

	for i, c := range cands {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		// The semaphore send can block until after ctx is canceled — re-check so
		// that race doesn't launch one extra paid call (same guard as
		// judgeRecallRuns, from the Codex adversarial review of #95).
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			v, model, err := runOverInitJudge(ctx, cfg, truncateRunes(c.Prompt, overInitPromptCap), c.Actions)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("over-initiative episode: %w", err)
					cancel()
				})
				return
			}
			outcomes[i] = oiOutcome{verdict: v, model: model}
		})
	}
	wg.Wait()

	if firstErr != nil {
		return oiResult{}, firstErr
	}
	// A parent-context cancel (SIGINT) with no judge error must still fail the
	// pass — the sequential loop surfaced it as the next call's error.
	if err := ctx.Err(); err != nil {
		return oiResult{}, fmt.Errorf("over-initiative episode: %w", err)
	}

	res := oiResult{Session: session, Episodes: len(cands), DecodeErrs: decodeErrs}
	for i, o := range outcomes {
		if res.Model == "" {
			res.Model = o.model
		}
		if o.verdict.OverInitiative {
			res.Flagged = append(res.Flagged, oiFinding{
				Prompt:     truncateRunes(cands[i].Prompt, dialogueCap),
				Actions:    actionLabels(cands[i].Actions),
				Scope:      o.verdict.Scope,
				Why:        o.verdict.Why,
				Confidence: o.verdict.Confidence,
			})
		}
	}
	return res, nil
}

// actionLabels renders each action as "Tool: detail" (or bare "Tool") for display.
func actionLabels(actions []analyst.AgentAction) []string {
	labels := make([]string, 0, len(actions))
	for _, a := range actions {
		if a.Detail != "" {
			labels = append(labels, a.Tool+": "+a.Detail)
		} else {
			labels = append(labels, a.Tool)
		}
	}
	return labels
}

// emitOverInitPrompts prints each candidate's assembled judge prompt without calling
// the model — the no-key path for wiring checks and dk's validation sample.
func emitOverInitPrompts(w io.Writer, cands []overInitCandidate) error {
	bw := bufio.NewWriter(w)
	for i, c := range cands {
		system, user := analyst.BuildOverInitiativePrompt(truncateRunes(c.Prompt, overInitPromptCap), c.Actions)
		if i == 0 {
			fmt.Fprintln(bw, "=== SYSTEM ===")
			fmt.Fprintln(bw, system)
		}
		fmt.Fprintf(bw, "\n=== USER (candidate %d) ===\n", i+1)
		fmt.Fprintln(bw, user)
	}
	if len(cands) == 0 {
		fmt.Fprintln(bw, "no no-pushback episodes with a mutating action")
	}
	return bw.Flush()
}

// writeOverInitText renders the flagged episodes densely: the actionable subset dk
// validates, each showing the scoped ask, the mutating actions, and the one-line
// rationale + confidence.
func writeOverInitText(w io.Writer, res oiResult) error {
	sink := out.NewSink(w, 0, 0)
	defer sink.Close()
	about(sink,
		"≡ over-initiative: LLM judge of the NO-PUSHBACK case — the agent mutated beyond the ask and the human let it stand.",
		"≡ the deterministic floor catches the pushback case; this leg reads opening-prompt SCOPE vs mutating actions. dk validates.")
	sink.Head("over-initiative session=%s model=%s episodes=%d flagged=%d", res.Session, res.Model, res.Episodes, len(res.Flagged))
	if len(res.Flagged) == 0 {
		sink.Head("no over-initiative flagged")
	}
	for i := range res.Flagged {
		f := &res.Flagged[i]
		sink.Row("⚑ [%s] scope=%s  %s", f.Confidence, f.Scope, f.Why)
		sink.Row("    ask: %s", f.Prompt)
		for _, a := range f.Actions {
			sink.Row("    did: %s", a)
		}
	}
	if res.DecodeErrs > 0 {
		sink.Head("decode-errs=%d", res.DecodeErrs)
	}
	return nil
}
