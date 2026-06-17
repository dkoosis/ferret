// Package conform is ferret's CONFORMANCE half: it replays a task's observed
// tool calls against a reference plan and scores how faithfully the agent
// followed it. ferret's mining half is DESCRIPTIVE — it counts what recurs and
// what it burned, but is blind to whether a call served the agent's intent. A
// session can end in a green "merge + commit" (a naive success label) while
// having silently bypassed a gate the agent itself committed to. Conformance is
// what surfaces that: it localizes the deviating call, not just the aggregate.
//
// The model is deliberately thin and LLM-free. The reference is an ordered list
// of step labels — the agent's OWN stated plan (read from prompt + reasoning) or
// a best-practice template. The observed trace is the task's tool calls, each
// tagged by the analyst with the plan step it served (or left untagged =
// off-plan). ferret never derives the plan or assigns the labels — that semantic
// work is analyst-side, exactly as task segmentation (kuv.2) splits a
// deterministic Go scaffold from an LLM merge. ferret owns only the scoring
// arithmetic, which is genuinely deterministic and not something a reader can do
// reliably by hand: an optimal alignment and the fitness/precision it implies.
//
// Algorithm: cost-based trace alignment (Adriansyah et al., "Conformance
// Checking Using Cost-Based Fitness Analysis", EDOC 2011/2012) over a sequential
// reference, with the classic token-replay framing of fitness vs precision (van
// der Aalst, Rozinat, "Conformance Checking of Processes Based on Monitoring
// Real Behavior", Information Systems 2008). Three move types — synchronous
// (call matched the next planned step), model-only (a planned step never
// executed → skipped gate), log-only (a call with no planned step → off-plan).
package conform

// MoveType labels one step of the optimal alignment between the reference plan
// and the observed trace.
type MoveType string

const (
	// MoveSync: an observed call matched the next planned step, in order.
	MoveSync MoveType = "sync"
	// MoveModel: a planned step with no matching call — a skipped step (the
	// load-bearing signal: a gate the agent committed to but never executed).
	MoveModel MoveType = "model"
	// MoveLog: an observed call serving no planned step — off-plan / extra work
	// ("invoked != served").
	MoveLog MoveType = "log"
)

// ObsCall is one observed tool call in the task span. Call is its index in the
// task (from the spine/segments scaffold), Tool its name, and Step the plan
// step the analyst assigned — empty means off-plan (it can never sync).
//
// Noise marks a pure tooling artifact — a buffering/flush/settle call that did
// no task work (e.g. the 76 standalone "echo flush" agent_shell calls in the
// loto-ltof slice). Noise is dropped before scoring, not counted as off-plan:
// interspersed noise SHATTERS step phases under sequential alignment (only the
// first consecutive run of a step's real calls syncs), so a raw trace scored
// fitness 0.07 where the de-noised logical trace scored 0.66. Deciding WHICH
// calls are noise is a semantic judgment, so the analyst sets the flag — same
// split as Step labeling; ferret only drops, counts, and reports it. Noise is a
// separate class from off-plan: off-plan (Step=="", Noise==false) is real extra
// work that lowers precision ("invoked != served"); noise is non-work removed
// from the trace entirely and surfaced as its own friction stat.
type ObsCall struct {
	Call  int    `json:"call"`
	Tool  string `json:"tool"`
	Step  string `json:"step,omitempty"`
	Noise bool   `json:"noise,omitempty"`
}

// Move is one step of the alignment. Step is set for sync/model moves (the
// reference step); Obs is set for sync/log moves (the FIRST observed call of the
// phase); Span is how many consecutive calls the move covers (a plan step is a
// phase that can span several calls — e.g. three review diffs; model moves have
// Span 0).
type Move struct {
	Type MoveType `json:"type"`
	Step string   `json:"step,omitempty"`
	Obs  *ObsCall `json:"obs,omitempty"`
	Span int      `json:"span,omitempty"`
}

// Result is the full conformance verdict for one task.
type Result struct {
	Moves []Move `json:"moves"`
	// Fitness ∈ [0,1]: how much of the plan the trace replayed — 1 minus the
	// alignment cost normalized by the worst-case cost (Adriansyah cost-based
	// fitness). 1 = every step executed in order, nothing extra.
	Fitness float64 `json:"fitness"`
	// Precision ∈ [0,1]: share of observed calls that matched a planned step in
	// order. Low precision with high fitness = "invoked != served": the calls
	// technically replay but most weren't anticipated by the plan.
	Precision  float64 `json:"precision"`
	Cost       int     `json:"cost"`       // model moves + off-plan calls
	WorstCost  int     `json:"worstCost"`  // |reference| + |logical calls|
	Sync       int     `json:"sync"`       // calls that served a planned step
	ModelMoves int     `json:"modelMoves"` // skipped planned steps
	LogMoves   int     `json:"logMoves"`   // off-plan calls
	// Noise is the count of buffering/flush calls dropped before scoring. Kept
	// as a separate stat (not folded into LogMoves) so the tooling artifact is
	// reported, never silently deleted — it is itself a descriptive finding.
	Noise int `json:"noise"`
}

// phase is a run of consecutive observed calls sharing one step label — a plan
// step is satisfied by a whole phase, not a single call (a "review" step might
// span three diff calls). first is the phase's opening call (for localization),
// span its call count. Off-plan calls (empty step) never merge: each is its own
// span-1 phase so the "invoked != served" count stays per-call.
type phase struct {
	step  string
	first ObsCall
	span  int
}

