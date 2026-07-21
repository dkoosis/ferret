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
