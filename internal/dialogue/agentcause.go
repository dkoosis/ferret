package dialogue

import "regexp"

// AgentCause is an agent-side INITIATIVE-CALIBRATION tag on a user turn: when the
// human reacts, this names how the AGENT mis-calibrated its initiative to provoke
// that reaction — acted too much, too little, interrupted needlessly, stopped too
// early, or would not stop. It is the mirror of the rest of this package: Move/Outcome
// read the HUMAN's intent; AgentCause reads the agent's judgment about when to act vs
// defer. Stop-timing is one BIDIRECTIONAL axis with two poles: premature-stop (too
// early) and over-persistence (too late) — the mirror images the epic's read demands.
//
// Anchored in prior art, not invented (ferret-bbp.11; nug 20bbb30cece7 "Prior art
// — agent initiative-calibration"): the four causes map to mixed-initiative
// interaction (Horvitz, CHI 1999), types & levels of automation (Parasuraman,
// Sheridan & Wickens, IEEE SMC-A 2000), appropriate reliance (Lee & See, Human
// Factors 2004), and the human-AI interaction guidelines (Amershi et al., CHI
// 2019). The full taxonomy is defined here as data; every detector is keyed on the
// human's own words (the bbp thesis). Two are context-free — premature-stop (a
// continuation demand) and over-initiative (a reversal of an unrequested action),
// read from the reaction alone (TagAgentCause). Two need the AGENT-turn shape threaded
// from the walk — under-initiative (the agent explained where execution was wanted) and
// miscalibrated-interruption (the agent asked permission on already-clear intent) —
// both gated on the agent under-acting plus a push-to-execute reaction (TagAgentCauseContext).
type AgentCause string

const (
	// CauseOverInitiative — the agent acted beyond the ask (a mutating/irreversible
	// step when advice/options/review was wanted). Horvitz expected-value-of-action;
	// Parasuraman over-automation; OWASP LLM06 Excessive Agency. Detected from the
	// human's reversal words (overInitiativeCues); agent-turn confirmation + the
	// no-pushback case are the deferred LLM leg (ferret-bbp.18).
	CauseOverInitiative AgentCause = "over-initiative"
	// CauseUnderInitiative — the agent explained where execution was wanted (prose, no
	// tool call, then the human re-issues). Parasuraman under-automation; Lee & See / Bo
	// under-reliance. Detected from a push-to-execute reaction (pushToExecuteCues) when
	// the agent neither acted nor asked (AgentContext); TagAgentCauseContext.
	CauseUnderInitiative AgentCause = "under-initiative"
	// CauseMiscalibratedInterruption — the agent asked permission for a low-risk,
	// reversible action whose intent was already clear. Horvitz cost-of-interruption;
	// Amershi G8/G9/G10. Detected from a push-to-execute reaction when the agent ended on
	// a permission question (AgentContext.AgentAskedPermission); TagAgentCauseContext.
	CauseMiscalibratedInterruption AgentCause = "miscalibrated-interruption"
	// CausePrematureStop — the agent handed back before the task was complete and the
	// human had to demand continuation. Long-Horizon-Terminal-Bench; SHIELDA "Stopping
	// Too Early". The stopped-too-EARLY pole of the bidirectional stop-timing axis
	// (see CauseOverPersistence for the too-LATE pole).
	CausePrematureStop AgentCause = "premature-stop"
	// CauseOverPersistence — the agent would not stop: it kept grinding an unproductive
	// path and the human had to call it off ("you're going in circles", "give up on
	// that", "let it go"). The stopped-too-LATE pole, mirror of premature-stop, read
	// from the human's own words (the bbp thesis) — the SAME modality as its twin, which
	// is why it lives here rather than as a friction counter. Anchor: Majgaonkar (ICSE
	// 2026, "inability to abandon an unproductive loop"); master map §5. The AGENT-
	// behaviour / no-pushback complement (the loop the human never calls out) is already
	// caught by ferret's friction metrics (score.KindLoop / retry counts) — referenced,
	// not rebuilt here, mirroring how over-initiative's no-pushback case is bbp.18.
	CauseOverPersistence AgentCause = "over-persistence"
)

