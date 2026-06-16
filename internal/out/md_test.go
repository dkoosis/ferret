package out

import (
	"strings"
	"testing"
)

// TestMarkdownReport is the golden-ish shape check for the human cost report:
// it must lead with 💸 cost leaks, then 🔁 context churn, then ✅ routines;
// friction must be sorted by measured burn (heaviest first); each finding's
// burn renders as "~Nk tokens / ~$X across M sessions; fix: <verb>"; and the
// evidence must be tucked into a collapsible <details> so the scan line stays
// terse.
func TestMarkdownReport(t *testing.T) {
	findings := []MDFinding{
		{Motif: []string{"Edit!", "Read"}, Kind: "friction", Action: "hook", Count: 23, Sessions: 12, FailRate: 0.95, Burn: 253000, Evidence: "a1b2c3d4@45"},
		{Motif: []string{"Bash!", "Bash"}, Kind: "friction", Action: "hook", Count: 8, Sessions: 5, FailRate: 1.0, Burn: 40000, Evidence: "deadbeef@9"},
		{Motif: []string{"Read", "Read"}, Kind: "loop", Action: "trim", Count: 30, Sessions: 9, FailRate: 0, Burn: 90000, Evidence: "cafef00d@3"},
		{Motif: []string{"go_build", "go_test"}, Kind: "routine", Action: "script", Count: 50, Sessions: 30, FailRate: 0, Burn: 12000, Evidence: "feedface@7"},
	}
	var b strings.Builder
	if err := Markdown(&b, "tool", len(findings), findings); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	got := b.String()

	// All three sections present.
	for _, want := range []string{"💸", "🔁", "✅"} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing section marker %q", want)
		}
	}
	// Cost-first order: 💸 leaks → 🔁 churn → ✅ routines.
	iLeak, iChurn, iRoutine := strings.Index(got, "💸"), strings.Index(got, "🔁"), strings.Index(got, "✅")
	if !(iLeak < iChurn && iChurn < iRoutine) {
		t.Errorf("sections out of order: 💸=%d 🔁=%d ✅=%d", iLeak, iChurn, iRoutine)
	}

	// Friction sorted by burn desc: the 253k motif precedes the 40k one.
	if iHeavy, iLight := strings.Index(got, "Edit!"), strings.Index(got, "Bash!"); iHeavy < 0 || iLight < 0 || iHeavy > iLight {
		t.Errorf("cost leaks not sorted by burn desc: Edit!=%d Bash!=%d", iHeavy, iLight)
	}

	// Burn renders as ~Nk tokens with a $ estimate and the fix verb per section.
	for _, want := range []string{"~253k tokens", "$0.76", "fix: hook", "fix: trim", "fix: script", "across 12 sessions"} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q", want)
		}
	}

	// Evidence is collapsible and carries the exemplar.
	if !strings.Contains(got, "<details>") || !strings.Contains(got, "a1b2c3d4@45") {
		t.Errorf("evidence must live in a <details> block")
	}
}

// TestMarkdownEdges covers the small-magnitude and truncation paths: a sub-cent
// burn must read "<$0.01" rather than "$0.00", and a capped finding slice must
// surface the count it dropped instead of silently truncating.
func TestMarkdownEdges(t *testing.T) {
	findings := []MDFinding{
		{Motif: []string{"Read"}, Kind: "routine", Action: "script", Count: 1, Sessions: 1, FailRate: 0, Burn: 500, Evidence: "x@1"},
	}
	var b strings.Builder
	if err := Markdown(&b, "tool", 6, findings); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "<$0.01") {
		t.Errorf("sub-cent burn should read <$0.01, got:\n%s", got)
	}
	if !strings.Contains(got, "+5 more") {
		t.Errorf("expected truncation note for 1-of-6 findings, got:\n%s", got)
	}
	// Empty core sections still render their header (stable, diffable shape).
	if !strings.Contains(got, "💸") || !strings.Contains(got, "🔁") {
		t.Errorf("empty cost-leak / churn sections must still render headers")
	}
}
