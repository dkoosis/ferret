// Package dialogue tags USER turns — the half of the transcript ferret's
// tool-call miner never reads. The tool stream is what the agent DID; the user's
// words beside it are what the human WANTED and whether they got it. Reading that
// language directly (it is written down, not inferred — same stance as the kuv
// intent-reframe) gives a higher-precision, cheaper friction marker than the
// agent-side burn/loop proxy, plus the only direct OUTCOME signal we have:
// acceptance vs abandonment (the missing PARADISE leg).
//
// v1 is regex/lexical only — high precision before any model. An LLM classifier
// is a deliberate v2 escalation, gated to phenomena beyond lexical reach
// (frustration-in-context, dialogue-act disambiguation); see the staged-LLM
// decision recorded in the bead/KG. Do NOT build an emotion detector: dev jargon
// ("kill", "dead", "crash", "braindead") is style, not affect (bbp constraint).
//
// Borrowed framing (cite the method + source in code, per the kuv convention):
//   - Repair structure (trouble source → repair initiation → repair solution):
//     Schegloff, Jefferson & Sacks 1977. A user turn that initiates repair right
//     after an agent action localizes a friction boundary from the HUMAN side.
//   - Dialogue acts / communicative functions: ISO 24617-2 (Bunt 2012). The
//     per-turn Move is a coarse, auditable subset of that taxonomy.
//   - Outcome = task success: PARADISE (Walker et al. 1997) — task success +
//     dialogue cost. Episode.Outcome supplies the success leg burn alone can't.
package dialogue

import (
	"regexp"
	"strings"
)

// Move is the communicative function of one user turn — a coarse, auditable
// subset of ISO 24617-2 dialogue acts. v1 classifies into Repair / Accept /
// Neutral; the finer moves are reserved consts the v2 (or LLM) layer can fill.
type Move string

const (
	// MoveRepair — the user corrects, rejects, or re-specifies the prior turn:
	// the trouble-source signal (Schegloff 1977). The strongest human-side marker
	// that the preceding agent action did not serve the task.
	MoveRepair Move = "repair"
	// MoveAccept — the user signals the prior turn served: approval or a "keep it"
	// directive. The success leg of the outcome.
	MoveAccept Move = "accept"
	// MoveNeutral — a turn carrying neither signal (a fresh request, a question, a
	// constraint). Not friction, not outcome — just task content.
	MoveNeutral Move = "neutral"

	// --- reserved for v2 / the finer ISO 24617-2 split (not yet emitted) ---

	// MoveReject — an outright rejection distinct from corrective repair. v2.
	MoveReject Move = "reject"
	// MoveClarify — the user answers an agent question or disambiguates. v2.
	MoveClarify Move = "clarify"
	// MoveConstrain — the user adds a constraint without rejecting prior work. v2.
	MoveConstrain Move = "constrain"
)

// Signal is one tagged user turn: where it sat, what it did, and the cue that
// fired (kept for traceability — every label points at the substring that
// earned it, so a human validator can audit the matcher cheaply).
type Signal struct {
	Turn int    `json:"turn"` // user-turn index within the session (0-based)
	Move Move   `json:"move"`
	Cue  string `json:"cue,omitempty"` // the matched cue phrase, "" for neutral
	Text string `json:"text"`          // compact label of the turn (truncated by caller)
}

// repairCues are high-precision corrective/rejection markers. Conservative by
// design: a missed repair costs less than a false alarm that wastes the human's
// validation budget. Matched as whole-turn negations or leading correctives, not
// loose substrings — "no" must lead the turn, never appear mid-sentence.
//
// Excludes dev jargon ("kill"/"dead"/"crash") on purpose: those are workflow
// vocabulary, not repair or affect.
var repairCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(no|nope|nah)\b`),
	regexp.MustCompile(`(?i)\b(not (that|what|right|quite)|that('?s| is) (not|wrong)|that('?s| is)n'?t)\b`),
	regexp.MustCompile(`(?i)\b(wrong|incorrect|undo|revert|redo)\b`),
	regexp.MustCompile(`(?i)\b(try again|do(n'?t| not) (do )?that)\b`),
	regexp.MustCompile(`(?i)\b(i mean|i meant|what i meant|as i (said|asked)|like i said)\b`),
	regexp.MustCompile(`(?i)\b(you (missed|forgot|skipped)|still (wrong|broken))\b`),
	regexp.MustCompile(`(?i)\bactually,?\s+no\b`),
}

// acceptCues are high-precision approval / "keep it" markers — the success leg.
// Leading-anchored where the cue is a common word ("yes"/"great") so it signals
// approval of the prior turn rather than appearing inside a fresh request.
var acceptCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(yes|yep|yeah|yup|great|perfect|nice|exactly|excellent|beautiful)\b`),
	regexp.MustCompile(`(?i)\b(lgtm|looks good|that works|works (great|now)|ship it|perfect)\b`),
	regexp.MustCompile(`(?i)\b(save (this|that|it)|keep (this|that|it)|use (that|this))\b`),
	regexp.MustCompile(`(?i)^(thanks|thank you)\b`),
}

// signalLeadRunes is the leading window a long turn is matched against. A repair
// or accept is characteristically a SHORT turn ("no, try snipe" / "great, ship
// it"); in a long composed turn (an email draft, pasted content) a buried "that
// isn't" is task content, not a correction. So a turn over signalShortRunes only
// earns a move when the cue sits in its leading window — precision over recall,
// the v1 contract. Prior art: short turns carry the dialogue-act signal
// (claude-code-log-analyzer arc_analyzer.py).
const (
	signalShortRunes = 200
	signalLeadRunes  = 64
)

// TagMove classifies one user turn into a v1 Move and returns the cue that fired.
// Repair wins over accept when both match (a "no, but the rename is great" turn
// is fundamentally a correction). A turn matching neither is MoveNeutral.
//
// The argument is the genuine user-prompt text (whitespace-collapsed); the caller
// is responsible for having stripped tool-result carriers and command envelopes —
// this function judges words, not transcript plumbing.
func TagMove(turn string) (Move, string) {
	t := strings.TrimSpace(turn)
	if t == "" {
		return MoveNeutral, ""
	}
	target := t
	// Count runes with a short-circuit so a huge pasted turn isn't fully
	// rune-sliced just to test its length.
	runeCount := 0
	for range t {
		runeCount++
		if runeCount > signalShortRunes {
			break
		}
	}
	if runeCount > signalShortRunes {
		target = leadWindow(t, signalLeadRunes) // long turn: judge only the lead
	}
	if cue, ok := firstMatch(target, repairCues); ok {
		return MoveRepair, cue
	}
	if cue, ok := firstMatch(target, acceptCues); ok {
		return MoveAccept, cue
	}
	return MoveNeutral, ""
}

// leadWindow returns the first n runes of s — the slice a long turn's move is
// judged against, so a cue buried deep in pasted content can't earn a label.
func leadWindow(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// firstMatch returns the matched substring of the first pattern that fires, so
// the Signal can carry the exact cue that earned its label.
func firstMatch(s string, pats []*regexp.Regexp) (cue string, ok bool) {
	for _, p := range pats {
		if m := p.FindString(s); m != "" {
			return strings.ToLower(strings.TrimSpace(m)), true
		}
	}
	return "", false
}
