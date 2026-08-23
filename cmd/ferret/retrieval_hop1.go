package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

// Hop1 is the LLM interp-fidelity leg of `ferret retrieval` (ferret-bbp.5): per
// episode, how faithfully did Claude's get_nug QUERY encode the human's ask. It
// rides the same episode slice the deterministic scorers use; the judge and its
// bias guards live in internal/analyst (Hop1 / RunCoverage). This file is the CLI
// seam: session guard, --emit-prompt assembly, the continue-on-error loop, and
// text/JSON rendering with the run's own token burn.

var (
	// errHop1RequiresSession blocks an unscoped --hop1: without --session it would
	// fan the paid judge across the whole corpus on every invocation — the exact
	// uncontrolled per-run cost the staged-LLM decision rules out.
	errHop1RequiresSession = errors.New("retrieval --hop1 requires --session (an unscoped judge would fan out across the whole corpus)")
	// errHop1PartialFailure marks a non-zero exit when at least one episode's judge
	// call failed; the run still completed and reported every episode.
	errHop1PartialFailure = errors.New("retrieval --hop1: one or more episodes failed to judge (see per-episode error rows)")
)

// validateHop1 enforces the session guard. Split out so the flag contract is
// testable without a live command.
func validateHop1(hop1 bool, session string) error {
	if hop1 && session == "" {
		return errHop1RequiresSession
	}
	return nil
}

// runRetrievalHop1 is the --hop1 branch of cmdRetrieval. The session guard runs
// earlier in cmdRetrieval, before the corpus is even loaded. It honours
// --emit-prompt (no network), then judges each episode — continuing past any
// single episode's failure — and renders the result with burn.
func runRetrievalHop1(c *common, eps []score.Episode) error {
	cmd := &CLI.Retrieval
	if cmd.EmitPrompt {
		fmt.Fprint(os.Stdout, hop1EmitPrompts(eps))
		return nil
	}

	// No API-key precheck here: a session of all-floor episodes never calls the
	// model, so requiring a key upfront would fail runs that cost nothing. Each
	// escalating episode's own call surfaces ErrNoAPIKey through the normal
	// continue-on-error per-episode path if no key is set.
	cfg := newAnalystConfig(cmd.Model, cmd.Timeout)
	ctx, stop := analystContext()
	defer stop()

	rows, anyErr := runHop1Episodes(ctx, cfg, eps, analyst.Hop1)
	roll := score.Aggregate(eps)
	if c.format == fmtJSON {
		if err := writeRetrievalJSON(os.Stdout, cmd.Session, roll, eps, c.limit, rows); err != nil {
			return err
		}
	} else {
		// Text --hop1 augments the deterministic scorecard rather than replacing
		// it — the JSON path keeps "rollup" too, so the two formats carry the same
		// content.
		sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
		defer sink.Close()
		writeRetrievalText(sink, cmd.Session, roll, eps)
		writeRetrievalHop1Text(sink, cmd.Session, rows)
	}
	if anyErr {
		return errHop1PartialFailure
	}
	return nil
}

// hop1Row pairs a judged episode's result with a per-episode error string (a
// failed call keeps the other already-paid-for judgments; the error rides here
// rather than aborting the run). Empty Err = judged (or floored) cleanly.
type hop1Row struct {
	Result analyst.Hop1Result
	Err    string
}

// hop1Judge is the seam the loop calls per episode — analyst.Hop1 in production,
// a fake in tests so continue-on-error is exercisable without a live API.
type hop1Judge func(ctx context.Context, cfg analyst.Config, episodeID string, ep score.Episode) (analyst.Hop1Result, error)