// collapse folds consecutive same-step observed calls into phases. Empty-step
// (off-plan) calls are kept distinct so their multiplicity is preserved.
func collapse(observed []ObsCall) []phase {
	phases := make([]phase, 0)
	for _, oc := range observed {
		if n := len(phases); n > 0 && oc.Step != "" && phases[n-1].step == oc.Step {
			phases[n-1].span++
			continue
		}
		phases = append(phases, phase{step: oc.Step, first: oc, span: 1})
	}
	return phases
}

// Align computes the optimal cost-based alignment of an observed trace against a
// sequential reference plan, then derives fitness and precision from it. It is a
// pure function of its inputs: same reference + observed → same Result, always.
//
// Observed calls are first collapsed into phases (a reference step is satisfied
// by a run of consecutive calls bearing its label). The alignment is then a
// Levenshtein-style DP over phases with three moves: a synchronous move (cost 0)
// when the next phase's step equals the next reference step; otherwise the
// cheapest path advances via a model move (skip a planned step) or a log move
// (consume an off-plan phase). A label mismatch costs 2 (one model + one log),
// never a free substitution — standard for conformance alignments.
//
// Fitness and precision are reported in CALLS, not phases: a log move over an
// off-plan phase costs its full span (every wasted invocation counts), and
// precision is the share of all observed calls that served a planned step.
//
// Noise calls are dropped before alignment so they cannot shatter step phases;
// the count is carried through to Result and reported separately. Scoring runs
// on the de-noised logical trace, so fitness/precision/worstCost all reckon in
// logical calls, not the raw observed length.
func Align(reference []string, observed []ObsCall) Result {
	logical, noise := denoise(observed)
	phases := collapse(logical)
	back := alignTable(reference, phases)
	moves := backtrack(reference, phases, back)
	return score(moves, len(reference), len(logical), noise)
}

// denoise splits the observed trace into the logical trace (real calls, scored)
// and the count of dropped noise calls. Order is preserved; a noise call is
// simply skipped, so it never collapses into or breaks a step phase. The
// decision of what is noise is the analyst's (ObsCall.Noise) — denoise only
// applies it deterministically.
func denoise(observed []ObsCall) (logical []ObsCall, noise int) {
	for _, oc := range observed {
		if oc.Noise {
			noise++
			continue
		}
		logical = append(logical, oc)
	}
	return logical, noise
}

// alignTable fills the DP back-pointer table: back[i][j] is the cheapest move
// into the cell aligning the first i reference steps with the first j phases.
func alignTable(reference []string, phases []phase) [][]MoveType {
	n, m := len(reference), len(phases)
	cost := make([][]int, n+1)
	back := make([][]MoveType, n+1)
	for i := range cost {
		cost[i] = make([]int, m+1)
		back[i] = make([]MoveType, m+1)
	}
	for i := 1; i <= n; i++ {
		cost[i][0], back[i][0] = i, MoveModel
	}
	for j := 1; j <= m; j++ {
		// A leading off-plan phase costs its span (per-call), not 1.
		cost[0][j], back[0][j] = cost[0][j-1]+phases[j-1].span, MoveLog
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost[i][j], back[i][j] = bestMove(cost, reference[i-1], phases[j-1], i, j)
		}
	}
	return back
}

// bestMove picks the cheapest move into cell (i,j): skip the planned step (model
// move, cost 1), consume the off-plan phase (log move, cost = its span), or sync
// when the phase's step matches. Ties prefer sync, so a real match always wins
// and the backtrack stays deterministic.
func bestMove(cost [][]int, refStep string, ph phase, i, j int) (int, MoveType) {
	best, bt := cost[i-1][j]+1, MoveModel
	if c := cost[i][j-1] + ph.span; c < best {
		best, bt = c, MoveLog
	}
	if ph.step != "" && ph.step == refStep {
		if c := cost[i-1][j-1]; c <= best {
			best, bt = c, MoveSync
		}
	}
	return best, bt
}

// score tallies the alignment into a Result. Fitness and precision are reported
// in CALLS: a log move costs its full span, and precision is the share of all
// observed calls that served a planned step.
func score(moves []Move, refLen, calls, noise int) Result {
	res := Result{Moves: moves, WorstCost: refLen + calls, Noise: noise}
	for _, mv := range moves {
		switch mv.Type {
		case MoveSync:
			res.Sync += mv.Span
		case MoveModel:
			res.ModelMoves++
		case MoveLog:
			res.LogMoves += mv.Span
		}
	}
	res.Cost = res.ModelMoves + res.LogMoves
	res.Fitness = 1
	if res.WorstCost > 0 {
		res.Fitness = 1 - float64(res.Cost)/float64(res.WorstCost)
	}
	res.Precision = 1
	if calls > 0 {
		res.Precision = float64(res.Sync) / float64(calls)
	}
	return res
}

// backtrack walks the DP back-pointer table from (n,m) to (0,0), emitting moves
// in trace order. Each sync/log move carries the phase's first call and span so
// the Result is self-contained (callers localize deviations from it alone).
func backtrack(reference []string, phases []phase, back [][]MoveType) []Move {
	i, j := len(reference), len(phases)
	rev := make([]Move, 0)
	for i > 0 || j > 0 {
		switch back[i][j] {
		case MoveSync:
			oc := phases[j-1].first
			rev = append(rev, Move{Type: MoveSync, Step: reference[i-1], Obs: &oc, Span: phases[j-1].span})
			i, j = i-1, j-1
		case MoveModel:
			rev = append(rev, Move{Type: MoveModel, Step: reference[i-1]})
			i--
		case MoveLog:
			oc := phases[j-1].first
			rev = append(rev, Move{Type: MoveLog, Obs: &oc, Span: phases[j-1].span})
			j--
		}
	}
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}
	return rev
}
