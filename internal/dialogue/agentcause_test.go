package dialogue

import "testing"

// TestTagAgentCausePrematureStop: a human turn demanding continuation after the
// agent stopped short tags CausePrematureStop; ordinary turns (including a fresh
// "continue with the next task" that isn't a stopped-early complaint) do not —
// the over-flag guard, since a false premature-stop libels a correct handoff.
func TestTagAgentCausePrematureStop(t *testing.T) {
	fires := []string{
		"you didn't finish — there are three more files",
		"why did you stop? keep going",
		"you're not done, the tests still fail",
		"finish the job",
		"keep going",
		"that's not all — do the rest",
		"you only did half of them",
		"continue",
		"don't stop now",
	}
	for _, s := range fires {
		if cause, cue, ok := TagAgentCause(s); !ok || cause != CausePrematureStop {
			t.Errorf("%q → (%q,%q,%v), want premature-stop", s, cause, cue, ok)
		}
	}

	quiet := []string{
		"looks great, ship it",
		"now add a test for the edge case",
		"no, use the other file",
		"can you explain why?",
		"the continuation of the contract is in section 4", // 'continuation' mid-sentence, not a demand
		"go ahead and merge it",
	}
	for _, s := range quiet {
		if cause, _, ok := TagAgentCause(s); ok {
			t.Errorf("%q → %q, want no cause (false-fire)", s, cause)
		}
	}
}

// TestTagAgentCauseOverInitiative: a human turn reversing an UNREQUESTED action
// tags CauseOverInitiative; a bare undo/redirect (an ordinary approach repair) and
// benign turns do not — the precision guard, since a false over-initiative libels a
// correctly-scoped action. A continuation demand still wins premature-stop.
func TestTagAgentCauseOverInitiative(t *testing.T) {
	fires := []string{
		"undo that — I didn't ask you to refactor it",
		"revert, I never asked for a rewrite",
		"who told you to delete the file?",
		"you weren't supposed to touch main.go",
		"stop editing the tests",
		"leave it alone",
		"leave the config alone",
		"leave the main.go alone",     // filename with a dot after "the"
		"leave the config file alone", // multi-word noun phrase (Gemini #83)
		"why did you overwrite my changes",
		"why did you create this file",  // 'create' — retrospective (Gemini #83)
		"why did you update the schema", // 'update' — retrospective (Gemini #83)
	}
	for _, s := range fires {
		if cause, cue, ok := TagAgentCause(s); !ok || cause != CauseOverInitiative {
			t.Errorf("%q → (%q,%q,%v), want over-initiative", s, cause, cue, ok)
		}
	}

	quiet := []string{
		"undo that",                  // bare reversal — repair, not an over-step claim
		"no, use the other file",     // approach redirect
		"why did you choose sonnet?", // 'choose' is not a mutating verb
		"don't break the build",      // a constraint, not a stop-mutating order
		// A bare prospective imperative reads identically to a forward constraint and
		// so is EXCLUDED — the confirmation that a mutation already happened is the
		// deferred bbp.18 agent-turn leg (Codex #83).
		"don't modify the schema",
		"refactor the parser, but don't modify the schema",
		"add the endpoint, don't touch the tests",
		"I didn't realize that was set", // 'realize', not a request disclaimer
		"add a test for the edge case",
		"leave the rest for tomorrow", // 'leave X for Y', not 'leave X alone'
	}
	for _, s := range quiet {
		if cause, _, ok := TagAgentCause(s); ok && cause == CauseOverInitiative {
			t.Errorf("%q → %q, want no over-initiative (false-fire)", s, cause)
		}
	}

	// Precedence: a continuation demand stays premature-stop even if it also carries
	// a stop/mutate word.
	if cause, _, _ := TagAgentCause("you didn't finish, keep going"); cause != CausePrematureStop {
		t.Errorf("continuation demand → %q, want premature-stop", cause)
	}
}

