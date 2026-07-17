package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
)

// roleAssistant is the transcript line type for model turns (user turns and
// tool results arrive as "user").
const roleAssistant = "assistant"

var (
	errSearchQueryRequired = errors.New("search: query required")
	errSearchBadContext    = errors.New("search: --context must be >= 0")
	errSearchBadLimit      = errors.New("search: --limit must be >= 0")
)

// Display widths. A hit prints a window around the match, not the whole entry —
// a buried email in a megabyte tool_result must stay one readable line. Context
// entries print at a wider cap since they carry no match to center on.
const (
	searchSnipRadius = 120 // runes kept on each side of a match in the hit snippet
	searchCtxCap     = 200 // max runes stored/printed per context entry
)

// searchOpts carries the resolved, validated flags into runSearch so the command
// wiring and the scan logic stay decoupled (tests drive runSearch directly).
type searchOpts struct {
	format  string
	limit   int // max hits; 0 = unlimited
	context int // entries of context before/after each hit
}

// searchHit is one match: where it lives, what kind of turn-block held it, the
// windowed snippet, and the surrounding entries. Agent is empty for a root
// session transcript (only subagent files carry one).
type searchHit struct {
	Session string    `json:"session"`
	Project string    `json:"project"`
	Agent   string    `json:"agent,omitempty"`
	Line    int       `json:"line"`
	Kind    string    `json:"kind"`
	Snippet string    `json:"snippet"`
	Before  []ctxLine `json:"before"`
	After   []ctxLine `json:"after"`
}

// ctxLine is one context entry beside a hit: its kind tag and (truncated) text.
type ctxLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// searchResult is the JSON envelope. Total is every match found (scanning runs
// past the cap); Truncated says the shown Hits are fewer than Total — the same
// AX truncation contract the row-capped commands emit via keyTotal/keyTruncated.
type searchResult struct {
	Query     string      `json:"query"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated"`
	Hits      []searchHit `json:"hits"`
}

// cmdSearch validates the flags, resolves the default transcript root the same
// way ingest/spine do, and runs the scan.
func cmdSearch() error {
	cmd := &CLI.Search
	if strings.TrimSpace(cmd.Query) == "" {
		return errSearchQueryRequired
	}
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	if cmd.Context < 0 {
		return errSearchBadContext
	}
	if cmd.Limit < 0 {
		return errSearchBadLimit
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	return runSearch(os.Stdout, root, cmd.Query,
		searchOpts{format: cmd.Format, limit: cmd.Limit, context: cmd.Context})
}

// runSearch scans every transcript under root for the literal query, collecting
// up to opts.limit hits (0 = unlimited) while counting every match so the output
// can honestly signal how many were withheld. Matching is a case-sensitive
// substring test over each turn-block's plain-text rendering (prompt, assistant
// text, thinking, tool call, tool result) — the same block decomposition the
// spine uses, reusing its helpers. Decode-broken lines are tolerated and tallied;
// a transcript that fails to read is skipped with a stderr note (one bad file must
// not kill a corpus-wide search).
func runSearch(w io.Writer, root, query string, opts searchOpts) error {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return err
	}

	var hits []searchHit
	var total, decodeErrs int

	for i := range srcs {
		src := srcs[i]
		remaining := -1 // unlimited
		if opts.limit > 0 {
			remaining = opts.limit - len(hits)
		}
		entries, pending, derr, serr := scanTranscript(src, query, remaining, &total)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "ferret: search: %s: %v (skipped)\n", src.Path, serr)
			continue
		}
		decodeErrs += derr
		for _, idx := range pending {
			e := entries[idx]
			hits = append(hits, searchHit{
				Session: src.Session,
				Project: src.Project,
				Agent:   src.Agent,
				Line:    e.line,
				Kind:    e.kind,
				Snippet: e.snip,
				Before:  contextLines(entries, idx-opts.context, idx),
				After:   contextLines(entries, idx+1, idx+1+opts.context),
			})
		}
	}

	if opts.format == fmtJSON {
		return out.JSON(w, searchResult{
			Query:     query,
			Total:     total,
			Truncated: total > len(hits),
			Hits:      hits,
		})
	}
	return writeSearchText(w, root, query, opts, srcs, hits, total, decodeErrs)
}

