// Package dialogue tags USER turns — the half of the transcript ferret's
// tool-call miner never reads. The tool stream is what the agent DID; the user's
// words beside it are what the human WANTED and whether they got it. Reading that
// language directly (it is written down, not inferred — same stance as the kuv
// intent-reframe) gives a higher-precision, cheaper friction marker than the
// agent-side burn/loop proxy, plus the only direct OUTCOME signal we have:
// acceptance vs abandonment (the missing PARADISE leg).
//
// v1 was regex/lexical only, three moves (repair/accept/neutral). v2 (ferret-bbp.7)
// keeps the regex-first posture but widens the taxonomy to the corpus-corroborated
// dialogue moves, split two ways: OUTCOME-BEARING moves that change an
// episode.Outcome (reject, narrowed repair, clarify, meta-communication, constrain
// flag, new-task detection) and CATALOG-only moves that are tagged for the record
// but move no finding (delegate-judgment, inform-FYI, solicit-opinion, status-check,
// long-paste, record-deposit). An LLM classifier stays a deliberate escalation,
// gated to phenomena beyond lexical reach; see the staged-LLM decision in the
// bead/KG. Do NOT build an emotion detector: dev jargon ("kill", "dead", "crash",
// "braindead") is style, not affect (bbp constraint).
//
// Borrowed framing (cite the method + source in code, per the kuv convention):
//   - Repair structure (trouble source → repair initiation → repair solution):
//     Schegloff, Jefferson & Sacks 1977. A user turn that initiates repair right
//     after an agent action localizes a friction boundary from the HUMAN side.
//   - Dialogue acts / communicative functions: ISO 24617-2 (Bunt 2012). The
//     per-turn Move is a coarse, auditable subset of that taxonomy. The
//     meta-communication move is ISO 24617-2's Partner Communication Management
//     dimension (style/process feedback: "be terse", "slow down").
//   - Follow-up-query motivation/action taxonomy grounding reject/constrain: Kim
//     et al. 2024, arXiv:2407.13166.
//   - Outcome = task success: PARADISE (Walker et al. 1997) — task success +
//     dialogue cost. Episode.Outcome supplies the success leg burn alone can't.
package dialogue

import (
	"regexp"
	"strings"
)

// Move is the communicative function of one user turn — a coarse, auditable
// subset of ISO 24617-2 dialogue acts.
type Move string

const (
	// --- v1 core (still emitted) ---

	// MoveRepair — narrowed in v2 to Redo-Differently: the user redirects the work
	// to a different approach (try again, undo/revert, "the other one", "i meant").
	// Distinct from MoveReject (contesting a claim as wrong). Both are the
	// trouble-source signal (Schegloff 1977) and both count as repairs for Outcome.
	MoveRepair Move = "repair"
	// MoveAccept — the user signals the prior turn served: approval or a "keep it"
	// directive. The success leg of the outcome.
	MoveAccept Move = "accept"
	// MoveNeutral — a turn carrying neither signal (a fresh request, a question, a
	// constraint). Not friction, not outcome — just task content.
	MoveNeutral Move = "neutral"

	// --- v2 outcome-bearing (ferret-bbp.7 Wave 1) ---

	// MoveReject — Disagree-Correct: the user contests a claim or deliverable as
	// wrong ("that's incorrect", "you're mistaken", "still broken"). Split out of v1
	// repair; counts as a repair for Outcome so the split is behavior-preserving.
	MoveReject Move = "reject"
	// MoveClarify — the user answers an agent question / disambiguates in response to
	// it. Context-sensitive: only emitted when the prior agent turn was a question
	// (MoveContext.PriorAgentQuestion). Friction — the agent had to ask.
	MoveClarify Move = "clarify"
	// MoveMetaCommunication — process/style feedback about HOW the agent is
	// communicating, not the task ("be terse", "slow down", "too much detail"). ISO
	// 24617-2 Partner Communication Management. Friction.
	MoveMetaCommunication Move = "meta-communication"
	// MoveConstrain — the user adds a constraint / hedge mid-task ("make sure…",
	// "only for…", "but keep…"). v2 ships this as a LOW-CONFIDENCE lexical candidate
	// flag only (Phase D option b), never a confident tag: it labels the turn but is
	// INERT for Outcome until a follow-on judge (bbp.7b) lands.
	MoveConstrain Move = "constrain"
	// MoveNewTask — a fresh directive / topic switch ("new task:", "moving on to…").
	// TagMove only DETECTS it; the cross-episode abandonment-by-topic-switch verdict
	// is wired in ClassifyCross (ferret-bbp.8), which the retrieval per-episode path
	// feeds with the next segment's opening move. Inert for the pure Classify path.
	MoveNewTask Move = "new-task"

	// --- v2 catalog-only (ferret-bbp.7 Wave 2): tagged for the record, inert for
	// Outcome (they hit Classify's default arm). ---

	// MoveDelegateJudgment — a standing directive handing the agent discretion
	// ("use your judgment", "whichever you prefer", "merge if it improves").
	MoveDelegateJudgment Move = "delegate-judgment"
	// MoveInformFYI — an informational aside ("fyi", "heads up", "note:").
	MoveInformFYI Move = "inform-fyi"
	// MoveSolicitOpinion — an evaluative question ("do you think", "would you
	// recommend", "is it worth").
	MoveSolicitOpinion Move = "solicit-opinion"
	// MoveStatusCheck — a progress interrogative ("status?", "where do we stand",
	// "what's left").
	MoveStatusCheck Move = "status-check"
	// MoveLongPaste — a long structured paste (length + list/code markers) rather
	// than a composed instruction.
	MoveLongPaste Move = "long-paste"
	// MoveRecordDeposit — a persist directive ("save that as a nug", "remember
	// that", "write it down").
	MoveRecordDeposit Move = "record-deposit"
)

