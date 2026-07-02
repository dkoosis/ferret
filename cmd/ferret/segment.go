package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/score"
)

// This file is the thin CLI dispatch + render shell over the deterministic
// segmenter, which lives in internal/score (the shared per-task substrate, per
// the kuv design doc Decision 2). cmd/ resolves a session prefix to a transcript
// and renders score.Result; the boundary/pivot/cost logic is score's.

// cmdSegments wires the kong CLI flags to segments(), resolving the transcript
// root the same way spine/ingest do.
func cmdSegments() error {
	cmd := &CLI.Segments
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
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
	return segments(os.Stdout, root, cmd.Session, cmd.Format)
}

// segmentSession resolves session (a prefix) to one transcript under root and
// streams it through the deterministic segmenter, returning the finished
// score.Result. It is the shared entry both `segments` and `candidates` build on,
// so the two commands segment a session identically (same boundary rules, same
// per-task cost). It reuses resolveSpineSource (spine/segments/candidates agree on
// which transcript a prefix names) and the same line-tolerant decode.
func segmentSession(root, session string) (score.Result, error) {
	src, distinct, err := resolveSpineSource(root, session)
	if err != nil {
		return score.Result{}, err
	}
	if distinct > 1 {
		fmt.Fprintf(os.Stderr,
			"ferret: --session %q matched %d sessions; emitting %q (use a longer prefix to disambiguate)\n",
			session, distinct, src.Session)
	}

	return score.SegmentSource(src)
}

// segments resolves session (a prefix) to one transcript under root and streams
// its deterministic task-boundary candidates to w.
func segments(w io.Writer, root, session, format string) error {
	res, err := segmentSession(root, session)
	if err != nil {
		return err
	}

	if format == fmtJSON {
		return writeSegmentsJSON(w, res)
	}
	return writeSegmentsText(w, res)
}

// writeSegmentsJSON emits the segmentation as indented JSON (the analyst-bundle
// feed: kuv.3/4/5 and 567 consume this as the deterministic scaffold).
func writeSegmentsJSON(w io.Writer, res score.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// writeSegmentsText emits the human-readable spine-style rendering.
func writeSegmentsText(w io.Writer, res score.Result) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "segments session=%s project=%s", res.Session, res.Project)
	if res.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", res.Agent)
	}
	fmt.Fprintln(bw)
	fmt.Fprintln(bw,
		"≡ DETERMINISTIC boundary candidates (1 per user prompt; affirmations/control "+
			"built-ins/system envelopes fold in as [cont]) + pivot hints. Semantic merge "+
			"of interleaved sub-goals + per-task intent = analyst-side, NOT ferret.")

	for i := range res.Segments {
		seg := &res.Segments[i]
		label := fmt.Sprintf("seg %d", seg.Index)
		if seg.Index == 0 {
			label = "preamble"
		}
		fmt.Fprintf(bw, "[%s] calls=%s", label, callRange(*seg))
		if seg.InBytes+seg.OutBytes > 0 {
			fmt.Fprintf(bw, " cost=%s out=%s", humanBytes(seg.InBytes+seg.OutBytes), humanBytes(seg.OutBytes))
		}
		if seg.Prompt != "" {
			fmt.Fprintf(bw, "  prompt: %s", truncateRunes(seg.Prompt, spineTextCap))
		}
		fmt.Fprint(bw, outcomeNote(*seg))
		fmt.Fprint(bw, dialogueOutcomeNote(seg, nextOpensNewTask(res.Segments, i)))
		fmt.Fprintln(bw)
		for _, p := range seg.Pivots {
			fmt.Fprintf(bw, "  [pivot] think#%d  cue=%q\n", p.Think, p.Cue)
		}
		for _, c := range seg.Conts {
			fmt.Fprintf(bw, "  [cont:%s] %s\n", c.Kind, c.Text)
		}
	}

	fmt.Fprintf(bw, "--- segments=%d calls=%d cost=%s out=%s pivots=%d conts=%d",
		boundaryCount(res), res.TotalCalls,
		humanBytes(res.TotalIn+res.TotalOut), humanBytes(res.TotalOut),
		res.Pivots, res.Conts)
	if res.OutOrphan > 0 {
		fmt.Fprintf(bw, " out-orphan=%s", humanBytes(res.OutOrphan))
	}
	if res.DecodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", res.DecodeErrs)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}

