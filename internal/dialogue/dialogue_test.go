package dialogue

import (
	"slices"
	"strings"
	"testing"
)

func TestTagMove(t *testing.T) {
	tests := []struct {
		name string
		turn string
		want Move
	}{
		// repair (v2 narrowed → Redo-Differently): redirect the work
		{"leading no", "no, use snipe instead", MoveRepair},
		{"not that", "not that one, the other file", MoveRepair},
		{"try again", "try again with the flag set", MoveRepair},
		{"i meant", "i meant the other package", MoveRepair},
		{"undo", "undo that change", MoveRepair},
		{"the other one", "use the other file instead", MoveRepair},

		// reject (v2 split → Disagree-Correct): contest a claim as wrong
		{"thats wrong", "that's wrong", MoveReject},
		{"youre wrong", "you're wrong about that", MoveReject},
		{"incorrect", "that is incorrect", MoveReject},
		{"you missed", "you missed the error case", MoveReject},
		{"still broken", "still broken after that", MoveReject},
		{"actually no", "actually no, that's not right", MoveReject},
		{"not correct", "that's not correct", MoveReject},

		// P0-1 repair-class recall: broad "that('s/is) (not|isn't) …" dismissals that
		// v1 counted as repair and the v2 narrowing dropped (they must count again).
		{"thats not the point", "that's not the point", MoveRepair},
		{"that isnt it", "that isn't it", MoveRepair},
		{"thats not going to work", "that's not going to work", MoveRepair},
		{"that is not what i want", "that is not what i want here", MoveRepair},

		// accept — success leg
		{"leading yes", "yes exactly that", MoveAccept},
		{"great", "great, that works", MoveAccept},
		{"lgtm", "lgtm ship it", MoveAccept},
		{"save this", "save this to the kg", MoveAccept},
		{"thanks", "thanks, perfect", MoveAccept},

		// meta-communication — process/style feedback (ISO PCM)
		{"be terse", "be terse from now on", MoveMetaCommunication},
		{"too verbose", "that's too verbose, cut it down", MoveMetaCommunication},
		{"slow down", "slow down and explain step by step", MoveMetaCommunication},
		{"stop explaining", "stop explaining and just do it", MoveMetaCommunication},

		// new-task detection — explicit topic switch (not bare imperatives)
		{"new task", "new task: refactor the parser", MoveNewTask},
		{"moving on", "moving on to the deploy script", MoveNewTask},
		{"switching to", "switching to the docs now", MoveNewTask},
		{"unrelated", "unrelated: the CI is red", MoveNewTask},
		// P1-4 added abandon-switch markers
		{"forget that", "forget that, let's look at the store", MoveNewTask},
		{"scrap that", "scrap that approach entirely", MoveNewTask},
		{"never mind that", "never mind that", MoveNewTask},
		{"new topic", "new topic — the deploy pipeline", MoveNewTask},
		{"next up", "next up, wire the CLI", MoveNewTask},

		// delegate-judgment — standing directive (catalog)
		{"use your judgment", "use your judgment on the naming", MoveDelegateJudgment},
		{"whichever", "whichever you prefer is fine", MoveDelegateJudgment},
		{"merge if", "merge if it improves the code", MoveDelegateJudgment},

		// record-deposit — persist directive (catalog); beats new-task on collision
		{"save as nug", "save that as a nug", MoveRecordDeposit},
		{"remember", "remember that we chose option b", MoveRecordDeposit},
		{"write it down", "write that down for later", MoveRecordDeposit},

		// status-check / solicit-opinion / inform-fyi (catalog)
		{"status", "status? where do we stand", MoveStatusCheck},
		{"whats left", "what's left to do here", MoveStatusCheck},
		{"do you think", "do you think this is the right approach", MoveSolicitOpinion},
		{"worth it", "is it worth adding a cache", MoveSolicitOpinion},
		{"fyi", "fyi the daemon restarted overnight", MoveInformFYI},
		{"heads up", "heads up, the schema changed", MoveInformFYI},

		// constrain — low-confidence flag (residual)
		{"make sure", "make sure it still passes the linter", MoveConstrain},
		{"but keep", "but keep the public API stable", MoveConstrain},

		// neutral — fresh task content, no signal
		{"fresh request", "add a test for the parser", MoveNeutral},
		{"question", "where do the events live?", MoveNeutral},
		{"empty", "", MoveNeutral},

		// dev-jargon trap — workflow vocabulary, NOT repair/reject or affect
		{"kill jargon", "kill that process and restart", MoveNeutral},
		{"dead jargon", "the daemon is dead, boot it", MoveNeutral},
		{"crash jargon", "reproduce the crash then patch it", MoveNeutral},
		{"braindead jargon", "the braindead retry loop needs a cap", MoveNeutral},

		// precedence: reject beats repair; both present
		{"reject beats repair", "no, that's wrong, try again", MoveReject},
		{"repair when only redirect", "no, but the rename is great", MoveRepair},
		// meta beats delegate on collision
		{"meta beats delegate", "use your judgment but be terse", MoveMetaCommunication},

		// embedded image marker stripped so the leading cue still fires
		{"image marker stripped", "[Image #1] that's wrong", MoveReject},

		// long-turn gate: a cue buried deep in composed/pasted content is task
		// content, not a correction — only the leading window counts.
		{"buried cue in long turn", "Help me respond to Jeremy about the call next week " +
			strings.Repeat("with a lot of neutral lead-in prose first ", 5) +
			"and then that isn't relevant", MoveNeutral},
		{"leading cue in long turn", "no, use bar instead — " +
			strings.Repeat("here is the long explanation that follows ", 10), MoveRepair},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := TagMove(tt.turn)
			if got != tt.want {
				t.Errorf("TagMove(%q) = %q, want %q", tt.turn, got, tt.want)
			}
		})
	}
}

