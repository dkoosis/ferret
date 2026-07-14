// Package reach mechanizes the memory-recall autopsy: it mines Claude Code
// transcripts for recall-opportunity moments (dk asking what's already
// known/decided/built, plus re-orientation asides), then classifies what the
// agent reached for FIRST in response — the trixi store, grep/gh forensics, or
// nothing. The headline is reach-rate: store-first reaches over opportunities,
// the keystone metric for epic tx-qw86 ("Memory where the action is").
//
// Phase 1 is transcript-only (this package): the hand autopsy proved that
// opportunity + reach mining needs no telemetry. The Phase-2 RU column (was the
// retrieved result actually used?) will join trixi's retrieval_events telemetry
// (tx-kji6) onto each store-reached Opportunity, keyed on (session, ts-window).
package reach

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/score"
	"github.com/dkoosis/ferret/internal/shellnorm"
	"github.com/dkoosis/ferret/internal/transcript"
)

// Class is the opportunity flavor: a recall question (dk asking what's already
// known/decided) or a re-orientation aside ("where are we / I forget"), the
// tx-vtea auxiliary metric. Both are opportunities; they roll up separately so a
// weak-signal aside doesn't dilute the headline recall-question rate.
type Class string

const (
	ClassRecall   Class = "recall"   // "do you remember / did we / where do we stand"
	ClassReorient Class = "reorient" // "where are we / I forget" (tx-vtea auxiliary)
)

// Reach is what the agent reached for FIRST after an opportunity — the channel
// that answered (or would have answered) the recall.
type Reach string

const (
	ReachStore Reach = "store" // trixi nug store: MCP get_nug, CLI trixi search/get — the reach we want
	ReachBeads Reach = "beads" // bd read (show/query/list/ready): adjacent structured recall
	ReachGrep  Reach = "grep"  // rg/grep/fd/find/Read/Glob/snipe: filesystem archaeology
	ReachGh    Reach = "gh"    // gh/git-log/git-grep/web: external forensics
	ReachNone  Reach = "none"  // no retrieval before the next turn — answered from context (or missed)
)

// isRetrieval reports whether a Reach is a retrieval action (i.e. resolves an
// open opportunity). ReachNone is the sentinel for "nothing fired".
func (r Reach) isRetrieval() bool { return r != ReachNone && r != "" }

// Opportunity is one recall-shaped user turn plus the channel that answered it.
type Opportunity struct {
	Session   string    `json:"session"`
	Project   string    `json:"project"`
	Timestamp time.Time `json:"ts"`
	Class     Class     `json:"class"`
	Cue       string    `json:"cue"`   // the trigger phrase that matched
	Text      string    `json:"text"`  // the user turn, truncated
	Reach     Reach     `json:"reach"` // what fired first
	FiredTool string    `json:"firedTool,omitempty"`
}

// Reached reports whether the store answered this opportunity (the reach-rate
// numerator). Store-first is the target behavior; every other channel is a miss.
func (o Opportunity) Reached() bool { return o.Reach == ReachStore }

const textCap = 140

// --- opportunity detection (recall.md triggers, verbatim) -------------------

// cuePattern binds a compiled regex to the human-legible cue it detects.
type cuePattern struct {
	cue string
	re  *regexp.Regexp
}

// recallPatterns are the recall.md trigger phrasings — dk asking what's already
// known/decided/built. This is the operationalization of the always-loaded
// recall rule; the detector deliberately mirrors it rather than inventing a set.
// Ordered most-specific-first: the first match supplies the display cue, so the
// broad "did we" is last and only labels turns no sharper phrasing caught.
var recallPatterns = compilePatterns([][2]string{
	{"do you remember", `\bdo(?:es)? (?:you|we) (?:remember|recall)\b`},
	{"what did we decide", `\bwhat did we (?:decide|do|say|choose|agree|call|name)\b`},
	{"where do we stand", `\bwhere (?:do|did|does) (?:we|things|it|this|that) stand\b`},
	{"already have", `\bwe already (?:have|did|built|decided|discussed)\b`},
	{"i thought we", `\bi thought (?:we|you|i)\b`},
	{"haven't we", `\b(?:haven['’]?t|weren['’]?t) we (?:already|do|have|decide)\b`},
	{"don't we already", `\bdon['’]?t we\b`},
	{"did we", `\bdid(?:n['’]?t)? we\b`},
})