// outcomeNote renders the WEAK terminal-action success flag (kuv.8) as a trailing
// note, or "" when the task shipped nothing. NOT ground truth — a hint beside the
// segment, never a verdict; absence is silence, not a failure label.
func outcomeNote(seg score.Segment) string {
	if seg.Outcome == nil || !seg.Outcome.Positive {
		return ""
	}
	return "  [outcome:shipped~weak] " + seg.Outcome.Signal
}

// dialogueOutcomeNote renders a segment's per-EPISODE dialogue Outcome — the
// PARADISE task-success leg (Walker et al. 1997) — as a trailing note: it tags the
// segment's user turns with the v1 move tagger and rolls them up via ClassifyCross.
// nextOpensNewTask carries the cross-episode abandonment tell: when this episode
// never reached acceptance and the NEXT segment opened on a fresh task, the human
// walked away unresolved (abandonment by topic switch, ferret-bbp.8). OutcomeUnknown
// (no accept/repair signal, no topic switch) is silence, mirroring outcomeNote — a
// per-segment outcome is a hint, never a verdict.
func dialogueOutcomeNote(seg *score.Segment, nextOpensNewTask bool) string {
	out := classifyTurnsCross(segmentUserTurns(seg), nextOpensNewTask)
	if out == dialogue.OutcomeUnknown {
		return ""
	}
	return "  [dialogue:" + string(out) + "]"
}

// classifyTurnsCross mirrors dialogue.ClassifyTurns but threads the cross-episode
// topic-switch signal: it TagMoves each raw user turn, then ClassifyCross()es the
// move sequence with nextOpensNewTask. The pure ClassifyTurns cannot see the next
// segment's opening move; this per-segment consumer can, so it wires the signal in
// the same way the retrieval per-episode path does (internal/score, ferret-bbp.8).
func classifyTurnsCross(turns []string, nextOpensNewTask bool) dialogue.Outcome {
	moves := make([]dialogue.Move, 0, len(turns))
	for _, t := range turns {
		m, _ := dialogue.TagMove(t)
		moves = append(moves, m)
	}
	return dialogue.ClassifyCross(moves, nextOpensNewTask)
}

// nextOpensNewTask reports whether the segment AFTER segs[i] opened on a fresh task
// (its opening prompt tags MoveNewTask) — the adjacency the per-segment
// dialogueOutcomeNote needs to read abandonment by topic switch. The final segment
// has no next, so it reports false and falls back to the pure single-episode verdict.
func nextOpensNewTask(segs []score.Segment, i int) bool {
	if i+1 >= len(segs) {
		return false
	}
	m, _ := dialogue.TagMove(segs[i+1].Prompt)
	return m == dialogue.MoveNewTask
}

// segmentUserTurns gathers one segment's genuine user turns in order: the prompt
// that opened it, then each affirmation continuation the segmenter folded in (the
// "lgtm"/"ship it" acks that carry the accept signal). Carrier and control
// continuations are transcript plumbing, not user language, so they are left out;
// the synthetic preamble (empty prompt) contributes no turn.
func segmentUserTurns(seg *score.Segment) []string {
	var turns []string
	if seg.Prompt != "" {
		turns = append(turns, seg.Prompt)
	}
	for i := range seg.Conts {
		if seg.Conts[i].Kind == "affirmation" {
			turns = append(turns, seg.Conts[i].Text)
		}
	}
	return turns
}

// callRange renders a segment's owned tool-call index span, or "none".
func callRange(seg score.Segment) string {
	if seg.FirstCall == -1 {
		return "none"
	}
	if seg.FirstCall == seg.LastCall {
		return strconv.Itoa(seg.FirstCall)
	}
	return fmt.Sprintf("%d..%d", seg.FirstCall, seg.LastCall)
}

// boundaryCount counts real prompt-opened segments (excludes the synthetic
// preamble) — the number of deterministic boundary candidates.
func boundaryCount(res score.Result) int {
	n := 0
	for i := range res.Segments {
		if res.Segments[i].Index > 0 {
			n++
		}
	}
	return n
}
