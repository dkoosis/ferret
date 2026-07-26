package feedback

import (
	"testing"

	"github.com/dkoosis/ferret/internal/label"
)

// TestRecognizeAnswer is the explicit-token corpus test (ferret-j33 test plan
// §1): every accepted short/long token form, the AC's own prefix-strip
// example, the benign-reassurance "no" guard, and the near-miss words that
// must NOT match despite sharing a leading letter with a real token.
func TestRecognizeAnswer(t *testing.T) {
	tests := []struct {
		name          string
		prompt        string
		wantValence   string
		wantRemainder string
		wantOK        bool
	}{
		{"bare y", "y", label.ValenceYes, "", true},
		{"bare Y uppercase", "Y", label.ValenceYes, "", true},
		{"bare yes", "yes", label.ValenceYes, "", true},
		{"bare YES uppercase", "YES", label.ValenceYes, "", true},
		{"bare n", "n", label.ValenceNo, "", true},
		{"bare no", "no", label.ValenceNo, "", true},
		{"bare s", "s", label.ValenceSkip, "", true},
		{"bare skip", "skip", label.ValenceSkip, "", true},
		// AC's own worked example: prefix-strip.
		{"y dash remainder", "y - now fix the tests", label.ValenceYes, "now fix the tests", true},
		{"y colon remainder", "y: now fix the tests", label.ValenceYes, "now fix the tests", true},
		{"no emdash remainder", "no — it clearly wasn't relevant", label.ValenceNo, "it clearly wasn't relevant", true},
		// benign-reassurance guard: must NOT match as a "no" vote.
		{"no worries", "no worries, that's fine", "", "", false},
		{"no problem", "no problem, all good", "", "", false},
		// near-misses sharing a leading letter/word but not the grammar.
		{"yesterday", "yesterday i fixed this", "", "", false},
		{"nope", "nope, that's not it", "", "", false},
		{"sure", "sure, go ahead", "", "", false},
		{"unrelated work turn", "please also check the tests", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValence, gotRemainder, gotOK := RecognizeAnswer(tt.prompt)
			if gotOK != tt.wantOK {
				t.Fatalf("RecognizeAnswer(%q) ok = %v, want %v", tt.prompt, gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotValence != tt.wantValence {
				t.Errorf("RecognizeAnswer(%q) valence = %q, want %q", tt.prompt, gotValence, tt.wantValence)
			}
			if gotRemainder != tt.wantRemainder {
				t.Errorf("RecognizeAnswer(%q) remainder = %q, want %q", tt.prompt, gotRemainder, tt.wantRemainder)
			}
		})
	}
}
