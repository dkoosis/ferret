// Package event defines the canonical Event — ferret's contract between
// transcript ingestion and everything downstream.
package event

import "time"

// Event is one normalized action. JSON keys are short: the artifact holds
// millions of these.
type Event struct {
	Seq       int       `json:"i"`          // order within source file — authoritative ordering
	Time      time.Time `json:"t,omitzero"` // advisory; some event types carry no timestamp
	Project   string    `json:"p"`
	Session   string    `json:"s"`
	Agent     string    `json:"a,omitempty"` // subagent id; "" = main thread
	Sidechain bool      `json:"sc,omitempty"`
	Kind      string    `json:"k"`   // tool | shell | prompt
	Action    string    `json:"act"` // tool name; for shell: normalized command
	Target    string    `json:"tgt,omitempty"`
	Detail    string    `json:"d,omitempty"`  // raw command segment, truncated
	Status    string    `json:"st,omitempty"` // ok | fail | cfail | none (no paired result)
	DurMS     int64     `json:"ms,omitempty"` // tool_use → tool_result latency
	Retry     bool      `json:"rt,omitempty"` // same action+target shortly after a failure
	Compound  bool      `json:"cp,omitempty"` // segment of a split compound bash chain
	// Swallow marks a shell segment in the left arm of `cmd 2>/dev/null ||
	// fallback` (ferret-cax) — a failure that is structurally invisible to
	// Status, because the error text is discarded and the chain exits with the
	// fallback's code, so resolve() sees no is_error and records ok. The tell
	// lives in the command text and must be captured here, at ingest:
	// shellnorm.Split has already dissolved the `||` by the time Detail is
	// written, so the two halves land in separate events and no downstream pass
	// can put them back together. Set from Segment.Swallowed.
	Swallow bool `json:"sw,omitempty"`
	// Pipe marks a shell segment that came from a pipeline (`a | b`) — info
	// shellnorm.Split's pipe collapse dissolves by the time Detail is written
	// (same one-way loss Swallow exists to capture), so it must be captured
	// here, at ingest. Set from Segment.Piped (ferret-cax item 3).
	Pipe bool `json:"pi,omitempty"`
	// Flags is a shell segment's option NAMES, values stripped — the cmd lens's
	// token material, set from Segment.Flags (ferret-dep).
	//
	// Stored rather than re-derived from Detail because Detail is truncated to
	// DetailMax: a long quoted positional pushes the option list past the cut,
	// and 9.6% of this corpus's shell events (13,747, measured 2026-08-30) lose
	// every flag that way. Empty is ambiguous by design — a command with no
	// options and one whose parse failed both yield nil, and neither is worth a
	// second field, since the cmd lens treats both as "token is the tool name".
	Flags []string `json:"fl,omitempty"`
	// CwdReset marks the trailing shell segment of a Bash call whose tool_result
	// reported "Shell cwd was reset" (ferret-cax item 5) — the harness resetting
	// a session's working directory out from under an in-flight sequence of
	// relative-path commands. Set in resolve() on a raw-content string match,
	// gated to a trailing KindShell segment the same way Approx is attributed.
	CwdReset bool `json:"cw,omitempty"`
	Approx   bool `json:"ax,omitempty"` // tool served via fuzzy/semantic fallback (snipe ~approx marker in tool_result); silence = exact match
	// Rung/CandidateCount/TriedRungs/IndexState come from a session+ts-joined
	// .snipe/usage.jsonl row (sn-r1do.2, internal/snipeusage). Empty Rung means
	// unjoined — either no usage.jsonl was loaded for this ingest run, or this
	// particular snipe call had no matching usage row — never "no rung fired":
	// snipe's telemetry.ClassifyRung always returns one of six non-empty
	// canonical tokens on every real emitted row.
	Rung           string   `json:"rg,omitempty"`
	CandidateCount int      `json:"cc,omitempty"`
	TriedRungs     []string `json:"tr,omitempty"`
	IndexState     string   `json:"ix,omitempty"`
	// Bytes is the measured context cost — InBytes + OutBytes — and stays the
	// ranking key every consumer (burn, stream, finding) already sorts on
	// (ferret-e4g). Kept as its own stored field rather than derived at read
	// time so those consumers need no change.
	Bytes int `json:"b,omitempty"`
	// InBytes is the request-side half of Bytes: for a shell segment, the
	// segment's own command text (Segment.Raw); for a non-Bash tool call, the
	// whole tool_use input envelope (blk.Input). Those two are measured
	// differently on purpose — a shell segment has no separate envelope to
	// measure the way a tool call's JSON input does — and that asymmetry was
	// already inside the conflated Bytes; splitting it only makes it visible.
	InBytes int `json:"ib,omitempty"`
	// OutBytes is the response-side half of Bytes: the tool_result payload
	// attributed to this event. A KindAttach event has no tool_use/tool_result
	// pair to split — it enters context as one whole payload with nothing to
	// call "input" — so its full Bytes is booked here.
	OutBytes int    `json:"ob,omitempty"`
	Skill    string `json:"skill,omitempty"`
	Plugin   string `json:"plug,omitempty"`
	MCP      string `json:"mcp,omitempty"`
	Version  string `json:"v,omitempty"`
	// Prompt is the full, untruncated user-turn text — only set on KindPrompt
	// events. Captured at ingestion so a downstream consumer can do linguistic /
	// query-quality analysis without re-parsing raw transcripts (ferret-d01).
	Prompt string `json:"q,omitempty"`
	// Query and Results capture a query-mode get_nug retrieval episode (ferret-sq.d0,
	// spec Decision 0) — set only on KindTool mcp__trixi__get_nug calls that carry a
	// query argument. By-id fetches (~55% of get_nug calls) carry neither. Query is
	// the full search string Claude sent; Results are the returned nug hits in rank
	// (result) order. Captured once at ingest, omitempty, no migration — the d01 way.
	Query   string   `json:"qq,omitempty"`
	Results []NugHit `json:"rs,omitempty"`
}

