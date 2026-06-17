package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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
//   - HARD BOUNDARY: every new user prompt opens a candidate segment — EXCEPT a
//     non-boundary turn (see classifyBoundary), which continues the current one.
//     A user turn is the strongest LLM-free evidence of a (possible) goal change,
//     but a bare "yes", a control built-in (/clear, /exit), or a system envelope
//     is not a new goal — folding these in keeps their following calls attributed
//     to the live task instead of spawning a false boundary. Each segment owns the
//     tool-call indices that follow its prompt, in encounter order, until the next
//     real boundary.
//   - PIVOT HINT: a lightweight string heuristic over thinking-block text that
//     flags reasoning which READS like a goal shift ("let me now…", "actually,…").
//     These are hints, not boundaries: the analyst decides whether a pivot inside a
//     segment is a genuine sub-task. No semantics, just leading-text cue matching.
//
// Everything here is a pure function of the transcript bytes: no time, no maps in
// output order, no randomness — repeated runs on the same input are byte-stable.

// segContCap bounds the continuation label rendered/stored per folded turn.
const segContCap = 48

// segPivot is one thinking-pivot hint: the global thinking-block index it sits at
// and the cue phrase that matched (the lowercased prefix-cue, for traceability).
type segPivot struct {
	Think int    `json:"think"`
	Cue   string `json:"cue"`
}

// segCont is one non-boundary turn folded into the current segment instead of
// opening a new candidate: a short affirmation, a control built-in, or a system
// envelope. Kind is the class ("affirmation"|"control"|"carrier"); Text is a
// compact label for analyst traceability.
type segCont struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
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
	InBytes   int        `json:"inBytes,omitempty"`  // tool_use input bytes the task spent
	OutBytes  int        `json:"outBytes,omitempty"` // tool_result output bytes the task's calls pulled in — the de-context payoff
	Pivots    []segPivot `json:"pivots,omitempty"`
	Conts     []segCont  `json:"conts,omitempty"`
}

// segResult is the whole emission for one session.
type segResult struct {
	Session    string    `json:"session"`
	Project    string    `json:"project"`
	Agent      string    `json:"agent,omitempty"`
	Segments   []segment `json:"segments"`
	TotalCalls int       `json:"totalCalls"`
	TotalIn    int       `json:"totalIn,omitempty"`   // sum of per-task input bytes
	TotalOut   int       `json:"totalOut,omitempty"`  // sum of per-task output bytes
	OutOrphan  int       `json:"outOrphan,omitempty"` // result bytes whose tool_use_id matched no owned call (deduped/forked) — counted, not attributed
	Pivots     int       `json:"pivots"`
	Conts      int       `json:"conts,omitempty"`
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

// affirmations are whole-prompt acknowledgements that CONTINUE the current task
// rather than open a new one ("short affirmations don't change topic" — prior art:
// claude-code-log-analyzer arc_analyzer.py Phase 2). Matched by exact equality on
// the normalized prompt, so "yes, but also do X" stays a boundary — only a bare
// "yes" folds in.
var affirmations = map[string]bool{
	"yes": true, "y": true, "yeah": true, "yep": true, "yup": true,
	"ok": true, "okay": true, "k": true, "sure": true, "yes please": true,
	"proceed": true, "continue": true, "go": true, "go ahead": true,
	"do it": true, "go for it": true, "sounds good": true, "please do": true,
	"please": true, "lgtm": true, "looks good": true, "approved": true,
	"ship it": true,
}

// controlCommands are no-op session-control built-ins (no plugin namespace) that
// carry no task work, so they CONTINUE the current segment. Namespaced work
// commands (/wrap:wrap, /lintbrush:clean) are NOT here and open a real task —
// the hand-run reads /wrap and /lintbrush:clean as distinct tasks F and G.
var controlCommands = map[string]bool{
	"clear": true, "exit": true, "quit": true, "compact": true,
	"resume": true, "help": true, "model": true, "login": true,
	"logout": true, "cost": true, "status": true, "doctor": true,
	"config": true, "vim": true, "terminal-setup": true,
}

// classifyBoundary reports whether a user prompt is a NON-boundary that continues
// the current segment, with its class and a compact label. Three deterministic
// classes: "carrier" (a system/local-command envelope, not user intent), "control"
// (a no-op built-in slash-command), "affirmation" (a short closed-set ack). Any
// other prompt — including namespaced work commands — opens a boundary.
func classifyBoundary(prompt string) (skip bool, kind, label string) {
	trimmed := strings.TrimSpace(prompt)
	low := strings.ToLower(trimmed)

	if strings.HasPrefix(low, "<local-command-") || strings.HasPrefix(low, "<task-notification") {
		return true, "carrier", carrierLabel(low)
	}
	if name, ok := commandName(trimmed); ok {
		if !strings.Contains(name, ":") && controlCommands[name] {
			return true, "control", "/" + name
		}
		return false, "", ""
	}
	if affirmations[strings.TrimRight(low, " .!,?…")] {
		return true, "affirmation", trimmed
	}
	return false, "", ""
}

// commandName extracts a slash-command's name from a <command-name>…</command-name>
// envelope, lowercased with the leading slash stripped (e.g. "/wrap:wrap" -> the
// namespaced "wrap:wrap"; "/clear" -> "clear"). ok is false when no such tag.
func commandName(prompt string) (name string, ok bool) {
	const open, closeTag = "<command-name>", "</command-name>"
	_, after, found := strings.Cut(prompt, open)
	if !found {
		return "", false
	}
	inner, _, found := strings.Cut(after, closeTag)
	if !found {
		return "", false
	}
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(inner), "/"))
	return name, name != ""
}

