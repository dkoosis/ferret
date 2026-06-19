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
	Approx    bool      `json:"ax,omitempty"` // tool served via fuzzy/semantic fallback (snipe ~approx marker in tool_result); silence = exact match
	Bytes     int       `json:"b,omitempty"`  // measured context cost: tool_use input + tool_result content
	Skill     string    `json:"skill,omitempty"`
	Plugin    string    `json:"plug,omitempty"`
	MCP       string    `json:"mcp,omitempty"`
	Version   string    `json:"v,omitempty"`
	// Prompt is the full, untruncated user-turn text — only set on KindPrompt
	// events. Captured at ingestion so a downstream consumer can do linguistic /
	// query-quality analysis without re-parsing raw transcripts (ferret-d01).
	Prompt string `json:"q,omitempty"`
}

const (
	KindTool   = "tool"
	KindShell  = "shell"
	KindPrompt = "prompt"

	StatusOK    = "ok"
	StatusFail  = "fail"
	StatusCFail = "cfail" // compound shell chain failed; failing segment unknown
	StatusNone  = "none"
)

// Stats accumulates ingest health counters.
type Stats struct {
	Files       int            `json:"files"`
	Lines       int            `json:"lines"`
	Events      int            `json:"events"`
	Prompts     int            `json:"prompts"`
	Unpaired    int            `json:"unpaired"`
	Fallback    int            `json:"shellFallback"`
	Deduped     int            `json:"deduped"`
	OrphanBytes int            `json:"orphanBytes"` // tool_result payload for a deduped/forked use with no pending event — accounted, not attributed to a burn row
	DecodeErrs  int            `json:"decodeErrs"`
	ByType      map[string]int `json:"byType"`
}

func NewStats() *Stats { return &Stats{ByType: map[string]int{}} }
