// Package floor measures the fixed cost every session pays before dk's first
// real turn: the SessionStart hook render (the ⭐ banner + itzy context block)
// and the deferred-tool/agent/mcp/skill roster the harness injects once at
// open. That cost is identical for a 3-turn session and a 3-hour one, so it
// scales with SESSION COUNT, not with work done — ferret-khh, filed after
// 16 sessions in one day paid it 16 times.
//
// This is a different question from the scoreboard (internal/mine/
// scoreboard.go, ferret-4vx): the scoreboard ranks per-COMMAND burn inside a
// session; floor measures the per-SESSION tax paid before any command runs,
// straight off the raw transcripts (transcript.Walk/ReadLines), the same way
// internal/reach mines recall-opportunity moments without going through the
// ingested events corpus.
//
// Rule files (~/.claude/rules/**, .claude/rules/**) are the fourth item the
// bead names, but they never appear as their own line in any transcript:
// Claude Code folds loaded memory into the system prompt, and the JSONL
// session log never records the system prompt — verified against a live
// corpus (2026-09-07): zero transcripts on this machine carry any rule-file
// marker text anywhere. Their bytes are real, just sourced differently —
// RuleFiles reads them straight off disk (os.Stat, not a guess) and the
// caller folds the total into every session's floor.
package floor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/transcript"
)

// hookAttachTypes are SessionStart hook records. One hook fires several: a
// stdout blob (hookSpecificOutput.additionalContext — this is where the ⭐
// banner and the <itzy-context> block actually live), a systemMessage
// duplicate of the banner, and an additionalContext note.
var hookAttachTypes = map[string]bool{
	"hook_success": true, "hook_system_message": true, "hook_additional_context": true,
}

// rosterAttachTypes are the fixed capability listings the harness injects
// once at open, before any command runs: deferred tools, subagent types, MCP
// server instructions, and the skill catalog.
var rosterAttachTypes = map[string]bool{
	"deferred_tools_delta": true, "agent_listing_delta": true,
	"mcp_instructions_delta": true, "skill_listing": true,
}

// attachPayload is the subset of an attachment record's own fields floor
// reads to classify it and to find the itzy marker. content/stdout/stderr
// are searched as plain text — itzy's render rides inside a hook's stdout as
// a JSON *string*, not as a field of its own.
type attachPayload struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
}

const (
	itzyOpenTag  = "<itzy-context>"
	itzyCloseTag = "</itzy-context>"
)

// itzyBytes measures the <itzy-context>…</itzy-context> block inside s, 0
// when the tag is absent. Best-effort text scan (the block is embedded plain
// text inside a JSON string, not a field), matching the tolerance every other
// ferret transcript scanner uses for schema drift.
func itzyBytes(s string) int {
	start := strings.Index(s, itzyOpenTag)
	if start < 0 {
		return 0
	}
	rest := s[start:]
	if end := strings.Index(rest, itzyCloseTag); end >= 0 {
		return end + len(itzyCloseTag)
	}
	return len(rest) // no closing tag observed — count to the end of the field
}

// Category buckets one session's pre-first-turn attachment bytes by what
// generated them.
type Category struct {
	// HookBytes is the whole serialized SessionStart hook record(s) —
	// banner + itzy context + any additionalContext note. Whole-payload, not
	// just the content/stdout/stderr fields: ferret-wmb's own lesson is that a
	// per-field allowlist hid ~235MB corpus-wide by dropping routing metadata
	// (hookName, toolUseID, the command string) that still enters context.
	HookBytes int `json:"hookBytes"`
	// ItzyBytes is the <itzy-context> block specifically — a SUBSET of
	// HookBytes, reported separately because it is one of the bead's four
	// named sources, never added a second time into Sum.
	ItzyBytes int `json:"itzyBytes"`
	// RosterBytes is deferred_tools_delta + agent_listing_delta +
	// mcp_instructions_delta + skill_listing, whole-payload.
	RosterBytes int `json:"rosterBytes"`
}