// MoveContext carries the per-turn context a context-sensitive move needs. Kept
// SEPARATE from internal/dialogue/attribute.go's TurnContext (retrieval-hop
// attribution, bbp.4/.5) so the two concerns don't entangle: this one is about the
// dialogue turn's neighbours, that one about a retrieval episode's result set.
//
// PriorAgentQuestion is the Phase C signal for MoveClarify — whether the agent's
// immediately-preceding turn was a question the human is now answering. Populating
// it requires walking the assistant turns beside the user turns (a caller that sees
// both), which is a follow-on wiring step; TagMove leaves it zero, so the plain
// (context-free) path never emits MoveClarify.
type MoveContext struct {
	PriorAgentQuestion bool
}

// questionTailRe matches a turn whose FINAL sentence is interrogative — a trailing
// "?" after any closing quote/paren/bracket/whitespace. A "?" buried mid-turn does
// not count: an assistant that asked then kept working left no open dependence link
// for the next user turn to fill. Precision-first, per the bbp regex house style.
var questionTailRe = regexp.MustCompile(`\?["'` + "`" + `)\]\s]*$`)

// AssistantAskedQuestion reports whether an assistant turn ended in a question — the
// deterministic CheckQuestion tell a caller uses to set MoveContext.PriorAgentQuestion.
// Borrowed method: ISO 24617-2 CheckQuestion (Bunt 2012) — a question opens a
// dependence link the next turn answers (→ MoveClarify).
func AssistantAskedQuestion(text string) bool {
	return questionTailRe.MatchString(strings.TrimSpace(text))
}

// permissionPhraseRe matches an assistant turn asking PERMISSION to act ("should I…",
// "want me to…", "shall I…") — as opposed to clarifying WHAT was meant. Paired with a
// trailing "?" it distinguishes a needless interruption (→ miscalibrated-interruption,
// ferret-bbp.11) from a genuine clarify. Borrowed method: Horvitz cost-of-interruption
// (CHI 1999) — confirming an action whose expected value already clears the bar;
// Amershi et al. G8/G9/G10 (CHI 2019).
var permissionPhraseRe = regexp.MustCompile(`(?i)\b(should i|shall (i|we)|want me to|(do|would) you (want|like) me to|ok(ay)? (to|if i)|can i|may i|let me know (if|whether))\b`)

