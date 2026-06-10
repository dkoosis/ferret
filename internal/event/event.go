// Package event defines the canonical Event — ferret's contract between
// transcript ingestion and everything downstream.
package event

import "time"

// Event is one normalized action. JSON keys are short: the artifact holds
// millions of these.
type Event struct {
	Seq       int       `json:"i"`           // order within source file — authoritative ordering
	Time      time.Time `json:"t,omitempty"` // advisory; some event types carry no timestamp
	Project   string    `json:"p"`
	Session   string    `json:"s"`
	Agent     string    `json:"a,omitempty"` // subagent id; "" = main thread
	Sidechain bool      `json:"sc,omitempty"`
	Kind      string    `json:"k"`   // tool | shell | prompt
	Action    string    `json:"act"` // tool name; for shell: normalized command
	Target    string    `json:"tgt,omitempty"`
	Detail    string    `json:"d,omitempty"`  // raw command segment, truncated
	Status    string    `json:"st,omitempty"` // ok | fail | none (no paired result)
	DurMS     int64     `json:"ms,omitempty"` // tool_use → tool_result latency
	Retry     bool      `json:"rt,omitempty"` // same action+target shortly after a failure
	Compound  bool      `json:"cp,omitempty"` // segment of a split compound bash chain
	Skill     string    `json:"skill,omitempty"`
	Plugin    string    `json:"plug,omitempty"`
	MCP       string    `json:"mcp,omitempty"`
	Version   string    `json:"v,omitempty"`
}

const (
	KindTool   = "tool"
	KindShell  = "shell"
	KindPrompt = "prompt"

	StatusOK   = "ok"
	StatusFail = "fail"
	StatusNone = "none"
)

// Stats accumulates ingest health counters.
type Stats struct {
	Files      int            `json:"files"`
	Lines      int            `json:"lines"`
	Events     int            `json:"events"`
	Prompts    int            `json:"prompts"`
	Unpaired   int            `json:"unpaired"`
	Fallback   int            `json:"shellFallback"`
	Deduped    int            `json:"deduped"`
	DecodeErrs int            `json:"decodeErrs"`
	ByType     map[string]int `json:"byType"`
}

func NewStats() *Stats { return &Stats{ByType: map[string]int{}} }