// Sum is this category's contribution to the transcript-measured floor.
// ItzyBytes is excluded: it is a subset of HookBytes, not an addend.
func (c Category) Sum() int { return c.HookBytes + c.RosterBytes }

// Session is one main-session transcript's floor measurement.
type Session struct {
	Session         string   `json:"session"`
	Project         string   `json:"project"`
	TranscriptBytes int      `json:"transcriptBytes"` // every raw line in the file, whole session
	Cat             Category `json:"cat"`
	PreTurnLines    int      `json:"preTurnLines"` // attachment lines read before the first assistant response
}

// FloorBytes is this session's fixed cost: the transcript-measured hook +
// roster bytes plus the caller-supplied rule-file bytes (session-invariant —
// the same rule set loads at every open, so it is added once per session,
// not summed from the transcript).
func (s Session) FloorBytes(ruleBytes int) int { return ruleBytes + s.Cat.Sum() }

// TurnBytes is everything that is NOT the floor: per-turn burn, spent after
// dk's first real ask.
func (s Session) TurnBytes() int { return s.TranscriptBytes - s.Cat.Sum() }

// Share is FloorBytes as a fraction of total session cost. ruleBytes is
// added to the denominator too — it is real cost paid on every request even
// though the JSONL log never carries it as a line of its own — which is what
// makes a short session show the worst ratio (the bead's Rule): a fixed
// numerator over a small denominator.
func (s Session) Share(ruleBytes int) float64 {
	total := s.TranscriptBytes + ruleBytes
	if total <= 0 {
		return 0
	}
	return float64(s.FloorBytes(ruleBytes)) / float64(total)
}

// ScanSource measures one main-session transcript's pre-first-turn floor.
// Subagent transcripts (src.Agent != "") carry no SessionStart hook at all —
// the caller filters them out, the same convention reach.ScanSource uses.
//
// The boundary is the first ASSISTANT line, not the first user line — verified
// against a live corpus (2026-09-07): a real prompt's own line is followed by
// the deferred-tool/agent/mcp/skill roster attachments (CC assembles the
// roster once it has a real prompt to respond to, not at raw session start),
// and only THEN does the model produce its first response. "Before the first
// user turn is read" means before the MODEL reads it — i.e. before that first
// assistant line — not before the user's own prompt line is written to disk.
// A slash-command echo or local-command caveat ahead of the real prompt never
// produces an assistant line of its own (CC handles them without a model
// call), so they fall inside the floor window for free, with no separate
// carrier/affirmation classification needed.
func ScanSource(src transcript.Source) (Session, error) {
	sess := Session{Session: src.Session, Project: src.Project}
	responded := false
	err := transcript.ReadLines(src.Path, func(line []byte) error {
		sess.TranscriptBytes += len(line)
		if responded {
			return nil
		}
		// A broken line is tolerated, not fatal — best-effort, like every other
		// transcript scanner here — so the unmarshal error itself is discarded
		// on purpose (json.Unmarshal on a malformed line leaves raw unusable).
		var raw transcript.Raw
		if json.Unmarshal(line, &raw) == nil {
			switch {
			case raw.Type == "attachment":
				classifyAttachment(raw.Attachment, &sess.Cat)
				sess.PreTurnLines++
			case raw.Type == "assistant" && !raw.IsSidechain:
				responded = true
			}
		}
		return nil
	})
	return sess, err
}

// classifyAttachment sizes and buckets one attachment record. Bytes are the
// WHOLE serialized payload (len(raw)), matching internal/event/build.go's
// established convention — a deliberate over-count bias, not a per-field
// allowlist.
func classifyAttachment(raw json.RawMessage, cat *Category) {
	if len(raw) == 0 {
		return
	}
	var p attachPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	n := len(raw)
	switch {
	case hookAttachTypes[p.Type]:
		cat.HookBytes += n
		cat.ItzyBytes += itzyBytes(p.Content) + itzyBytes(p.Stdout) + itzyBytes(p.Stderr)
	case rosterAttachTypes[p.Type]:
		cat.RosterBytes += n
	}
}

