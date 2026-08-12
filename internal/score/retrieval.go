package score

import (
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/event"
)

// Retrieval scoring (ferret-sq.1) is the always-on deterministic northstar for
// trixi get_nug search quality. It reads one session's ordered event stream and
// the human-turn moves beside it, never a ground-truth relevance label: the
// "following actions" after a search are implicit relevance feedback — the signal
// classic IR pays human judges to produce — read here for free off the trace.
// Prior art for treating downstream behaviour as a relevance label: Joachims,
// KDD 2002 (clickthrough as implicit feedback). The framing maps onto the
// two-hop episode model in internal/dialogue/attribute.go: query quality is the
// HopInterp leg, result quality the HopRetrieval leg.
//
// Everything here is a pure function of the event + dialogue stream: no time, no
// map-order output, no randomness — repeated runs on the same input are byte-stable.

const (
	// toolGetNug / toolSetNug are the trixi search + write tools. A query-mode
	// get_nug (Query != "") roots a retrieval episode; a by-id fetch carries no
	// query and is not an episode. A set_nug right after an empty search is the
	// coverage-gap tell (C1).
	toolGetNug = "mcp__trixi__get_nug"
	toolSetNug = "mcp__trixi__set_nug"

	// oversizeResultCap is the set-size QPP threshold (R3a): a result set larger
	// than this reads as a low-specificity query (the QPP "low clarity" analog;
	// Cronen-Townsend et al., SIGIR 2002). Tunable; get_nug's default page is 10.
	oversizeResultCap = 25
)

// Episode is one retrieval episode: a query-mode get_nug call (or a self-requery
// chain serving the same intent) plus the following actions up to the next human
// turn or task boundary. It is the per-task unit the retrieval scorers ride, and
// carries every deterministic signal a downstream rollup or the attribute-hop
// wiring needs — already reduced to booleans so attribution is a pure read.
type Episode struct {
	Session string `json:"session"`
	Project string `json:"project"`
	Agent   string `json:"agent,omitempty"`

	RootSeq int    `json:"rootSeq"`          // Seq of the rooting get_nug call
	Prompt  string `json:"prompt,omitempty"` // human turn that PRECEDED the root query ("" = none) — the Hop1 interp-fidelity judge's input
	Query   string `json:"query"`            // the (first) search string
	Queries int    `json:"queries"`          // get_nug calls in the chain — Q2 reformulation depth
	Results int    `json:"results"`          // size of the served (last query's) result set

	// ResultIDs is the served (last query's) candidate set, ranked, projected for
	// the JSON emit — the ranked ids the Results count alone drops. It is the
	// gold-marking target for downstream recall-eval fixtures (trixi-bot gg-zhj):
	// pure passthrough of event.Results, no new tracing.
	ResultIDs []ResultHit `json:"resultIds,omitempty"`

	SelfRequery bool `json:"selfRequery,omitempty"` // ≥2 get_nug calls before a consumed result — Q1 / HopInterp tell
	RetryMotif  bool `json:"retryMotif,omitempty"`  // a following action carried event.Retry (bd_show!→bd_show / retry-after-failure) — a HopInterp tell, kept SEPARATE from SelfRequery so RU's FirstTry leg stays the get_nug-chain-only signal
	EmptyResult bool `json:"emptyResult,omitempty"` // served set was empty — R3a / HopRetrieval tell
	Oversized   bool `json:"oversized,omitempty"`   // served set over the QPP cap — R3a tell

	// No omitempty on either: these are RU's primary consumption verdict — false
	// is "not consumed", a real finding, not "unevaluated" (mirrors GoalReached,
	// ferret-917).
	ConsumedStrict bool   `json:"consumedStrict"`       // tell 1: explicit id/content reference
	ConsumedLoose  bool   `json:"consumedLoose"`        // tell 1 ∨ 2 ∨ 3
	ConsumedID     string `json:"consumedId,omitempty"` // the referenced nug id (tell 1), for audit
	ConsumedRank   int    `json:"consumedRank,omitempty"`   // 1-based rank of the consumed id in Results (0 = no explicit rank) — R2

	ClosingMove dialogue.Move    `json:"closingMove,omitempty"` // move of the human turn that closed the episode
	Outcome     dialogue.Outcome `json:"outcome"`               // PARADISE rollup of the closing reaction

	CoverageGap bool `json:"coverageGap,omitempty"` // C1: empty search → set_nug (the nug didn't exist)
	GoodAbandon bool `json:"goodAbandon,omitempty"` // C2: confirmed absence was the useful answer
	Answerable  bool `json:"answerable"`            // in RU's denominator (not a coverage gap / good abandonment)
}