// TestTagMoveFixPassNegatives pins the false-positive guards from the bbp.7 fix
// pass — ordinary coding/workflow turns that MUST NOT tag an outcome-bearing move,
// so the friction/Outcome signal stays clean on dk's own transcripts.
func TestTagMoveFixPassNegatives(t *testing.T) {
	tests := []struct {
		name string
		turn string
		want Move
	}{
		// P1-1: bare "wrong"/"not right" must not tag reject.
		{"interrogative wrong", "what's wrong with the parser?", MoveNeutral},
		{"noun-phrase wrong", "fix the wrong file", MoveNeutral},
		{"negated wrong", "nothing wrong with that", MoveNeutral},
		{"temporal not right now", "not right now, later", MoveNeutral},
		// P1-2: code/perf phrasing must not tag meta-communication.
		{"split file", "split this file into two", MoveNeutral},
		{"make it shorter", "make it shorter", MoveNeutral},
		{"make it smaller", "make it smaller", MoveNeutral},
		{"speed up query", "speed up the query", MoveNeutral},
		{"function shorter", "make the function shorter", MoveNeutral},
		// P1-3: intensifier/assessment construction must not tag repair. "that's not
		// that hard" is an assessment (task is easy), NOT a dismissal — dismissal
		// cues key on specific objects (point/it/work), not a blanket "that's not".
		{"assessment not that hard", "that's not that hard to do", MoveNeutral},
		{"bare not that hard", "not that hard", MoveNeutral},
		{"not that big a deal", "not that big a deal", MoveNeutral},
		// P2: benign leading "no" must not tag repair.
		{"no worries", "no worries", MoveNeutral},
		{"no rush", "no rush on that", MoveNeutral},
		{"no problem then accept", "no problem, ship it", MoveAccept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := TagMove(tt.turn); got != tt.want {
				t.Errorf("TagMove(%q) = %q, want %q", tt.turn, got, tt.want)
			}
		})
	}
}

// TestTagMoveContextClarify covers the Phase C context-sensitive move: a turn that
// matches no lexical cue but arrives right after an agent question is the human
// answering it (MoveClarify). Without the context flag the same turn is neutral,
// and a turn that DOES match a friction cue keeps that stronger label.
func TestTagMoveContextClarify(t *testing.T) {
	tests := []struct {
		name string
		turn string
		ctx  MoveContext
		want Move
	}{
		{"answer after question is clarify", "the second one", MoveContext{PriorAgentQuestion: true}, MoveClarify},
		{"same turn no context is neutral", "the second one", MoveContext{}, MoveNeutral},
		{"reject still wins over clarify", "no, that's wrong", MoveContext{PriorAgentQuestion: true}, MoveReject},
		{"accept still wins over clarify", "yes, perfect", MoveContext{PriorAgentQuestion: true}, MoveAccept},
		{"plain TagMove never emits clarify", "the second one", MoveContext{}, MoveNeutral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := TagMoveContext(tt.turn, tt.ctx); got != tt.want {
				t.Errorf("TagMoveContext(%q, %+v) = %q, want %q", tt.turn, tt.ctx, got, tt.want)
			}
		})
	}
}

