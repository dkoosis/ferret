package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/dkoosis/ferret/internal/conform"
	"github.com/dkoosis/ferret/internal/score"
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
//   - conformance = how well the task's calls matched a reference plan (kuv.4
//     fitness × precision). The plan is analyst-side — not derivable from a raw
//     session — so it is an OPTIONAL side-input (--conformance): supplied per task,
//     it folds in as the fourth multiplicand (conformanceFactor); a task with no
//     plan keeps the three reference-free factors. LOW conformance RAISES the leak
//     rank — a task that ran off its plan churned (ferret-567.2).
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
	// Conformance is the joined kuv.4 verdict when a reference plan was supplied for
	// this task (--conformance); nil when none was, so the row ranked reference-free.
	Conformance *candConformance `json:"conformance,omitempty"`
}

// candConformance is the compact conformance read surfaced per candidate: the two
// [0,1] axes conform.Align reports (kuv.4) and the leak factor they folded in.
type candConformance struct {
	Fitness   float64 `json:"fitness"`
	Precision float64 `json:"precision"`
	Factor    float64 `json:"factor"` // the multiplicand folded into Score (∈ [1,2])
}

// candConformSpec is one task's analyst-supplied conformance input, joined to a
// candidate by its Task index: the reference plan (ordered step labels) and the
// observed calls labeled with the step each served. Same shape conform.Align
// consumes — ferret scores it but never derives it (the plan + call labeling is the
// semantic, analyst-side half). Supplied via --conformance; a task with no matching
// spec keeps the reference-free three-factor rank.
type candConformSpec struct {
	Task      int               `json:"task"`
	Reference []string          `json:"reference"`
	Observed  []conform.ObsCall `json:"observed"`
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

// scoreCandidate computes the composite leak score, the out-weight ratio, and the
// conformance factor for one task. Pure (no IO) so the factor arithmetic is unit-
// testable. A zero-cost task scores 0 — it spent no tokens, so it is no leak. conf
// is the joined conformance verdict when a reference plan was supplied for the task,
// else nil (the reference-free fallback → factor 1, so a session with no plan ranks
// exactly as before the kuv.4 join, ferret-567.2).
func scoreCandidate(inBytes, outBytes, pivots int, conf *conform.Result) (score, outWeight, confFactor float64) {
	cost := inBytes + outBytes
	if cost == 0 {
		return 0, 0, 1
	}
	outWeight = float64(outBytes) / float64(cost)
	outWeightFactor := 1 + outWeight
	thrashFactor := 1 + thrashWeight*float64(pivots)
	confFactor = conformanceFactor(conf)
	return float64(cost) * outWeightFactor * thrashFactor * confFactor, outWeight, confFactor
}

// conformanceFactor folds a task's kuv.4 verdict into a leak multiplicand ∈ [1, 2]:
// 1 + (1 − fitness×precision), mirroring out-weight's [1, 2] range. Perfect
// conformance (both axes 1) → 1, no penalty; fully non-conformant (either axis 0) →
// 2. Both axes count (the bead's "fitness/precision"): fitness catches skipped plan
// steps, precision the off-plan calls that dominate a leak, and their product
// penalizes either failing. nil (no reference plan supplied) → 1, the reference-free
// fallback.
func conformanceFactor(conf *conform.Result) float64 {
	if conf == nil {
		return 1
	}
	return 1 + (1 - conf.Fitness*conf.Precision)
}

// rankCandidates projects a segResult into ranked cost-leak candidates. Tasks with
// zero cost (a conversational turn that issued no tool calls) are NOT candidates —
// they leak nothing to automate or de-context — and the synthetic preamble
// (Index 0) is excluded. Sorted by score descending, with deterministic
// tie-breaks (cost desc, then task index asc) so repeated runs are byte-stable.
//
// specs is the optional per-task conformance side-input (--conformance), keyed by
// segment Index: a task with a supplied reference plan folds kuv.4 conformance in as
// the fourth factor; a task with none ranks reference-free. nil/empty = every task
// reference-free (ferret-567.2).
func rankCandidates(res score.Result, specs map[int]candConformSpec) candResult {
	out := candResult{
		Session:   res.Session,
		Project:   res.Project,
		Agent:     res.Agent,
		OutOrphan: res.OutOrphan,
	}
	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Index == 0 {
			continue // preamble
		}
		cost := seg.InBytes + seg.OutBytes
		if cost == 0 {
			continue
		}
		conf := alignSpec(specs, seg.Index)
		score, ow, factor := scoreCandidate(seg.InBytes, seg.OutBytes, len(seg.Pivots), conf)
		out.Candidates = append(out.Candidates, candidate{
			Task:        seg.Index,
			Intent:      truncateRunes(seg.Prompt, candIntentCap),
			Calls:       segCallCount(*seg),
			InBytes:     seg.InBytes,
			OutBytes:    seg.OutBytes,
			Cost:        cost,
			OutWeight:   ow,
			Pivots:      len(seg.Pivots),
			Score:       score,
			Conformance: conformanceOf(conf, factor),
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

var (
	errCandConformRead = errors.New("candidates: cannot read --conformance file")
	errCandConformJSON = errors.New("candidates: --conformance file is not valid JSON (want an array of {task,reference,observed})")
)

// readCandConformSpecs loads the optional per-task conformance side-input: a JSON
// array of {task, reference, observed}, joined to candidates by task index. An empty
// path means no reference plans — every task ranks reference-free — and returns an
// empty map. A later spec wins on a duplicate task index (last-write, deterministic).
func readCandConformSpecs(path string) (map[int]candConformSpec, error) {
	if path == "" {
		return map[int]candConformSpec{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCandConformRead, err)
	}
	var list []candConformSpec
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%w: %w", errCandConformJSON, err)
	}
	specs := make(map[int]candConformSpec, len(list))
	for _, s := range list {
		specs[s.Task] = s
	}
	return specs, nil
}

// alignSpec runs conform.Align for the task at index idx when a spec was supplied
// for it, returning the verdict to fold in; nil when no spec (the reference-free
// path) or the spec carries no reference plan (conform needs ≥1 step — an empty
// reference contributes nothing, so it is skipped rather than scored as total miss).
func alignSpec(specs map[int]candConformSpec, idx int) *conform.Result {
	spec, ok := specs[idx]
	if !ok || len(spec.Reference) == 0 {
		return nil
	}
	r := conform.Align(spec.Reference, spec.Observed)
	return &r
}

// conformanceOf renders the per-candidate conformance read from a joined verdict and
// its folded factor; nil verdict (reference-free task) → nil, so the JSON field is
// omitted.
func conformanceOf(conf *conform.Result, factor float64) *candConformance {
	if conf == nil {
		return nil
	}
	return &candConformance{Fitness: conf.Fitness, Precision: conf.Precision, Factor: factor}
}

// segCallCount returns the number of tool calls a segment owns (0 when it owns
// none — FirstCall is -1).
func segCallCount(seg score.Segment) int {
	if seg.FirstCall == -1 {
		return 0
	}
	return seg.LastCall - seg.FirstCall + 1
}

// cmdCandidates wires the kong CLI flags to candidates(), resolving the transcript
// root the same way spine/segments do.
func cmdCandidates() error {
	cmd := &CLI.Candidates
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	// No --session → corpus-recurrence mode (Phase 2, kuv.12): rank task-shapes
	// that recur across many sessions. --session → Phase 1 per-session ranking.
	if cmd.Session == "" {
		return corpusCandidates(os.Stdout, root, cmd.Format, cmd.Top, cmd.MinSessions)
	}
	return candidates(os.Stdout, root, cmd.Session, cmd.Format, cmd.Top, cmd.Conformance)
}

// candidates segments one session and emits its ranked cost-leak bundle to w. When
// conformancePath is set it loads the per-task reference plans and folds kuv.4
// conformance into the leak score for the tasks it covers (ferret-567.2); empty =
// the reference-free three-factor ranking.
func candidates(w io.Writer, root, session, format string, top int, conformancePath string) error {
	res, err := segmentSession(root, session)
	if err != nil {
		return err
	}
	specs, err := readCandConformSpecs(conformancePath)
	if err != nil {
		return err
	}
	ranked := rankCandidates(res, specs)
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
		"≡ per-task cost-leak candidates ranked by cost × out-weight × thrash(pivots) × conformance. "+
			"conformance (calls vs a supplied reference plan, --conformance) folds in as a 4th factor when "+
			"present (conf=fit/prec→factor); tasks without a plan rank on the three reference-free factors. "+
			"de-context the high out-weight rows (ow→1 = mostly tool output); automate/script the recurring "+
			"low-thrash ones.")

	for _, c := range res.Candidates {
		fmt.Fprintf(bw, "[task %d] score=%s cost=%s out=%s ow=%.2f pivots=%d calls=%d",
			c.Task, humanBytes(int(c.Score)), humanBytes(c.Cost), humanBytes(c.OutBytes),
			c.OutWeight, c.Pivots, c.Calls)
		if c.Conformance != nil {
			fmt.Fprintf(bw, " conf=%.2f/%.2f→%.2f",
				c.Conformance.Fitness, c.Conformance.Precision, c.Conformance.Factor)
		}
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
	// AXI #9 (contextual disclosure): chain the natural next stage — adjudicate
	// the ranked bundle into proposals. Data first, hint last (AXI #8); omitted
	// on an empty ranking (nothing to adjudicate). Text mode only.
	if len(res.Candidates) > 0 {
		fmt.Fprintf(bw, "next: ferret adjudicate --session %s --propose\n", res.Session)
	}
	return bw.Flush()
}
