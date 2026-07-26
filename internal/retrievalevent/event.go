// Package retrievalevent mirrors the trixi⇄ferret retrieval-outcome contract's
// producer-side row shape (~/Projects/dk/Project/trixi/specs/retrieval-outcome-contract-design.md,
// authority: trixi's internal/observe/retrievalevent/event.go). This is
// ferret's LEAF, stdlib-only copy of that shape — ferret does not import the
// trixi module; it vendors the fixture (testdata/retrieval-golden.jsonl,
// tx-dii8m) and decodes it with its own structs so the two modules stay
// independently buildable.
//
// schema_version rides every row (contract Trap 1): a shape change that isn't
// version-bumped must break the join loudly, not compute silently-wrong
// numbers. ReadEvents in read.go asserts it.
package retrievalevent

// SchemaVersion is the schema version this reader was built against. Bump on
// any shape change; ReadEvents rejects any row whose schema_version differs.
const SchemaVersion = 1

// Row kinds. Both kinds ride one append-only stream (contract call 8).
const (
	KindSearch = "search"
	KindRead   = "read"
)

// AgentTypeParent is the agent_type the producer stamps on the main agent's own
// rows — the human-facing agent, as opposed to a spawned subagent (general-
// purpose, Explore, …). session_id is SHARED across a parent and its subagents
// (contract Trap 2), so agent_type is the discriminator any consumer keying on
// "the human's own retrieval" must filter by; session_id alone admits every
// subagent's searches. See internal/retrievalevent/testdata/retrieval-golden.jsonl
// (a general-purpose subagent search rides the parent's session_id).
const AgentTypeParent = "parent"

// AttributionSessionFallback flags a row whose agent_id could not be resolved
// to a specific agent within the join window, so agent_id was set to
// session_id as a self-describing degraded key. This collapses the
// agent_id/agent_type parent↔subagent discriminator (contract Trap 2), so
// ferret filters these rows out of per-agent verdicts rather than emit a
// mis-keyed record.
const AttributionSessionFallback = "session-fallback"

// Returned is one ranked hit inside a search row's returned[].
type Returned struct {
	NugID string  `json:"nug_id"`
	Score float64 `json:"score"`
}

// SearchRef is the foreign key a read row carries back to the search row that
// surfaced the nug. EventID is the collision-proof primary key; the other
// fields stay for human-legible joins.
type SearchRef struct {
	EventID   string `json:"event_id"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	TS        string `json:"ts"`
}

// Event is one retrieval-event row, decoded straight off the vendored
// producer fixture. One struct serves both kinds; omitempty drops the fields
// that don't apply to a given kind. Field tags mirror trixi's event.go
// verbatim so decoding either producer stream needs no translation layer.
type Event struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	EventID       string `json:"event_id"`
	TS            string `json:"ts"` // RFC3339Nano, UTC

	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`

	// Search rows.
	Query             string     `json:"query,omitempty"`
	Returned          []Returned `json:"returned,omitempty"`
	Gate              string     `json:"gate,omitempty"`
	ConfigFingerprint string     `json:"config_fingerprint,omitempty"`
	Project           string     `json:"project,omitempty"`

	// Read rows.
	NugID     string     `json:"nug_id,omitempty"`
	SearchRef *SearchRef `json:"search_ref,omitempty"`

	// Attribution flags a degraded key; see AttributionSessionFallback.
	Attribution string `json:"attribution,omitempty"`
}