// carrierLabel returns the leading tag name of a system envelope (the lowercased
// prompt), e.g. "<local-command-stdout>…" -> "local-command-stdout".
func carrierLabel(low string) string {
	if i := strings.IndexByte(low, '>'); i > 1 {
		return strings.Trim(low[:i+1], "<>")
	}
	return "carrier"
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
	conts      int            // non-boundary turns folded into a segment (filtered)
	callOwner  map[string]int // tool_use id → owning segment index, for output-byte attribution
	outOrphan  int            // result bytes for an unowned tool_use id (deduped/forked)
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
			if skip, kind, label := classifyBoundary(prompt); skip {
				s.addContinuation(kind, label)
				return
			}
			s.openSegment(prompt)
			return
		}
		// A user line with no genuine text is a tool_result carrier: it opens no
		// boundary, but its result payloads are the output cost — attribute each
		// to the task that issued the matching call.
		s.scanResults(raw.Message.Content)
		return
	}

	// assistant line: count tool calls and scan thinking for pivot hints.
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
		switch blk.Type {
		case "tool_use":
			s.addCall(blk.ID, len(blk.Input))
		case "thinking":
			s.scanThinking(blk)
		}
	}
}

// scanResults attributes each tool_result's payload size to the segment that owns
// the matching tool_use (by id), so output cost lands on the task that pulled it in
// even when the result arrives after a later boundary. A result for an unowned id
// (a deduped/forked call) is counted as orphan output, not attributed.
func (s *segmenter) scanResults(blocks transcript.Blocks) {
	for i := range blocks {
		blk := &blocks[i]
		if blk.Type != "tool_result" {
			continue
		}
		n := len(blk.Content)
		if idx, ok := s.callOwner[blk.ToolUseID]; ok {
			s.segs[idx].OutBytes += n
			continue
		}
		s.outOrphan += n
	}
}

// promptText extracts the genuine user-prompt text from a content block list:
// the concatenation of its text blocks, whitespace-collapsed. A list with no text
// block (e.g. a pure tool_result carrier) returns "" — not a boundary.
func promptText(blocks transcript.Blocks) string {
	var parts []string
	for i := range blocks {
		if blocks[i].Type != blockTypeText {
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

// addContinuation folds a non-boundary turn into the currently-open segment. A
// continuation before any real segment carries no task meaning and is dropped
// (still counted, for an honest filtered tally). Calls that follow it attribute to
// the same segment, since no boundary was opened.
func (s *segmenter) addContinuation(kind, label string) {
	s.conts++
	if !s.started || s.preambleOnly() {
		return
	}
	cur := &s.segs[len(s.segs)-1]
	cur.Conts = append(cur.Conts, segCont{Kind: kind, Text: truncateRunes(collapseWS(label), segContCap)})
}

// addCall assigns the next tool-call index to the open segment, charges its input
// bytes to that task, and records the call's id so its result can be attributed
// back here. Calls that precede the first prompt open a synthetic preamble segment
// (empty prompt) so no call is silently dropped — a transcript can begin with
// sidechain/tool activity.
func (s *segmenter) addCall(id string, inBytes int) {
	if !s.started {
		s.segs = append(s.segs, segment{Index: 0, Prompt: "", FirstCall: -1, LastCall: -1})
		s.started = true
		s.preamble = true
	}
	owner := len(s.segs) - 1
	cur := &s.segs[owner]
	if cur.FirstCall == -1 {
		cur.FirstCall = s.callIndex
	}
	cur.LastCall = s.callIndex
	cur.InBytes += inBytes
	if id != "" {
		if s.callOwner == nil {
			s.callOwner = map[string]int{}
		}
		s.callOwner[id] = owner
	}
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

// result finalizes the accumulated segmentation into a renderable value, summing
// the per-task input/output byte totals.
func (s *segmenter) result(src transcript.Source) segResult {
	var totalIn, totalOut int
	for i := range s.segs {
		totalIn += s.segs[i].InBytes
		totalOut += s.segs[i].OutBytes
	}
	return segResult{
		Session:    src.Session,
		Project:    src.Project,
		Agent:      src.Agent,
		Segments:   s.segs,
		TotalCalls: s.callIndex,
		TotalIn:    totalIn,
		TotalOut:   totalOut,
		OutOrphan:  s.outOrphan,
		Pivots:     s.pivots,
		Conts:      s.conts,
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
		"≡ DETERMINISTIC boundary candidates (1 per user prompt; affirmations/control "+
			"built-ins/system envelopes fold in as [cont]) + pivot hints. Semantic merge "+
			"of interleaved sub-goals + per-task intent = analyst-side, NOT ferret.")

	for _, seg := range res.Segments {
		label := fmt.Sprintf("seg %d", seg.Index)
		if seg.Index == 0 {
			label = "preamble"
		}
		fmt.Fprintf(bw, "[%s] calls=%s", label, callRange(seg))
		if seg.InBytes+seg.OutBytes > 0 {
			fmt.Fprintf(bw, " cost=%s out=%s", humanBytes(seg.InBytes+seg.OutBytes), humanBytes(seg.OutBytes))
		}
		if seg.Prompt != "" {
			fmt.Fprintf(bw, "  prompt: %s", truncateRunes(seg.Prompt, spineTextCap))
		}
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

// callRange renders a segment's owned tool-call index span, or "none".
func callRange(seg segment) string {
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
func boundaryCount(res segResult) int {
	n := 0
	for _, seg := range res.Segments {
		if seg.Index > 0 {
			n++
		}
	}
	return n
}
