package analyst

import (
	"strings"
	"testing"
)

// TestBuildRecallPrompt_ShapeAndGrounding pins the memory-use prompt: it must
// carry the final answer, the run outcome, every fragment's source+text, and the
// findings schema — and it must frame a MEMORY-use judgment, not the tool-for-
// intent judgment analyst.go emits (the two must not bleed).
func TestBuildRecallPrompt_ShapeAndGrounding(t *testing.T) {
	recalled := []RecalledItem{
		{Source: "chat: Planning (02 Jan)", Text: "we chose flat-file memory behind a seam"},
		{Source: "decision: dbd722513b42", Text: "trixi-bot is 5-6 modules + glue"},
	}
	system, user := BuildRecallPrompt("The memory backend is a flat file behind the Provider seam.", "completed", recalled)

	for _, want := range []string{
		"FINAL ANSWER:",
		"The memory backend is a flat file",
		"RUN OUTCOME: completed",
		"RECALLED FRAGMENTS:",
		"chat: Planning (02 Jan)",
		"we chose flat-file memory behind a seam",
		"decision: dbd722513b42",
		`"findings"`, // schema instruction present
		`"fit":"served|mismatch"`,
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q\n---\n%s", want, user)
		}
	}

	// The judge is about memory use, not tool-for-intent — the canonical
	// adjudicate signal (snipe-vs-rg) must be absent so the two prompts can't be
	// confused for one another.
	if strings.Contains(system, "snipe") || strings.Contains(system, "tool-for-intent") {
		t.Errorf("recall system prompt leaked tool-for-intent framing:\n%s", system)
	}
	// The conservatism must lean toward NOT crediting use (the inverse of
	// analyst.go), or the health metric inflates.
	if !strings.Contains(system, "mismatch") || !strings.Contains(system, "MEMORY") {
		t.Errorf("recall system prompt missing memory-use framing:\n%s", system)
	}
}

// TestParseRecall_ServedAndMismatchContract locks the exact fit values the
// trixi-bot mapper (gg-8rq.16.2) projects onto use/miss: "served" → used,
// "mismatch" → miss carrying `better`. A drift in these strings silently breaks
// the downstream projection, so pin them here at the producing repo.
func TestParseRecall_ServedAndMismatchContract(t *testing.T) {
	resp := `{"findings":[
	  {"task":"describe the memory backend","call":"chat: Planning (02 Jan)","toolUsed":"recall","fit":"served","why":"the flat-file choice surfaces verbatim in the answer","confidence":"high"},
	  {"task":"describe the memory backend","call":"decision: dbd722513b42","toolUsed":"recall","fit":"mismatch","better":"the Provider seam contract","why":"module count is off-topic for the backend question","confidence":"medium"}
	]}`
	findings, err := ParseFindings(resp)
	if err != nil {
		t.Fatalf("ParseFindings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(findings))
	}
	if findings[0].Fit != FitServed {
		t.Errorf("finding[0].Fit = %q, want served", findings[0].Fit)
	}
	if findings[1].Fit != FitMismatch || findings[1].Better != "the Provider seam contract" {
		t.Errorf("finding[1] = %+v, want mismatch with better set", findings[1])
	}
}
