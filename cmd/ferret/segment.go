package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/transcript"
)

// Segmentation is the DETERMINISTIC half of task segmentation (ferret-kuv.2).
//
// The design is a HYBRID: ferret (Go, LLM-free) emits hard-boundary CANDIDATES
// and cheap thinking-pivot HINTS; a Claude analyst later does the semantic merge
// of interleaved sub-goals across non-adjacent turns and writes one stated-intent
// sentence per task. ferret never infers intent — it only marks the places a task
// boundary plausibly sits, deterministically, so the analyst has a scaffold.
//
// Two deterministic signals:
//   - HARD BOUNDARY: every new user prompt opens a candidate segment. This is the
//     load-bearing signal — a user turn is the strongest LLM-free evidence of a
//     (possible) goal change. Each segment owns the tool-call indices that follow
//     its prompt, in encounter order, until the next prompt.
//   - PIVOT HINT: a lightweight string heuristic over thinking-block text that
//     flags reasoning which READS like a goal shift ("let me now…", "actually,…").
//     These are hints, not boundaries: the analyst decides whether a pivot inside a
//     segment is a genuine sub-task. No semantics, just leading-text cue matching.
//
// Everything here is a pure function of the transcript bytes: no time, no maps in
// output order, no randomness — repeated runs on the same input are byte-stable.

// segPivot is one thinking-pivot hint: the global thinking-block index it sits at
// and the cue phrase that matched (the lowercased prefix-cue, for traceability).
type segPivot struct {
	Think int    `json:"think"`
	Cue   string `json:"cue"`
}

// segment is one deterministic boundary candidate. FirstCall/LastCall are
// inclusive 0-based tool-call indices (encounter order) the segment owns, or -1
// when the segment owns no calls. Prompt is the verbatim user text that opened it
// (empty only for the synthetic preamble that holds calls preceding any prompt).
type segment struct {
	Index     int        `json:"index"`
	Prompt    string     `json:"prompt"`
	FirstCall int        `json:"firstCall"`
	LastCall  int        `json:"lastCall"`
	Pivots    []segPivot `json:"pivots,omitempty"`
}

// segResult is the whole emission for one session.
type segResult struct {
	Session    string    `json:"session"`
	Project    string    `json:"project"`
	Agent      string    `json:"agent,omitempty"`
	Segments   []segment `json:"segments"`
	TotalCalls int       `json:"totalCalls"`
	Pivots     int       `json:"pivots"`
	DecodeErrs int       `json:"decodeErrs,omitempty"`
}

// pivotCues are leading-text heuristics that read as a goal shift in reasoning.
// Matched against the lowercased, whitespace-collapsed PREFIX of a thinking block
// only (a cue buried mid-paragraph is not a pivot signal). Ordered longest-first
// so the most specific cue wins deterministically when several would match.
var pivotCues = []string{
	"on second thought",
	"let me step back",
	"let me reconsider",
	"different approach",
	"let me switch",
	"switching to",
	"let me now",
	"now let me",
	"moving on",
	"but first",
	"new task",
	"instead",
	"actually,",
	"okay, now",
	"ok, now",
	"wait,",
	"next,",
}

// segmenter accumulates the deterministic segmentation as it streams a transcript
// in order. It is the single source of truth for both text and JSON renderings.
type segmenter struct {
	segs       []segment
	callIndex  int  // next tool-call index to assign (encounter order)
	thinkIdx   int  // next thinking-block index to assign
	boundaries int  // real prompt-opened segments so far (the 1-based Index source)
	started    bool // a real prompt or preamble has opened the first segment
	preamble   bool // calls seen before any prompt → synthetic segment 0
	pivots     int
	decodeErr  int
}

// feed decodes one transcript line and folds its blocks into the segmentation.
// Mirrors emitSpineLine's tolerance: a bad line bumps decodeErr and is dropped.
func (s *segmenter) feed(line []byte) {
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		s.decodeErr++
		return
	}
	if raw.IsMeta || raw.Message == nil {
		return
	}
	if raw.Type != "assistant" && raw.Type != "user" {
		return
	}

	if raw.Type == "user" {
		if prompt := promptText(raw.Message.Content); prompt != "" {
			s.openSegment(prompt)
			return
		}
		// A user line with no genuine text (a tool_result carrier) is not a
		// boundary — its blocks carry no tool_use, so nothing to count here.
		return
	}

	// assistant line: count tool calls and scan thinking for pivot hints.
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
		switch blk.Type {
		case "tool_use":
			s.addCall()
		case "thinking":
			s.scanThinking(blk)
		}
	}
}

// promptText extracts the genuine user-prompt text from a content block list:
// the concatenation of its text blocks, whitespace-collapsed. A list with no text
// block (e.g. a pure tool_result carrier) returns "" — not a boundary.
func promptText(blocks transcript.Blocks) string {
	var parts []string
	for i := range blocks {
		if blocks[i].Type != "text" {
			continue
		}
		if body := strings.TrimSpace(blocks[i].Text); body != "" {
			parts = append(parts, body)
		}
	}
	return collapseWS(strings.Join(parts, " "))
}

// openSegment starts a new boundary candidate at the current call index.
func (s *segmenter) openSegment(prompt string) {
	s.started = true
	s.boundaries++
	s.segs = append(s.segs, segment{
		Index:     s.boundaries,
		Prompt:    prompt,
		FirstCall: -1,
		LastCall:  -1,
	})
}

