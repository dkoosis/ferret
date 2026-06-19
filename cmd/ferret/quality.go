package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/conform"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
	"github.com/dkoosis/ferret/internal/transcript"
)

// This file is the thin CLI dispatch + render shell over the reference-free
// quality scorers in internal/score (ScoreAxes + ClusterByShape, kuv.5). It has
// two scopes, forced by arithmetic not preference: per-task axes are meaningful
// per session, but pass^k consistency needs k attempts at the same shape and a
// single session almost never repeats an exact Shape — so consistency is computed
// corpus-wide. --session PREFIX → that session's per-task axes; no --session →
// the corpus consistency report (the same scope-by-presence pattern candidates
// uses). cmd/ resolves the transcript(s); the scoring lives in score.

// qualitySpreadWarnCap is the cost coefficient-of-variation above which a
// recurring shape is flagged as inconsistently handled (the pass^k warning,
// parallel to conformance's flowerModel): same task, wildly variable cost.
const qualitySpreadWarnCap = 0.5

// qualityTaskSpec is one task's conformance reference + observed labeled calls —
// the same analyst-supplied shape `ferret conformance` consumes, scoped to a
// single task. Used to enrich the adaptivity axis of that task (ferret-t5d).
type qualityTaskSpec struct {
	Reference []string          `json:"reference"`
	Observed  []conform.ObsCall `json:"observed"`
}

// qualitySpec is the OPTIONAL conformance enrichment input for `ferret quality
// --session`: a map of task Index → that task's conformance spec. Supplied by
// piping JSON on stdin (no new flag — same stdin convention `ferret conformance`
// uses for its spec). When present, listed tasks get a conformance-enriched
// adaptivity axis (recoverable-vs-loop localized by the alignment); unlisted tasks
// and the no-spec case fall back to the reference-free proxy unchanged.
type qualitySpec map[int]qualityTaskSpec

// cmdQuality wires the kong CLI flags to the per-session or corpus quality scope.
// For the per-session scope, a conformance spec piped on stdin (JSON: task-index →
// {reference, observed}) enriches the adaptivity axis from the alignment; with no
// piped spec the reference-free axes are emitted unchanged.
func cmdQuality() error {
	cmd := &CLI.Quality
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
	if strings.TrimSpace(cmd.Session) != "" {
		spec, err := readPipedQualitySpec()
		if err != nil {
			return err
		}
		return qualitySessionWithSpec(os.Stdout, root, cmd.Session, cmd.Format, spec)
	}
	return qualityCorpus(os.Stdout, root, cmd.Format)
}

// readPipedQualitySpec reads a conformance enrichment spec from stdin ONLY when
// stdin is a pipe/redirect (data is being fed), never an interactive terminal —
// so the default `ferret quality --session X` stays reference-free and does not
// block waiting on a TTY. nil spec = reference-free path.
func readPipedQualitySpec() (qualitySpec, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return nil, nil //nolint:nilerr // a TTY/unstattable stdin means "no spec piped"
	}
	return readQualitySpec(os.Stdin)
}

// readQualitySpec parses the task-index → conformance-spec JSON. An empty stream
// yields a nil spec (reference-free), not an error.
func readQualitySpec(r io.Reader) (qualitySpec, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConformReadSpec, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var spec qualitySpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, fmt.Errorf("%w: %w", errConformBadSpec, err)
	}
	return spec, nil
}

// qualitySession segments one session and emits its per-task reference-free axes.
func qualitySession(w io.Writer, root, session, format string) error {
	return qualitySessionWithSpec(w, root, session, format, nil)
}

// qualitySessionWithSpec segments one session and emits its per-task axes; when
// spec carries conformance results keyed by task index, those tasks' adaptivity is
// computed from the alignment instead of the reference-free proxy. A nil spec is
// exactly the reference-free path.
func qualitySessionWithSpec(w io.Writer, root, session, format string, spec qualitySpec) error {
	res, err := segmentSession(root, session)
	if err != nil {
		return err
	}
	if len(spec) == 0 {
		score.ScoreAxes(&res)
	} else {
		score.ScoreAxesWithConform(&res, alignQualitySpec(spec))
	}
	if format == fmtJSON {
		return writeQualitySessionJSON(w, res)
	}
	return writeQualitySessionText(w, res)
}

// alignQualitySpec runs each task's conformance spec through conform.Align,
// producing the per-task results ScoreAxesWithConform consumes.
func alignQualitySpec(spec qualitySpec) map[int]conform.Result {
	out := make(map[int]conform.Result, len(spec))
	for idx, ts := range spec {
		out[idx] = conform.Align(ts.Reference, ts.Observed)
	}
	return out
}

// qualityCorpus walks every transcript, scores each task's axes, clusters tasks
// by exact shape across all sessions, and emits the consistency report. A
// transcript that fails to read is skipped (advisory view; one bad session must
// not abort the scan), mirroring aggregateCorpus.
func qualityCorpus(w io.Writer, root, format string) error {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return err
	}
	var (
		allSegs  []score.Segment
		sessions int
		tasks    int
	)
	for _, src := range srcs {
		res, serr := score.SegmentSource(src)
		if serr != nil {
			continue
		}
		sessions++
		score.ScoreAxes(&res)
		for i := range res.Segments {
			seg := res.Segments[i]
			if seg.Index == 0 || seg.FirstCall < 0 {
				continue
			}
			tasks++
			allSegs = append(allSegs, seg)
		}
	}
	clusters := score.ClusterByShape(allSegs)
	if format == fmtJSON {
		return writeQualityCorpusJSON(w, root, sessions, tasks, clusters)
	}
	return writeQualityCorpusText(w, root, sessions, tasks, clusters)
}

