package mine

import "sort"

// Finding is the shared projection both renderers consume: a mined motif
// classified into an action verb, with its measured cost. Mine once, render
// twice — the agent (JSON) and the human (md) reports are two views of this.
//
// burn is REAL context cost, not an estimate: every occurrence of the motif is
// re-matched against the corpus and its member events' measured byte sizes are
// summed, then converted to tokens. burn is the sort key — a rare motif that
// drags megabytes of file contents back into context outranks a frequent but
// cheap one.
type Finding struct {
	IDs      []uint32    // motif token ids (render via Corpus.Tokens)
	Kind     FindingKind // routine | friction | loop | noise
	Action   Action      // script | hook | tool-fix | trim
	Count    int         // total occurrences across the corpus
	Sessions int         // distinct streams containing the motif
	FailRate float64     // share of member tokens that are fail-marked
	Burn     int         // measured tokens of context the motif's occurrences consumed
	Surprise float64     // mean bits/tok of the sessions the motif recurs in (0 = no signal)

	// Dialogue + hop signals (ferret-bbp.6) — the human-side OUTCOME and the
	// deterministic Hop2 retrieval-quality grade for the sessions this motif recurs
	// in, rolled on by AttachDialogue so the MAIN findings pipeline surfaces them
	// beside burn: a motif that drags context AND ends repair-heavy/abandoned with an
	// empty-set retrieval is worse than burn alone shows. Aggregated per HOST SESSION
	// (the Surprise grain) — a coarse association, not per-occurrence attribution.
	// Zero values mean no signal (the session carried no accept/repair, or the motif
	// touched no scored retrieval). Hop1 is the LLM interp-fidelity leg (ferret-bbp.5):
	// reserved, left zero so it enriches later without reshaping the struct.
	Outcome string // dialogue PARADISE outcome: success | repair-heavy | abandoned ("" = unknown)
	Hop2    string // worst QPP retrieval grade across host sessions: low | mid | high ("" = no retrieval)
	Hop1    string // reserved — LLM interp-fidelity grade (ferret-bbp.5), zero until built
	Repairs int    // repair-tagged user turns summed across host sessions
	Accepts int    // accept-tagged user turns summed across host sessions

	// Friction is the deterministic architectural-friction rollup (ferret-bbp.10)
	// for the host sessions this motif recurs in: failed actions, late preconditions,
	// ignored constraints, confirmation waste, premature stops, turns-to-goal. Counts
	// SUM across host sessions and TurnsToGoal takes the worst (longest) path, so a
	// burn-ranked finding also reads "...and these sessions burned N failed actions
	// getting there". Zero = no friction signal. Set by AttachDialogue.
	Friction Friction

	ExStream int // exemplar location
	ExSeq    int
}

// Friction is the deterministic friction rollup for a session (or a motif's host
// sessions, summed). It holds plain counts — no dependency on package score — so
// the producer (cmd/ferret) converts score.Friction into this at the emit seam,
// the same decoupling StreamDialogue's canonical strings use. Rates are recovered
// at render from the Actions / Prompts denominators.
type Friction struct {
	Prompts            int  // genuine user turns (rate denominator)
	Actions            int  // tool + shell events (rate denominator)
	TurnsToGoal        int  // user turns to the first terminal goal signal (worst-wins across sessions)
	GoalReached        bool // a terminal goal signal fired in any host session
	FailedActions      int  // tool/shell events with a fail status
	LatePreconditions  int  // retries after a failure (precondition surfaced mid-act)
	IgnoredConstraints int  // constraint turns a later repair contradicted
	ConfirmationWaste  int  // bare permission-grant turns
	PrematureStops     int  // continue/finish/do-the-rest prods
}

// Any reports whether the rollup carries any friction signal — the render gate.
func (f *Friction) Any() bool {
	return f.FailedActions > 0 || f.LatePreconditions > 0 || f.IgnoredConstraints > 0 ||
		f.ConfirmationWaste > 0 || f.PrematureStops > 0 || f.GoalReached
}

// add folds another session's friction into the aggregate: counts SUM,
// TurnsToGoal takes the worst (longest) path, GoalReached ORs.
func (f *Friction) add(o Friction) {
	f.Prompts += o.Prompts
	f.Actions += o.Actions
	f.FailedActions += o.FailedActions
	f.LatePreconditions += o.LatePreconditions
	f.IgnoredConstraints += o.IgnoredConstraints
	f.ConfirmationWaste += o.ConfirmationWaste
	f.PrematureStops += o.PrematureStops
	if o.TurnsToGoal > f.TurnsToGoal {
		f.TurnsToGoal = o.TurnsToGoal
	}
	f.GoalReached = f.GoalReached || o.GoalReached
}

