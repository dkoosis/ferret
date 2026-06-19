package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/score"
	"github.com/dkoosis/ferret/internal/transcript"
)

// Corpus candidates is Phase 2 of the candidates bundle (ferret-kuv.12) — the
// cross-session half of the metrics-engine→analyst loop.
//
// Phase 1 ('candidates --session') ranks ONE session's tasks. This half answers
// the recurrence question the n-grams first surfaced — "I do that all the time" —
// but keyed on the TASK unit, not a raw call-sequence: it walks the whole corpus,
// segments every session into tasks, and clusters tasks by a deterministic
// SHAPE so a routine you repeat across many sessions rises to the top with the
// total context it has cost you.
//
// Shape = the sorted SET of tool-shape tokens a task touches (Read, Edit,
// sh:git_commit, mcp:trixi.set_nug …). Set, not sequence, is deliberate: two runs
// of the same routine that interleave their reads and edits differently are the
// same shape, so the clustering is robust to ordering the way Jones&Klinkner's
// same-task-pair classifier is robust to surface variation. ferret stops at the
// deterministic shape; merging shapes that share an INTENT (a snipe-run and an
// rg-run of "find the callers" are one intent, two shapes) stays analyst-side —
// the same division of labor as segments (deterministic boundaries, semantic
// merge by Claude). ferret never infers intent.
//
// Rank = aggregate leak: summed cost × out-weight across every occurrence. A
// shape that recurs across many sessions accrues cost on every run, so recurrence
// folds into the summed cost rather than being a separate multiplier — the score
// is "how much context has this repeated task-shape cost me, in total, weighted
// by how much of it was reclaimable tool output." minSessions gates out one-off
// shapes (that is Phase 1's job); the analyst reads the survivors and proposes a
// script for the recurring routine or a de-context for the output-heavy one.

// corpusSigCap bounds the distinct tokens in a shape signature. A task touching
// more than this many distinct tools is a sprawling mixed task; its overflow
// tokens collapse so the key stays bounded (rare, and such tasks cluster together
// as "big mixed" rather than each forming a singleton).
const corpusSigCap = 12

// corpusCand is one recurring task-shape aggregated across the corpus.
type corpusCand struct {
	Shape       []string `json:"shape"`       // sorted unique shape tokens — the recurrence key
	Sig         string   `json:"sig"`         // the joined signature (display + stable join key)
	Occurrences int      `json:"occurrences"` // tasks matching this shape, across all sessions
	Sessions    int      `json:"sessions"`    // distinct sessions exhibiting it — the "all the time" signal
	Cost        int      `json:"cost"`        // summed in+out bytes across occurrences (the aggregate leak)
	OutBytes    int      `json:"outBytes"`    // summed tool-output bytes — the de-context payoff
	OutWeight   float64  `json:"outWeight"`   // OutBytes / Cost
	MeanCost    int      `json:"meanCost"`    // Cost / Occurrences — per-instance leak magnitude
	Intent      string   `json:"intent"`      // a heavy-exemplar opening prompt — the analyst rewrites it
	Score       float64  `json:"score"`       // composite rank (see file header)
}

// corpusResult is the whole ranked corpus-recurrence bundle.
type corpusResult struct {
	Root        string       `json:"root"`
	Candidates  []corpusCand `json:"candidates"`
	Shapes      int          `json:"shapes"`   // recurring shapes (≥ minSessions) before --top capping
	Sessions    int          `json:"sessions"` // transcripts walked
	Tasks       int          `json:"tasks"`    // costed tasks clustered
	MinSessions int          `json:"minSessions"`
	Top         int          `json:"top"`
	Truncated   bool         `json:"truncated"`
	DecodeErrs  int          `json:"decodeErrs,omitempty"`
}

// shapeAgg accumulates one shape's totals as the corpus walk streams tasks into
// it. sessions is a set so the same shape recurring twice in one session counts
// as one session of recurrence, not two.
type shapeAgg struct {
	tokens     []string
	occ        int
	cost       int
	outBytes   int
	sessions   map[string]struct{}
	intent     string // opening prompt of the heaviest occurrence seen
	intentCost int
}

// shapeSignature reduces a task's ordered shape tokens to its canonical key: the
// sorted set of distinct tokens, capped at corpusSigCap. ok is false for a task
// that owns no shaped call (nothing to cluster on).
func shapeSignature(shape []string) (sig string, tokens []string, ok bool) {
	if len(shape) == 0 {
		return "", nil, false
	}
	seen := make(map[string]struct{}, len(shape))
	uniq := make([]string, 0, len(shape))
	for _, t := range shape {
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		uniq = append(uniq, t)
	}
	if len(uniq) == 0 {
		return "", nil, false
	}
	sort.Strings(uniq)
	if len(uniq) > corpusSigCap {
		uniq = uniq[:corpusSigCap]
	}
	return strings.Join(uniq, " + "), uniq, true
}

// scoreCorpusShape is the aggregate leak score: summed cost weighted by the share
// that was tool output. Pure, so the factor arithmetic is unit-testable. A
// zero-cost shape scores 0 (it never reaches the aggregator, but the guard keeps
// the function total).
func scoreCorpusShape(cost, outBytes int) (score, outWeight float64) {
	if cost == 0 {
		return 0, 0
	}
	outWeight = float64(outBytes) / float64(cost)
	return float64(cost) * (1 + outWeight), outWeight
}

