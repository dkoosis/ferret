package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Candidates is Phase 1 of the metrics-engine→analyst loop (ferret-567 step 4).
//
// ferret is NOT the scorer/end-product — it is a metrics engine feeding a Claude
// analyst. This command takes ONE session's deterministic tasks (the segmenter's
// output) and ranks them as cost-leak candidates, compact enough that the analyst
// reads the bundle and returns concrete proposals: automate the recurring routine,
// or de-context the output-heavy IO. The ranking is deterministic and reference-
// free; the analysis is the product.
//
// Per-task leak score (the bead's cost × out-weight × thrash × conformance):
//   - cost      = inBytes + outBytes the task spent (the leak magnitude).
//   - out-weight = outBytes / cost — the share of the cost that is tool OUTPUT
//     pulled into context. A high out-weight task is the de-context payoff: its
//     spend is dominated by results that could be summarized/filtered before they
//     enter the model. Folded in as a factor (1 + out-weight) ∈ [1, 2].
//   - thrash    = within-task goal-shift pivots (the segmenter's pivot hints). A
//     task that pivots mid-stream churned; folded in as (1 + thrashWeight × pivots).
//   - conformance = how well the task's calls matched a reference plan. This needs
//     a reference plan, which is analyst-side (kuv.4) and not available from a raw
//     session — so it is a DEFERRED multiplicand, held at 1 until joined. Phase 1
//     ranks on the three reference-free factors and says so.
//
// Cross-session recurrence ("I do that all the time") is the OTHER half — corpus-
// keyed, needs task-similarity clustering — and ships separately as kuv.12.

// candIntentCap bounds the rendered intent stub per candidate. The analyst
// rewrites it into a stated-intent sentence anyway; this is just enough to
// recognize which task a row is.
const candIntentCap = 100

// thrashWeight is how much each within-task pivot bumps the leak score: one pivot
// → 1.5×. Pivots are a coarse thrash proxy (the deterministic signal the segmenter
// already carries), not a model-scored predictability — kept deliberately gentle.
const thrashWeight = 0.5

// candidate is one ranked cost-leak task in a session.
type candidate struct {
	Task      int     `json:"task"`     // segment Index (>0; the synthetic preamble is excluded)
	Intent    string  `json:"intent"`   // truncated opening prompt — the analyst rewrites it
	Calls     int     `json:"calls"`    // tool calls the task owns
	InBytes   int     `json:"inBytes"`  // tool_use input bytes
	OutBytes  int     `json:"outBytes"` // tool_result output bytes — the de-context payoff
	Cost      int     `json:"cost"`     // inBytes + outBytes
	OutWeight float64 `json:"outWeight"`
	Pivots    int     `json:"pivots"` // within-task goal shifts (thrash proxy)
	Score     float64 `json:"score"`  // composite leak rank (see file header)
}

// candResult is the whole ranked bundle for one session.
type candResult struct {
	Session    string      `json:"session"`
	Project    string      `json:"project"`
	Agent      string      `json:"agent,omitempty"`
	Candidates []candidate `json:"candidates"`
	Tasks      int         `json:"tasks"`     // ranked tasks before --top capping
	TotalCost  int         `json:"totalCost"` // sum of per-task cost across ranked tasks
	TotalOut   int         `json:"totalOut"`
	OutOrphan  int         `json:"outOrphan,omitempty"`
	Top        int         `json:"top"`
	Truncated  bool        `json:"truncated"`
}

// scoreCandidate computes the composite leak score and the out-weight ratio for
// one task. Pure (no IO) so the factor arithmetic is unit-testable. A zero-cost
// task scores 0 — it spent no tokens, so it is no leak.
func scoreCandidate(inBytes, outBytes, pivots int) (score, outWeight float64) {
	cost := inBytes + outBytes
	if cost == 0 {
		return 0, 0
	}
	outWeight = float64(outBytes) / float64(cost)
	outWeightFactor := 1 + outWeight
	thrashFactor := 1 + thrashWeight*float64(pivots)
	// conformanceFactor is held at 1: a reference plan to score against is analyst-
	// side (kuv.4), not derivable from a raw session.
	return float64(cost) * outWeightFactor * thrashFactor, outWeight
}