// scannedEntry is one extracted turn-block, kept only long enough to resolve the
// context of hits in the same transcript. Its text is pre-capped: the full block
// text (which can be megabytes) exists only during the one line's match+snippet.
// snip holds the windowed match preview, set only for entries accepted as hits.
type scannedEntry struct {
	kind string
	text string
	line int
	snip string
}

// searchScan accumulates one transcript's entries and hits. remaining caps how
// many hits this transcript may accept (-1 = unlimited); total counts every
// match — accepted or not — so the caller can report withheld hits.
type searchScan struct {
	query     string
	remaining int
	total     *int

	entries []scannedEntry
	pending []int // indices into entries that were accepted as hits
	decErrs int
	lineNo  int
}

// feed decodes one transcript line and folds its content blocks into the scan.
// A decode-broken line is tallied and skipped (tolerant, per the raw-transcript
// reality); only user/assistant messages carry searchable content.
func (s *searchScan) feed(line []byte) {
	s.lineNo++
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		s.decErrs++
		return
	}
	if raw.IsMeta || raw.Message == nil {
		return
	}
	if raw.Type != roleAssistant && raw.Type != "user" {
		return
	}
	for b := range raw.Message.Content {
		s.block(raw.Type, &raw.Message.Content[b])
	}
}

// block renders one content block to an entry, records a hit when it matches,
// and stores the entry (text pre-capped) so later hits can draw context from it.
func (s *searchScan) block(lineType string, blk *transcript.Block) {
	kind, text, ok := blockEntry(lineType, blk)
	if !ok {
		return
	}
	if s.remaining == 0 {
		// Hit cap already spent before this transcript: no hit here can be
		// accepted, so no entry can ever serve as context — just keep the
		// honest total.
		if strings.Contains(text, s.query) {
			*s.total++
		}
		return
	}
	e := scannedEntry{kind: kind, text: truncateRunes(text, searchCtxCap), line: s.lineNo}
	if strings.Contains(text, s.query) {
		*s.total++
		if s.remaining < 0 || len(s.pending) < s.remaining {
			e.snip = snippet(text, s.query)
			s.pending = append(s.pending, len(s.entries))
		}
	}
	s.entries = append(s.entries, e)
}

// scanTranscript reads one transcript into a searchScan and returns its entries,
// accepted-hit indices, decode-error count, and any read error.
func scanTranscript(
	src transcript.Source, query string, remaining int, total *int,
) ([]scannedEntry, []int, int, error) {
	s := &searchScan{query: query, remaining: remaining, total: total}
	err := transcript.ReadLines(src.Path, func(line []byte) error {
		s.feed(line)
		return nil
	})
	return s.entries, s.pending, s.decErrs, err
}

// blockEntry renders one content block to a searchable, single-line plain-text
// entry, reusing the spine's decomposition: user/assistant text, thinking (with
// the same text-fallback the spine uses), a tool call as name + compact args, a
// tool result as its unwrapped scalar body. ok is false for empty or ignored
// blocks (images, unknown types).
func blockEntry(lineType string, blk *transcript.Block) (kind, text string, ok bool) {
	switch blk.Type {
	case "thinking":
		body := strings.TrimSpace(blk.Thinking)
		if body == "" {
			body = strings.TrimSpace(blk.Text)
		}
		if body == "" {
			return "", "", false
		}
		return "think", collapseWS(body), true
	case blockTypeText:
		body := strings.TrimSpace(blk.Text)
		if body == "" {
			return "", "", false
		}
		if lineType == roleAssistant {
			return "asst", collapseWS(body), true
		}
		return "user", collapseWS(body), true
	case "tool_use":
		body := blk.Name
		if len(blk.Input) > 0 {
			body += " " + compactJSON(blk.Input)
		}
		body = collapseWS(body)
		if body == "" {
			return "", "", false
		}
		return "call", body, true
	case "tool_result":
		body := scalarString(blk.Content)
		if body == "" {
			return "", "", false
		}
		return "rslt", body, true
	}
	return "", "", false
}

