package score

import (
	"math"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// corpusFromVocab builds a *mine.Corpus from string streams, interning tokens
// into the Vocab — the minimal shape WeighByCorpus reads (Streams + Vocab).
func corpusFromVocab(streams [][]string) *mine.Corpus {
	c := &mine.Corpus{}
	id := map[string]uint32{}
	for _, s := range streams {
		var toks []mine.Tok
		for i, t := range s {
			tid, ok := id[t]
			if !ok {
				tid = uint32(len(c.Vocab))
				id[t] = tid
				c.Vocab = append(c.Vocab, t)
			}
			toks = append(toks, mine.Tok{ID: tid, Seq: i})
		}
		c.Streams = append(c.Streams, toks)
	}
	return c
}

func TestScoreLandmarks(t *testing.T) {
	tests := []struct {
		name       string
		milestones []Milestone
		shape      []string
		wantScore  float64
		wantHits   int
	}{
		{
			name: "all hit → 1.0",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},
				{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
			},
			shape:     []string{"Read", "Edit", "sh:git_commit"},
			wantScore: 1.0,
			wantHits:  2,
		},
		{
			name: "none hit → 0.0",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},
				{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
			},
			shape:     []string{"Edit", "Write"},
			wantScore: 0.0,
			wantHits:  0,
		},
		{
			name: "weighted partial — uniqueness weight steers the score",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},            // generic, hit
				{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 9}, // rare, missed
			},
			shape:     []string{"Read", "Edit"},
			wantScore: 1.0 / 10.0, // hit-weight 1 over total 10 — the rare miss dominates
			wantHits:  1,
		},
		{
			name: "out-of-order still credits (backtrack-tolerant)",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},
				{ID: "test", Tools: []string{"sh:go_test"}, Weight: 1},
				{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
			},
			// reversed order vs the milestones — set-coverage, no alignment
			shape:     []string{"sh:git_commit", "sh:go_test", "Read"},
			wantScore: 1.0,
			wantHits:  3,
		},
		{
			name: "repeated tokens credit once (no double count)",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},
				{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 1},
			},
			shape:     []string{"Read", "Read", "Read", "Edit"}, // many reads, no commit
			wantScore: 0.5,
			wantHits:  1,
		},
		{
			name: "milestone with multiple tools — any satisfies it",
			milestones: []Milestone{
				{ID: "vcs", Tools: []string{"sh:git_commit", "sh:git_push"}, Weight: 1},
			},
			shape:     []string{"Edit", "sh:git_push"},
			wantScore: 1.0,
			wantHits:  1,
		},
		{
			name:       "empty milestone set → 0.0, no panic",
			milestones: nil,
			shape:      []string{"Read", "Edit"},
			wantScore:  0.0,
			wantHits:   0,
		},
		{
			name: "empty shape → 0.0, all missed",
			milestones: []Milestone{
				{ID: "read", Tools: []string{"Read"}, Weight: 1},
			},
			shape:     nil,
			wantScore: 0.0,
			wantHits:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScoreLandmarks(tt.milestones, tt.shape)
			if math.Abs(got.Score-tt.wantScore) > 1e-9 {
				t.Errorf("Score = %v; want %v", got.Score, tt.wantScore)
			}
			if got.HitCount != tt.wantHits {
				t.Errorf("HitCount = %d; want %d", got.HitCount, tt.wantHits)
			}
			if got.Total != len(tt.milestones) {
				t.Errorf("Total = %d; want %d", got.Total, len(tt.milestones))
			}
			if len(got.Hits) != len(tt.milestones) {
				t.Errorf("len(Hits) = %d; want %d (one row per milestone)", len(got.Hits), len(tt.milestones))
			}
		})
	}
}

// TestScoreLandmarksByteStable: same input, same output across runs (the score
// package acceptance contract — pure function of the inputs).
func TestScoreLandmarksByteStable(t *testing.T) {
	ms := []Milestone{
		{ID: "read", Tools: []string{"Read"}, Weight: 2},
		{ID: "commit", Tools: []string{"sh:git_commit"}, Weight: 5},
	}
	shape := []string{"Read", "sh:git_commit"}
	a := ScoreLandmarks(ms, shape)
	b := ScoreLandmarks(ms, shape)
	if a.Score != b.Score || a.HitWeight != b.HitWeight || a.TotalWeight != b.TotalWeight {
		t.Fatalf("non-deterministic: %+v vs %+v", a, b)
	}
	// Hit rows must be one-per-milestone in milestone order, carrying weight.
	if len(a.Hits) != 2 || a.Hits[0].Milestone != "read" || a.Hits[1].Milestone != "commit" {
		t.Fatalf("hit rows out of order or wrong: %+v", a.Hits)
	}
	if !a.Hits[0].Hit || a.Hits[0].Weight != 2 {
		t.Errorf("hit row 0 = %+v; want hit, weight 2", a.Hits[0])
	}
}

// TestWeighByCorpus: rarer tools (fewer streams touch them) get a higher weight.
func TestWeighByCorpus(t *testing.T) {
	// Three streams: "Read" appears in all three, "sh:git_push" in just one.
	corpus := corpusFromVocab([][]string{
		{"Read", "Edit", "Read"},
		{"Read", "Write"},
		{"Read", "sh:git_push"},
	})
	ms := []Milestone{
		{ID: "read", Tools: []string{"Read"}},
		{ID: "push", Tools: []string{"sh:git_push"}},
	}
	WeighByCorpus(ms, corpus)
	if ms[0].Weight <= 0 || ms[1].Weight <= 0 {
		t.Fatalf("weights not filled: %+v", ms)
	}
	if ms[1].Weight <= ms[0].Weight {
		t.Errorf("rare push (weight %v) should outweigh common read (weight %v)", ms[1].Weight, ms[0].Weight)
	}
}

// TestWeighByCorpusOverride: a caller-set weight is left untouched.
func TestWeighByCorpusOverride(t *testing.T) {
	corpus := corpusFromVocab([][]string{{"Read", "Read"}})
	ms := []Milestone{
		{ID: "read", Tools: []string{"Read"}, Weight: 42},
	}
	WeighByCorpus(ms, corpus)
	if ms[0].Weight != 42 {
		t.Errorf("override weight clobbered: got %v; want 42", ms[0].Weight)
	}
}

// TestWeighByCorpusUnknownTool: a milestone whose tool never appears still gets a
// finite, positive weight (max rarity), never a divide-by-zero or NaN.
func TestWeighByCorpusUnknownTool(t *testing.T) {
	corpus := corpusFromVocab([][]string{{"Read"}, {"Edit"}})
	ms := []Milestone{{ID: "ghost", Tools: []string{"NeverSeen"}}}
	WeighByCorpus(ms, corpus)
	if ms[0].Weight <= 0 || math.IsNaN(ms[0].Weight) || math.IsInf(ms[0].Weight, 0) {
		t.Errorf("unknown-tool weight = %v; want finite positive", ms[0].Weight)
	}
}
