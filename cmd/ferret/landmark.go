package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

var (
	errLandmarkBadSpec      = errors.New("landmark: spec is not valid JSON")
	errLandmarkNoMilestones = errors.New("landmark: spec.milestones must list ≥1 milestone")
	errLandmarkNegWeight    = errors.New("landmark: milestone weight must be ≥0 (a non-zero weight is an override; negatives break the [0,1] progress score)")
	errLandmarkReadSpec     = errors.New("landmark: cannot read spec")
)

// landmarkHighWeightCap is the uniqueness-weight threshold above which a MISSED
// milestone is the load-bearing "goal likely not reached" signal — parallel to
// conformance's flowerPrecisionCap. A landmark this rare must hold on any path to
// the goal (Pereira/Oren/Meneguzzi), so missing it is strong evidence the goal was
// not reached. Tuned for the WeighByCorpus idf scale (ln(N)+1-ish): a tool seen in
// only a small share of streams clears it; a generic one (Read/Edit) does not.
const landmarkHighWeightCap = 2.0

// landmarkSpec is the analyst-supplied input, parallel to conformSpec: the loose
// milestone set (each an ID + the Shape tokens that satisfy it) and the observed
// Shape tokens for the task. The analyst supplies the milestone SEMANTICS; the
// engine fills each uniqueness Weight from corpus frequency (WeighByCorpus) unless
// the spec carries an explicit override. Observed is Shape tokens directly (v1,
// per the plan) — a --session-derived mode is a deliberate follow-on (ferret-afm).
type landmarkSpec struct {
	Task       string            `json:"task,omitempty"`
	Milestones []score.Milestone `json:"milestones"`
	Shape      []string          `json:"shape"`
}

// cmdLandmark wires the kong CLI flags to the landmark progress scorer. Two
// scopes (mirroring quality's --session-vs-corpus fork): with --session it
// segments a session and scores each task's goal against the milestone-set
// library; otherwise it reads a single-task --spec/stdin. In both scopes the
// corpus at --data fills uniqueness weights (uniform fallback if absent).
func cmdLandmark() error {
	cmd := &CLI.Landmark
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	if strings.TrimSpace(cmd.Session) != "" {
		root := cmd.Root
		if root == "" {
			r, err := defaultRoot()
			if err != nil {
				return err
			}
			root = r
		}
		return landmarkSession(os.Stdout, root, cmd.Session, cmd.Data, cmd.Format)
	}
	spec, err := readLandmarkSpec(cmd.Spec)
	if err != nil {
		return err
	}
	if len(spec.Milestones) == 0 {
		return errLandmarkNoMilestones
	}
	for _, m := range spec.Milestones {
		if m.Weight < 0 {
			return fmt.Errorf("%w: %q has weight %g", errLandmarkNegWeight, m.ID, m.Weight)
		}
	}
	weighMilestones(spec.Milestones, cmd.Data)
	res := score.ScoreLandmarks(spec.Milestones, spec.Shape)
	if cmd.Format == fmtJSON {
		return writeLandmarkJSON(os.Stdout, spec, res)
	}
	return writeLandmarkText(os.Stdout, spec, res)
}

// weighMilestones fills each unweighted milestone's uniqueness weight from the
// corpus at dataDir (default ~/.ferret) via score.WeighByCorpus — the measured
// uniqueness path (rarer tools weigh more). When no corpus is available (no
// events.jsonl yet, or it fails to build), it falls back to a uniform weight of 1
// for any still-unweighted milestone so the command remains usable from a bare
// spec. A spec-supplied weight is always honored (WeighByCorpus skips it).
func weighMilestones(milestones []score.Milestone, dataDir string) {
	if corpus := landmarkCorpus(dataDir); corpus != nil {
		score.WeighByCorpus(milestones, corpus)
	}
	for i := range milestones {
		if milestones[i].Weight == 0 {
			milestones[i].Weight = 1 // uniform fallback (no corpus / unknown tool path covered upstream)
		}
	}
}