// TestTagAgentCauseUnderInitiative: after the agent EXPLAINED where execution was
// wanted (no tool call, no permission ask), a push-to-execute reaction tags
// under-initiative; the same reaction after the agent ACTED does not (it reads as a
// next step, not an under-action). Keyed on the human's words + the agent-turn context.
func TestTagAgentCauseUnderInitiative(t *testing.T) {
	proseOnly := AgentContext{AgentActed: false, AgentAskedPermission: false}
	fires := []string{
		"just do it",
		"stop explaining and just make the change",
		"i told you to implement it",
		"actually do it",
		"less talk — just fix it",
		"just do the refactor",
	}
	for _, s := range fires {
		if cause, cue, ok := TagAgentCauseContext(s, proseOnly); !ok || cause != CauseUnderInitiative {
			t.Errorf("%q (prose-only) → (%q,%q,%v), want under-initiative", s, cause, cue, ok)
		}
	}

	// Same reactions, but the agent already acted: no under-action → no cause.
	acted := AgentContext{AgentActed: true}
	for _, s := range []string{"just do it", "actually do it", "just do the refactor"} {
		if cause, _, ok := TagAgentCauseContext(s, acted); ok && cause == CauseUnderInitiative {
			t.Errorf("%q (agent acted) → %q, want no under-initiative", s, cause)
		}
	}

	// Benign task turns carry no push-to-execute cue → no cause even prose-only.
	for _, s := range []string{"add a test for the edge case", "what do you think?", "looks good, ship it"} {
		if cause, _, ok := TagAgentCauseContext(s, proseOnly); ok {
			t.Errorf("%q → %q, want no cause (false-fire)", s, cause)
		}
	}
}

// TestTagAgentCauseMiscalibratedInterruption: after the agent asked PERMISSION on
// already-clear intent, a push-to-execute reaction tags miscalibrated-interruption —
// the permission ask routes it here rather than to under-initiative.
func TestTagAgentCauseMiscalibratedInterruption(t *testing.T) {
	asked := AgentContext{AgentAskedPermission: true}
	fires := []string{
		"just do it",
		"stop asking and do it",
		"quit asking — get it done",
		"yes, do it",
		"go ahead and make it",
	}
	for _, s := range fires {
		if cause, cue, ok := TagAgentCauseContext(s, asked); !ok || cause != CauseMiscalibratedInterruption {
			t.Errorf("%q (agent asked permission) → (%q,%q,%v), want miscalibrated-interruption", s, cause, cue, ok)
		}
	}

	// The permission ask wins even if the agent also acted (it asked when it shouldn't
	// have): still miscalibrated-interruption, not under-initiative.
	if cause, _, _ := TagAgentCauseContext("just do it", AgentContext{AgentActed: true, AgentAskedPermission: true}); cause != CauseMiscalibratedInterruption {
		t.Errorf("push after permission-ask → %q, want miscalibrated-interruption", cause)
	}

	// A context-free cause still wins over the agent-context pass.
	if cause, _, _ := TagAgentCauseContext("undo that, I didn't ask you to", asked); cause != CauseOverInitiative {
		t.Errorf("reversal → %q, want over-initiative (context-free wins)", cause)
	}
}

// TestAssistantAskedPermission: a trailing permission question is a permission ask; a
// clarifying question or a declarative sentence is not.
func TestAssistantAskedPermission(t *testing.T) {
	yes := []string{
		"I can refactor the parser. Should I go ahead?",
		"Want me to delete the shim?",
		"That's a big change — do you want me to proceed?",
	}
	for _, s := range yes {
		if !AssistantAskedPermission(s) {
			t.Errorf("%q → false, want permission ask", s)
		}
	}
	no := []string{
		"Which file did you mean?",         // clarify, not permission
		"I refactored the parser.",         // declarative, no question
		"Should I use tabs? Anyway, done.", // permission phrase but not the trailing sentence
	}
	for _, s := range no {
		if AssistantAskedPermission(s) {
			t.Errorf("%q → true, want not a permission ask", s)
		}
	}
}