// AssistantAskedPermission reports whether an assistant turn ENDED in a permission-
// seeking question — a trailing question (AssistantAskedQuestion) whose phrasing asks
// to DO something. The tell that routes a human's push-to-execute reaction to
// miscalibrated-interruption (a needless ask) rather than under-initiative (a bare
// explanation). See permissionPhraseRe for the citation.
func AssistantAskedPermission(text string) bool {
	t := strings.TrimSpace(text)
	// The permission phrase must sit in the FINAL question, not anywhere in the turn:
	// "Should I use tabs? Which file did you mean?" closes on a clarify, so the early
	// "should I" must NOT tag it a permission ask (Codex/Gemini #85).
	return AssistantAskedQuestion(t) && permissionPhraseRe.MatchString(lastSentence(t))
}

// lastSentence returns the final sentence of s — the run after the last INTERNAL
// sentence terminator ([.!?]), so a trailing clause is judged on its own. "Should I
// use tabs? Which file did you mean?" → "Which file did you mean?"; a single-sentence
// turn returns unchanged. Abbreviations (e.g. "e.g.") can over-split, but the cost is
// only a missed permission match on a rare shape — precision-safe by construction.
func lastSentence(s string) string {
	s = strings.TrimSpace(s)
	// Drop the trailing terminator(s) so LastIndexAny finds the boundary BEFORE the
	// final sentence, not its own closing mark.
	trimmed := strings.TrimRight(s, " \t?!.")
	if i := strings.LastIndexAny(trimmed, ".!?"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// Signal is one tagged user turn: where it sat, what it did, and the cue that
// fired (kept for traceability — every label points at the substring that
// earned it, so a human validator can audit the matcher cheaply).
type Signal struct {
	Turn int    `json:"turn"` // user-turn index within the session (0-based)
	Move Move   `json:"move"`
	Cue  string `json:"cue,omitempty"` // the matched cue phrase, "" for neutral
	Text string `json:"text"`          // compact label of the turn (truncated by caller)
	// Cause is the agent-side initiative-calibration tag when this turn's reaction
	// reveals one (ferret-bbp.11) — WHY the agent provoked the human's reaction.
	// "" when no initiative-calibration signal; CauseCue is the phrase that matched.
	Cause    AgentCause `json:"cause,omitempty"`
	CauseCue string     `json:"causeCue,omitempty"`
	// Embedded carries the parsed inner-interaction stats when this long-paste
	// turn is itself a pasted CC transcript rather than opaque content
	// (ferret-bbp.19). nil for every other move and for a plain (non-transcript-
	// shaped) long-paste — populated by the caller via ParseEmbeddedInteraction.
	Embedded *EmbeddedInteraction `json:"embedded,omitempty"`
}

// EmbeddedInteraction is the parsed structure of a long-paste turn that is
// itself a pasted CC interaction — ⏺ tool-call / ⎿ result / ❯ embedded user
// meta-turn markers — rather than opaque code/log content (ferret-bbp.19).
// Evidence: trixi session 43ad3b27 (2026-07-21), dk pasted a trixi-ask misfire
// carrying inner tool calls + a buried meta-question that flattened to opaque
// long-paste text, misdirecting the candidate ranker. Cross-session join (the
// paste's ORIGIN transcript) is an explicit non-goal here — this only reads
// what the paste itself carries.
type EmbeddedInteraction struct {
	Calls     int      `json:"calls"`               // inner ⏺ tool-call line count
	Tools     []string `json:"tools,omitempty"`     // distinct tool-name+command-head, first-seen order
	UserTurns int      `json:"userTurns,omitempty"` // inner ❯ embedded user meta-turn count
}

// IsRepairMove reports whether a move counts as a repair for Outcome purposes —
// both the narrowed MoveRepair (Redo-Differently) and MoveReject (Disagree-Correct).
// Callers that gate on "did the human push back on this action" (episode.Classify,
// AttributeHop, the retrieval closing-move tells) MUST use this rather than a bare
// `== MoveRepair`, so the v2 repair→reject split stays behavior-preserving.
func IsRepairMove(m Move) bool { return m == MoveRepair || m == MoveReject }

// rejectCues — Disagree-Correct: the human contests a claim/deliverable as WRONG.
// Split out of v1's repair set. Wrongness is matched as a PREDICATE ("that's wrong",
// "you're mistaken"), never a bare "wrong" — bare `\bwrong\b` false-fires on
// "what's wrong with the parser?" (interrogative), "fix the wrong file" (noun
// modifier), and "nothing wrong with that" (negated). "not right" likewise only
// as a predicate about the work, so "not right now" (temporal) stays neutral.
// Excludes dev jargon ("kill"/"dead"/"crash"/"braindead") on purpose.
var rejectCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(that|this|it)('?s| is| was)\s+(wrong|incorrect|mistaken)\b`),
	regexp.MustCompile(`(?i)\bthat('?s| is)?\s*not (right|correct|true)\b`),
	regexp.MustCompile(`(?i)\bthat('?s| is)n'?t (right|correct|true)\b`),
	regexp.MustCompile(`(?i)\byou('?re| are) (wrong|mistaken|incorrect)\b`),
	regexp.MustCompile(`(?i)\bnot (true|correct|quite)\b`),
	regexp.MustCompile(`(?i)\b(you (missed|forgot|skipped)|still (wrong|broken))\b`),
	regexp.MustCompile(`(?i)\bactually,?\s+no\b`),
}

// repairCues — Redo-Differently: the human redirects the work (retry, undo, "the
// other one", re-specify, dismiss the approach). Narrowed from v1 (wrongness-claim
// cues moved to rejectCues). A leading bare "no" is handled separately (matchRepair)
// so "no worries"/"no problem" don't false-fire. "not that <object>" requires the
// object so the intensifier "not that hard"/"not that big a deal" stays neutral.
var repairCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(try again|redo|do it (again|differently)|do that (again|differently))\b`),
	regexp.MustCompile(`(?i)\bdo(n'?t| not) (do )?that\b`),
	regexp.MustCompile(`(?i)\b(undo|revert|roll back)\b`),
	regexp.MustCompile(`(?i)\bnot that (one|file|way|thing|approach|part|line|function)\b`),
	regexp.MustCompile(`(?i)\bnot what (i|you|we)\b`),
	regexp.MustCompile(`(?i)\bthe other (one|file|way|approach)\b`),
	// dismissal — "that's not the point", "that isn't it", "that's not going to
	// work": v1 counted these as repair; the v2 narrowing dropped them (recall
	// regression). Matched on the specific dismissal OBJECTS, not a blanket
	// "that('s) not …" — the blanket form false-fires on the intensifier/assessment
	// "that's not that hard" (P1-3). Reject's wrongness cues run first, so "that's
	// not right" still routes to reject.
	regexp.MustCompile(`(?i)\bthat('?s| is) not (the (point|answer|issue|problem|goal|idea)|going to work|gonna work|it|what)\b`),
	regexp.MustCompile(`(?i)\bthat isn'?t (it|right|what|going to work)\b`),
	regexp.MustCompile(`(?i)\bthat (won'?t|doesn'?t|wouldn'?t) work\b`),
	regexp.MustCompile(`(?i)\b(i mean|i meant|what i meant|as i (said|asked)|like i said)\b`),
}

// leadingNoRe / benignNoRe gate the leading-"no" redirect ("no, use X instead"):
// a genuine correction, EXCEPT the reassurance openers "no worries"/"no problem"/
// "no rush" etc. RE2 has no lookahead, so the exclusion is a second pattern.
var (
	leadingNoRe = regexp.MustCompile(`(?i)^(no|nope|nah)\b`)
	benignNoRe  = regexp.MustCompile(`(?i)^(no|nope|nah)\b\s+(worries|worry|problem|problemo|rush|hurry|biggie|big deal|thanks|thank you)\b`)
)

// matchRepair reports whether target is a Redo-Differently move — a repairCue OR a
// leading "no" that is not a benign reassurance opener.
func matchRepair(target string) (cue string, ok bool) {
	if cue, ok := firstMatch(target, repairCues); ok {
		return cue, true
	}
	if leadingNoRe.MatchString(target) && !benignNoRe.MatchString(target) {
		return "no", true
	}
	return "", false
}

// metaCues — Partner Communication Management (ISO 24617-2): feedback on HOW the
// agent communicates, not the task. Narrowed (dk plan-review) to communication-
// directed phrasing only: bare "shorter"/"smaller"/"speed up"/"more detail" false-
// fire on ordinary coding tasks ("make it shorter", "speed up the query", "split
// this file", "make the function shorter"), which would inject noise into the
// friction/Outcome signal on dk's own transcripts. Genuine process complaints stay.
var metaCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbe (more )?(terse|succinct|concise|brief|quiet)\b`),
	regexp.MustCompile(`(?i)\btoo (verbose|wordy|long-winded|chatty)\b`),
	regexp.MustCompile(`(?i)\btoo much (detail|explanation|narration|output|talking)\b`),
	regexp.MustCompile(`(?i)\byou('?re| are) (being )?(too )?(verbose|wordy|chatty|repeating yourself)\b`),
	regexp.MustCompile(`(?i)\b(slow down|stop explaining|stop narrating|stop asking|no need to (explain|narrate)|don'?t (explain|narrate))\b`),
	regexp.MustCompile(`(?i)\b(less (detail|verbose)|keep it (short|brief|terse))\b`),
}

// delegateCues — a standing directive handing the agent discretion.
var delegateCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\buse your (judg?ement|judgment|discretion)\b`),
	regexp.MustCompile(`(?i)\b(your call|up to you|you decide|as you see fit)\b`),
	regexp.MustCompile(`(?i)\bwhichever you (prefer|think|like)\b`),
	regexp.MustCompile(`(?i)\b(whatever you think|if it improves|merge if)\b`),
}

// recordDepositCues — a persist directive. Ranked ABOVE newTask so "save that as a
// nug" (imperative + object) is a deposit, not a fresh task (Phase B.0 collision).
var recordDepositCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(save|write|record|store|jot)\b[^.]*\b(as|to|in|down)\b[^.]*\bnug\b`),
	regexp.MustCompile(`(?i)\b(remember (that|this|to)|write (that|this|it) down|jot (that|this) down)\b`),
	regexp.MustCompile(`(?i)\b(make a note|add a bead|file a bead|save (that|this|it) as a nug)\b`),
}

