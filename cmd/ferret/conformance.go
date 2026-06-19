package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/conform"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

var (
	errConformBadSpec  = errors.New("conformance: spec is not valid JSON")
	errConformNoRef    = errors.New("conformance: spec.reference must list ≥1 plan step")
	errConformReadSpec = errors.New("conformance: cannot read spec")
)

// flowerPrecisionCap defines the "invoked != served" warning (dk's "flower
// model" shorthand): when most observed calls served no planned step — precision
// below the cap, with at least one off-plan call — the conformance read is weak.
// Either the reference is too loose to be a real check (the process-mining flower
// model accepts anything) or the task drifted off-plan. A strict sequential
// reference can't be a literal flower model, so we detect the symptom — a low
// share of calls that served the plan — rather than the model's permissiveness.
const flowerPrecisionCap = 0.5

// conformSpec is the analyst-supplied input: the reference plan (ordered step
// labels, from the agent's stated intent or a best-practice template) and the
// observed trace (the task's tool calls, each tagged with the step it served;
// untagged = off-plan). ferret scores it but never produces it — deriving the
// plan and labeling the calls is the semantic, analyst-side half.
type conformSpec struct {
	Task      string            `json:"task,omitempty"`
	Reference []string          `json:"reference"`
	Observed  []conform.ObsCall `json:"observed"`
	// Outcome is the optional WEAK terminal-action success label for this task,
	// carried over from the segments scaffold (score.LabelOutcomes, kuv.8). It is
	// reported alongside the conformance verdict as ONE WEAK FEATURE and is
	// DELIBERATELY NOT folded into fitness/precision — conform's "naive success
	// label" (see internal/conform/conform.go header) stays a separate, weaker
	// signal than the alignment, never a reweight of it. nil = no signal.
	Outcome *score.Outcome `json:"outcome,omitempty"`
}

// cmdConformance wires the kong CLI flags to conformance().
func cmdConformance() error {
	cmd := &CLI.Conformance
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	spec, err := readConformSpec(cmd.Spec)
	if err != nil {
		return err
	}
	if len(spec.Reference) == 0 {
		return errConformNoRef
	}
	res := conform.Align(spec.Reference, spec.Observed)
	if cmd.Format == fmtJSON {
		return writeConformanceJSON(os.Stdout, spec, res)
	}
	return writeConformanceText(os.Stdout, spec, res)
}

// readConformSpec reads the spec from a file path, or from stdin when the path
// is empty or "-".
func readConformSpec(path string) (conformSpec, error) {
	var r io.Reader = os.Stdin
	if path != "" && path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return conformSpec{}, fmt.Errorf("%w: %w", errConformReadSpec, err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return conformSpec{}, fmt.Errorf("%w: %w", errConformReadSpec, err)
	}
	var spec conformSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return conformSpec{}, fmt.Errorf("%w: %w", errConformBadSpec, err)
	}
	return spec, nil
}

// flowerModel reports whether the result trips the "invoked != served" warning:
// at least one off-plan call and a minority of calls serving the plan.
func flowerModel(res conform.Result) bool {
	return res.LogMoves > 0 && res.Precision < flowerPrecisionCap
}

// writeConformanceJSON emits the scored result (schema is the contract).
func writeConformanceJSON(w io.Writer, spec conformSpec, res conform.Result) error {
	return out.JSON(w, map[string]any{
		"task":        spec.Task,
		"reference":   spec.Reference,
		"observed":    len(spec.Observed),
		"fitness":     res.Fitness,
		"precision":   res.Precision,
		"cost":        res.Cost,
		"worstCost":   res.WorstCost,
		"sync":        res.Sync,
		"skipped":     res.ModelMoves,
		"offPlan":     res.LogMoves,
		"noise":       res.Noise,
		"flowerModel": flowerModel(res),
		"outcome":     spec.Outcome, // weak terminal-action label; not scored (kuv.8)
		"moves":       res.Moves,
	})
}

// writeConformanceText emits the dense human rendering: scores, the alignment
// (one row per move, deviations marked), and the deviation roll-up + warning.
func writeConformanceText(w io.Writer, spec conformSpec, res conform.Result) error {
	bw := bufio.NewWriter(w)

	about := func(lines ...string) {
		for _, ln := range lines {
			fmt.Fprintln(bw, ln)
		}
	}
	about(
		"≡ conformance: replay a task's calls against a reference plan (the agent's stated",
		"≡ intent or a best-practice template). fitness = plan followed; precision = calls anticipated.",
		"≡ ✓ sync (call served the step) · ✗ skipped (planned, never executed) · + off-plan (invoked, served nothing).")

	fmt.Fprint(bw, "conformance")
	if spec.Task != "" {
		fmt.Fprintf(bw, " task=%q", spec.Task)
	}
	fmt.Fprintln(bw)
	fmt.Fprintf(bw, "reference (%d steps): %s\n", len(spec.Reference), strings.Join(spec.Reference, " → "))
	if res.Noise > 0 {
		fmt.Fprintf(bw, "observed: %d calls (%d logical scored, %d noise dropped) — %d on-plan\n",
			len(spec.Observed), len(spec.Observed)-res.Noise, res.Noise, res.Sync)
	} else {
		fmt.Fprintf(bw, "observed: %d calls (%d on-plan)\n", len(spec.Observed), res.Sync)
	}
	fmt.Fprintf(bw, "fitness=%.2f precision=%.2f  cost=%d/worst=%d\n",
		res.Fitness, res.Precision, res.Cost, res.WorstCost)

	fmt.Fprintln(bw, "alignment:")
	for _, mv := range res.Moves {
		switch mv.Type {
		case conform.MoveSync:
			fmt.Fprintf(bw, "  ✓ %-16s %s\n", mv.Step, obsLabel(mv.Obs, mv.Span))
		case conform.MoveModel:
			fmt.Fprintf(bw, "  ✗ %-16s (skipped — planned, never executed)\n", mv.Step)
		case conform.MoveLog:
			fmt.Fprintf(bw, "  + %-16s %s\n", "off-plan", obsLabel(mv.Obs, mv.Span))
		}
	}

	if spec.Outcome != nil && spec.Outcome.Positive {
		// WEAK success hint (kuv.8), reported beside the verdict but NOT scored:
		// the task ended in a terminal VCS action. A commit can be WIP and a push
		// reverted, so this never adjusts fitness/precision — dk's labels rule.
		fmt.Fprintf(bw, "outcome: shipped~weak (terminal action %s — NOT scored)\n", spec.Outcome.Signal)
	}
	fmt.Fprintf(bw, "deviations: %d skipped step(s), %d off-plan call(s)\n", res.ModelMoves, res.LogMoves)
	if flowerModel(res) {
		fmt.Fprintf(bw,
			"⚠ invoked != served: precision %.2f — most calls served no planned step "+
				"(reference too loose, or task drifted off-plan)\n", res.Precision)
	}
	return bw.Flush()
}

// obsLabel renders a move's observed phase as "call N Tool" (or "call N (K
// calls)" when the phase spans K>1 calls), tolerating a nil call. The phase is
// anchored at its first call N plus a count, NOT a "N–M" range: dropping noise
// can leave a phase's calls non-contiguous in the original index space, so a
// range would falsely imply the calls run unbroken.
func obsLabel(oc *conform.ObsCall, span int) string {
	if oc == nil {
		return ""
	}
	head := fmt.Sprintf("call %d", oc.Call)
	if span > 1 {
		head = fmt.Sprintf("call %d (%d calls)", oc.Call, span)
	}
	if oc.Tool == "" {
		return head
	}
	return head + "  " + oc.Tool
}