// continuationCues match a human turn that demands the agent CONTINUE work it
// stopped short of — the direct, deterministic tell of a premature stop, read from
// the human's own words (the bbp thesis). Kept corrective: a bare "continue" is an
// ambiguous fresh go-ahead, so the cues require a stopped-early framing ("you
// didn't finish", "why did you stop", "keep going", "you're not done") or a
// leading continuation imperative used as a redirect.
var continuationCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\byou (did ?n'?t|didnt|failed to|forgot to|stopped before you) (finish|complete|do the rest|get through)\b`),
	regexp.MustCompile(`(?i)\bwhy('?d| did) you stop\b`),
	regexp.MustCompile(`(?i)\b(don'?t|do not) stop\b`),
	regexp.MustCompile(`(?i)\byou('?re| are) not (done|finished)\b`),
	regexp.MustCompile(`(?i)\b(keep going|carry on|go on and finish|finish (it|the (rest|job|task)|what you started)|do the rest|complete (it|the (rest|task)))\b`),
	regexp.MustCompile(`(?i)\b(that'?s|thats) not (all|everything|done)\b`),
	regexp.MustCompile(`(?i)\byou (only|just) (did|finished|got through) (part|some|half)\b`),
}

// leadingContinueRe is a leading continuation imperative used as a redirect after
// the agent handed back — "continue", "keep going", "go on" at the very start of
// the turn (mirrors matchRepair's leading-"no" handling).
var leadingContinueRe = regexp.MustCompile(`(?i)^(continue|keep going|go on|proceed)\b`)

// overInitiativeCues match a human turn that REVERSES or halts an action the agent
// took beyond the ask — the deterministic tell of over-initiative, read from the
// human's own words (the bbp thesis). Anchor: Horvitz expected-value-of-action (CHI
// 1999) — the agent acted where the expected value did not warrant it; Parasuraman
// over-automation; OWASP LLM06 Excessive Agency.
//
// Precision-first/corrective, mirroring the premature-stop guard: a bare "undo"/
// "revert" is an ordinary approach repair (it already tags MoveRepair) and does NOT
// imply the agent OVER-stepped, so these cues require the UNREQUESTED framing — the
// human disclaims the request ("I didn't ask", "who told you to", "you weren't
// supposed to"), orders the agent to halt an action already in flight ("stop
// editing"), or questions an unprompted mutation ("leave it alone", "why did you
// delete"). Every cue carries a RETROSPECTIVE marker (a past-tense verb, a request
// disclaimer, or "stop"/"leave", which presuppose an action already underway); a
// bare prospective imperative ("don't modify the schema") is deliberately EXCLUDED —
// it reads identically to an ordinary forward constraint ("refactor the parser, but
// don't modify the schema") and would libel a correctly-scoped action (Codex #83).
// That case needs the agent-turn confirmation (there really WAS an unrequested
// mutating call) — the deferred LLM leg (ferret-bbp.18), which also handles the
// no-pushback case (the human stays silent). This floor fires only on an explicit
// human reversal.
var overInitiativeCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bi (did ?n'?t|didnt|never) ask(ed)?\b`),
	regexp.MustCompile(`(?i)\bi (did ?n'?t|didnt|never) (tell|want) you to\b`),
	regexp.MustCompile(`(?i)\bwho (asked|told) you to\b`),
	regexp.MustCompile(`(?i)\byou (weren'?t|were not|aren'?t|are not) (supposed|asked|meant) to\b`),
	regexp.MustCompile(`(?i)\b(stop|quit) (chang|edit|modif|touch|delet|rewrit|refactor|overwrit|remov|updat|creat)\w*\b`),
	regexp.MustCompile(`(?i)\bleave (it|that|them|those|this|the [\w.-]+(?:\s+[\w.-]+){0,2}) alone\b`),
	regexp.MustCompile(`(?i)\bwhy did you (chang|edit|modif|touch|delet|rewrit|refactor|overwrit|remov|updat|creat)\w*\b`),
}