// TestAssistantAskedQuestion covers the CheckQuestion predicate that populates
// MoveContext.PriorAgentQuestion: a trailing "?" (through closing punctuation) is a
// question; a mid-turn "?" followed by more work is not; empty is not.
func TestAssistantAskedQuestion(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"trailing question", "Which file should I edit?", true},
		{"trailing through closing paren", "Should I use the first one (foo.go)?)", true},
		{"trailing whitespace after mark", "Do you want me to proceed?  \n", true},
		{"mid-turn question then work", "Should I? Actually I'll just do it.", false},
		{"statement", "I edited the file.", false},
		{"empty", "", false},
		{"whitespace only", "   \n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AssistantAskedQuestion(tt.text); got != tt.want {
				t.Errorf("AssistantAskedQuestion(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// TestLongPaste covers the structural catalog move: a long turn with list/code
// markers reads as a paste, but a long composed instruction with a leading cue does
// not (the cue wins), and a short list is not a paste.
func TestLongPaste(t *testing.T) {
	longList := strings.Repeat("- item with enough text to clear the length floor here\n", 30)
	if got, _ := TagMove(longList); got != MoveLongPaste {
		t.Errorf("long bulleted paste = %q, want long-paste", got)
	}
	longFence := "```\n" + strings.Repeat("some code line goes here and there\n", 30) + "```"
	if got, _ := TagMove(longFence); got != MoveLongPaste {
		t.Errorf("long code-fence paste = %q, want long-paste", got)
	}
	if got, _ := TagMove("- one\n- two\n- three"); got != MoveNeutral {
		t.Errorf("short list = %q, want neutral (under length floor)", got)
	}
}

// transcriptPasteFixture builds a synthetic long-paste turn shaped like a pasted
// CC interaction: 6 inner ⏺ tool-call lines (3 distinct tools, each called
// twice), a ⎿ result line under each, and one ❯ embedded user meta-turn — the
// ferret-bbp.19 evidence shape (recall-misfire paste carrying inner tool calls +
// a buried human aside). Filler padding clears the longPasteRunes floor without
// itself tripping any dialogue cue in the lead window.
func transcriptPasteFixture() string {
	filler := strings.Repeat("some ordinary padding text to clear the length floor for this fixture case ", 8)
	return filler + "\n" +
		"⏺ trixi ask(query)\n⎿ result: 3 hits\n" +
		"⏺ trixi get(id)\n⎿ result: ok\n" +
		"⏺ trixi search(term)\n⎿ result: ok\n" +
		"⏺ trixi ask(query2)\n⎿ result: ok\n" +
		"⏺ trixi get(id2)\n⎿ result: ok\n" +
		"⏺ trixi search(term2)\n⎿ result: ok\n" +
		"❯ wait, that's not what I asked for\n"
}

// TestParseEmbeddedInteraction covers the transcript-shaped long-paste path
// (ferret-bbp.19): a paste carrying ⏺/⎿/❯ markers at structural density yields
// the inner call count, distinct tool names, and inner user-meta-turn count.
func TestParseEmbeddedInteraction(t *testing.T) {
	ei, ok := ParseEmbeddedInteraction(transcriptPasteFixture())
	if !ok {
		t.Fatal("ParseEmbeddedInteraction: want ok=true for transcript-shaped paste")
	}
	if ei.Calls < 6 {
		t.Errorf("Calls = %d, want >= 6", ei.Calls)
	}
	wantTools := []string{"trixi ask", "trixi get", "trixi search"}
	for _, want := range wantTools {
		if !slices.Contains(ei.Tools, want) {
			t.Errorf("Tools = %v, want to contain %q", ei.Tools, want)
		}
	}
	if ei.UserTurns != 1 {
		t.Errorf("UserTurns = %d, want 1", ei.UserTurns)
	}
}

// TestParseEmbeddedInteractionRejectsPlainPaste guards the negative case: a
// plain long code-fence or bulleted-list paste (no CC transcript markers) must
// NOT be promoted to a transcript read — no false promotion on the existing
// dialogue goldens (ferret-bbp.19 AC).
func TestParseEmbeddedInteractionRejectsPlainPaste(t *testing.T) {
	longList := strings.Repeat("- item with enough text to clear the length floor here\n", 30)
	if _, ok := ParseEmbeddedInteraction(longList); ok {
		t.Error("plain bulleted paste must not parse as a transcript-shaped interaction")
	}
	longFence := "```\n" + strings.Repeat("some code line goes here and there\n", 30) + "```"
	if _, ok := ParseEmbeddedInteraction(longFence); ok {
		t.Error("plain code-fence paste must not parse as a transcript-shaped interaction")
	}
	// A single stray glyph (no density, no corroborating marker) must not fire —
	// "marker + structure, not glyph alone" (bead constraint).
	oneGlyph := strings.Repeat("padding text to clear the length floor for this case ", 20) + "⏺ mentioned once in passing"
	if _, ok := ParseEmbeddedInteraction(oneGlyph); ok {
		t.Error("a single ⏺ glyph with no corroborating density must not fire")
	}
}

// TestTagMoveStillLongPasteOnTranscriptShapedPaste guards the non-goal boundary:
// a transcript-shaped paste still classifies as MoveLongPaste (no new Move/lens
// — ferret-bbp.19 shape), just with richer stats available via
// ParseEmbeddedInteraction.
func TestTagMoveStillLongPasteOnTranscriptShapedPaste(t *testing.T) {
	if got, _ := TagMove(transcriptPasteFixture()); got != MoveLongPaste {
		t.Errorf("transcript-shaped paste move = %q, want long-paste", got)
	}
}

// TestIsRepairMove pins the predicate the repair→reject split rides on: both moves
// count as repairs so episode.Classify / AttributeHop / the retrieval tells stay
// behavior-preserving.
func TestIsRepairMove(t *testing.T) {
	for _, m := range []Move{MoveRepair, MoveReject} {
		if !IsRepairMove(m) {
			t.Errorf("IsRepairMove(%q) = false, want true", m)
		}
	}
	for _, m := range []Move{MoveAccept, MoveNeutral, MoveClarify, MoveConstrain, MoveNewTask} {
		if IsRepairMove(m) {
			t.Errorf("IsRepairMove(%q) = true, want false", m)
		}
	}
}

func TestTagMoveCue(t *testing.T) {
	if _, cue := TagMove("no, try the other one"); cue == "" {
		t.Error("repair turn returned empty cue; want the matched phrase for traceability")
	}
	if _, cue := TagMove("just add a function"); cue != "" {
		t.Errorf("neutral turn returned cue %q; want empty", cue)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		moves []Move
		want  Outcome
	}{
		{"clean success", []Move{MoveNeutral, MoveAccept}, OutcomeSuccess},
		{"one repair then accept", []Move{MoveNeutral, MoveRepair, MoveAccept}, OutcomeSuccess},
		{"repair heavy but lands", []Move{MoveRepair, MoveRepair, MoveRepair, MoveAccept}, OutcomeRepairHeavy},
		{"abandoned on repair", []Move{MoveNeutral, MoveRepair}, OutcomeAbandoned},
		{"accept then trailing repair", []Move{MoveRepair, MoveAccept, MoveRepair}, OutcomeAbandoned},
		{"no signal", []Move{MoveNeutral, MoveNeutral}, OutcomeUnknown},
		{"empty", nil, OutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.moves); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.moves, got, tt.want)
			}
		})
	}
}

