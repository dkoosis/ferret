package score

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/shellnorm"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
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

// Pivot is one thinking-pivot hint: the global thinking-block index it sits at
// and the cue phrase that matched (the lowercased prefix-cue, for traceability).
type Pivot struct {
	Think int    `json:"think"`
	Cue   string `json:"cue"`
}

// Cont is one non-boundary turn folded into the current segment instead of
// opening a new candidate: a short affirmation, a control built-in, or a system
// envelope. Kind is the class ("affirmation"|"control"|"carrier"); Text is a
// compact label for analyst traceability.
type Cont struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Segment is one deterministic boundary candidate. FirstCall/LastCall are
// inclusive 0-based tool-call indices (encounter order) the segment owns, or -1
// when the segment owns no calls. Prompt is the verbatim user text that opened it
// (empty only for the synthetic preamble that holds calls preceding any prompt).
type Segment struct {
	Index     int    `json:"index"`
	Prompt    string `json:"prompt"`
	FirstCall int    `json:"firstCall"`
	LastCall  int    `json:"lastCall"`
	// FirstTS/LastTS are the transcript timestamps (RFC3339Nano) of this
	// segment's first and last owned call — its wall-clock span. Empty when the
	// segment owns no calls. The bridge the retrieval-outcome contract's ts→
	// segment join rides: a search event (trixi's sidecar clock) lands in the
	// segment whose [FirstTS,LastTS] interval contains its ts (ferret-bbp.16).
	FirstTS  string   `json:"firstTS,omitempty"`
	LastTS   string   `json:"lastTS,omitempty"`
	InBytes  int      `json:"inBytes,omitempty"`  // tool_use input bytes the task spent
	OutBytes int      `json:"outBytes,omitempty"` // tool_result output bytes the task's calls pulled in — the de-context payoff
	Shape    []string `json:"shape,omitempty"`    // ordered tool-shape tokens of the calls this task owns — the cross-session recurrence key (kuv.12)
	Pivots   []Pivot  `json:"pivots,omitempty"`
	Conts    []Cont   `json:"conts,omitempty"`
	// Outcome is a WEAK, deterministic positive-outcome label for this task — set
	// only when the task owns a terminal VCS action (commit/push/PR). nil = no
	// signal (NOT a negative label; absence is silence). See outcome.go. (kuv.8)
	Outcome *Outcome `json:"outcome,omitempty"`
	// Axes is the per-task reference-free quality block (efficiency/adaptivity),
	// set by ScoreAxes in place. nil until scored (mirrors *Outcome). See
	// quality.go. (kuv.5)
	Axes *Axes `json:"axes,omitempty"`
}

