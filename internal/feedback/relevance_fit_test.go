package feedback

import "testing"

// TestSearchFit covers the returned-nug aggregation: a search served iff its
// best-graded RETURNED nug is materially relevant (>=2); it is a mismatch when
// every returned nug is marginal/irrelevant; and it is unjudgeable (ok=false)
// when no returned nug carries a grade — the caller must not invent a
// disagreement from missing data.
func TestSearchFit(t *testing.T) {
	cases := []struct {
		name     string
		returned []string
		grades   map[string]int
		wantFit  JudgeFit
		wantOK   bool
	}{
		{
			name:     "one exact returned → served",
			returned: []string{"n1", "n2"},
			grades:   map[string]int{"n1": 0, "n2": 3},
			wantFit:  FitServed, wantOK: true,
		},
		{
			name:     "one relevant (grade 2) returned → served",
			returned: []string{"n1"},
			grades:   map[string]int{"n1": 2},
			wantFit:  FitServed, wantOK: true,
		},
		{
			name:     "all returned marginal/irrelevant → mismatch",
			returned: []string{"n1", "n2"},
			grades:   map[string]int{"n1": 1, "n2": 0},
			wantFit:  FitMismatch, wantOK: true,
		},
		{
			name:     "no returned nug graded → unknown",
			returned: []string{"n9"},
			grades:   map[string]int{"n1": 3}, // graded, but not among the returned
			wantFit:  FitUnknown, wantOK: false,
		},
		{
			name:     "empty returned set → unknown",
			returned: nil,
			grades:   map[string]int{"n1": 3},
			wantFit:  FitUnknown, wantOK: false,
		},
		{
			name:     "relevant returned nug credited even when a returned nug is ungraded",
			returned: []string{"n1", "n2"},
			grades:   map[string]int{"n2": 2}, // n1 ungraded, n2 relevant
			wantFit:  FitServed, wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fit, ok := SearchFit(c.returned, c.grades)
			if fit != c.wantFit || ok != c.wantOK {
				t.Errorf("SearchFit(%v, %v) = (%q, %v), want (%q, %v)", c.returned, c.grades, fit, ok, c.wantFit, c.wantOK)
			}
		})
	}
}

// TestSearchFitFeedsDisagree threads the aggregation into the selector end to
// end: a lattice "helped" on a search whose returned nugs are all irrelevant is
// exactly the disagreement the tap should surface.
func TestSearchFitFeedsDisagree(t *testing.T) {
	fit, ok := SearchFit([]string{"n1"}, map[string]int{"n1": 0})
	if !ok || fit != FitMismatch {
		t.Fatalf("setup: SearchFit = (%q,%v), want (mismatch,true)", fit, ok)
	}
	// score.VerdictHelped + FitMismatch must trigger — asserted in select_test's
	// truth table; here we confirm the aggregated fit is the value that feeds it.
	if fit != FitMismatch {
		t.Errorf("aggregated fit = %q, want the mismatch that disagrees with a lattice=helped", fit)
	}
}