// snippet centers the first occurrence of query in text, keeping the whole match
// plus up to searchSnipRadius runes on each side, and marks a side with … only
// where text was actually cut. text is assumed to contain query (callers snippet
// only matches).
func snippet(text, query string) string {
	before, after, found := strings.Cut(text, query)
	if !found {
		return truncateRunes(text, 2*searchSnipRadius)
	}
	lead, before := trimHead(before)
	after, trail := trimTail(after)
	return lead + before + query + after + trail
}

// trimHead keeps the last searchSnipRadius runes of s, returning a … marker when
// it dropped any. It walks rune boundaries in place — s can be a multi-megabyte
// tool result, and a []rune copy of it would cost 4× its bytes per hit.
func trimHead(s string) (marker, kept string) {
	total := utf8.RuneCountInString(s)
	if total <= searchSnipRadius {
		return "", s
	}
	skip := total - searchSnipRadius
	for i := range s {
		if skip == 0 {
			return "…", s[i:]
		}
		skip--
	}
	return "", s
}

// trimTail keeps the first searchSnipRadius runes of s, returning a … marker when
// it dropped any. Same in-place rune walk as trimHead, for the same reason.
func trimTail(s string) (kept, marker string) {
	n := 0
	for i := range s {
		if n == searchSnipRadius {
			return s[:i], "…"
		}
		n++
	}
	return s, ""
}

// contextLines clamps [lo,hi) to the entry slice and maps the window to ctxLines.
// It returns a non-nil (possibly empty) slice so JSON emits [] rather than null.
func contextLines(entries []scannedEntry, lo, hi int) []ctxLine {
	if lo < 0 {
		lo = 0
	}
	if hi > len(entries) {
		hi = len(entries)
	}
	lines := make([]ctxLine, 0, hi-lo)
	for i := lo; i < hi; i++ {
		lines = append(lines, ctxLine{Kind: entries[i].kind, Text: entries[i].text})
	}
	return lines
}

// writeSearchText emits the human-readable report: a header carrying the query,
// scope, and TOTAL match count, then one block per shown hit (location, context
// before, the match snippet, context after), then a tail accounting for any hits
// withheld by the cap. The cap is hit-level (each hit is multi-line), so this
// writes directly rather than through the row-capped out.Sink.
func writeSearchText(
	w io.Writer, root, query string, opts searchOpts,
	srcs []transcript.Source, hits []searchHit, total, decodeErrs int,
) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "search q=%q root=%s sessions=%d hits=%d (limit=%d context=%d)",
		query, root, len(srcs), total, opts.limit, opts.context)
	if decodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", decodeErrs)
	}
	fmt.Fprintln(bw)

	for i := range hits {
		h := &hits[i]
		fmt.Fprintf(bw, "── %s project=%s", h.Session, h.Project)
		if h.Agent != "" {
			fmt.Fprintf(bw, " agent=%s", h.Agent)
		}
		fmt.Fprintf(bw, " line=%d [%s]\n", h.Line, h.Kind)
		for j := range h.Before {
			c := h.Before[j]
			fmt.Fprintf(bw, "  -%d [%s] %s\n", len(h.Before)-j, c.Kind, c.Text)
		}
		fmt.Fprintf(bw, "    %s\n", h.Snippet)
		for j := range h.After {
			c := h.After[j]
			fmt.Fprintf(bw, "  +%d [%s] %s\n", j+1, c.Kind, c.Text)
		}
	}

	if total > len(hits) {
		fmt.Fprintf(bw, "… +%d more hits (raise --limit)\n", total-len(hits))
	}
	return bw.Flush()
}