// StreamDialogue is one session's deterministic dialogue + Hop2 rollup, keyed into
// AttachDialogue by Corpus.StreamKeys ("project/session@agent"). The producer
// (cmd/ferret) reads the events the corpus is built from, tags user turns with the
// dialogue mover, and grades each retrieval episode's Hop2 QPP — then hands the
// per-session reduction here. Outcome / Hop2 hold the canonical taxonomy strings
// (dialogue.Outcome / score.QPPGrade) so package mine stays decoupled from those
// packages; "" in either means the session produced no signal.
type StreamDialogue struct {
	Outcome  string   // dialogue.Outcome for the session ("" = unknown / no signal)
	Hop2     string   // worst score.QPPGrade across the session's retrieval episodes ("" = none)
	Repairs  int      // repair-tagged user turns in the session
	Accepts  int      // accept-tagged user turns in the session
	Friction Friction // deterministic architectural-friction rollup for the session (bbp.10)
}

// FindingKind is what the motif IS.
type FindingKind string

// Action is what to DO about it — every Finding ends in a verb.
type Action string

const (
	KindRoutine  FindingKind = "routine"  // low-entropy chain you repeat by hand
	KindFriction FindingKind = "friction" // a failure and its recovery
	KindLoop     FindingKind = "loop"     // revisits a step — redundant re-work
	KindNoise    FindingKind = "noise"    // frequent but not actionable

	ActionScript  Action = "script"   // codify the routine
	ActionHook    Action = "hook"     // guard the failure with a hook
	ActionToolFix Action = "tool-fix" // the tool itself needs fixing
	ActionTrim    Action = "trim"     // cut the redundant context
)

// bytesPerToken is the standard rough tokenizer ratio: burn is reported in
// tokens so it reads as "what this costs the model", not raw bytes.
const bytesPerToken = 4

// bucketKind maps a rank bucket to its Finding kind + default action.
func bucketKind(bucket string) (FindingKind, Action) {
	switch bucket {
	case BucketScript:
		return KindRoutine, ActionScript
	case BucketFriction:
		return KindFriction, ActionHook
	case BucketLoop:
		return KindLoop, ActionTrim
	default: // BucketWatch
		return KindNoise, ActionTrim
	}
}

