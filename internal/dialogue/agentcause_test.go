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
		"don't modify the schema",
		"leave it alone",
		"leave the config alone",
		"why did you overwrite my changes",
	}
	for _, s := range fires {
		if cause, cue, ok := TagAgentCause(s); !ok || cause != CauseOverInitiative {
			t.Errorf("%q → (%q,%q,%v), want over-initiative", s, cause, cue, ok)
		}
	}

	quiet := []string{
		"undo that",                     // bare reversal — repair, not an over-step claim
		"no, use the other file",        // approach redirect
		"why did you choose sonnet?",    // 'choose' is not a mutating verb
		"don't break the build",         // a constraint, not a stop-mutating order
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
