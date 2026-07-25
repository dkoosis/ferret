package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/transcript"
)

var (
	errSpineSessionRequired = errors.New("spine: --session PREFIX required")
	errSpineNoMatch         = errors.New("spine: no transcript matches --session")
)

// Spine truncation widths. Prompts and thinking are the intent signal the spine
// exists to preserve, so they keep a generous per-block budget; tool args are
// squeezed to one readable line; tool-result bodies never print at all — only
// their status and measured size. That asymmetry is the whole point: a 1.2MB
// transcript is mostly result payloads (file dumps, command output), and
// collapsing those to "status + size" is what turns it into a ~21KB spine.
const (
	spineTextCap = 4000 // max runes per prompt / thinking / assistant-text block
	spineArgCap  = 200  // max runes per tool-call arg rendering

	blockTypeText = "text" // transcript content-block type for plain text
)

// cmdSpine wires the kong CLI flags to spine(), resolving the default
// transcript root the same way ingest does.
func cmdSpine() error {
	cmd := &CLI.Spine
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	// nil: the human `ferret spine` CLI path renders every value in full,
	// unchanged by ferret-5c0's prompt-bound placeholder mapping.
	return spine(os.Stdout, root, cmd.Session, nil)
}

// spineCounts tallies what the spine emitted — printed as a trailer so a reader
// can sanity-check coverage against the raw transcript.
type spineCounts struct {
	prompts, thinking, text, calls, results, decodeErrs int
}

// spine resolves session (a prefix) to one transcript under root and streams its
// compact spine to w: in transcript order — user prompts, assistant thinking and
// text, each tool call (name + key args, truncated), and each tool result
// (status + size). It reuses transcript.ReadLines/Raw for decoding and tolerates
// a malformed line rather than aborting (a truncated final line is common).
//
// ph carries the ferret-5c0 placeholder table for tool-call args: nil for the
// human `ferret spine` CLI path (renders every value in full, unchanged); a
// fresh *placeholderTable for an LLM-prompt-bound render (adjudicate.go), so
// this one render pass's repeated volatile values compress to tokens.
func spine(w io.Writer, root, session string, ph *placeholderTable) error {
	src, distinct, err := resolveSpineSource(root, session)
	if err != nil {
		return err
	}
	if distinct > 1 {
		fmt.Fprintf(os.Stderr,
			"ferret: --session %q matched %d sessions; emitting %q (use a longer prefix to disambiguate)\n",
			session, distinct, src.Session)
	}

	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "spine session=%s project=%s", src.Session, src.Project)
	if src.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", src.Agent)
	}
	fmt.Fprintln(bw)

	var c spineCounts
	if err := transcript.ReadLines(src.Path, func(line []byte) error {
		emitSpineLine(bw, line, &c, ph)
		return nil
	}); err != nil {
		return err
	}

	fmt.Fprintf(bw, "--- prompts=%d thinking=%d text=%d calls=%d results=%d",
		c.prompts, c.thinking, c.text, c.calls, c.results)
	if c.decodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", c.decodeErrs)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}

// emitSpineLine decodes one transcript line and writes its spine rows. Only
// assistant/user messages carry spine content; everything else (summaries,
// system, meta) is skipped. Decode failures bump a counter and are dropped — a
// best-effort line-level tolerance matching the raw-transcript reality.
func emitSpineLine(bw *bufio.Writer, line []byte, c *spineCounts, ph *placeholderTable) {
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		c.decodeErrs++
		return
	}
	if raw.IsMeta || raw.Message == nil {
		return
	}
	if raw.Type != "assistant" && raw.Type != "user" {
		return
	}
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
		switch blk.Type {
		case "thinking":
			emitThinking(bw, blk, c)
		case blockTypeText:
			emitText(bw, raw.Type, blk, c)
		case "tool_use":
			emitToolUse(bw, blk, c, ph)
		case "tool_result":
			emitToolResult(bw, blk, c)
		}
	}
}

// emitThinking writes an assistant thinking block. CC keys the body under
// "thinking"; older/variant schemas put it in "text", so fall back to that.
func emitThinking(bw *bufio.Writer, blk *transcript.Block, c *spineCounts) {
	body := strings.TrimSpace(blk.Thinking)
	if body == "" {
		body = strings.TrimSpace(blk.Text)
	}
	if body == "" {
		return
	}
	c.thinking++
	fmt.Fprintf(bw, "[think] %s\n", truncateRunes(body, spineTextCap))
}

// emitText writes a text block: an assistant message ([asst]) or, on a user
// line, a genuine prompt ([user]).
func emitText(bw *bufio.Writer, lineType string, blk *transcript.Block, c *spineCounts) {
	body := strings.TrimSpace(blk.Text)
	if body == "" {
		return
	}
	if lineType == "assistant" {
		c.text++
		fmt.Fprintf(bw, "[asst] %s\n", truncateRunes(body, spineTextCap))
		return
	}
	c.prompts++
	fmt.Fprintf(bw, "[user] %s\n", truncateRunes(body, spineTextCap))
}

// emitToolUse writes a tool call: name + the most salient argument, truncated.
func emitToolUse(bw *bufio.Writer, blk *transcript.Block, c *spineCounts, ph *placeholderTable) {
	c.calls++
	if args := renderArgs(blk.Input, ph); args != "" {
		fmt.Fprintf(bw, "[call] %s  %s\n", blk.Name, args)
		return
	}
	fmt.Fprintf(bw, "[call] %s\n", blk.Name)
}

// emitToolResult writes a tool result collapsed to status + measured payload
// size — the body itself never prints (that collapse is what keeps the spine
// dense).
func emitToolResult(bw *bufio.Writer, blk *transcript.Block, c *spineCounts) {
	c.results++
	status := "ok"
	if blk.IsError != nil && *blk.IsError {
		status = "FAIL"
	}
	fmt.Fprintf(bw, "[rslt] %s  %s\n", status, humanBytes(len(blk.Content)))
}

