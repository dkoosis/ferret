package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

// cmdRetrieval scores get_nug retrieval episodes across the corpus (or one
// --session) into the RU northstar + the Phase-1 deterministic Q/R/C scorers.
// It mirrors `ferret conformance`: a dense text scorecard or the json schema,
// reading the events artifact the summary/report commands already build.
func cmdRetrieval() error {
	cmd := &CLI.Retrieval
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	eps, err := buildRetrievalEpisodes(c.eventsPath(), cmd.Session)
	if err != nil {
		return err
	}
	roll := score.Aggregate(eps)
	if c.format == fmtJSON {
		return writeRetrievalJSON(os.Stdout, cmd.Session, roll, eps, c.limit)
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	writeRetrievalText(sink, cmd.Session, roll, eps)
	return nil
}

// buildRetrievalEpisodes reads the events artifact, groups events into ordered
// per-(project,session,agent) streams, and assembles each stream's retrieval
// episodes. A --session prefix scopes to matching streams. Subagent transcripts
// are separate streams: interleaving them would fabricate episodes that never ran.
func buildRetrievalEpisodes(eventsPath, sessionPrefix string) ([]score.Episode, error) {
	streams := map[string][]event.Event{}
	var order []string
	err := event.Read(eventsPath, func(ev *event.Event) error {
		if sessionPrefix != "" && !sessionMatches(ev.Session, sessionPrefix) {
			return nil
		}
		key := ev.Project + "/" + ev.Session + "@" + ev.Agent
		if _, ok := streams[key]; !ok {
			order = append(order, key)
		}
		streams[key] = append(streams[key], *ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var eps []score.Episode
	for _, key := range order {
		evs := streams[key]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
		eps = append(eps, score.BuildEpisodes(evs)...)
	}
	return eps, nil
}

// sessionMatches reports whether a session id is selected by the prefix flag —
// a leading-prefix or substring match, the same loose rule `tokens` uses.
func sessionMatches(session, prefix string) bool {
	return strings.HasPrefix(session, prefix) || strings.Contains(session, prefix)
}

// writeRetrievalText emits the dense human scorecard: the RU northstar in both
// consumed variants with its three component legs, then the Q/R/C scorers, then
// the worst-offending episodes. The about-lines say what each number measures.
func writeRetrievalText(sink *out.Sink, session string, r score.Rollup, eps []score.Episode) {
	about(sink,
		"≡ retrieval: get_nug search quality. RU = served rate = consumed ∧ first-try ∧ ¬abandoned,",
		"≡ over answerable episodes (coverage gaps + good-abandonments excluded — they measure the store).",
		"≡ strict = explicit id/content use · loose = + circumstantial/negative tells. Gate for the number, read legs for why.")

	scope := "corpus"
	if session != "" {
		scope = "session=" + session
	}
	sink.Head("retrieval %s episodes=%d answerable=%d", scope, r.Episodes, r.Answerable)
	sink.Head("RU      strict=%.2f loose=%.2f", r.RUStrict, r.RULoose)
	sink.Head("  legs  consumed strict=%.2f/loose=%.2f · firsttry=%.2f · nonabandon=%.2f",
		r.ConsumedRateStrict, r.ConsumedRateLoose, r.FirstTryRate, r.NonAbandonRate)
	sink.Head("query   Q1 self-requery=%.2f · Q2 mean-depth=%.2f", r.Q1SelfRequeryRate, r.Q2MeanDepth)
	sink.Head("result  R1 hit strict=%.2f/loose=%.2f · R2 MRR=%.2f (n=%d) · R7 grounding=%.2f (n=%d)",
		r.R1HitStrict, r.R1HitLoose, r.R2MRR, r.R2Ranked, r.R7GroundingRate, r.R7Retrieving)
	sink.Head("  R3a   empty=%d (%.2f) · oversized=%d (%.2f)",
		r.R3aEmpty, r.R3aEmptyRate, r.R3aOversized, r.R3aOversizedRate)
	sink.Head("cover   C1 coverage-gap=%d · C2 good-abandon=%d", r.C1CoverageGap, r.C2GoodAbandon)

	if len(eps) == 0 {
		return
	}
	sink.Head("episodes (✗ = answerable miss · ! = self-requery · ∅ = empty · gap/abandon = excluded):")
	for i := range eps {
		e := eps[i]
		sink.Row("  %s %-7s %s  %s", episodeMark(e), e.Outcome, episodeFlags(e), truncQuery(e.Query))
	}
}

// episodeMark is the leading glyph: an answerable miss (returned, not served) is
// the actionable case; an excluded episode is parked.
func episodeMark(e score.Episode) string {
	switch {
	case !e.Answerable:
		return "·"
	case e.ServedLoose():
		return "✓"
	default:
		return "✗"
	}
}

// episodeFlags renders the compact per-episode signal set.
func episodeFlags(e score.Episode) string {
	s := fmt.Sprintf("q%d r%d", e.Queries, e.Results)
	if e.SelfRequery {
		s += " !requery"
	}
	if e.EmptyResult {
		s += " ∅"
	}
	if e.Oversized {
		s += " oversized"
	}
	if e.ConsumedStrict {
		s += fmt.Sprintf(" used@%d", e.ConsumedRank)
	} else if e.ConsumedLoose {
		s += " used~loose"
	}
	if e.CoverageGap {
		s += " coverage-gap"
	}
	if e.GoodAbandon {
		s += " good-abandon"
	}
	if hop := e.Hop(); hop != "none" {
		s += " hop:" + string(hop)
	}
	return s
}

func truncQuery(q string) string {
	const queryCap = 60
	if len(q) <= queryCap {
		return q
	}
	return q[:queryCap] + "…"
}

// writeRetrievalJSON emits the scored rollup (schema is the contract) plus the
// per-episode rows, capped by --limit.
func writeRetrievalJSON(w io.Writer, session string, r score.Rollup, eps []score.Episode, limit int) error {
	capped := eps
	if limit > 0 && len(capped) > limit {
		capped = capped[:limit]
	}
	scope := "corpus"
	if session != "" {
		scope = session
	}
	return out.JSON(w, map[string]any{
		"scope":      scope,
		"rollup":     r,
		"episodes":   capped,
		keyTotal:     len(eps),
		keyTruncated: len(capped) < len(eps),
	})
}