// aggregateCorpus walks every transcript under root, segments each into tasks,
// and clusters the costed tasks by shape. It returns the per-shape aggregates
// plus the session/task/decode-error tallies for the result header. A transcript
// that fails to read is skipped (its tasks simply don't contribute) — the corpus
// view is advisory and one unreadable session must not abort the whole scan.
func aggregateCorpus(root string) (aggs map[string]*shapeAgg, sessions, tasks, decodeErrs int, err error) {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	aggs = map[string]*shapeAgg{}
	for _, src := range srcs {
		res, serr := score.SegmentSource(src)
		if serr != nil {
			continue
		}
		sessions++
		decodeErrs += res.DecodeErrs
		tasks += foldSession(aggs, res)
	}
	return aggs, sessions, tasks, decodeErrs, nil
}

// foldSession clusters one session's costed tasks into the shape aggregates and
// returns the number it counted (the preamble and zero-cost/shapeless tasks are
// skipped). Split out of aggregateCorpus to keep that walk flat.
func foldSession(aggs map[string]*shapeAgg, res score.Result) (counted int) {
	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Index == 0 {
			continue // preamble
		}
		cost := seg.InBytes + seg.OutBytes
		if cost == 0 {
			continue
		}
		sig, tokens, ok := shapeSignature(seg.Shape)
		if !ok {
			continue
		}
		counted++
		a := aggs[sig]
		if a == nil {
			a = &shapeAgg{tokens: tokens, sessions: map[string]struct{}{}}
			aggs[sig] = a
		}
		a.occ++
		a.cost += cost
		a.outBytes += seg.OutBytes
		a.sessions[res.Session] = struct{}{}
		if seg.Prompt != "" && cost > a.intentCost {
			a.intent = seg.Prompt
			a.intentCost = cost
		}
	}
	return counted
}

// rankCorpus projects the shape aggregates into the ranked bundle, keeping only
// shapes that recur across at least minSessions distinct sessions (a one-session
// shape is Phase 1's per-session job, not corpus recurrence). Sorted by aggregate
// leak score descending, with deterministic tie-breaks so repeated runs are
// byte-stable.
func rankCorpus(aggs map[string]*shapeAgg, minSessions int) []corpusCand {
	cands := make([]corpusCand, 0, len(aggs))
	for sig, a := range aggs {
		if len(a.sessions) < minSessions {
			continue
		}
		score, ow := scoreCorpusShape(a.cost, a.outBytes)
		cands = append(cands, corpusCand{
			Shape:       a.tokens,
			Sig:         sig,
			Occurrences: a.occ,
			Sessions:    len(a.sessions),
			Cost:        a.cost,
			OutBytes:    a.outBytes,
			OutWeight:   ow,
			MeanCost:    a.cost / a.occ,
			Intent:      truncateRunes(a.intent, candIntentCap),
			Score:       score,
		})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		x, y := cands[i], cands[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Sessions != y.Sessions {
			return x.Sessions > y.Sessions
		}
		if x.Occurrences != y.Occurrences {
			return x.Occurrences > y.Occurrences
		}
		return x.Sig < y.Sig
	})
	return cands
}

// corpusCandidates walks root, ranks recurring task-shapes, and emits the bundle.
func corpusCandidates(w io.Writer, root, format string, top, minSessions int) error {
	aggs, sessions, tasks, decodeErrs, err := aggregateCorpus(root)
	if err != nil {
		return err
	}
	cands := rankCorpus(aggs, minSessions)
	res := corpusResult{
		Root:        root,
		Shapes:      len(cands),
		Sessions:    sessions,
		Tasks:       tasks,
		MinSessions: minSessions,
		Top:         top,
		DecodeErrs:  decodeErrs,
	}
	if top > 0 && len(cands) > top {
		cands = cands[:top]
		res.Truncated = true
	}
	res.Candidates = cands
	if format == fmtJSON {
		return writeCorpusJSON(w, res)
	}
	return writeCorpusText(w, res)
}

// writeCorpusJSON emits the bundle as indented JSON — the analyst feed.
func writeCorpusJSON(w io.Writer, res corpusResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// writeCorpusText emits the human/analyst-readable recurrence ranking.
func writeCorpusText(w io.Writer, res corpusResult) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "corpus candidates root=%s sessions=%d tasks=%d\n", res.Root, res.Sessions, res.Tasks)
	fmt.Fprintln(bw,
		"≡ recurring task-SHAPES across the corpus, ranked by aggregate cost × out-weight. A shape = the "+
			"sorted set of tool tokens a task touches (the deterministic 'I do that all the time' key); the "+
			"analyst merges shapes that share an intent and scripts the recurring ones / de-contexts the high-ow ones.")

	for _, c := range res.Candidates {
		fmt.Fprintf(bw, "[shape] sessions=%d occ=%d cost=%s out=%s ow=%.2f mean=%s  tokens: %s",
			c.Sessions, c.Occurrences, humanBytes(c.Cost), humanBytes(c.OutBytes),
			c.OutWeight, humanBytes(c.MeanCost), c.Sig)
		if c.Intent != "" {
			fmt.Fprintf(bw, "  intent: %s", c.Intent)
		}
		fmt.Fprintln(bw)
	}

	fmt.Fprintf(bw, "--- shapes=%d shown=%d sessions=%d tasks=%d (min-sessions=%d)",
		res.Shapes, len(res.Candidates), res.Sessions, res.Tasks, res.MinSessions)
	if res.Truncated {
		fmt.Fprintf(bw, " (top %d of %d)", res.Top, res.Shapes)
	}
	if res.DecodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", res.DecodeErrs)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}