// reorientPatterns are the tx-vtea auxiliary re-orientation asides — dk losing
// the thread, leaning on Claude as working memory. Weaker recall signal than a
// direct question, tallied separately.
var reorientPatterns = compilePatterns([][2]string{
	{"where are we", `\bwhere are we\b`},
	{"i forget", `\bi (?:forget|forgot)\b`},
	{"remind me", `\bremind me\b`},
	{"what were we", `\bwhat were we\b`},
	{"lost track", `\b(?:lost track|losing track|lost the thread)\b`},
	{"confused where", `\bconfused (?:about |by )?where (?:we|things|i)\b`},
})

func compilePatterns(specs [][2]string) []cuePattern {
	out := make([]cuePattern, 0, len(specs))
	for _, s := range specs {
		out = append(out, cuePattern{cue: s[0], re: regexp.MustCompile("(?i)" + s[1])})
	}
	return out
}

// Classify reports whether a user prompt is a recall opportunity, returning the
// matched cue and its class. Recall questions win over re-orientation when both
// match (the stronger signal). ok is false when no trigger fires.
func Classify(prompt string) (cue string, class Class, ok bool) {
	for _, p := range recallPatterns {
		if p.re.MatchString(prompt) {
			return p.cue, ClassRecall, true
		}
	}
	for _, p := range reorientPatterns {
		if p.re.MatchString(prompt) {
			return p.cue, ClassReorient, true
		}
	}
	return "", "", false
}

// --- reach classification ---------------------------------------------------

// bd read subcommands — the ones that consult prior state. Writes (update/close/
// create/delete/reopen) are not a reach and fall through to skip.
var bdReadSub = map[string]bool{
	"bd_show": true, "bd_query": true, "bd_list": true, "bd_ready": true,
	"bd_search": true, "bd_blocked": true, "bd_stats": true, "bd_prime": true,
	"bd_comments": true, "bd_dep": true, "bd_epic": true, "bd_memories": true,
}

// grepCmds are filesystem/code-search reaches (normalized base command).
var grepCmds = map[string]bool{
	"rg": true, "grep": true, "egrep": true, "fgrep": true, "ripgrep": true,
	"fd": true, "find": true, "cat": true, "ls": true, "bat": true, "eza": true,
	"head": true, "tail": true, "ag": true, "ferret": true, "tree": true, "dtree": true,
}

// classifyReachBlock maps one assistant tool_use block to a Reach. Non-retrieval
// tools (Edit, Write, set_nug, Task, TodoWrite …) return ReachNone so they don't
// resolve an open opportunity — only a genuine retrieval action does.
func classifyReachBlock(blk *transcript.Block) (Reach, string) {
	if blk.Type != "tool_use" || blk.Name == "" {
		return ReachNone, ""
	}
	name := blk.Name
	switch {
	case name == "mcp__trixi__get_nug":
		return ReachStore, "get_nug"
	case strings.HasPrefix(name, "mcp__trixi__"):
		// set_nug/query_metrics/signal/del_nug: writes or non-recall — not a reach.
		return ReachNone, ""
	case name == "Grep" || name == "Glob" || name == "Read" || name == "NotebookRead":
		return ReachGrep, name
	case name == "WebSearch" || name == "WebFetch":
		return ReachGh, name
	case name == "Bash":
		return classifyBash(blk)
	default:
		return ReachNone, ""
	}
}

// classifyBash inspects a Bash tool_use, splitting the compound command and
// returning the FIRST statement that is a retrieval reach. A build/test/edit
// shell (make, go, git commit) is not a reach → ReachNone.
func classifyBash(blk *transcript.Block) (Reach, string) {
	var input struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(blk.Input, &input)
	segs, _ := shellnorm.Split(input.Command)
	for _, seg := range segs {
		if r, tool := classifyShellCmd(seg.Cmd); r.isRetrieval() {
			return r, tool
		}
	}
	return ReachNone, ""
}

// classifyShellCmd maps a shellnorm-normalized command (base or base_subcommand,
// e.g. "trixi_search", "bd_show", "git_log") to a Reach.
func classifyShellCmd(cmd string) (Reach, string) {
	switch {
	case cmd == "trixi_search" || cmd == "trixi_get":
		return ReachStore, cmd
	case bdReadSub[cmd]:
		return ReachBeads, cmd
	case grepCmds[cmd] || cmd == "snipe" || strings.HasPrefix(cmd, "snipe_"):
		return ReachGrep, cmd
	case strings.HasPrefix(cmd, "gh_"):
		return ReachGh, cmd
	case cmd == "git_log" || cmd == "git_grep" || cmd == "git_show" || cmd == "git_blame":
		return ReachGh, cmd
	default:
		return ReachNone, ""
	}
}

