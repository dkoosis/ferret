package friction

import (
	"github.com/dkoosis/ferret/internal/event"
)

// Signature is a known friction signature: a fingerprint ferret has recorded as
// friction at least once, plus a human label for the /wrap trap-graduation
// prompt to name it. Signatures are supplied to the detector as a slice (v1
// loads them from a JSONL file — see signature.go); registering the FIRST
// occurrence of an unseen friction fingerprint also mints one, so a single pass
// over a stream both records new signatures and flags repeats of old ones.
type Signature struct {
	// Fingerprint is the normalized signature (see Fingerprint). It is the join
	// key: two events match iff their fingerprints are byte-equal.
	Fingerprint string `json:"fingerprint"`
	// Label is a short human name for the friction ("git branch-flip loop",
	// "tail-pipe false-green"), carried into the match record so the downstream
	// prompt can name the trap without re-deriving it.
	Label string `json:"label,omitempty"`
}

// MatchRecord is emitted when a fresh friction event repeats a known signature.
// It carries enough for a /wrap trap-graduation prompt to act: WHICH signature
// matched (Fingerprint + Label), WHERE (Session/Agent + Seq, the turn ordinal
// within the source), and that this is the Occurrence-th sighting (always ≥2 —
// the first sighting records the signature and emits nothing). Raw is the
// unmasked friction text of this occurrence, kept as an exemplar so a human
// reviewing the match sees the concrete command, not just its template.
type MatchRecord struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label,omitempty"`
	Session     string `json:"session"`
	Agent       string `json:"agent,omitempty"`
	Seq         int    `json:"seq"`
	Occurrence  int    `json:"occurrence"` // ≥2; this event is the Nth sighting
	Raw         string `json:"raw,omitempty"`
}

// Detector holds the fingerprint registry and flags recurrences as it observes a
// stream of events. It is single-pass and order-sensitive: the first event for a
// fingerprint records it (occurrence 1, no match emitted); each later matching
// event increments the count and yields a MatchRecord (occurrence ≥2).
//
// A Detector seeded with known signatures (NewDetector) treats every matching
// fresh event as a repeat: a seeded fingerprint is already at occurrence 1, so
// its first fresh sighting emits occurrence 2 — exactly the "flag the 2nd
// occurrence of a known signature" contract. An empty Detector learns the corpus
// as it goes and flags within-corpus repeats.
type Detector struct {
	counts map[string]int
	labels map[string]string
}

// NewDetector builds a detector pre-seeded with known signatures. Each seeded
// signature starts at occurrence 1 (already-seen), so the next matching event is
// flagged as occurrence 2. A nil/empty slice yields a detector that learns
// signatures from the stream itself.
func NewDetector(known []Signature) *Detector {
	d := &Detector{counts: make(map[string]int), labels: make(map[string]string)}
	for _, s := range known {
		if s.Fingerprint == "" {
			continue
		}
		d.counts[s.Fingerprint] = 1
		if s.Label != "" {
			d.labels[s.Fingerprint] = s.Label
		}
	}
	return d
}

// Observe feeds one event to the detector. It returns a MatchRecord and true
// when the event is a friction event whose fingerprint was already known (a 2nd+
// occurrence); it returns false for non-friction events and for the FIRST
// sighting of a fingerprint (which it records silently). The occurrence count in
// a returned record is always ≥2.
func (d *Detector) Observe(e event.Event) (MatchRecord, bool) {
	raw, ok := FrictionText(e)
	if !ok {
		return MatchRecord{}, false
	}
	fp := Fingerprint(raw)
	d.counts[fp]++
	n := d.counts[fp]
	if n < 2 {
		return MatchRecord{}, false // first sighting: record the signature, flag nothing
	}
	return MatchRecord{
		Fingerprint: fp,
		Label:       d.labels[fp],
		Session:     e.Session,
		Agent:       e.Agent,
		Seq:         e.Seq,
		Occurrence:  n,
		Raw:         raw,
	}, true
}

// Scan runs the detector over a whole event slice in order and returns every
// match record (2nd+ occurrences). It is the batch entry point the CLI uses; the
// streaming Observe is exposed for callers that fold matches into another pass.
func Scan(known []Signature, events []event.Event) []MatchRecord {
	d := NewDetector(known)
	var out []MatchRecord
	for i := range events {
		if m, ok := d.Observe(events[i]); ok {
			out = append(out, m)
		}
	}
	return out
}

// FrictionText extracts the raw string to fingerprint from an event, reporting
// false when the event is not a friction event. A friction event is a FAILED
// action (Status fail or cfail) — the deterministic, regex-first friction signal
// already in the event model. We deliberately do not read prompt sentiment:
// dev jargon ("kill the process", "dead code") is style, not friction, so this
// is a status gate, never an affect detector.
//
// The text joins the normalized Action with the most specific volatile context
// available — the raw command segment (Detail) for a shell failure, else the
// Target (a path/symbol) for a tool failure — so Fingerprint can mask the
// volatile part and leave the invariant shape of the failure.
func FrictionText(e event.Event) (string, bool) {
	switch e.Status {
	case event.StatusFail, event.StatusCFail:
	default:
		return "", false
	}
	if e.Action == "" && e.Detail == "" && e.Target == "" {
		return "", false
	}
	ctx := e.Detail
	if ctx == "" {
		ctx = e.Target
	}
	if ctx == "" {
		return e.Action, true
	}
	if e.Action == "" {
		return ctx, true
	}
	return e.Action + " " + ctx, true
}