// runHop1Episodes judges every episode, continuing past a per-episode failure: a
// transient single-episode error never discards the run's other judgments. anyErr
// reports whether at least one episode failed (the caller's non-zero-exit signal).
//
// Judge calls fan out across an 8-wide semaphore (ferret-fk8, mirroring
// judgeRecallRuns): each episode's row lands in its own slot of an index-aligned
// slice, so concurrency cannot scramble output order. Unlike judgeRecallRuns
// there is no fail-fast — a per-episode error fills that slot and the rest keep
// judging, same as the prior sequential loop. Cancellation stops launching new
// calls; episodes never launched produce no row.
func runHop1Episodes(ctx context.Context, cfg analyst.Config, eps []score.Episode, judge hop1Judge) (rows []hop1Row, anyErr bool) {
	const maxConcurrency = 8

	slots := make([]hop1Row, len(eps))
	launched := make([]bool, len(eps))
	failed := make([]bool, len(eps))

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i := range eps {
		if ctx.Err() != nil {
			anyErr = true
			break
		}
		sem <- struct{}{}
		// The semaphore send can block until after ctx is canceled — re-check so
		// that race doesn't launch one extra paid call (same guard as
		// judgeRecallRuns, from the Codex adversarial review of #95).
		if ctx.Err() != nil {
			<-sem
			anyErr = true
			break
		}
		launched[i] = true
		ep := eps[i]
		wg.Go(func() {
			defer func() { <-sem }()
			id := hop1EpisodeID(ep)
			res, err := judge(ctx, cfg, id, ep)
			if err != nil {
				res.Episode = id
				res.LLMCalled = true
				slots[i] = hop1Row{Result: res, Err: err.Error()}
				failed[i] = true
				return
			}
			slots[i] = hop1Row{Result: res}
		})
	}
	wg.Wait()

	rows = make([]hop1Row, 0, len(eps))
	for i := range slots {
		if !launched[i] {
			continue
		}
		if failed[i] {
			anyErr = true
		}
		rows = append(rows, slots[i])
	}
	return rows, anyErr
}

// hop1EpisodeID labels an episode by its session and rooting sequence — stable and
// human-readable, the caller-supplied id analyst.Hop1 carries through.
func hop1EpisodeID(ep score.Episode) string {
	return fmt.Sprintf("%s#%d", ep.Session, ep.RootSeq)
}

// hop1EmitPrompts renders, per episode, the assembled judge prompt for an
// escalated (clean-first-try) episode or a one-line floor note for a floored one —
// the no-network dry run that mirrors `ferret adjudicate --emit-prompt`.
func hop1EmitPrompts(eps []score.Episode) string {
	var b strings.Builder
	for i := range eps {
		ep := eps[i]
		id := hop1EpisodeID(ep)
		if !analyst.Hop1Escalates(ep) {
			fmt.Fprintf(&b, "--- %s: %s ---\n", id, hop1FloorNote(ep))
			continue
		}
		system, user := analyst.BuildCoveragePrompt(ep.Prompt, ep.Query)
		fmt.Fprintf(&b, "--- %s: escalate ---\n=== SYSTEM ===\n%s\n\n=== USER ===\n%s\n\n", id, system, user)
	}
	return b.String()
}

// hop1FloorNote explains why a floored episode was decided without an LLM call.
func hop1FloorNote(ep score.Episode) string {
	switch {
	case ep.SelfRequery:
		return "floor: low (self-requery), no LLM call"
	case ep.RetryMotif:
		return "floor: low (retry-after-failure), no LLM call"
	default:
		return "no signal (no opening prompt captured), no LLM call"
	}
}

// writeRetrievalHop1Text emits the dense Hop1 scorecard: the run's own judge
// cost, then a per-episode grade + reason (or floor / error) row. This cost is
// deliberately NOT labeled "burn" — mine.Finding.Burn is pure waste (friction,
// loops) where sorting descending is correct; Hop1's tokens buy a diagnosis, so
// the raw count means nothing without weighing what it bought (memory
// ferret-hop1-cost-not-waste-2026-06-30). Never fold this into a burn×count sort.
func writeRetrievalHop1Text(sink *out.Sink, session string, rows []hop1Row) {
	about(sink,
		"≡ hop1: interp-fidelity — did Claude's query faithfully encode the human's ask? low|mid|high.",
		"≡ floor self-requery/retry → low (no LLM) · clean-first-try → judged (LLM, results-blind) · dk validates.")

	var inTot, outTot int64
	judged := 0
	for i := range rows {
		r := rows[i].Result
		inTot += r.InputTokens
		outTot += r.OutputTokens
		if r.LLMCalled {
			judged++
		}
	}
	sink.Head("hop1 %s episodes=%d llm-judged=%d", retrievalScopeText(session), len(rows), judged)
	sink.Head("judge cost in=%s out=%s total=%s (tokens paid for a diagnosis, not waste — not comparable to burn)", compactBurn(int(inTot)), compactBurn(int(outTot)), compactBurn(int(inTot+outTot)))

	for i := range rows {
		r := rows[i].Result
		grade := string(r.Grade)
		if grade == "" {
			grade = "-"
		}
		sink.Row("  %-5s %s", grade, r.Episode)
		switch {
		case rows[i].Err != "":
			sink.Row("    err: %s", rows[i].Err)
		case r.Why != "":
			sink.Row("    why: %s", r.Why)
		case !r.LLMCalled:
			sink.Row("    floor: no LLM call")
		}
	}
}