// addCall assigns the next tool-call index to the open segment. Calls that precede
// the first prompt open a synthetic preamble segment (empty prompt) so no call is
// silently dropped — a transcript can begin with sidechain/tool activity.
func (s *segmenter) addCall() {
	if !s.started {
		s.segs = append(s.segs, segment{Index: 0, Prompt: "", FirstCall: -1, LastCall: -1})
		s.started = true
		s.preamble = true
	}
	cur := &s.segs[len(s.segs)-1]
	if cur.FirstCall == -1 {
		cur.FirstCall = s.callIndex
	}
	cur.LastCall = s.callIndex
	s.callIndex++
}

// scanThinking records a pivot hint when a thinking block's leading text matches a
// goal-shift cue. The hint is attached to the currently-open segment; thinking that
// precedes any prompt is ignored (no segment to attach to, and no boundary meaning).
func (s *segmenter) scanThinking(blk *transcript.Block) {
	body := strings.TrimSpace(blk.Thinking)
	if body == "" {
		body = strings.TrimSpace(blk.Text)
	}
	idx := s.thinkIdx
	s.thinkIdx++
	if body == "" || !s.started || s.preambleOnly() {
		return
	}
	prefix := leadingLower(body, 40)
	for _, cue := range pivotCues {
		if strings.HasPrefix(prefix, cue) {
			cur := &s.segs[len(s.segs)-1]
			cur.Pivots = append(cur.Pivots, segPivot{Think: idx, Cue: cue})
			s.pivots++
			return
		}
	}
}

// preambleOnly reports whether the only open segment is the synthetic preamble
// (no real prompt has arrived yet) — pivots there carry no task meaning.
func (s *segmenter) preambleOnly() bool {
	return len(s.segs) == 1 && s.segs[0].Index == 0
}

// leadingLower returns the lowercased, whitespace-collapsed first n runes of s —
// the window pivot cues are matched against.
func leadingLower(s string, n int) string {
	return strings.ToLower(truncateRunes(collapseWS(s), n))
}

// result finalizes the accumulated segmentation into a renderable value.
func (s *segmenter) result(src transcript.Source) segResult {
	return segResult{
		Session:    src.Session,
		Project:    src.Project,
		Agent:      src.Agent,
		Segments:   s.segs,
		TotalCalls: s.callIndex,
		Pivots:     s.pivots,
		DecodeErrs: s.decodeErr,
	}
}

// cmdSegments wires the kong CLI flags to segments(), resolving the transcript
// root the same way spine/ingest do.
func cmdSegments() error {
	cmd := &CLI.Segments
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
	if cmd.Format != "text" && cmd.Format != fmtJSON {
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

// segments resolves session (a prefix) to one transcript under root and streams
// its deterministic task-boundary candidates to w. It reuses resolveSpineSource
// (so spine and segments agree on which transcript a prefix names) and the same
// line-tolerant decode.
func segments(w io.Writer, root, session, format string) error {
	src, distinct, err := resolveSpineSource(root, session)
	if err != nil {
		return err
	}
	if distinct > 1 {
		fmt.Fprintf(os.Stderr,
			"ferret: --session %q matched %d sessions; emitting %q (use a longer prefix to disambiguate)\n",
			session, distinct, src.Session)
	}

	var sm segmenter
	if err := transcript.ReadLines(src.Path, func(line []byte) error {
		sm.feed(line)
		return nil
	}); err != nil {
		return err
	}
	res := sm.result(src)

	if format == fmtJSON {
		return writeSegmentsJSON(w, res)
	}
	return writeSegmentsText(w, res)
}

// writeSegmentsJSON emits the segmentation as indented JSON (the analyst-bundle
// feed: kuv.3/4/5 and 567 consume this as the deterministic scaffold).
func writeSegmentsJSON(w io.Writer, res segResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// writeSegmentsText emits the human-readable spine-style rendering.
func writeSegmentsText(w io.Writer, res segResult) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "segments session=%s project=%s", res.Session, res.Project)
	if res.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", res.Agent)
	}
	fmt.Fprintln(bw)
	fmt.Fprintln(bw,
		"≡ DETERMINISTIC boundary candidates (1 per user prompt) + pivot hints. "+
			"Semantic merge of interleaved sub-goals + per-task intent = analyst-side, NOT ferret.")

	for _, seg := range res.Segments {
		label := fmt.Sprintf("seg %d", seg.Index)
		if seg.Index == 0 {
			label = "preamble"
		}
		fmt.Fprintf(bw, "[%s] calls=%s", label, callRange(seg))
		if seg.Prompt != "" {
			fmt.Fprintf(bw, "  prompt: %s", truncateRunes(seg.Prompt, spineTextCap))
		}
		fmt.Fprintln(bw)
		for _, p := range seg.Pivots {
			fmt.Fprintf(bw, "  [pivot] think#%d  cue=%q\n", p.Think, p.Cue)
		}
	}

	fmt.Fprintf(bw, "--- segments=%d calls=%d pivots=%d", boundaryCount(res), res.TotalCalls, res.Pivots)
	if res.DecodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", res.DecodeErrs)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}

// callRange renders a segment's owned tool-call index span, or "none".
func callRange(seg segment) string {
	if seg.FirstCall == -1 {
		return "none"
	}
	if seg.FirstCall == seg.LastCall {
		return fmt.Sprintf("%d", seg.FirstCall)
	}
	return fmt.Sprintf("%d..%d", seg.FirstCall, seg.LastCall)
}

// boundaryCount counts real prompt-opened segments (excludes the synthetic
// preamble) — the number of deterministic boundary candidates.
func boundaryCount(res segResult) int {
	n := 0
	for _, seg := range res.Segments {
		if seg.Index > 0 {
			n++
		}
	}
	return n
}