// --- session scan -----------------------------------------------------------

// Window bounds opportunity detection to [Since, Until] (inclusive dates).
type Window struct {
	Since time.Time
	Until time.Time // end-of-day is applied by contains
}

func (w Window) contains(ts time.Time) bool {
	if ts.IsZero() {
		return false
	}
	if !w.Since.IsZero() && ts.Before(w.Since) {
		return false
	}
	if !w.Until.IsZero() && ts.After(w.Until) {
		return false
	}
	return true
}

// scanner resolves opportunities against the first following retrieval action as
// it streams one transcript in order. A genuine new user turn closes any open
// opportunity (unresolved → ReachNone); carrier/control/affirmation turns and
// tool_result carriers do not close the arc.
type scanner struct {
	project, session string
	win              Window
	open             bool
	cur              Opportunity
	found            []Opportunity
	lastTS           time.Time
	decodeErrs       int
}

// ScanSource tags every recall opportunity in one transcript and resolves each
// to the channel that answered it. Subagent transcripts (src.Agent != "") carry
// no dk prompts and are skipped by the caller, not here.
func ScanSource(src transcript.Source, win Window) ([]Opportunity, int, error) {
	s := &scanner{project: src.Project, session: src.Session, win: win}
	err := transcript.ReadLines(src.Path, func(line []byte) error {
		s.feed(line)
		return nil
	})
	if err != nil {
		return nil, s.decodeErrs, err
	}
	s.closeOpen() // EOF closes a still-open opportunity as unanswered
	return s.found, s.decodeErrs, nil
}

func (s *scanner) feed(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return // blank/whitespace line (common JSONL trailer): not a decode error
	}
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		s.decodeErrs++
		return
	}
	if raw.IsMeta || raw.Message == nil {
		return
	}
	if ts := parseTS(raw.Timestamp); !ts.IsZero() {
		s.lastTS = ts
	}
	switch raw.Type {
	case "user":
		s.feedUser(raw)
	case "assistant":
		s.feedAssistant(raw)
	}
}

// feedUser opens a new opportunity on a genuine recall-shaped turn. A tool_result
// carrier (no prompt text) or a fold-in (carrier/control/affirmation) leaves an
// open opportunity standing — the agent's response arc is still in progress.
func (s *scanner) feedUser(raw transcript.Raw) {
	prompt := score.PromptText(raw.Message.Content)
	if prompt == "" {
		return // tool_result carrier: not a boundary
	}
	if skip, _, _ := score.ClassifyBoundary(prompt); skip {
		return // carrier/control/affirmation: continues the arc, opens nothing
	}
	// A genuine user turn closes any open opportunity (its arc ended) …
	s.closeOpen()
	// … and may open a new one.
	cue, class, ok := Classify(prompt)
	if !ok {
		return
	}
	ts := parseTS(raw.Timestamp)
	if ts.IsZero() {
		ts = s.lastTS
	}
	if !s.win.contains(ts) {
		return
	}
	s.open = true
	s.cur = Opportunity{
		Session:   s.session,
		Project:   s.project,
		Timestamp: ts,
		Class:     class,
		Cue:       cue,
		Text:      truncate(prompt, textCap),
		Reach:     ReachNone,
	}
}

// feedAssistant resolves an open opportunity against the first retrieval tool_use
// in this assistant turn. Non-retrieval blocks are skipped; the opportunity stays
// open across turns until a retrieval action fires or a new user boundary closes it.
func (s *scanner) feedAssistant(raw transcript.Raw) {
	if !s.open {
		return
	}
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
		if r, tool := classifyReachBlock(blk); r.isRetrieval() {
			s.cur.Reach = r
			s.cur.FiredTool = tool
			s.found = append(s.found, s.cur)
			s.open = false
			return
		}
	}
}

func (s *scanner) closeOpen() {
	if s.open {
		s.found = append(s.found, s.cur) // Reach stays ReachNone
		s.open = false
	}
}

// parseTS decodes a transcript RFC3339 timestamp, returning the zero time on any
// parse failure (some event types carry no timestamp).
func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s) // superset: parses plain + fractional
	if err != nil {
		return time.Time{}
	}
	return t
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ") // keep the scorecard row single-lined
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// trim back to a rune boundary (n < len(s) here, so the index is safe)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
