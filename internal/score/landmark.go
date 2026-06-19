package score

import (
	"math"
	"slices"

	"github.com/dkoosis/ferret/internal/mine"
)

// Landmark-based goal-PROGRESS scoring (ferret-kuv.10).
//
// Algorithm — landmark-based goal/plan recognition: Pereira, Oren & Meneguzzi,
// "Landmark-based approaches for goal recognition as planning", Artificial
// Intelligence vol. 279 (AIJ 2020). A landmark is a fact that MUST hold on any
// path to the goal; counting achieved landmarks — rarer ones weighted more —
// ranks progress cheaply, with no full plans and no enumerated candidate-goal
// set. We adapt "fact landmark" → "milestone the call sequence must touch" and
// "uniqueness" → an inverse-frequency weight rewarding milestones few goals share.
//
// This is the PROGRESS axis: it complements conformance ("deviation", kuv.4) and
// the TRACE quality axes (kuv.5). Unlike conformance there is NO ordered reference
// plan and NO alignment cost — a milestone is satisfied if the trace touches it
// ANYWHERE, in any order, any number of times. That backtrack tolerance is the
// point: the progress measure PDDL/plan-recognition needs without a DP alignment,
// a move-cost model, or a candidate-goal enumeration.
//
// Split (mirrors conform's analyst/engine seam): the analyst supplies a milestone
// set's SEMANTICS — its ID and the Shape tokens (Tools) that satisfy it; the
// engine supplies the uniqueness WEIGHT, computed from corpus frequency
// (WeighByCorpus), not asserted. ScoreLandmarks itself is a pure, corpus-free
// function of (milestones, shape) — same input, same output, byte-stable, like
// outcome.go's terminalAction scan.

// Milestone is one necessary goal landmark. The analyst supplies ID + Tools (the
// Shape tokens that satisfy it, e.g. ["Read"], ["sh:git_commit"],
// ["mcp:trixi.get_nug"] — any one of them counts as a hit). Weight is the
// uniqueness weight: engine-filled from inverse corpus frequency by WeighByCorpus
// (omitempty so an analyst spec needn't carry it), unless the caller sets a
// non-zero value, which is honored as an override.
type Milestone struct {
	ID     string   `json:"id"`
	Tools  []string `json:"tools"`
	Weight float64  `json:"weight,omitempty"`
}

// Hit is the per-milestone result: whether the trace touched it and the weight
// that hit/miss carried into the rollup.
type Hit struct {
	Milestone string  `json:"milestone"`
	Hit       bool    `json:"hit"`
	Weight    float64 `json:"weight"`
}

// Progress is the weighted goal-progress verdict for one task: one Hit row per
// milestone (in milestone order), the weighted Score in [0,1], and the count /
// weight rollups a renderer reports.
type Progress struct {
	Hits        []Hit   `json:"hits"`
	Score       float64 `json:"score"`
	HitCount    int     `json:"hitCount"`
	Total       int     `json:"total"`
	HitWeight   float64 `json:"hitWeight"`
	TotalWeight float64 `json:"totalWeight"`
}

// ScoreLandmarks scores goal progress: how much of the milestone set's weight the
// observed shape tokens cover. One pass per milestone, order-free, repeat-tolerant
// (the backtrack tolerance) — set-coverage, not alignment. Score = Σ(hit weights)
// / Σ(all weights) ∈ [0,1]; 0 when there are no milestones or no weight. Pure: no
// I/O, no corpus, no time — repeated runs are byte-stable (the score contract).
func ScoreLandmarks(milestones []Milestone, shape []string) Progress {
	hits := make([]Hit, 0, len(milestones))
	var hitWeight, totalWeight float64
	var hitCount int
	for _, m := range milestones {
		hit := milestoneHit(m, shape)
		hits = append(hits, Hit{Milestone: m.ID, Hit: hit, Weight: m.Weight})
		totalWeight += m.Weight
		if hit {
			hitCount++
			hitWeight += m.Weight
		}
	}
	var score float64
	if totalWeight > 0 {
		score = hitWeight / totalWeight
	}
	return Progress{
		Hits:        hits,
		Score:       score,
		HitCount:    hitCount,
		Total:       len(milestones),
		HitWeight:   hitWeight,
		TotalWeight: totalWeight,
	}
}

// milestoneHit reports whether the observed shape touches the milestone — true if
// ANY of its Tools appears ANYWHERE in shape, order-free and repeat-tolerant. One
// function so the match policy (binary hit; partial credit deferred — Q3) tunes in
// one place, mirroring outcome.go's terminalAction loop.
func milestoneHit(m Milestone, shape []string) bool {
	for _, want := range m.Tools {
		if slices.Contains(shape, want) {
			return true
		}
	}
	return false
}

// WeighByCorpus fills each milestone's Weight from the INVERSE frequency of its
// Tools across the corpus — the faithful form of the landmark "uniqueness" term:
// a milestone whose tools few streams touch weighs more than a generic one. This
// is the only corpus-touching part; isolating it keeps ScoreLandmarks pure and the
// frequency arithmetic in the engine layer (same posture as mine/surprise.go).
//
// Weight uses smoothed inverse document frequency over streams (documents):
// idf = ln((N+1)/(df+1)) + 1, where df is the number of streams that contain ANY
// of the milestone's tools and N is the stream count. The +1 smoothing keeps the
// weight finite and positive even for a tool that never appears (df=0, max rarity)
// and for one in every stream (df=N) — never a divide-by-zero or NaN. A milestone
// is satisfied by ANY of its tools, so its df counts a stream once if that stream
// holds any of them (consistent with milestoneHit's any-of semantics).
//
// A milestone with a caller-set (non-zero) Weight is left untouched (override).
func WeighByCorpus(milestones []Milestone, corpus *mine.Corpus) {
	if corpus == nil {
		return
	}
	n := len(corpus.Streams)
	for i := range milestones {
		if milestones[i].Weight != 0 {
			continue // honor caller override
		}
		df := streamDF(corpus, milestones[i].Tools)
		milestones[i].Weight = math.Log(float64(n+1)/float64(df+1)) + 1
	}
}

// streamDF counts how many streams contain at least one of the given tools — the
// document frequency for an any-of milestone. Tokens are matched by Vocab string,
// so a milestone's Shape tokens line up with the corpus's lens tokens.
func streamDF(corpus *mine.Corpus, tools []string) int {
	want := make(map[uint32]bool, len(tools))
	for _, t := range tools {
		for id, v := range corpus.Vocab {
			if v == t {
				want[uint32(id)] = true
			}
		}
	}
	if len(want) == 0 {
		return 0 // no tool in vocab → unknown, max rarity
	}
	var df int
	for _, stream := range corpus.Streams {
		for _, tok := range stream {
			if want[tok.ID] {
				df++
				break // count this stream once
			}
		}
	}
	return df
}