// ResultHit is one candidate nug in an episode's served result set, projected for
// the JSON emit: the nug id, its per-query match score, and its 1-based rank
// (slice position in the served set). Surfaces the ranked candidate list that the
// Results count alone drops.
type ResultHit struct {
	ID string `json:"id"`
	// No omitempty: 0.0 is a real, legitimate low-relevance score, not "unscored".
	Score float64 `json:"score"`
	Rank  int     `json:"rank"`
}

// projectResults ranks the served candidate set for the JSON emit: 1-based rank is
// the slice position, id + score read straight from event.Results. Returns nil for
// an empty set so the field stays omitempty.
func projectResults(hits []event.NugHit) []ResultHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]ResultHit, len(hits))
	for i, h := range hits {
		out[i] = ResultHit{ID: h.ID, Score: h.Score, Rank: i + 1}
	}
	return out
}

// FirstTry reports the query-was-right-first-time leg of RU (HopInterp clean).
func (e Episode) FirstTry() bool { return !e.SelfRequery }

// NonAbandon reports the human-outcome leg of RU: the human did not walk away or
// repair this retrieval hop.
func (e Episode) NonAbandon() bool { return e.Outcome != dialogue.OutcomeAbandoned }

// ServedStrict / ServedLoose are RU's per-episode AND-gate in each consumed
// variant: a returned nug was used, the query was right first time, and the human
// did not abandon. No blend — all three must hold (spec §RU).
func (e Episode) ServedStrict() bool {
	return e.ConsumedStrict && e.FirstTry() && e.NonAbandon()
}

func (e Episode) ServedLoose() bool {
	return e.ConsumedLoose && e.FirstTry() && e.NonAbandon()
}

// TurnContext projects the episode's deterministic signals into the dialogue
// attribution contract (fills internal/dialogue/attribute.go): the self-requery /
// empty / oversized booleans AttributeHop reads to place a repair onto the hop it
// indicts.
//
// SelfRequery here is the UNION of both self-correction tells — a get_nug
// reformulation chain (Episode.SelfRequery) OR a following action the event
// builder flagged as a retry-after-failure (Episode.RetryMotif, the
// bd_show!→bd_show motif). Either means the agent tried to fix its own action
// before the human spoke, which is the HopInterp signal. RU keeps the narrower
// SelfRequery; only this attribution view widens it.
func (e Episode) TurnContext() dialogue.TurnContext {
	return dialogue.TurnContext{
		SelfRequery:     e.SelfRequery || e.RetryMotif,
		EmptyResult:     e.EmptyResult,
		OversizedResult: e.Oversized,
	}
}

// Hop attributes the episode's closing human reaction to the retrieval hop it
// indicts, via the now-wired TurnContext. A non-repair reaction yields HopNone.
func (e Episode) Hop() dialogue.Hop {
	return dialogue.AttributeHop(e.ClosingMove, e.TurnContext())
}

// epBuild accumulates one in-progress episode as the event stream is walked.
type epBuild struct {
	session, project, agent string
	rootSeq                 int
	prompt                  string // human turn preceding the root query (Episode.Prompt)
	query                   string
	queries                 int
	results                 []event.NugHit
	selfRequery             bool
	retryMotif              bool // a following action carried event.Retry

	refID     string // first returned id referenced by a following action (tell 1)
	refRank   int    // its 1-based rank in results
	advanced  bool   // an Edit/Write task-advancing action followed (tell 2 input)
	sawSetNug bool   // a set_nug followed (coverage-gap input / tell-2 disqualifier)

	hasClose    bool
	closingMove dialogue.Move
}