// overPersistenceCues match a human turn CALLING OFF an unproductive effort — the
// deterministic tell that the agent kept going when it should have stopped, read from
// the human's own words (the bbp thesis). The stopped-too-LATE mirror of the
// continuationCues; anchor: Majgaonkar (ICSE 2026, "inability to abandon an
// unproductive loop"), master map §5.
//
// Precision-first/corrective, like its twin: a bare "stop" is too broad (it collides
// with over-initiative's "stop editing" and meta's "stop explaining"), so the cues
// require an ABANDON framing — a circling/looping metaphor ("going in circles",
// "you're looping"), an explicit give-up-on-this-path ("give up on that approach",
// "let it go"), or a not-working-so-stop redirect. "stop editing" (a mutation halt →
// over-initiative) and "keep going" (→ premature-stop) are checked BEFORE these and
// are disjoint. The agent-behaviour / no-pushback case (the loop the human never calls
// out) is the friction complement (score.KindLoop / retries), not rebuilt here.
var overPersistenceCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(going (in|around in) circles|round in circles)\b`),
	regexp.MustCompile(`(?i)\byou('?re| are) (just )?(looping|spinning your wheels|chasing your tail|going in circles)\b`),
	regexp.MustCompile(`(?i)\b(give up on|abandon|drop) (this|that|the) (approach|idea|path|line|attempt|route|thing)\b`),
	regexp.MustCompile(`(?i)\b(just )?stop (trying|already)\b`),
	regexp.MustCompile(`(?i)\b(this|that|it) ?(is ?n'?t|'?s not) working[\s,.—-]*(just )?(stop|give up|move on|try (something|another))\b`),
	regexp.MustCompile(`(?i)\blet (it|this|that) go\b`),
	regexp.MustCompile(`(?i)\bmove on (from (this|that|it)|already)\b`),
	regexp.MustCompile(`(?i)\b(you keep|you'?re) (doing|trying|repeating) the same (thing|approach|fix)\b`),
	regexp.MustCompile(`(?i)\bsame (thing|error|failure|mistake) (over and over|again and again|repeatedly)\b`),
	regexp.MustCompile(`(?i)\bthat'?s enough (of (this|that))\b`),
}

// AgentContext carries the AGENT-turn shape the two under-action causes need: what
// the assistant turn(s) between the previous human turn and this one looked like.
// under-initiative and miscalibrated-interruption both key on the agent UNDER-acting
// (it explained, or asked, instead of doing); the human's words alone can't carry the
// "did it act / did it ask permission" facts, so the walk threads them here. Mirrors
// MoveContext (the Move-side per-turn context); kept separate so the two concerns —
// the human's move and the agent's initiative — don't entangle.
type AgentContext struct {
	// AgentResponded is true if an assistant turn actually intervened since the previous
	// human turn. Without it a first turn (or two human turns in a row) has a zero-value
	// context and there is NO agent action to judge — the under-action detectors must not
	// fire (Codex #85: a first-turn "just do it" would otherwise false-tag under-initiative).
	AgentResponded bool
	// AgentActed is true if an assistant turn since the previous human turn made a tool
	// call — the agent EXECUTED. Gates OUT under-initiative (a pure explanation).
	AgentActed bool
	// AgentAskedPermission is true if the last assistant text since the previous human
	// turn ended in a permission-seeking question ("should I…", "want me to…"). Routes a
	// push-to-execute reaction to miscalibrated-interruption rather than under-initiative.
	AgentAskedPermission bool
}