// Findings projects ranked cards into the shared Finding model, measuring each
// motif's real occurrence count and burn against the corpus, then sorting by
// burn (the cost-leak ranking). maxGap must match the value that mined the
// cards so the re-match sees the same gapped occurrences.
//
// surprise (stream key → mean bits/tok from ScoreSurprise) splits the ambiguous
// routine bucket: a recurring chain whose host sessions are more surprising than
// cut isn't a routine to script — it's friction to fix, so it's re-filed under
// friction/hook. A nil surprise index disables the split and leaves base kinds
// intact (the fix-baseline path, which only needs burn, passes nil).
func Findings(c *Corpus, cards []*Card, maxGap int, surprise map[string]float64, cut float64) []*Finding {
	out := make([]*Finding, 0, len(cards))
	for _, card := range cards {
		kind, action := bucketKind(card.Bucket)
		count, sessions, burnBytes := measure(c, card.IDs, maxGap)
		if count == 0 {
			continue // motif no longer matches (shouldn't happen, but never emit a phantom)
		}
		surp := 0.0
		if s, ok := motifSurprise(c, card.IDs, maxGap, surprise); ok {
			surp = s
			// low-surprise recurring seq → routine (script it);
			// high-surprise recurring seq → friction (a loop wearing routine's clothes).
			if kind == KindRoutine && s > cut {
				kind, action = KindFriction, ActionHook
			}
		}
		out = append(out, &Finding{
			IDs: card.IDs, Kind: kind, Action: action,
			Count: count, Sessions: sessions,
			FailRate: failRate(c, card.IDs),
			Burn:     burnBytes / bytesPerToken,
			Surprise: surp,
			ExStream: card.ExStream, ExSeq: card.ExSeq,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Burn != out[j].Burn {
			return out[i].Burn > out[j].Burn
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// AttachDialogue rolls the per-session dialogue + Hop2 signals onto each finding,
// de-islanding the dialogue / retrieval scorers into the MAIN findings pipeline
// (ferret-bbp.6). For each motif it aggregates over the streams it recurs in — the
// same host-session grain motifSurprise uses: repair/accept turns SUM, and the
// WORST outcome / worst Hop2 grade surface, so a burn-ranked finding also reads
// "...and these sessions ended abandoned with an empty-set retrieval". A nil index
// is the off switch (the fix-baseline path, which only needs burn, passes nil).
// Hop1 (the bbp.5 LLM interp leg) is intentionally never set here — it enriches the
// reserved field later without touching this pass.
func AttachDialogue(findings []*Finding, c *Corpus, idx map[string]StreamDialogue, maxGap int) {
	if idx == nil {
		return
	}
	for _, f := range findings {
		agg := aggregateDialogue(f, c, idx, maxGap)
		f.Outcome, f.Hop2 = agg.Outcome, agg.Hop2
		f.Repairs, f.Accepts = agg.Repairs, agg.Accepts
		f.Friction = agg.Friction
	}
}

// aggregateDialogue rolls every stream whose motif hosts this finding into one
// worst-wins StreamDialogue: counts summed, Outcome/Hop2 taking the most-severe grade.
func aggregateDialogue(f *Finding, c *Corpus, idx map[string]StreamDialogue, maxGap int) StreamDialogue {
	var agg StreamDialogue
	for si, st := range c.Streams {
		if !containsMotif(st, f.IDs, maxGap) {
			continue
		}
		sd, ok := idx[c.StreamKeys[si]]
		if !ok {
			continue
		}
		agg.Repairs += sd.Repairs
		agg.Accepts += sd.Accepts
		agg.Friction.add(sd.Friction)
		if outcomeSeverity(sd.Outcome) > outcomeSeverity(agg.Outcome) {
			agg.Outcome = sd.Outcome
		}
		if hopSeverity(sd.Hop2) > hopSeverity(agg.Hop2) {
			agg.Hop2 = sd.Hop2
		}
	}
	return agg
}

// outcomeSeverity ranks dialogue.Outcome strings so the worst (most negative) one
// across a motif's host sessions wins the rollup — abandoned outranks repair-heavy
// outranks success; unknown / "" carry no signal (severity 0).
func outcomeSeverity(o string) int {
	switch o {
	case "abandoned":
		return 3
	case "repair-heavy":
		return 2
	case "success":
		return 1
	default:
		return 0
	}
}

// hopSeverity ranks score.QPPGrade strings so the worst retrieval grade wins the
// rollup — low (the clearest Hop2 fail) outranks mid outranks high; "" = no
// retrieval episode scored, no signal.
func hopSeverity(g string) int {
	switch g {
	case "low":
		return 3
	case "mid":
		return 2
	case "high":
		return 1
	default:
		return 0
	}
}

// measure re-matches the motif across every stream and returns total
// occurrences, distinct streams, and summed member-byte cost. Matching is
// greedy and non-overlapping: once a motif completes, the scan resumes past
// its end, so a stream of N back-to-back routines counts as N, not N-choose-k.
func measure(c *Corpus, ids []uint32, maxGap int) (count, sessions, burnBytes int) {
	for _, st := range c.Streams {
		hits := 0
		i := 0
		for i < len(st) {
			end, bytes, ok := matchAt(st, i, ids, maxGap)
			if !ok {
				i++
				continue
			}
			hits++
			burnBytes += bytes
			i = end + 1
		}
		if hits > 0 {
			count += hits
			sessions++
		}
	}
	return count, sessions, burnBytes
}

// matchAt greedily matches ids[0] at st[start], then each subsequent id within
// maxGap positions of the prior match. Returns the end position and summed
// member bytes on success.
func matchAt(st []Tok, start int, ids []uint32, maxGap int) (end, bytes int, ok bool) {
	if len(ids) == 0 || start >= len(st) || st[start].ID != ids[0] {
		return 0, 0, false
	}
	pos := start
	bytes = st[start].Bytes
	for k := 1; k < len(ids); k++ {
		next := -1
		for p := pos + 1; p <= pos+maxGap && p < len(st); p++ {
			if st[p].ID == ids[k] {
				next = p
				break
			}
		}
		if next < 0 {
			return 0, 0, false
		}
		bytes += st[next].Bytes
		pos = next
	}
	return pos, bytes, true
}

// motifSurprise returns the mean surprise (bits/tok) of the scored streams the
// motif occurs in, and whether any such stream existed. Streams absent from the
// index (too short to score, or no surprise computed) don't contribute; a motif
// seen only in unscored streams yields ok=false, so the caller keeps its base
// kind. A nil index always yields ok=false — the split is off.
func motifSurprise(c *Corpus, ids []uint32, maxGap int, idx map[string]float64) (float64, bool) {
	if idx == nil {
		return 0, false
	}
	sum, n := 0.0, 0
	for si, st := range c.Streams {
		if !containsMotif(st, ids, maxGap) {
			continue
		}
		if bits, ok := idx[c.StreamKeys[si]]; ok {
			sum += bits
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// containsMotif reports whether the motif matches anywhere in the stream, under
// the same gapped greedy match the burn measurement uses.
func containsMotif(st []Tok, ids []uint32, maxGap int) bool {
	for i := range st {
		if _, _, ok := matchAt(st, i, ids, maxGap); ok {
			return true
		}
	}
	return false
}

// failRate is the share of the motif's member tokens carrying a fail mark —
// near 1 for a friction motif, 0 for a clean routine.
func failRate(c *Corpus, ids []uint32) float64 {
	if len(ids) == 0 {
		return 0
	}
	fails := 0
	for _, id := range ids {
		if failMarked(c.Vocab[id]) {
			fails++
		}
	}
	return float64(fails) / float64(len(ids))
}
