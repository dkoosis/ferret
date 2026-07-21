package analyst

import (
	"strings"
	"testing"
)

// TestBuildOverInitiativePrompt_ShapeAndGrounding pins the no-pushback over-
// initiative prompt: it must carry the opening prompt, every mutating action with
// its detail, and the verdict schema — and it must frame a SCOPE-vs-ACTION,
// results-blind judgment, not the tool-for-intent judgment analyst.go emits.
func TestBuildOverInitiativePrompt_ShapeAndGrounding(t *testing.T) {
	actions := []AgentAction{
		{Tool: "Edit", Detail: "internal/score/reach.go"},
		{Tool: "Write", Detail: "internal/score/newfile.go"},
	}
	system, user := BuildOverInitiativePrompt("What are my options for decoupling reach from score?", actions)

	for _, want := range []string{
		"OPENING PROMPT:",
		"What are my options for decoupling reach from score?",
		"AGENT ACTIONS",
		"Edit: internal/score/reach.go",
		"Write: internal/score/newfile.go",
		`"overInitiative"`, // schema instruction present
		`"scope":"advice|execution|ambiguous"`,
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q\n---\n%s", want, user)
		}
	}

	// The judge is about scope-vs-action, not tool-for-intent — the canonical
	// adjudicate signal (snipe-vs-rg) must be absent so the two prompts can't be
	// confused for one another.
	if strings.Contains(system, "snipe") || strings.Contains(system, "tool-for-intent") {
		t.Errorf("over-initiative system prompt leaked tool-for-intent framing:\n%s", system)
	}
	// The stance must be results-blind (silence is not consent) and precision-first
	// (unsure → do not flag), or the score libels correctly-scoped actions.
	if !strings.Contains(system, "silence") {
		t.Errorf("over-initiative prompt missing the results-blind (silence-is-not-consent) stance:\n%s", system)
	}
	if !strings.Contains(system, "conservative") && !strings.Contains(system, "Conservative") {
		t.Errorf("over-initiative prompt missing the precision-first stance:\n%s", system)
	}
}

// TestBuildOverInitiativePrompt_ActionWithoutDetail renders a bare tool name (no
// detail) without a dangling separator — a mutating call whose input we couldn't
// summarize still lists cleanly.
func TestBuildOverInitiativePrompt_ActionWithoutDetail(t *testing.T) {
	_, user := BuildOverInitiativePrompt("Should we refactor?", []AgentAction{{Tool: "Write"}})
	if !strings.Contains(user, "- Write\n") {
		t.Errorf("bare action should render as %q, got:\n%s", "- Write", user)
	}
	if strings.Contains(user, "Write: \n") {
		t.Errorf("bare action left a dangling %q separator:\n%s", ": ", user)
	}
}

// TestParseOverInitiative_Contract locks the verdict shape the caller aggregates
// on: overInitiative gates whether the episode is flagged, scope + why + confidence
// carry the read a human validator checks. A drift in these keys silently drops
// flagged episodes, so pin them at the producing repo.
func TestParseOverInitiative_Contract(t *testing.T) {
	resp := "```json\n" + `{"overInitiative":true,"scope":"advice","why":"the prompt asked for options; the agent edited two files","confidence":"high"}` + "\n```"
	v, err := ParseOverInitiative(resp)
	if err != nil {
		t.Fatalf("ParseOverInitiative: %v", err)
	}
	if !v.OverInitiative {
		t.Errorf("overInitiative = false, want true")
	}
	if v.Scope != "advice" {
		t.Errorf("scope = %q, want advice", v.Scope)
	}
	if v.Confidence != "high" {
		t.Errorf("confidence = %q, want high", v.Confidence)
	}
}

// TestParseOverInitiative_NotFlagged confirms a false verdict parses as not-flagged
// (the execution-scope case the caller drops).
func TestParseOverInitiative_NotFlagged(t *testing.T) {
	resp := `{"overInitiative":false,"scope":"execution","why":"the prompt said fix it — execution was authorized","confidence":"medium"}`
	v, err := ParseOverInitiative(resp)
	if err != nil {
		t.Fatalf("ParseOverInitiative: %v", err)
	}
	if v.OverInitiative {
		t.Errorf("overInitiative = true, want false for an execution-scoped prompt")
	}
}
