package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/analyst"
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
	if err := validateHop1(cmd.Hop1, cmd.Session); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	eps, err := buildRetrievalEpisodes(c.eventsPath(), cmd.Session)
	if err != nil {
		return err
	}
	if cmd.Hop1 {
		return runRetrievalHop1(c, eps)
	}
	roll := score.Aggregate(eps)
	if c.format == fmtJSON {
		return writeRetrievalJSON(os.Stdout, cmd.Session, roll, eps, c.limit, nil)
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

// retrievalScopeText labels a scorecard's scope for the human-readable renderers:
// "corpus" for the whole corpus, "session=<id>" when scoped.
func retrievalScopeText(session string) string {
	if session == "" {
		return "corpus"
	}
	return "session=" + session
}

// writeRetrievalText emits the dense human scorecard: the RU northstar in both
// consumed variants with its three component legs, then the Q/R/C scorers, then
// the worst-offending episodes. The about-lines say what each number measures.
func writeRetrievalText(sink *out.Sink, session string, r score.Rollup, eps []score.Episode) {
	about(sink,
		"≡ retrieval: get_nug search quality. RU = served rate = consumed ∧ first-try ∧ ¬abandoned,",
		"≡ over answerable episodes (coverage gaps + good-abandonments excluded — they measure the store).",
		"≡ strict = explicit id/content use · loose = + circumstantial/negative tells. Gate for the number, read legs for why.")

	sink.Head("retrieval %s episodes=%d answerable=%d", retrievalScopeText(session), r.Episodes, r.Answerable)
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

// hop1JSON is one episode's Hop1 verdict as it rides on the episode's JSON object
// (the joinable per-episode signal the retrieval-outcome contract reads back). The
// episode identifies itself, so this omits Hop1Result.Episode and adds the
// per-episode error string.
type hop1JSON struct {
	Grade        analyst.Hop1Grade `json:"grade,omitempty"`
	Why          string            `json:"why,omitempty"`
	LLMCalled    bool              `json:"llmCalled"`
	InputTokens  int64             `json:"inputTokens,omitempty"`
	OutputTokens int64             `json:"outputTokens,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// episodeJSON is an episode plus its optional Hop1 verdict; the embedded Episode's
// fields promote to the top level so the object is the plain episode shape with a
// sibling "hop1" key (omitted when --hop1 wasn't passed).
type episodeJSON struct {
	score.Episode
	Hop1 *hop1JSON `json:"hop1,omitempty"`
}

// writeRetrievalJSON emits the scored rollup (schema is the contract) plus the
// per-episode rows, capped by --limit. When hop1 rows are supplied (--hop1), each
// episode's object gains a "hop1" verdict and the envelope a session-level
// "hop1Cost" token sum; nil rows leave the common path free of hop1 noise.
func writeRetrievalJSON(w io.Writer, session string, r score.Rollup, eps []score.Episode, limit int, hop1 []hop1Row) error {
	capped := eps
	if limit > 0 && len(capped) > limit {
		capped = capped[:limit]
	}
	scope := "corpus"
	if session != "" {
		scope = session
	}
	payload := map[string]any{
		"scope":      scope,
		"rollup":     r,
		keyTotal:     len(eps),
		keyTruncated: len(capped) < len(eps),
	}
	if hop1 == nil {
		payload["episodes"] = capped
	} else {
		payload["episodes"] = wrapEpisodesHop1(capped, hop1)
		var inTot, outTot int64
		for i := range hop1 {
			inTot += hop1[i].Result.InputTokens
			outTot += hop1[i].Result.OutputTokens
		}
		payload["hop1Cost"] = map[string]int64{"inputTokens": inTot, "outputTokens": outTot}
	}
	return out.JSON(w, payload)
}

// wrapEpisodesHop1 merges each capped episode with its Hop1 verdict (aligned by
// index). Burn is summed over ALL rows by the caller, not just the capped subset.
func wrapEpisodesHop1(capped []score.Episode, hop1 []hop1Row) []episodeJSON {
	wrapped := make([]episodeJSON, len(capped))
	for i := range capped {
		wrapped[i].Episode = capped[i]
		if i < len(hop1) {
			r := hop1[i].Result
			wrapped[i].Hop1 = &hop1JSON{
				Grade:        r.Grade,
				Why:          r.Why,
				LLMCalled:    r.LLMCalled,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				Error:        hop1[i].Err,
			}
		}
	}
	return wrapped
}
