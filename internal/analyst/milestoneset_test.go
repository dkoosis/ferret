package analyst

import (
	"math"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/score"
)

// corpusFromVocab builds a *mine.Corpus from string streams, interning tokens
// into the Vocab — the minimal shape score.WeighByCorpus reads (Streams + Vocab).
// Mirrors the helper in internal/score/landmark_test.go so this package can drive
// WeighByCorpus without a real events artifact.
func corpusFromVocab(streams [][]string) *mine.Corpus {
	c := &mine.Corpus{}
	intern := map[string]uint32{}
	for i, s := range streams {
		var toks []mine.Tok
		for _, t := range s {
			tid, ok := intern[t]
			if !ok {
				tid = uint32(len(c.Vocab))
				intern[t] = tid
				c.Vocab = append(c.Vocab, t)
			}
			toks = append(toks, mine.Tok{ID: tid, Seq: i})
		}
		c.Streams = append(c.Streams, toks)
	}
	return c
}

// TestMilestoneSetsNonEmpty: every catalog entry names ≥1 milestone, each with an
// ID and ≥1 Tool — a milestone set with no satisfiable milestone scores 0 for
// everything and is dead weight in the library.
func TestMilestoneSetsNonEmpty(t *testing.T) {
	sets := MilestoneSets()
	if len(sets) == 0 {
		t.Fatal("MilestoneSets() is empty; the library must ship canned goal patterns")
	}
	for _, s := range sets {
		if s.ID == "" {
			t.Errorf("milestone set has empty ID")
		}
		if len(s.Milestones) == 0 {
			t.Errorf("milestone set %q has no milestones", s.ID)
		}
		for _, m := range s.Milestones {
			if m.ID == "" {
				t.Errorf("set %q: milestone with empty ID", s.ID)
			}
			if len(m.Tools) == 0 {
				t.Errorf("set %q: milestone %q has no tools (can never hit)", s.ID, m.ID)
			}
		}
	}
}

// TestMatchGoal: a stated goal maps to the milestone set whose cues it matches.
func TestMatchGoal(t *testing.T) {
	tests := []struct {
		name   string
		goal   string
		wantID string
		found  bool
	}{
		{"ship feature", "Ship the landmark scoring feature", "ship-feature", true},
		{"implement+commit", "implement the parser and commit it", "ship-feature", true},
		{"fix bug", "fix the panic in the segmenter", "fix-bug", true},
		{"investigate", "investigate why the corpus build is slow", "investigate", true},
		{"explore phrasing", "explore the analyst package and explain it", "investigate", true},
		{"refactor", "refactor the scoring engine into smaller files", "refactor", true},
		{"no match", "order a pizza for the team", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, ok := MatchGoal(tt.goal)
			if ok != tt.found {
				t.Fatalf("MatchGoal(%q) found=%v, want %v", tt.goal, ok, tt.found)
			}
			if ok && set.ID != tt.wantID {
				t.Errorf("MatchGoal(%q) = %q, want %q", tt.goal, set.ID, tt.wantID)
			}
		})
	}
}

// TestMatchGoalIsDeterministic: the same goal always maps to the same set (cue
// scan is order-stable, longest/most-specific cue wins).
func TestMatchGoalIsDeterministic(t *testing.T) {
	const goal = "fix the bug and ship the feature"
	first, ok := MatchGoal(goal)
	if !ok {
		t.Fatal("expected a match for a multi-cue goal")
	}
	for i := range 8 {
		got, ok := MatchGoal(goal)
		if !ok || got.ID != first.ID {
			t.Fatalf("MatchGoal not deterministic: run %d = %q (ok=%v), want %q", i, got.ID, ok, first.ID)
		}
	}
}

// TestMilestonesForGoal: maps a goal to its []score.Milestone with engine-filled
// uniqueness weights — the exact input ferret-afm's --session mode scores against.
func TestMilestonesForGoal(t *testing.T) {
	// A corpus where "sh:git_commit" is rare (one stream) and "Read" is common
	// (every stream) — the commit milestone must weigh more than the read one.
	corpus := corpusFromVocab([][]string{
		{"Read", "Edit", "Read"},
		{"Read", "Read"},
		{"Read", "Edit", "sh:go_test", "sh:git_commit"},
	})

	ms, ok := MilestonesForGoal("ship the feature and commit", corpus)
	if !ok {
		t.Fatal("MilestonesForGoal: expected a ship-feature match")
	}
	if len(ms) == 0 {
		t.Fatal("MilestonesForGoal returned no milestones")
	}

	// Weights must be filled (non-zero) by WeighByCorpus.
	byID := map[string]float64{}
	for _, m := range ms {
		if m.Weight == 0 {
			t.Errorf("milestone %q weight not filled by WeighByCorpus", m.ID)
		}
		byID[m.ID] = m.Weight
	}

	// The commit milestone (rare tool) must outweigh the read milestone (ubiquitous).
	read, hasRead := byID["read"]
	commit, hasCommit := byID["commit"]
	if hasRead && hasCommit && !(commit > read) {
		t.Errorf("rare commit milestone (%.3f) should outweigh ubiquitous read (%.3f)", commit, read)
	}
}

// TestMilestonesForGoalNoMatch: an unrecognized goal yields no milestone set.
func TestMilestonesForGoalNoMatch(t *testing.T) {
	corpus := corpusFromVocab([][]string{{"Read"}})
	ms, ok := MilestonesForGoal("buy groceries", corpus)
	if ok || ms != nil {
		t.Errorf("MilestonesForGoal on an unrecognized goal = (%v, %v), want (nil, false)", ms, ok)
	}
}

// TestMilestonesForGoalNilCorpus: with no corpus, weights fall back to a uniform
// non-zero default so ScoreLandmarks still produces a meaningful ratio (it divides
// by total weight; all-zero would make every score 0).
func TestMilestonesForGoalNilCorpus(t *testing.T) {
	ms, ok := MilestonesForGoal("fix the bug", nil)
	if !ok {
		t.Fatal("expected a fix-bug match")
	}
	for _, m := range ms {
		if m.Weight <= 0 {
			t.Errorf("milestone %q has non-positive weight %v with a nil corpus", m.ID, m.Weight)
		}
	}
	// Sanity: the returned set scores a full-coverage shape at 1.0.
	shape := make([]string, 0, len(ms))
	for _, m := range ms {
		shape = append(shape, m.Tools[0])
	}
	got := score.ScoreLandmarks(ms, shape).Score
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("full-coverage shape scored %v, want 1.0", got)
	}
}

// TestMilestonesForGoalReturnsCopy: two calls for the same goal must not share
// backing milestone slices — WeighByCorpus mutates Weight in place, so a shared
// slice would let one caller's corpus leak into another's weights.
func TestMilestonesForGoalReturnsCopy(t *testing.T) {
	c1 := corpusFromVocab([][]string{{"Read"}, {"Read"}, {"sh:git_commit"}})
	c2 := corpusFromVocab([][]string{{"sh:git_commit"}, {"sh:git_commit"}, {"Read"}})

	a, _ := MilestonesForGoal("ship the feature", c1)
	b, _ := MilestonesForGoal("ship the feature", c2)

	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected non-empty milestone sets")
	}
	a[0].Weight = -999
	if b[0].Weight == -999 {
		t.Error("MilestonesForGoal returns aliased slices; calls share backing storage")
	}
}