// newTaskCues — a fresh directive / topic switch. DETECTION floor: keyed on
// explicit topic-switch + abandon-switch markers, NOT bare imperatives (those would
// swallow every neutral task turn). Precision over recall — deeper recall tuning
// belongs with ferret-bbp.8, where the abandonment-by-topic-switch effect on Outcome
// is measurable; tuning cues here blind is guessing.
var newTaskCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(new task|new topic|next up|moving on|switching to|unrelated)\b`),
	regexp.MustCompile(`(?i)^(next task|different (task|thing|topic)|one more (thing|task))\b`),
	regexp.MustCompile(`(?i)^(let'?s (move on|switch|do something (else|different))|on to the next)\b`),
	regexp.MustCompile(`(?i)^(forget (that|this|it)|scrap (that|this|it)|never ?mind( that| this| it)?)\b`),
	regexp.MustCompile(`(?i)\b(separate (task|issue|thing)|switch(ing)? gears|moving on to)\b`),
}

// statusCheckCues — a progress interrogative.
var statusCheckCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(status\?|progress\?|where (do we|are we) stand|where are we)\b`),
	regexp.MustCompile(`(?i)\b(what'?s (left|the status)|how'?s it going|are we done|is it done)\b`),
}

// solicitCues — an evaluative question soliciting the agent's opinion.
var solicitCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b((what )?do you think|would you recommend|do you recommend)\b`),
	regexp.MustCompile(`(?i)\b(is it worth|worth (doing|it)|would you|thoughts\?)\b`),
}

