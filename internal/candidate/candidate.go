// Package candidate defines ferret's candidate record — the frozen row shape
// ferret emits to the JSONL spool the trixi-bot distiller consumes (sensor→kg
// v1, epic gg-eqn). ferret is the PRODUCER side of a versioned contract:
// schema_version rides every row and the consumer loudly rejects a mismatch, so
// any shape change here is breaking and must bump SchemaVersion. This is the
// mirror image of internal/retrievalevent, where ferret is the consumer; here
// ferret produces and trixi-bot consumes.
//
// LLM-free invariant: a candidate carries statistical SIGNALS about a transcript
// span (novelty, recurrence, a content fingerprint) plus a pointer to the span —
// never any claim text. ferret proposes WHERE a fact might live, never WHAT it
// is; the LLM proposer and judge live downstream in trixi-bot. The struct has no
// field for claim text by construction, so a caller cannot violate the invariant.
package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// SchemaVersion is the candidate row schema this producer emits. It rides every
// row (Candidate.SchemaVersion) and the trixi-bot consumer rejects any row whose
// value differs — a shape change that isn't version-bumped must break the join
// loudly, not compute silently-wrong numbers. Bump on any field change.
const SchemaVersion = 1

// Sensor is the producer tag stamped on every row. Fixed to "ferret" for this
// producer; the field exists so a future multi-sensor spool stays self-describing.
const Sensor = "ferret"

// AgentParent is the agent label for a main-thread (non-subagent) transcript.
// ferret's event model uses "" for the main thread; the contract row spells it
// "parent" (matching internal/retrievalevent.AgentTypeParent) so a consumer
// never has to special-case the empty string.
const AgentParent = "parent"

// idPrefix namespaces the candidate id ("fc-" = ferret candidate) so an id is
// self-describing in a log line or a cross-sensor spool.
const idPrefix = "fc-"

// idHexLen is how many hex chars of the sha256 the id keeps (16 = 64 bits) —
// enough that a collision across a realistic corpus is negligible, short enough
// to stay legible in a digest line.
const idHexLen = 16

// fingerprintAlgo prefixes the signals fingerprint so the hash is
// self-describing (algorithm-agile): "sha256:" + hex.
const fingerprintAlgo = "sha256:"

// Candidate is one emitted spool row: a pointer to a salient transcript span
// plus the deterministic signals that surfaced it. JSON keys are the frozen
// contract (spec: sensor-to-kg-v1.md §Spool schema); the trixi-bot consumer
// decodes exactly these fields.
type Candidate struct {
	SchemaVersion int     `json:"schema_version"`
	ID            string  `json:"id"`
	Sensor        string  `json:"sensor"`
	EmittedAt     string  `json:"emitted_at"` // RFC3339Nano, UTC
	Source        Source  `json:"source"`
	Signals       Signals `json:"signals"`
}

// Source points at the transcript span a candidate was drawn from. TranscriptPath
// is threaded from transcript.Source (the Event stream alone does not carry it),
// so the downstream distiller can fetch the span text by (path, seq range).
type Source struct {
	TranscriptPath string `json:"transcript_path"`
	Project        string `json:"project"`
	Session        string `json:"session"`
	Agent          string `json:"agent"`
	SeqStart       int    `json:"seq_start"`
	SeqEnd         int    `json:"seq_end"`
}

// Signals are the deterministic, LLM-free measurements that surfaced the span.
// NoveltyBits is the span's mean per-token surprisal under the corpus model;
// Recurrence is how many times the span's normalized shape has been seen;
// Fingerprint is "sha256:" + hex of the span's Drain fingerprint (a stable
// content hash that masks volatile tokens).
type Signals struct {
	NoveltyBits float64 `json:"novelty_bits"`
	Recurrence  int     `json:"recurrence"`
	Fingerprint string  `json:"fingerprint"`
}

// FingerprintHash returns the hex sha256 of a Drain fingerprint string — the
// stable content hash of a span's normalized shape. It is the RAW hash (no
// algorithm prefix): ID mixes it into the candidate id, and Signals.Fingerprint
// wraps it with the algorithm prefix for the row.
func FingerprintHash(drain string) string {
	sum := sha256.Sum256([]byte(drain))
	return hex.EncodeToString(sum[:])
}

// ID computes the sensor-stable candidate id from the fields that identify a
// span: session, its Seq range, and the fingerprint hash of its content
// (spec: id = sha256-16 of "ferret|"+session+"|"+start+"-"+end+"|"+fpHash). The
// id is idempotent — the same span re-scanned in a later run hashes to the same
// id, so the spool writer and the downstream consumer both dedup on it across
// re-runs. Recurrence and novelty are DELIBERATELY excluded: they drift run to
// run (more sessions → higher recurrence), and mixing them in would fork the id
// for a span that has not changed.
func ID(session string, seqStart, seqEnd int, fpHash string) string {
	sum := sha256.Sum256([]byte("ferret|" + session + "|" +
		strconv.Itoa(seqStart) + "-" + strconv.Itoa(seqEnd) + "|" + fpHash))
	return idPrefix + hex.EncodeToString(sum[:])[:idHexLen]
}

// New assembles a Candidate, computing its id and wrapping the fingerprint hash
// for the row. emittedAt is passed in (not read from the clock) so emit stays
// deterministic and testable. drain is the span's Drain fingerprint; an empty
// src.Agent normalizes to AgentParent.
func New(src Source, emittedAt time.Time, noveltyBits float64, recurrence int, drain string) Candidate {
	if src.Agent == "" {
		src.Agent = AgentParent
	}
	fp := FingerprintHash(drain)
	return Candidate{
		SchemaVersion: SchemaVersion,
		ID:            ID(src.Session, src.SeqStart, src.SeqEnd, fp),
		Sensor:        Sensor,
		EmittedAt:     emittedAt.UTC().Format(time.RFC3339Nano),
		Source:        src,
		Signals: Signals{
			NoveltyBits: noveltyBits,
			Recurrence:  recurrence,
			Fingerprint: fingerprintAlgo + fp,
		},
	}
}