// recurring returns the clusters seen more than once (the ones with a pass^k
// signal), sorted by K descending then Key, plus the count of single-attempt
// shapes folded out (reported as "no signal", never a spurious 1.0).
func recurring(clusters []score.Cluster) (multi []score.Cluster, singletons int) {
	for _, c := range clusters {
		if c.K < 2 {
			singletons++
			continue
		}
		multi = append(multi, c)
	}
	sort.SliceStable(multi, func(i, j int) bool {
		if multi[i].K != multi[j].K {
			return multi[i].K > multi[j].K
		}
		return multi[i].Key < multi[j].Key
	})
	return multi, singletons
}

// writeQualitySessionText emits the per-task axes rows + a roll-up.
func writeQualitySessionText(w io.Writer, res score.Result) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "quality session=%s project=%s\n", res.Session, res.Project)
	fmt.Fprintln(bw,
		"≡ reference-free per-task axes (TRACE): efficiency = friction-free (distinct tool "+
			"kinds / calls — low = thrash); adaptivity = recoverable (did churn resolve or run "+
			"to the boundary). cost is a reported column, NOT folded into the axes.")

	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Axes == nil {
			continue
		}
		fmt.Fprintf(bw, "[task %d] eff=%.2f adapt=%.2f  cost=%s  shape: %s",
			seg.Index, seg.Axes.Efficiency, seg.Axes.Adaptivity,
			humanBytes(seg.InBytes+seg.OutBytes), shapeLabel(seg.Shape))
		fmt.Fprint(bw, outcomeNote(*seg))
		fmt.Fprintln(bw)
	}

	fmt.Fprintf(bw, "--- tasks=%d calls=%d cost=%s\n",
		boundaryCount(res), res.TotalCalls, humanBytes(res.TotalIn+res.TotalOut))
	return bw.Flush()
}

// writeQualitySessionJSON emits the per-task axes (the contract).
func writeQualitySessionJSON(w io.Writer, res score.Result) error {
	tasks := make([]map[string]any, 0)
	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Axes == nil {
			continue
		}
		tasks = append(tasks, map[string]any{
			"index":      seg.Index,
			"efficiency": seg.Axes.Efficiency,
			"adaptivity": seg.Axes.Adaptivity,
			"cost":       seg.InBytes + seg.OutBytes,
			"shape":      seg.Shape,
		})
	}
	return out.JSON(w, map[string]any{
		"session": res.Session,
		"project": res.Project,
		"tasks":   tasks,
	})
}

// writeQualityCorpusText emits the cross-session pass^k consistency report.
func writeQualityCorpusText(w io.Writer, root string, sessions, tasks int, clusters []score.Cluster) error {
	bw := bufio.NewWriter(w)
	multi, singletons := recurring(clusters)

	fmt.Fprintf(bw, "quality corpus root=%s sessions=%d tasks=%d\n", root, sessions, tasks)
	fmt.Fprintln(bw,
		"≡ pass^k consistency (tau-bench): tasks sharing an exact tool-shape are repeat attempts at "+
			"one recurring task; consistency = how tightly their cost clusters (1 = predictable = "+
			"delightful). spread = coefficient of variation of cost. single-attempt shapes have no signal.")

	for _, c := range multi {
		fmt.Fprintf(bw, "[shape] k=%d consistency=%.2f spread=%.2f  tokens: %s\n",
			c.K, c.Consistency, c.Spread, shapeLabel(c.Shape))
		if c.Spread > qualitySpreadWarnCap {
			fmt.Fprintf(bw,
				"  ⚠ inconsistent: cost varies %.0f%% across %d attempts — a recurring task handled unpredictably\n",
				c.Spread*100, c.K)
		}
	}

	fmt.Fprintf(bw, "--- recurring-shapes=%d single-attempt=%d sessions=%d tasks=%d\n",
		len(multi), singletons, sessions, tasks)
	return bw.Flush()
}

// writeQualityCorpusJSON emits the consistency clusters (the contract). Only the
// recurring (k≥2) clusters carry a signal, but every cluster is emitted with its
// K so a consumer sees the single-attempt sentinels too.
func writeQualityCorpusJSON(w io.Writer, root string, sessions, tasks int, clusters []score.Cluster) error {
	rows := make([]map[string]any, 0, len(clusters))
	for _, c := range clusters {
		rows = append(rows, map[string]any{
			"shape":       c.Shape,
			"k":           c.K,
			"consistency": c.Consistency,
			"spread":      c.Spread,
		})
	}
	return out.JSON(w, map[string]any{
		"root":     root,
		"sessions": sessions,
		"tasks":    tasks,
		"clusters": rows,
	})
}

// shapeLabel joins a task's ordered shape tokens for display, truncated.
func shapeLabel(shape []string) string {
	return truncateRunes(strings.Join(shape, ","), spineTextCap)
}