// resolveSpineSource matches session (a prefix) against the transcript tree under
// root and picks the single best source to emit: an exact session match wins,
// then the root-session file (no agent) over a subagent file, then a
// deterministic id/agent tiebreak. It returns the chosen source plus the count
// of DISTINCT sessions that matched — so the caller can warn on an ambiguous
// prefix (a session and its own subagent count as one session, not two).
func resolveSpineSource(root, session string) (transcript.Source, int, error) {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return transcript.Source{}, 0, err
	}
	var matches []transcript.Source
	distinct := map[string]struct{}{}
	for _, s := range srcs {
		if strings.HasPrefix(s.Session, session) || strings.Contains(s.Session, session) {
			matches = append(matches, s)
			distinct[s.Session] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return transcript.Source{}, 0, fmt.Errorf("%w: %q", errSpineNoMatch, session)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if ei, ej := matches[i].Session == session, matches[j].Session == session; ei != ej {
			return ei // exact session match first
		}
		if ai, aj := matches[i].Agent == "", matches[j].Agent == ""; ai != aj {
			return ai // root session before subagent transcripts
		}
		if matches[i].Session != matches[j].Session {
			return matches[i].Session < matches[j].Session
		}
		return matches[i].Agent < matches[j].Agent
	})
	return matches[0], len(distinct), nil
}

// renderArgs renders a tool_use input as one compact line: the most salient
// argument as key=value (the bash command, the file path, the search pattern…),
// falling back to the whole object compacted. Truncated to spineArgCap so a giant
// Write payload or pasted blob can't blow the spine's density budget.
//
// ph is the per-render placeholder table (ferret-5c0): nil (the human `ferret
// spine` CLI path) renders every occurrence in full, exactly as before this
// change. Non-nil (the LLM-prompt-bound render path — adjudicate/hop1/
// over-initiative) renders the first distinct value in full and every repeat
// within the same render pass as that value's stable short token, so a
// volatile constant (a repo's absolute path, a scratch dir) that recurs across
// many tool calls pays its full string cost once instead of on every call.
func renderArgs(input json.RawMessage, ph *placeholderTable) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil {
		return truncateRunes(compactJSON(input), spineArgCap) // not an object — render raw
	}
	for _, k := range []string{
		"command", "file_path", "pattern", "query", "path", "url",
		"prompt", "description", "old_string", "content",
	} {
		if v, ok := m[k]; ok {
			val := scalarString(v)
			if ph != nil {
				if tok, seen := ph.lookup(val); seen {
					return k + "=" + tok
				}
				ph.register(val)
			}
			return truncateRunes(k+"="+val, spineArgCap)
		}
	}
	return truncateRunes(compactJSON(input), spineArgCap)
}

// placeholderTable interns volatile tool-arg values seen while rendering ONE
// spine into a prompt-bound token stream (ferret-5c0, following PostHog's
// _simplify_window_id/_simplify_url pattern). The first render of a distinct
// value pays its full string cost — that's what establishes, in context, what
// the token stands for; every later repeat within the same render pass costs
// only a few bytes ("[P3]") instead of the value again. reverse lets a caller
// restore a token-bearing string (e.g. an LLM finding that echoes a token back
// in its own words) to the real value before it reaches a human — dk must
// never see a raw placeholder token, only the LLM prompt should.
type placeholderTable struct {
	tokens  map[string]string // value -> token, minted on first sight
	reverse map[string]string // token -> value
}

// newPlaceholderTable returns an empty table, ready for one render pass.
func newPlaceholderTable() *placeholderTable {
	return &placeholderTable{tokens: map[string]string{}, reverse: map[string]string{}}
}

// lookup reports the stable token already minted for val, if any.
func (t *placeholderTable) lookup(val string) (string, bool) {
	tok, ok := t.tokens[val]
	return tok, ok
}

// register mints and returns a new stable token for val (first sight only —
// callers check lookup first so a value never gets minted twice).
func (t *placeholderTable) register(val string) string {
	tok := fmt.Sprintf("[P%d]", len(t.tokens)+1)
	t.tokens[val] = tok
	t.reverse[tok] = val
	return tok
}

// expand restores every placeholder token appearing in s to its real value —
// the human-facing inverse of register. Safe on a nil table (renders s
// unchanged) so callers on the human/non-mapped path never need a nil check.
func (t *placeholderTable) expand(s string) string {
	if t == nil || len(t.reverse) == 0 {
		return s
	}
	for tok, val := range t.reverse {
		s = strings.ReplaceAll(s, tok, val)
	}
	return s
}

// scalarString unwraps a JSON string to its raw text (so command="go test" reads
// cleanly, not command="\"go test\""); non-string values render compacted. All
// internal whitespace collapses to single spaces — a tool arg is a one-liner.
func scalarString(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return collapseWS(s)
	}
	return collapseWS(compactJSON(v))
}

// compactJSON strips insignificant whitespace from a JSON value; on any error it
// returns the input verbatim (best-effort — the spine is a readable artifact, not
// a re-serialization contract).
func compactJSON(b json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return collapseWS(string(b))
	}
	return buf.String()
}

// collapseWS folds any run of whitespace (including newlines) into single spaces
// so a value stays on one spine line.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes caps s at n runes, appending a "…(+K)" marker noting how many
// runes were dropped. Rune-safe so a cut never splits a multibyte character.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + fmt.Sprintf("…(+%d)", len(runes)-n)
}

// humanBytes renders a byte count glance-readably: B under 1KiB, then KB/MB.
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
