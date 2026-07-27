package main

import (
	"fmt"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// fixtureScores builds n lo→hi sorted StreamScores with distinct stream keys,
// mimicking ScoreSurprise's output so splitSurprise can be tested without a
// disk corpus.
func fixtureScores(n int) []mine.StreamScore {
	out := make([]mine.StreamScore, n)
	for i := range out {
		out[i] = mine.StreamScore{
			Stream: fmt.Sprintf("sess-%02d", i),
			Toks:   10 + i,
			Bits:   float64(i), // already sorted low→high
		}
	}
	return out
}

// TestSplitSurpriseNoOverlap guards ferret-045: cmdSurprise split the lo→hi
// score stream into "most routine" (front) and "most surprising" (back)
// sections with two independent slices that overlapped on small corpora —
// when len(scores) <= limit the same streams rendered in BOTH sections. The
// partition must keep the sections disjoint at every corpus size, and both the
// text and JSON output paths consume the same split so they stay in parity.
func TestSplitSurpriseNoOverlap(t *testing.T) {
	const limit = 20 // cmdSurprise's default; half = 10
	for _, n := range []int{0, 1, 2, 3, 5, 9, 10, 11, 15, 19, 20, 21, 40} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			scores := fixtureScores(n)
			routine, thrash := splitSurprise(scores, limit)

			// No stream may appear in both sections (the bug).
			seen := map[string]bool{}
			for _, s := range routine {
				seen[s.Stream] = true
			}
			for _, s := range thrash {
				if seen[s.Stream] {
					t.Errorf("n=%d: stream %q listed in both routine and thrash", n, s.Stream)
				}
			}

			// Each section is capped at limit/2.
			if half := limit / 2; len(routine) > half || len(thrash) > half {
				t.Errorf("n=%d: section over cap: routine=%d thrash=%d half=%d",
					n, len(routine), len(thrash), half)
			}

			// routine holds the lowest-bits streams, thrash the highest.
			if len(routine) > 0 && len(thrash) > 0 {
				if routine[0].Bits > thrash[0].Bits {
					t.Errorf("n=%d: routine should hold lower bits than thrash", n)
				}
			}
		})
	}
}