// landmarkCorpus builds the corpus from dataDir/events.jsonl under the tool lens,
// or returns nil when the artifact is absent or fails to build. Weighting is an
// enhancement, never a hard dependency: a missing corpus degrades to uniform
// weights, not an error (the scorer's correctness does not need it).
func landmarkCorpus(dataDir string) *mine.Corpus {
	dir := dataDir
	if dir == "" || dir == "~/.ferret" {
		d, err := defaultData()
		if err != nil {
			return nil
		}
		dir = d
	}
	path := filepath.Join(dir, "events.jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	l, err := lens.Get("tool")
	if err != nil {
		return nil
	}
	corpus, err := mine.Build(path, l, mine.Options{})
	if err != nil {
		return nil
	}
	return corpus
}

// landmarkSession is the --session scope: it segments one session (the same
// resolution segments/quality use), maps each task's stated goal to its milestone
// set, scores the task's Shape, and renders the per-task progress. The corpus at
// dataDir fills uniqueness weights (uniform fallback when absent — weighting is an
// enhancement, never a hard dependency). The mapping + scoring loop lives in
// internal/analyst (ScoreSessionLandmarks) so this stays a thin render shell.
func landmarkSession(w io.Writer, root, session, dataDir, format string) error {
	res, err := segmentSession(root, session)
	if err != nil {
		return err
	}
	sp := analyst.ScoreSessionLandmarks(res, landmarkCorpus(dataDir))
	if format == fmtJSON {
		return writeLandmarkSessionJSON(w, sp)
	}
	return writeLandmarkSessionText(w, sp)
}

// writeLandmarkSessionJSON emits the per-session landmark verdict as indented
// JSON (the analyst.SessionProgress schema is the contract).
func writeLandmarkSessionJSON(w io.Writer, sp analyst.SessionProgress) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(sp)
}

// writeLandmarkSessionText emits the dense per-task rendering: one block per task
// (its goal kind + weighted progress + per-milestone hit/miss rows), with
// unrecognized tasks marked, and a recognized/total rollup.
func writeLandmarkSessionText(w io.Writer, sp analyst.SessionProgress) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "landmark session=%s project=%s", sp.Session, sp.Project)
	if sp.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", sp.Agent)
	}
	fmt.Fprintln(bw)
	about := func(lines ...string) {
		for _, ln := range lines {
			fmt.Fprintln(bw, ln)
		}
	}
	about(
		"≡ per-task goal PROGRESS: each task's stated goal maps to a necessary-milestone set",
		"≡ (milestone-set library), scored set-coverage over its calls — order-free, backtrack-tolerant.",
		"≡ ✓ hit · ✗ missed (necessary, never reached); an unrecognized goal is surfaced, not scored.")

	for i := range sp.Tasks {
		tp := &sp.Tasks[i]
		if !tp.Recognized {
			fmt.Fprintf(bw, "[task %d] goal=%q  unrecognized (no milestone set matched — skipped)\n",
				tp.Index, truncateRunes(tp.Goal, spineTextCap))
			continue
		}
		p := tp.Progress
		fmt.Fprintf(bw, "[task %d] %s  progress=%.2f  (%d/%d milestones, weight %.2f/%.2f)\n",
			tp.Index, tp.GoalKind, p.Score, p.HitCount, p.Total, p.HitWeight, p.TotalWeight)
		fmt.Fprintf(bw, "  goal: %s\n", truncateRunes(tp.Goal, spineTextCap))
		for _, h := range p.Hits {
			if h.Hit {
				fmt.Fprintf(bw, "  ✓ %-16s (weight %.2f)\n", h.Milestone, h.Weight)
				continue
			}
			fmt.Fprintf(bw, "  ✗ %-16s (weight %.2f — necessary, never reached)\n", h.Milestone, h.Weight)
		}
		if missingNecessary(p) {
			fmt.Fprintf(bw,
				"  ⚠ missing necessary milestone (uniqueness ≥ %.1f) — goal likely not reached\n",
				landmarkHighWeightCap)
		}
	}

	fmt.Fprintf(bw, "--- tasks=%d recognized=%d\n", sp.Total, sp.Recognized)
	return bw.Flush()
}

