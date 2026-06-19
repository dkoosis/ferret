package score

import (
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
)

// Reference-free, LLM-free per-task quality axes + pass^k consistency (ferret-kuv.5).
//
// TRACE (arXiv:2510.02837) argues for reference-free quality AXES — efficiency,
// hallucination, adaptivity — instead of only task-success. We adopt the two a
// deterministic transcript miner can compute and drop hallucination (it needs
// semantic grounding ferret lacks — analyst territory):
//
//   - Efficiency = friction-free. The THRASH signal: calls that add no new KIND
//     of work. Scored as distinct shape tokens / call count — a tight task touches
//     a fresh tool kind per call (ratio→1); a read⇄search loop hammers the same
//     few kinds over many calls (ratio→0). Bytes are deliberately NOT folded in:
//     InBytes/OutBytes are heavy-tailed (one 2 MB read would crater an otherwise
//     clean task), so they ride a reported cost column, never the [0,1] axis.
//   - Adaptivity = recoverable. A repeated shape token is churn; what separates a
//     healthy loop (Edit→test ×3 → commit) from flailing is RESOLUTION, not the
//     loop — they are identical as loops. So adaptivity scores whether the churn
//     resolved: a task that ended on a non-looping token, or shipped (Outcome), has
//     recovered; one that ran the loop to the segment boundary is stuck. Outcome is
//     a load-bearing confirmer here, not a cosmetic nudge.
//
// Both axes are pure functions of the segmentation — same Result → same axes, the
// byte-stable contract this package guarantees (doc.go). The framing is an
// ADAPTATION of TRACE by analogy; the arithmetic is ours.

const (
	// loopMin is how many times one shape token must recur for the task to read as
	// a loop (churn) at all. Below it, repetition is incidental, not a loop.
	loopMin = 3
	// adaptivity gradations: no churn is maximally adaptive; recovered churn paid a
	// recovery cost; a loop running to the boundary unresolved is stuck.
	adaptClean     = 1.0
	adaptRecovered = 0.7
	adaptStuck     = 0.2
	// clusterNoSignal flags a k==1 consistency cluster: one attempt is no evidence
	// of (in)consistency, so it is a sentinel, NOT a spurious perfect 1.0.
	clusterNoSignal = -1.0
)

// Axes is the per-task reference-free quality block. Both ∈ [0,1] (Adaptivity also
// takes the clusterNoSignal-free fixed gradations above); higher is better.
type Axes struct {
	Efficiency float64 `json:"efficiency"`
	Adaptivity float64 `json:"adaptivity"`
}

// ScoreAxes annotates every scored segment in res with its quality axes, in place
// — the LabelOutcomes shape (kuv.8), so the axes ride the same per-task scaffold.
// The synthetic preamble (Index==0) and segments that own no calls (FirstCall<0)
// are skipped (nil Axes = not scored, never a zero score).
func ScoreAxes(res *Result) {
	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Index == 0 || seg.FirstCall < 0 {
			continue
		}
		a := axesFor(*seg)
		seg.Axes = &a
	}
}

// axesFor is the single pure scorer for one task's axes, so the efficiency and
// adaptivity formulas tune in one place.
func axesFor(seg Segment) Axes {
	return Axes{Efficiency: efficiency(seg), Adaptivity: adaptivity(seg)}
}

// efficiency = distinct shape tokens / owned call count, clamped to [0,1]. Lower
// ratio (many calls, few distinct kinds) = thrash = low efficiency; one fresh kind
// per call = 1.0. Compound Bash calls can push distinct above call count (one call,
// two verbs) — the clamp keeps that an efficient 1.0, not a >1 artifact.
func efficiency(seg Segment) float64 {
	calls := seg.LastCall - seg.FirstCall + 1
	distinct := distinctShapeTokens(seg.Shape)
	if seg.FirstCall < 0 || distinct == 0 {
		return 0
	}
	return clampUnit(float64(distinct) / float64(calls))
}

// adaptivity scores recovery from churn. No looping token → no churn → fully
// adaptive. With a loop, the task is stuck only if it neither shipped nor moved
// off the looping token by its last call; otherwise it recovered.
func adaptivity(seg Segment) float64 {
	repeated := repeatedTokens(seg.Shape, loopMin)
	if len(repeated) == 0 {
		return adaptClean
	}
	shipped := seg.Outcome != nil && seg.Outcome.Positive
	last := seg.Shape[len(seg.Shape)-1]
	if !shipped && repeated[last] {
		return adaptStuck
	}
	return adaptRecovered
}