// rankCandidates projects a segResult into ranked cost-leak candidates. Tasks with
// zero cost (a conversational turn that issued no tool calls) are NOT candidates —
// they leak nothing to automate or de-context — and the synthetic preamble
// (Index 0) is excluded. Sorted by score descending, with deterministic
// tie-breaks (cost desc, then task index asc) so repeated runs are byte-stable.
func rankCandidates(res segResult) candResult {
	out := candResult{
		Session:   res.Session,
		Project:   res.Project,
		Agent:     res.Agent,
		OutOrphan: res.OutOrphan,
	}
	for _, seg := range res.Segments {
		if seg.Index == 0 {
			continue // preamble
		}
		cost := seg.InBytes + seg.OutBytes
		if cost == 0 {
			continue
		}
		score, ow := scoreCandidate(seg.InBytes, seg.OutBytes, len(seg.Pivots))
		out.Candidates = append(out.Candidates, candidate{
			Task:      seg.Index,
			Intent:    truncateRunes(seg.Prompt, candIntentCap),
			Calls:     segCallCount(seg),
			InBytes:   seg.InBytes,
			OutBytes:  seg.OutBytes,
			Cost:      cost,
			OutWeight: ow,
			Pivots:    len(seg.Pivots),
			Score:     score,
		})
		out.TotalCost += cost
		out.TotalOut += seg.OutBytes
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		return a.Task < b.Task
	})
	out.Tasks = len(out.Candidates)
	return out
}

// segCallCount returns the number of tool calls a segment owns (0 when it owns
// none — FirstCall is -1).
func segCallCount(seg segment) int {
	if seg.FirstCall == -1 {
		return 0
	}
	return seg.LastCall - seg.FirstCall + 1
}

// cmdCandidates wires the kong CLI flags to candidates(), resolving the transcript
// root the same way spine/segments do.
func cmdCandidates() error {
	cmd := &CLI.Candidates
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	root := cmd.Root
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	return candidates(os.Stdout, root, cmd.Session, cmd.Format, cmd.Top)
}

// candidates segments one session and emits its ranked cost-leak bundle to w.
func candidates(w io.Writer, root, session, format string, top int) error {
	res, err := segmentSession(root, session)
	if err != nil {
		return err
	}
	ranked := rankCandidates(res)
	ranked.Top = top
	if top > 0 && len(ranked.Candidates) > top {
		ranked.Candidates = ranked.Candidates[:top]
		ranked.Truncated = true
	}
	if format == fmtJSON {
		return writeCandidatesJSON(w, ranked)
	}
	return writeCandidatesText(w, ranked)
}

// writeCandidatesJSON emits the bundle as indented JSON — the analyst feed.
func writeCandidatesJSON(w io.Writer, res candResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// writeCandidatesText emits the human/analyst-readable ranking.
func writeCandidatesText(w io.Writer, res candResult) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "candidates session=%s project=%s", res.Session, res.Project)
	if res.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", res.Agent)
	}
	fmt.Fprintln(bw)
	fmt.Fprintln(bw,
		"≡ per-task cost-leak candidates ranked by cost × out-weight × thrash(pivots). conformance "+
			"(calls vs a reference plan) is an analyst-side multiplicand, not yet joined. de-context the "+
			"high out-weight rows (ow→1 = mostly tool output); automate/script the recurring low-thrash ones.")

	for _, c := range res.Candidates {
		fmt.Fprintf(bw, "[task %d] score=%s cost=%s out=%s ow=%.2f pivots=%d calls=%d",
			c.Task, humanBytes(int(c.Score)), humanBytes(c.Cost), humanBytes(c.OutBytes),
			c.OutWeight, c.Pivots, c.Calls)
		if c.Intent != "" {
			fmt.Fprintf(bw, "  intent: %s", c.Intent)
		}
		fmt.Fprintln(bw)
	}

	fmt.Fprintf(bw, "--- tasks=%d shown=%d cost=%s out=%s",
		res.Tasks, len(res.Candidates), humanBytes(res.TotalCost), humanBytes(res.TotalOut))
	if res.OutOrphan > 0 {
		fmt.Fprintf(bw, " out-orphan=%s", humanBytes(res.OutOrphan))
	}
	if res.Truncated {
		fmt.Fprintf(bw, " (top %d of %d)", res.Top, res.Tasks)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}