// readLandmarkSpec reads the spec from a file path, or from stdin when the path is
// empty or "-" (identical structure to readConformSpec).
func readLandmarkSpec(path string) (landmarkSpec, error) {
	var r io.Reader = os.Stdin
	if path != "" && path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return landmarkSpec{}, fmt.Errorf("%w: %w", errLandmarkReadSpec, err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return landmarkSpec{}, fmt.Errorf("%w: %w", errLandmarkReadSpec, err)
	}
	var spec landmarkSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return landmarkSpec{}, fmt.Errorf("%w: %w", errLandmarkBadSpec, err)
	}
	return spec, nil
}

// missingNecessary reports whether a high-weight (necessary) milestone was missed
// — the load-bearing "goal likely not reached" signal, parallel to flowerModel.
func missingNecessary(res score.Progress) bool {
	for _, h := range res.Hits {
		if !h.Hit && h.Weight >= landmarkHighWeightCap {
			return true
		}
	}
	return false
}

// writeLandmarkJSON emits the scored result (schema is the contract, parallel to
// the conformance JSON envelope).
func writeLandmarkJSON(w io.Writer, spec landmarkSpec, res score.Progress) error {
	return out.JSON(w, map[string]any{
		"task":             spec.Task,
		"milestones":       res.Total,
		"observed":         len(spec.Shape),
		"progress":         res.Score,
		"hitCount":         res.HitCount,
		"total":            res.Total,
		"hitWeight":        res.HitWeight,
		"totalWeight":      res.TotalWeight,
		"missingNecessary": missingNecessary(res),
		"hits":             res.Hits,
	})
}

// writeLandmarkText emits the dense human rendering: about-lines, the weighted
// progress, one row per milestone (hit/missed, with weight), the rollup, and the
// missing-necessary-milestone warning.
func writeLandmarkText(w io.Writer, spec landmarkSpec, res score.Progress) error {
	bw := bufio.NewWriter(w)

	about := func(lines ...string) {
		for _, ln := range lines {
			fmt.Fprintln(bw, ln)
		}
	}
	about(
		"≡ landmark: score a task's goal PROGRESS by how many necessary milestones its calls",
		"≡ touched — set-coverage, order-free, backtrack-tolerant (no plan, no alignment cost).",
		"≡ each milestone weighted by uniqueness (rarer tools weigh more); ✓ hit · ✗ missed.")

	fmt.Fprint(bw, "landmark")
	if spec.Task != "" {
		fmt.Fprintf(bw, " task=%q", spec.Task)
	}
	fmt.Fprintln(bw)
	fmt.Fprintf(bw, "milestones: %d   observed: %d shape tokens\n", res.Total, len(spec.Shape))
	fmt.Fprintf(bw, "progress=%.2f  (%d/%d milestones, weight %.2f/%.2f)\n",
		res.Score, res.HitCount, res.Total, res.HitWeight, res.TotalWeight)

	fmt.Fprintln(bw, "milestones:")
	for _, h := range res.Hits {
		if h.Hit {
			fmt.Fprintf(bw, "  ✓ %-16s (weight %.2f)\n", h.Milestone, h.Weight)
			continue
		}
		fmt.Fprintf(bw, "  ✗ %-16s (weight %.2f — necessary, never reached)\n", h.Milestone, h.Weight)
	}

	missed := res.Total - res.HitCount
	fmt.Fprintf(bw, "missed: %d milestone(s)\n", missed)
	if missingNecessary(res) {
		fmt.Fprintf(bw,
			"⚠ missing necessary milestone: a high-weight (uniqueness ≥ %.1f) milestone was never "+
				"reached — goal likely not reached\n", landmarkHighWeightCap)
	}
	return bw.Flush()
}