// Result is the whole segmentation emission for one session — the shared per-task
// unit every scorer in this package rides.
type Result struct {
	Session    string    `json:"session"`
	Project    string    `json:"project"`
	Agent      string    `json:"agent,omitempty"`
	Segments   []Segment `json:"segments"`
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

// segmenter accumulates the deterministic segmentation as it streams a transcript
// in order. It is the single source of truth for both text and JSON renderings.
type segmenter struct {
	segs       []Segment
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
		if prompt := turn.PromptText(raw.Message.Content); prompt != "" {
			if skip, kind, label := turn.ClassifyBoundary(prompt); skip {
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
			s.addCall(blk.ID, len(blk.Input), CallShapeTokens(blk), raw.Timestamp)
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

// CallShapeTokens derives a task-shape token sequence for one tool_use block —
// the deterministic recurrence key the corpus half (kuv.12) clusters on. It
// mirrors the event builder/tool-lens vocabulary so a shape reads in the same
// terms as the rest of ferret: a built-in tool is its name (Read, Edit), an MCP
// call compresses to mcp:server.tool, and a Bash call expands to one "sh:<verb>"
// token per shell statement (so a compound `git add && git commit` contributes
// both verbs). A Bash with no parseable command degrades to a single "sh".
func CallShapeTokens(blk *transcript.Block) []string {
	name := blk.Name
	if name == "" {
		return nil
	}
	if name != "Bash" {
		if strings.HasPrefix(name, "mcp__") {
			return []string{shapeMCP(name)}
		}
		return []string{name}
	}
	var input struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(blk.Input, &input)
	segs, _ := shellnorm.Split(input.Command)
	if len(segs) == 0 {
		return []string{"sh"}
	}
	toks := make([]string, 0, len(segs))
	for _, seg := range segs {
		toks = append(toks, "sh:"+seg.Cmd)
	}
	return toks
}

// shapeMCP compresses an mcp__server__tool action name to mcp:server.tool — the
// same short form the tool lens uses, kept local so the score package does not
// reach into internal/lens internals.
func shapeMCP(name string) string {
	parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
	if len(parts) == 2 {
		return "mcp:" + parts[0] + "." + parts[1]
	}
	return "mcp:" + parts[0]
}

// openSegment starts a new boundary candidate at the current call index.
func (s *segmenter) openSegment(prompt string) {
	s.started = true
	s.boundaries++
	s.segs = append(s.segs, Segment{
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
	cur.Conts = append(cur.Conts, Cont{Kind: kind, Text: truncateRunes(collapseWS(label), segContCap)})
}

// addCall assigns the next tool-call index to the open segment, charges its input
// bytes to that task, and records the call's id so its result can be attributed
// back here. Calls that precede the first prompt open a synthetic preamble segment
// (empty prompt) so no call is silently dropped — a transcript can begin with
// sidechain/tool activity.
func (s *segmenter) addCall(id string, inBytes int, shape []string, ts string) {
	if !s.started {
		s.segs = append(s.segs, Segment{Index: 0, Prompt: "", FirstCall: -1, LastCall: -1})
		s.started = true
		s.preamble = true
	}
	owner := len(s.segs) - 1
	cur := &s.segs[owner]
	if cur.FirstCall == -1 {
		cur.FirstCall = s.callIndex
		cur.FirstTS = ts
	}
	cur.LastCall = s.callIndex
	cur.LastTS = ts
	cur.InBytes += inBytes
	cur.Shape = append(cur.Shape, shape...)
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
			cur.Pivots = append(cur.Pivots, Pivot{Think: idx, Cue: cue})
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
func (s *segmenter) result(src transcript.Source) Result {
	var totalIn, totalOut int
	for i := range s.segs {
		totalIn += s.segs[i].InBytes
		totalOut += s.segs[i].OutBytes
	}
	return Result{
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

// SegmentSource streams one resolved transcript through the deterministic
// segmenter. It is the per-source seam the corpus half (kuv.12) iterates over,
// so a single session and a whole-corpus walk segment each transcript by exactly
// the same rules. cmd/ resolves a session prefix to a transcript.Source and calls
// this; leaf scorers consume the returned Result.
func SegmentSource(src transcript.Source) (Result, error) {
	var sm segmenter
	if err := transcript.ReadLines(src.Path, func(line []byte) error {
		sm.feed(line)
		return nil
	}); err != nil {
		return Result{}, err
	}
	res := sm.result(src)
	// Annotate each task with the weak terminal-action outcome label so it rides
	// the per-task scaffold every consumer reads (segments output, conformance,
	// quality axes). A pure post-pass over Segment.Shape — kept out of the
	// segmenter so the boundary logic stays single-purpose (kuv.8).
	LabelOutcomes(&res)
	return res, nil
}

// collapseWS folds any run of whitespace (including newlines) into single spaces
// so a value stays on one line. Mirrors cmd's spine helper of the same name —
// kept local so the score engine carries no dependency on the cmd render layer.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes caps s at n runes, appending a "…(+K)" marker noting how many
// runes were dropped. Rune-safe so a cut never splits a multibyte character.
// Mirrors cmd's spine helper of the same name (see collapseWS).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + fmt.Sprintf("…(+%d)", len(runes)-n)
}