// NugHit is one returned nug in a get_nug result, in rank order (slice position
// is the rank). ID is the nug id; Score is the per-query match score read straight
// from the result envelope. The spec's Decision 0 assumed the CC log carried no
// per-hit score; the real envelope does (a descending per-query score distinct
// from the static meta.trixiQuality), so it is captured — Score is zero/omitted
// only when a given hit or result lacks one. This unblocks the score-distribution
// metric (R3b/NQC) the spec deferred.
type NugHit struct {
	ID    string  `json:"id"`
	Score float64 `json:"sc,omitempty"`
}

const (
	KindTool   = "tool"
	KindShell  = "shell"
	KindPrompt = "prompt"
	// KindAttach is a harness-injected attachment record — a hook's output, the
	// skill listing, a CLAUDE.md pull, an agent/tool-registry delta. These enter
	// the model's context with no tool_use to key on, so before ferret-rfc they
	// were invisible to every ranking: consumeLine dropped every line that was
	// not assistant/user, and Stats.ByType counted them without keeping one.
	//
	// They are not a rounding error. Measured over the corpus (3,923
	// transcripts): hook_success 90.0MB across 207,329 records and skill_listing
	// 82.4MB across 3,617 each rival Read — the top-ranked tool — at 83.6MB;
	// ~30 classes total ~235MB. Action carries the attachment class.
	KindAttach = "attach"

	StatusOK    = "ok"
	StatusFail  = "fail"
	StatusCFail = "cfail" // compound shell chain failed; failing segment unknown
	StatusNone  = "none"
)

// Stats accumulates ingest health counters.
type Stats struct {
	Files       int `json:"files"`
	Lines       int `json:"lines"`
	Events      int `json:"events"`
	Prompts     int `json:"prompts"`
	Attachments int `json:"attachments"` // KindAttach events kept (ferret-rfc)
	Unpaired    int `json:"unpaired"`
	Fallback    int `json:"shellFallback"`
	Deduped     int `json:"deduped"`
	OrphanBytes int `json:"orphanBytes"` // tool_result payload for a deduped/forked use with no pending event — accounted, not attributed to a burn row
	DecodeErrs  int `json:"decodeErrs"`
	// UsageSources/UsageRecords/UsageJoined are the corpus-level answer to "was
	// usage.jsonl even loaded, and how much of it joined" (sn-r1do.2) — set only
	// when an ingest run supplied --snipe-usage; zero otherwise.
	UsageSources int `json:"usageSources,omitempty"`
	UsageRecords int `json:"usageRecords,omitempty"`
	UsageJoined  int `json:"usageJoined,omitempty"`
	// APICalls/APIDupes are the token-ledger leg (ferret-x2v): distinct API
	// calls whose usage was captured, and repeat lines collapsed by message id.
	// APIDupes is reported rather than hidden because it is large (~35% of
	// usage-bearing lines) and a regression there silently inflates every spend
	// number by that much.
	APICalls int `json:"apiCalls,omitempty"`
	APIDupes int `json:"apiDupes,omitempty"`
	// CwdResets counts CwdReset events seen this ingest (ferret-cax item 5).
	CwdResets int            `json:"cwdResets,omitempty"`
	ByType    map[string]int `json:"byType"`
}

func NewStats() *Stats { return &Stats{ByType: map[string]int{}} }