// TestClassifyTurns is the per-EPISODE rollup contract (ferret-bbp.2): an
// episode's ordered user turns (a segment's opening prompt + folded acks) tag to
// moves and roll up into one Outcome. Covers the success leg and every non-success
// path, including repair-heavy — which only surfaces when several repairs land in
// the SAME episode (a structure the move-string view expresses but per-segment
// extraction rarely yields, since most repairs open fresh segments).
func TestClassifyTurns(t *testing.T) {
	tests := []struct {
		name  string
		turns []string
		want  Outcome
	}{
		{"neutral then accept is success",
			[]string{"add a foo function", "lgtm ship it"}, OutcomeSuccess},
		{"one repair then accept still success",
			[]string{"add a foo function", "no, use bar", "great, that works"}, OutcomeSuccess},
		{"three repairs then accept is repair-heavy",
			[]string{"no wrong", "still wrong", "no not that", "yes perfect"}, OutcomeRepairHeavy},
		{"opens on repair, no accept is abandoned",
			[]string{"no, that's wrong, revert it"}, OutcomeAbandoned},
		{"only neutral turns is unknown",
			[]string{"add a test for the parser", "where do the events live?"}, OutcomeUnknown},
		{"empty episode is unknown", nil, OutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyTurns(tt.turns); got != tt.want {
				t.Errorf("ClassifyTurns(%q) = %q, want %q", tt.turns, got, tt.want)
			}
		})
	}
}

func TestAttributeHop(t *testing.T) {
	tests := []struct {
		name string
		move Move
		ctx  TurnContext
		want Hop
	}{
		{"non-repair is none", MoveAccept, TurnContext{SelfRequery: true}, HopNone},
		{"self-requery is interp", MoveRepair, TurnContext{SelfRequery: true}, HopInterp},
		{"empty result is retrieval", MoveRepair, TurnContext{EmptyResult: true}, HopRetrieval},
		{"oversized is retrieval", MoveRepair, TurnContext{OversizedResult: true}, HopRetrieval},
		{"no signal is none", MoveRepair, TurnContext{}, HopNone},
		{"self-requery beats retrieval", MoveRepair, TurnContext{SelfRequery: true, EmptyResult: true}, HopInterp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AttributeHop(tt.move, tt.ctx); got != tt.want {
				t.Errorf("AttributeHop(%q, %+v) = %q, want %q", tt.move, tt.ctx, got, tt.want)
			}
		})
	}
}
