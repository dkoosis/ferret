package dialogue

import "regexp"

// AgentCause is an agent-side INITIATIVE-CALIBRATION tag on a user turn: when the
// human reacts, this names how the AGENT mis-calibrated its initiative to provoke
// that reaction — acted too much, too little, interrupted needlessly, or stopped
// too early. It is the mirror of the rest of this package: Move/Outcome read the
// HUMAN's intent; AgentCause reads the agent's judgment about when to act vs defer.
//
// Anchored in prior art, not invented (ferret-bbp.11; nug 20bbb30cece7 "Prior art
// — agent initiative-calibration"): the four causes map to mixed-initiative
// interaction (Horvitz, CHI 1999), types & levels of automation (Parasuraman,
// Sheridan & Wickens, IEEE SMC-A 2000), appropriate reliance (Lee & See, Human
// Factors 2004), and the human-AI interaction guidelines (Amershi et al., CHI
// 2019). The full taxonomy is defined here as data; detectors land as slices, each
// keyed on the human's own words (the bbp thesis). Shipped: premature-stop (a
// continuation demand) and over-initiative (a reversal of an unrequested action).
// Pending — need agent-turn context threaded from the transcript walk: under-
// initiative (prose-only where execution was wanted) and miscalibrated-interruption
// (a permission question on a reversible action whose intent was clear).
type AgentCause string

const (
	// CauseOverInitiative — the agent acted beyond the ask (a mutating/irreversible
	// step when advice/options/review was wanted). Horvitz expected-value-of-action;
	// Parasuraman over-automation; OWASP LLM06 Excessive Agency. Detected from the
	// human's reversal words (overInitiativeCues); agent-turn confirmation + the
	// no-pushback case are the deferred LLM leg (ferret-bbp.18).
	CauseOverInitiative AgentCause = "over-initiative"
	// CauseUnderInitiative — the agent explained where execution was wanted (prose,
	// no tool call, then the human re-issues). Parasuraman under-automation; Lee &
	// See / Bo under-reliance. (detector: pending)
	CauseUnderInitiative AgentCause = "under-initiative"
	// CauseMiscalibratedInterruption — the agent asked permission for a low-risk,
	// reversible action whose intent was already clear. Horvitz cost-of-interruption;
	// Amershi G8/G9/G10. (detector: pending)
	CauseMiscalibratedInterruption AgentCause = "miscalibrated-interruption"
	// CausePrematureStop — the agent handed back before the task was complete and the
	// human had to demand continuation. Long-Horizon-Terminal-Bench; SHIELDA
	// "Stopping Too Early". Bidirectional with the over-persistence pole ferret's
	// friction metrics already catch (Majgaonkar, ICSE 2026: "inability to abandon an
	// unproductive loop").
	CausePrematureStop AgentCause = "premature-stop"
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
// supposed to"), orders the agent to stop mutating ("stop editing", "don't touch"),
// or questions an unprompted mutation ("leave it alone", "why did you delete").
// The agent-turn confirmation (there really WAS an unrequested mutating call) and
// the no-pushback case (the human stays silent) are the deferred LLM leg (ferret-
// bbp.18); this floor fires only on an explicit human reversal.
var overInitiativeCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bi (did ?n'?t|didnt|never) ask(ed)?\b`),
	regexp.MustCompile(`(?i)\bi (did ?n'?t|didnt|never) (tell|want) you to\b`),
	regexp.MustCompile(`(?i)\bwho (asked|told) you to\b`),
	regexp.MustCompile(`(?i)\byou (weren'?t|were not|aren'?t|are not) (supposed|asked|meant) to\b`),
	regexp.MustCompile(`(?i)\b(stop|quit) (chang|edit|modif|touch|delet|rewrit|refactor|overwrit|remov|updat|creat)\w*\b`),
	regexp.MustCompile(`(?i)\b(don'?t|do not) (chang|edit|modif|touch|delet|rewrit|refactor|overwrit|remov)\w*\b`),
	regexp.MustCompile(`(?i)\bleave (it|that|them|those|this|the \w+) alone\b`),
	regexp.MustCompile(`(?i)\bwhy did you (chang|edit|modif|touch|delet|rewrit|refactor|overwrit|remov)\w*\b`),
}

// TagAgentCause reads a user turn for an agent-side initiative-calibration cause.
// Detects CausePrematureStop (a continuation demand) and CauseOverInitiative (a
// reversal of an unrequested action) from the human's words; under-initiative and
// miscalibrated-interruption return ("", "", false) until their agent-turn detectors
// land. Premature-stop is checked first so behavior is preserved; the cue sets are
// disjoint in practice (a continuation demand never reads as a reversal). ok=false
// means no initiative-calibration signal on this turn.
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
	return "", "", false
}