// trixiAskAction / trixiSearchAction are the shellnorm-normalized Action tokens
// for a query-mode trixi CLI recall call (ferret-bbp.20) — the Bash-tool
// counterpart of toolGetNug. The event builder (internal/event/build.go)
// populates Query/Results identically to the MCP arm; admitting them here is
// scorer-side generalization only, no new capture logic.
const (
	trixiAskAction    = "trixi_ask"
	trixiSearchAction = "trixi_search"
)

// isQueryRetrieval reports whether an event roots a retrieval episode: a
// query-mode get_nug MCP call, or a query-mode trixi ask/search CLI shell call
// (ferret-bbp.20) — Query is set only on query-mode calls; by-id fetches (MCP
// get_nug by-id, CLI trixi get) carry none, on either arm.
func isQueryRetrieval(ev *event.Event) bool {
	if ev.Query == "" {
		return false
	}
	switch ev.Kind {
	case event.KindTool:
		return ev.Action == toolGetNug
	case event.KindShell:
		return ev.Action == trixiAskAction || ev.Action == trixiSearchAction
	default:
		return false
	}
}

// BuildEpisodes assembles the retrieval episodes from ONE session's events, in
// Seq order. A query-mode retrieval call — MCP get_nug or CLI trixi ask/search
// (ferret-bbp.20) — roots an episode; consecutive query-mode calls before any
// consumption or human turn fold in as a self-requery chain; a human turn closes
// the open episode as its reaction. Calls before the first search are not
// episodes (no query to score).
func BuildEpisodes(evs []event.Event) []Episode {
	var (
		eps        []Episode
		cur        *epBuild
		lastPrompt string // most recent human turn — the Prompt of any episode rooted after it
	)
	flush := func() {
		if cur != nil {
			eps = append(eps, cur.finalize())
			cur = nil
		}
	}
	for i := range evs {
		ev := &evs[i]
		switch {
		case ev.Kind == event.KindPrompt:
			if cur != nil {
				cur.closingMove, _ = dialogue.TagMove(ev.Prompt)
				cur.hasClose = true
				flush()
			}
			// Update AFTER closing: a prompt closes the open episode as its
			// reaction, then becomes the preceding-prompt for the next one.
			lastPrompt = ev.Prompt
		case isQueryRetrieval(ev):
			// Fold a reformulation into the open episode only while it serves the
			// same intent: no consuming action and no human turn since the root.
			if cur != nil && cur.foldsReformulation() {
				cur.addQuery(ev)
			} else {
				flush()
				cur = newEpBuild(ev, lastPrompt)
			}
		case cur != nil:
			cur.addAction(ev)
		}
	}
	flush()
	return eps
}

// foldsReformulation reports whether a new query-mode get_nug should fold into
// the open episode as a same-intent reformulation rather than open a new one.
func (b *epBuild) foldsReformulation() bool {
	return !b.consumed() && !b.hasClose
}

func newEpBuild(ev *event.Event, prompt string) *epBuild {
	return &epBuild{
		session: ev.Session,
		project: ev.Project,
		agent:   ev.Agent,
		rootSeq: ev.Seq,
		prompt:  prompt,
		query:   ev.Query,
		queries: 1,
		results: ev.Results,
	}
}

// addQuery folds a reformulation into the chain: it bumps the depth, marks the
// self-requery, and advances the served set to the latest query's results.
func (b *epBuild) addQuery(ev *event.Event) {
	b.queries++
	b.selfRequery = true
	b.results = ev.Results
}