// pushToExecuteCues match a human turn that pushes the agent to ACT after it under-
// acted — the shared reaction tell for the two under-action causes, read from the
// human's own words (the bbp thesis). The AgentContext routes WHICH cause; this cue
// establishes the human wanted execution, not more talk. Bare acknowledgements ("go
// ahead", "yes", "proceed") fold upstream as affirmations (turn.ClassifyBoundary), so
// these deliberately target the non-bare, impatient forms that survive to this point.
var pushToExecuteCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bjust (do|make|run|write|fix|build|add|change|finish|implement) (it|that|them|the \w+)\b`),
	regexp.MustCompile(`(?i)\b(do it now|get it done|just do it|go ahead and (do|make|run|fix|change|implement)|go for it)\b`),
	regexp.MustCompile(`(?i)\b(stop (explaining|asking|talking)|less talk|enough (talk|explaining|questions)|quit asking)\b`),
	regexp.MustCompile(`(?i)\bi (said|asked you to|told you to|already (said|asked you to)) (do|make|run|write|fix|build|add|change|implement)\b`),
	regexp.MustCompile(`(?i)\b(actually|then|so) (do|make|run|write|fix|build) it\b`),
	regexp.MustCompile(`(?i)\byes,? (do it|make the change|go ahead and \w+)\b`),
}

// TagAgentCause reads a user turn for the three CONTEXT-FREE initiative-calibration
// causes, from the human's words alone: CausePrematureStop (a continuation demand),
// CauseOverInitiative (a reversal of an unrequested action), and CauseOverPersistence
// (a call to abandon an unproductive path). The stop-timing pair leads — premature-stop
// then over-persistence, its mirror pole — and over-initiative sits between them;
// checked in this order so behavior is preserved and the cue sets stay disjoint (a
// continuation demand never reads as a reversal or an abandon-call, and "stop editing"
// routes to over-initiative before over-persistence's "stop trying"). The two
// agent-turn-context causes are TagAgentCauseContext's job. ok=false means no
// context-free signal on this turn.
func TagAgentCause(turn string) (cause AgentCause, cue string, ok bool) {
	if c, matched := firstMatch(turn, continuationCues); matched {
		return CausePrematureStop, c, true
	}
	if leadingContinueRe.MatchString(turn) {
		return CausePrematureStop, "continue", true
	}
	if c, matched := firstMatch(turn, overInitiativeCues); matched {
		return CauseOverInitiative, c, true
	}
	if c, matched := firstMatch(turn, overPersistenceCues); matched {
		return CauseOverPersistence, c, true
	}
	return "", "", false
}

// TagAgentCauseContext reads a user turn for any of the four initiative-calibration
// causes, consulting ctx for the two that need agent-turn context. The context-free
// pair (premature-stop, over-initiative) is checked first via TagAgentCause; the two
// under-action causes then need the agent to have UNDER-acted plus a push-to-execute
// reaction, keyed on the human's words (the bbp thesis):
//   - miscalibrated-interruption: the agent asked permission — a needless interruption
//     on already-clear intent — and the human pushes it to just proceed.
//   - under-initiative: the agent only explained (no tool call, no permission ask) and
//     the human re-issues the directive to actually execute.
//
// When the agent DID act and did not needlessly ask, a push-to-execute reads as a NEXT
// step, not an under-action — no cause. ok=false means no signal on this turn.
func TagAgentCauseContext(turn string, ctx AgentContext) (cause AgentCause, cue string, ok bool) {
	if c, cc, hit := TagAgentCause(turn); hit {
		return c, cc, hit
	}
	c, matched := firstMatch(turn, pushToExecuteCues)
	if !matched {
		return "", "", false
	}
	switch {
	case ctx.AgentAskedPermission:
		// A permission ask implies an agent turn happened, so this arm is inherently safe.
		return CauseMiscalibratedInterruption, c, true
	case ctx.AgentResponded && !ctx.AgentActed:
		// under-initiative needs a real intervening agent turn that under-acted; without
		// AgentResponded a first-turn push-to-execute has nothing to attribute (Codex #85).
		return CauseUnderInitiative, c, true
	default:
		return "", "", false
	}
}