// informCues — an informational aside.
var informCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(fyi|note:|heads up|just so you know|for (the )?record|for reference)\b`),
	regexp.MustCompile(`(?i)\b(i noticed|just noting|for what it'?s worth)\b`),
}

// constrainCues — a hedge/constraint mid-task. LOW-CONFIDENCE candidate flag only
// (Phase D-b): residual, checked below the confident moves, and INERT for Outcome.
var constrainCues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(make sure|be sure to|just (make sure|ensure)|only (if|when|for|the)|as long as)\b`),
	regexp.MustCompile(`(?i)\b(but keep|but don'?t|without (breaking|changing|touching)|stick to|don'?t break)\b`),
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

// imageMarkerRe strips Claude Code's pasted-image placeholder ("[Image #1]") from a
// turn before it is judged, so a leading marker can't push a real cue out of the
// lead window or defeat a `^`-anchored cue (segment.go folds a WHOLE-turn marker;
// this handles the embedded case for every TagMove caller, including the events
// path). Mirrors internal/score/segment.go's wholeImageMarkerRe.
// TODO(bbp): a markdown image whose alt text is literally "Image"/"Image #N"
// (`![Image](url)`) would be stripped here — RE2 has no lookbehind to require the
// preceding char is not "!", and the FP is cosmetic + rare, so left as-is.
var imageMarkerRe = regexp.MustCompile(`(?i)\[image(\s+#\d+)?\]`)

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
	// longPasteRunes is the length above which a structured turn (list/code markers)
	// is a paste, not a composed instruction. Well above signalShortRunes so an
	// ordinary long instruction is not mislabeled.
	longPasteRunes = 800
)

// TagMove classifies one user turn into a v2 Move and returns the cue that fired.
// It is the context-free entry point (MoveContext zero) — it never emits
// MoveClarify, which needs the prior-agent-question signal. See TagMoveContext.
func TagMove(turn string) (Move, string) {
	return TagMoveContext(turn, MoveContext{})
}

// TagMoveContext classifies one user turn under the v2 taxonomy, consulting ctx for
// the context-sensitive moves. The precedence is a single TOTAL order (Phase B.0),
// highest first — the friction/outcome-bearing moves win over catalog and accept,
// and the low-confidence constrain flag + structural long-paste are residuals:
//
//	reject → repair → meta → delegate → record-deposit → new-task → status-check →
//	solicit-opinion → inform-fyi → accept → constrain → clarify → long-paste → neutral
//
// Real collisions resolved by the order: reject beats repair ("no, that's wrong" is
// a wrongness claim); meta beats delegate ("use your judgment but be terse" is
// process feedback); record-deposit beats new-task ("save that as a nug" is a
// deposit, not a fresh directive). MoveClarify sits low — it rescues a would-be
// neutral turn that is actually answering the agent's question.
//
// The argument is the genuine user-prompt text (whitespace-collapsed); the caller
// is responsible for having stripped tool-result carriers and command envelopes —
// this function judges words, not transcript plumbing.
func TagMoveContext(turn string, ctx MoveContext) (Move, string) {
	t := strings.TrimSpace(imageMarkerRe.ReplaceAllString(turn, " "))
	t = strings.TrimSpace(collapseSpaces(t))
	if t == "" {
		return MoveNeutral, ""
	}

	// Long structured paste is judged on the FULL turn (length is the signal), but
	// only as a residual so it can't preempt a dialogue cue sitting in the lead.
	longPasteCue, longPasteHit := longPaste(t)

	target := t
	if isLong(t) {
		target = leadWindow(t, signalLeadRunes) // long turn: judge only the lead
	}

	// Total precedence order — first match wins. Reject and repair lead (repair via
	// matchRepair, which folds in the guarded leading-"no"); the rest are plain
	// cue-list passes.
	if cue, ok := firstMatch(target, rejectCues); ok {
		return MoveReject, cue
	}
	if cue, ok := matchRepair(target); ok {
		return MoveRepair, cue
	}
	for _, pass := range []struct {
		move Move
		pats []*regexp.Regexp
	}{
		{MoveMetaCommunication, metaCues},
		{MoveDelegateJudgment, delegateCues},
		{MoveRecordDeposit, recordDepositCues},
		{MoveNewTask, newTaskCues},
		{MoveStatusCheck, statusCheckCues},
		{MoveSolicitOpinion, solicitCues},
		{MoveInformFYI, informCues},
		{MoveAccept, acceptCues},
		{MoveConstrain, constrainCues},
	} {
		if cue, ok := firstMatch(target, pass.pats); ok {
			return pass.move, cue
		}
	}
	// Clarify is the context-sensitive rescue: a turn that matched none of the above
	// but arrived right after an agent question is the human answering it.
	if ctx.PriorAgentQuestion {
		return MoveClarify, "prior-agent-question"
	}
	if longPasteHit {
		return MoveLongPaste, longPasteCue
	}
	return MoveNeutral, ""
}

// longPaste reports whether s is a long structured paste — over longPasteRunes with
// list, fenced-code, or CC-transcript markers — so a bulk paste reads as content,
// not instruction.
func longPaste(s string) (cue string, ok bool) {
	if !overRunes(s, longPasteRunes) {
		return "", false
	}
	if strings.Contains(s, "```") {
		return "code-fence", true
	}
	if strings.Count(s, "- ")+strings.Count(s, "* ")+strings.Count(s, "• ") >= 3 {
		return "list-markers", true
	}
	if isTranscriptShaped(s) {
		return "cc-transcript", true
	}
	return "", false
}

// transcriptMarkerRe matches any of the three CC transcript-rendering markers a
// paste of a prior CC interaction carries: ⏺ opens a tool-call, ⎿ opens its
// result, ❯ opens an embedded user turn. Position-based (not line-anchored):
// TagMoveContext collapses whitespace (including newlines) before the residual
// long-paste check runs, so a marker scan must not depend on "^"/"$" surviving
// that collapse — it walks marker OCCURRENCES and slices the text between them
// instead, which is robust whether or not the original newlines survived.
var transcriptMarkerRe = regexp.MustCompile(`⏺|⎿|❯`)

// toolNameHeadRe extracts the tool-name + command head from a ⏺ segment's
// captured text: the run up to the first "(" (a call's opening paren).
var toolNameHeadRe = regexp.MustCompile(`^([^(\n]+)\(`)

// transcriptCallFloor is the minimum inner ⏺ tool-call count that reads as
// STRUCTURAL density rather than one stray glyph mention. Glyph renderings can
// vary across paste sources (bead constraint: "match on marker + structure, not
// glyph alone"), so isTranscriptShaped never trusts a single bare ⏺ — it
// requires call DENSITY corroborated by a ⎿ result or ❯ user-turn marker
// somewhere in the text.
const transcriptCallFloor = 2

// isTranscriptShaped reports whether s carries enough CC-transcript markers — ⏺
// tool-call density, corroborated by a ⎿ result or ❯ embedded user turn — to
// read as a pasted prior interaction rather than opaque code/log content.
func isTranscriptShaped(s string) bool {
	if strings.Count(s, "⏺") < transcriptCallFloor {
		return false
	}
	return strings.Contains(s, "⎿") || strings.Contains(s, "❯")
}

// ParseEmbeddedInteraction parses a long-paste turn into its embedded-
// interaction stats: inner tool calls, distinct tool names, and inner user
// meta-turns (ferret-bbp.19). ok is false when s is not transcript-shaped — a
// plain code/log dump stays a bare long-paste, no false promotion.
func ParseEmbeddedInteraction(s string) (*EmbeddedInteraction, bool) {
	if !isTranscriptShaped(s) {
		return nil, false
	}
	markers := transcriptMarkerRe.FindAllStringIndex(s, -1)
	ei := &EmbeddedInteraction{}
	seen := make(map[string]bool, len(markers))
	for i, m := range markers {
		end := len(s)
		if i+1 < len(markers) {
			end = markers[i+1][0]
		}
		content := strings.TrimSpace(s[m[1]:end])
		switch s[m[0]:m[1]] {
		case "⏺":
			ei.Calls++
			name := toolNameHead(content)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			ei.Tools = append(ei.Tools, name)
		case "❯":
			ei.UserTurns++
		}
	}
	return ei, true
}

// toolNameHead extracts the tool-name + command head from one ⏺ segment's
// captured text: the run up to the first "(" (a call's opening paren), trimmed.
// Absent a paren, the whole trimmed segment, capped to a short window so a
// runaway segment can't dominate the Tools list.
func toolNameHead(seg string) string {
	if m := toolNameHeadRe.FindStringSubmatch(seg); m != nil {
		return strings.TrimSpace(m[1])
	}
	return leadWindow(seg, 40)
}

// isLong reports whether s exceeds the short-turn threshold (rune-count short
// circuit so a huge paste isn't fully sliced just to measure it).
func isLong(s string) bool { return overRunes(s, signalShortRunes) }

// overRunes reports whether s has more than n runes, counting with a short circuit.
func overRunes(s string, n int) bool {
	count := 0
	for range s {
		count++
		if count > n {
			return true
		}
	}
	return false
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

// collapseSpaces folds runs of whitespace into single spaces — image-marker
// stripping can leave doubled spaces that would defeat a cue's word boundaries.
func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

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