// pass^k consistency per intent-cluster (tau-bench, arXiv:2406.12045).
//
// tau-bench's pass^k is the probability of succeeding on ALL k attempts at a task
// — reliability, not average quality. We adapt it: k tasks sharing an exact ordered
// Shape are k attempts at one recurring shape (the kuv.12 recurrence key, exact
// per Q4), and consistency is how tightly their per-attempt COST clusters.
//
// Why cost, not the efficiency axis: under exact-Shape clustering the axes are
// (almost) shape-determined — same token sequence → same distinct/call ratio and
// same loop/outcome verdict — so their within-cluster spread is ~0 and a
// consistency-over-axes score would be a decorative 1.0. Resource COST genuinely
// varies across attempts at the same shape (one run reads 2 KB, another 2 MB), so
// it is the real reliability signal. Spread is the coefficient of variation (σ/μ),
// scale-free so a heavy-tailed byte magnitude does not masquerade as inconsistency;
// consistency = 1 − CV, clamped. The σ is mine.MeanStdDev (Q3: reuse, don't
// reinvent the variance). NOTE: this is a deliberate deviation from the plan's
// "consistency over efficiency" — surfaced for dk; see the bead.

// Cluster is one recurring-shape group and its cross-attempt cost consistency.
// Consistency is clusterNoSignal when K==1 (no reliability evidence from one try).
type Cluster struct {
	Key         string   `json:"key"`   // the exact ordered-Shape join — the recurrence key
	Shape       []string `json:"shape"` // the shared ordered shape tokens
	K           int      `json:"k"`     // attempts at this shape across the corpus
	Consistency float64  `json:"consistency"`
	Spread      float64  `json:"spread"` // coefficient of variation of per-attempt cost
}

// clusterAgg accumulates one shape's per-attempt costs across the corpus walk.
type clusterAgg struct {
	shape []string
	costs []float64
}

// ClusterByShape groups scored segments by their exact ordered Shape across all
// supplied segments (pass the whole corpus's segments for the corpus-wide pass^k),
// and scores each group's cross-attempt cost consistency. The synthetic preamble
// and shapeless tasks are excluded. Output is sorted by Key so repeated runs are
// byte-stable regardless of input order.
func ClusterByShape(segs []Segment) []Cluster {
	groups := map[string]*clusterAgg{}
	for i := range segs {
		seg := &segs[i]
		if seg.Index == 0 || len(seg.Shape) == 0 {
			continue
		}
		key := strings.Join(seg.Shape, " ")
		g := groups[key]
		if g == nil {
			g = &clusterAgg{shape: seg.Shape}
			groups[key] = g
		}
		g.costs = append(g.costs, float64(seg.InBytes+seg.OutBytes))
	}
	out := make([]Cluster, 0, len(groups))
	for key, g := range groups {
		cons, spread := consistency(g.costs)
		out = append(out, Cluster{
			Key:         key,
			Shape:       g.shape,
			K:           len(g.costs),
			Consistency: cons,
			Spread:      spread,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// consistency scores how tightly k per-attempt costs cluster: spread is the
// coefficient of variation (σ/μ), consistency is 1−CV clamped to [0,1]. k<2 has no
// reliability signal (sentinel); an all-zero-cost cluster is trivially consistent.
func consistency(costs []float64) (score, spread float64) {
	if len(costs) < 2 {
		return clusterNoSignal, 0
	}
	mean, std := mine.MeanStdDev(costs)
	if mean == 0 {
		return 1, 0
	}
	cv := std / mean
	return clampUnit(1 - cv), cv
}

// distinctShapeTokens counts the distinct non-empty tokens in a shape.
func distinctShapeTokens(shape []string) int {
	seen := make(map[string]struct{}, len(shape))
	for _, t := range shape {
		if t == "" {
			continue
		}
		seen[t] = struct{}{}
	}
	return len(seen)
}

// repeatedTokens returns the set of shape tokens occurring at least minCount times
// — the tokens whose recurrence reads as a loop.
func repeatedTokens(shape []string, minCount int) map[string]bool {
	counts := make(map[string]int, len(shape))
	for _, t := range shape {
		if t == "" {
			continue
		}
		counts[t]++
	}
	out := map[string]bool{}
	for t, c := range counts {
		if c >= minCount {
			out[t] = true
		}
	}
	return out
}

// clampUnit clamps x to [0,1].
func clampUnit(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}