// RuleFiles sums the byte size of every file any of globs matches — read
// straight off disk (os.Stat), never estimated. A glob matching nothing
// (e.g. a project with no .claude/rules directory) contributes 0 and is not
// an error: an absent rule tier is a real, measured state, not a failure.
func RuleFiles(globs ...string) (files, totalBytes int, err error) {
	for _, g := range globs {
		if g == "" {
			continue
		}
		matches, gerr := filepath.Glob(g)
		if gerr != nil {
			return files, totalBytes, gerr
		}
		for _, m := range matches {
			fi, serr := os.Stat(m)
			if serr != nil || fi.IsDir() {
				continue
			}
			files++
			totalBytes += int(fi.Size())
		}
	}
	return files, totalBytes, nil
}

// Report is the corpus-wide sample: every scanned main-session transcript
// plus the rule-file bytes, measured once (session-invariant) and applied to
// every session in Totals below.
type Report struct {
	Sessions         []Session `json:"sessions"`
	RuleFilesGlobal  int       `json:"ruleFilesGlobal"`
	RuleBytesGlobal  int       `json:"ruleBytesGlobal"`
	RuleFilesProject int       `json:"ruleFilesProject"`
	RuleBytesProject int       `json:"ruleBytesProject"`
	DecodeErrs       int       `json:"decodeErrs,omitempty"` // transcripts skipped: unreadable, not malformed-line-tolerant
}

// RuleBytes is the total fixed rule-file cost folded into every session's
// floor (global + this project's tier).
func (r Report) RuleBytes() int { return r.RuleBytesGlobal + r.RuleBytesProject }

// Totals rolls up every sampled session against the fixed rule-byte cost —
// the headline the bead asks for: fixed floor and per-turn burn reported
// separately, and the floor expressed as a share of total session bytes so a
// short session (small denominator, same fixed numerator) shows the worst
// ratio rather than being averaged away by long ones.
type Totals struct {
	Sessions        int     `json:"sessions"`
	RuleBytes       int     `json:"ruleBytes"`
	FloorBytes      int64   `json:"floorBytes"`      // Σ per-session FloorBytes
	TranscriptBytes int64   `json:"transcriptBytes"` // Σ per-session TranscriptBytes
	TurnBytes       int64   `json:"turnBytes"`       // Σ per-session TurnBytes — the per-turn burn half
	SharePct        float64 `json:"sharePct"`        // aggregate floor share across every sampled session
	MedianSharePct  float64 `json:"medianSharePct"`  // per-session share, median — the corpus average hides the worst case
	WorstSharePct   float64 `json:"worstSharePct"`
	WorstSession    string  `json:"worstSession"`
}

// Rollup aggregates sessions against ruleBytes (the same fixed rule-file
// total for every session — see Report.RuleBytes).
func Rollup(sessions []Session, ruleBytes int) Totals {
	t := Totals{Sessions: len(sessions), RuleBytes: ruleBytes}
	if len(sessions) == 0 {
		return t
	}
	shares := make([]float64, 0, len(sessions))
	for _, s := range sessions {
		t.FloorBytes += int64(s.FloorBytes(ruleBytes))
		t.TranscriptBytes += int64(s.TranscriptBytes)
		t.TurnBytes += int64(s.TurnBytes())
		sh := s.Share(ruleBytes)
		shares = append(shares, sh)
		if sh*100 > t.WorstSharePct {
			t.WorstSharePct = sh * 100
			t.WorstSession = s.Session
		}
	}
	if denom := t.TranscriptBytes + int64(ruleBytes)*int64(len(sessions)); denom > 0 {
		t.SharePct = float64(t.FloorBytes) / float64(denom) * 100
	}
	sort.Float64s(shares)
	t.MedianSharePct = shares[len(shares)/2] * 100
	return t
}