// addAction records a following action's contribution to the consumption tells:
// an explicit reference to a returned id (tell 1), a task-advancing Edit/Write
// (tell 2 input), or a set_nug (coverage-gap input + tell-2 disqualifier). It
// also folds the event.Retry self-requery motif — a following action the event
// builder flagged as a retry-after-failure (bd_show!→bd_show) — into the episode,
// for the hop-attribution TurnContext (HopInterp). This is a hop-attribution-only
// signal; it deliberately does not touch the RU consumption tells.
func (b *epBuild) addAction(ev *event.Event) {
	if ev.Retry {
		b.retryMotif = true
	}
	if b.refID == "" {
		if id, rank := referencedID(ev, b.results); id != "" {
			b.refID, b.refRank = id, rank
		}
	}
	switch ev.Action {
	case "Edit", "Write", "NotebookEdit":
		b.advanced = true
	case toolSetNug:
		b.sawSetNug = true
	}
}

// consumed reports whether a consuming action has already been seen — the gate
// for folding a later get_nug as a same-intent reformulation (spec: a chain folds
// only "before any consumed result").
func (b *epBuild) consumed() bool { return b.refID != "" || b.advanced }

// topicSwitchedAway reports whether the human closed this episode by opening a
// fresh task (MoveNewTask) — the cross-episode abandonment tell ClassifyCross reads
// when the episode never reached acceptance (ferret-bbp.8).
func (b *epBuild) topicSwitchedAway() bool {
	return b.hasClose && b.closingMove == dialogue.MoveNewTask
}

// referencedID scans an event's text fields for the first returned nug id it
// mentions, returning the id and its 1-based rank in the result set. This is the
// deterministic "explicit reference" tell (tell 1): a later command, target, or
// prompt that names a retrieved id (or a CLI fetch of it) consumed that nug.
func referencedID(ev *event.Event, results []event.NugHit) (id string, rank int) {
	hay := ev.Target + "\x00" + ev.Detail + "\x00" + ev.Query + "\x00" + ev.Prompt
	for i := range results {
		if results[i].ID != "" && strings.Contains(hay, results[i].ID) {
			return results[i].ID, i + 1
		}
	}
	return "", 0
}

func (b *epBuild) finalize() Episode {
	empty := len(b.results) == 0
	// Nothing was returned → nothing could be consumed, in either variant. The
	// tells only apply to a non-empty result set.
	consumedStrict := !empty && b.refID != ""
	tell2 := !empty && b.advanced && !b.selfRequery && !b.sawSetNug
	tell3 := !empty && b.hasClose && !dialogue.IsRepairMove(b.closingMove)

	var moves []dialogue.Move
	if b.hasClose {
		moves = []dialogue.Move{b.closingMove}
	}
	// The closing move IS the next segment's opening turn, so a MoveNewTask close
	// means the human topic-switched away — ClassifyCross reads that as abandonment
	// when this episode never reached acceptance (ferret-bbp.8).
	outcome := dialogue.ClassifyCross(moves, b.topicSwitchedAway())

	coverageGap := empty && b.sawSetNug
	goodAbandon := empty && !coverageGap && !b.selfRequery && !dialogue.IsRepairMove(b.closingMove)

	return Episode{
		Session:        b.session,
		Project:        b.project,
		Agent:          b.agent,
		RootSeq:        b.rootSeq,
		Prompt:         b.prompt,
		Query:          b.query,
		Queries:        b.queries,
		Results:        len(b.results),
		ResultIDs:      projectResults(b.results),
		SelfRequery:    b.selfRequery,
		RetryMotif:     b.retryMotif,
		EmptyResult:    empty,
		Oversized:      len(b.results) > oversizeResultCap,
		ConsumedStrict: consumedStrict,
		ConsumedLoose:  consumedStrict || tell2 || tell3,
		ConsumedID:     b.refID,
		ConsumedRank:   b.refRank,
		ClosingMove:    b.closingMove,
		Outcome:        outcome,
		CoverageGap:    coverageGap,
		GoodAbandon:    goodAbandon,
		Answerable:     !coverageGap && !goodAbandon,
	}
}
