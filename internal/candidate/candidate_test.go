package candidate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestID_StableAndIdempotent pins the id derivation: the same span always hashes
// to the same id (idempotency is the whole point — a re-scan must not fork the
// id), and the id is prefixed + length-bounded per the contract.
func TestID_StableAndIdempotent(t *testing.T) {
	fp := FingerprintHash("edit read edit")
	got := ID("de34f538", 112, 118, fp)
	again := ID("de34f538", 112, 118, fp)
	if got != again {
		t.Fatalf("ID not idempotent: %q vs %q", got, again)
	}
	if !strings.HasPrefix(got, "fc-") {
		t.Errorf("ID %q missing fc- prefix", got)
	}
	if n := len(strings.TrimPrefix(got, "fc-")); n != idHexLen {
		t.Errorf("ID hex len = %d, want %d (id=%q)", n, idHexLen, got)
	}
}

// TestID_DiffersOnEverySpanField asserts each identifying input actually moves
// the id — a collision on any of them would let two distinct spans share an id
// and silently dedup one away.
func TestID_DiffersOnEverySpanField(t *testing.T) {
	base := ID("s", 1, 5, "aa")
	cases := map[string]string{
		"session":   ID("s2", 1, 5, "aa"),
		"seq_start": ID("s", 2, 5, "aa"),
		"seq_end":   ID("s", 1, 6, "aa"),
		"fp":        ID("s", 1, 5, "bb"),
	}
	for name, other := range cases {
		if other == base {
			t.Errorf("ID collided when %s changed", name)
		}
	}
}

// TestNew_ContractShape asserts New fills the frozen row: schema_version, sensor,
// the "sha256:" fingerprint wrapping, and empty-agent → parent normalization.
func TestNew_ContractShape(t *testing.T) {
	at := time.Date(2026, 7, 28, 4, 12, 9, 123456789, time.UTC)
	c := New(Source{Session: "sess", SeqStart: 3, SeqEnd: 9}, at, 9.42, 3, "some drain")

	if c.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", c.SchemaVersion, SchemaVersion)
	}
	if c.Sensor != Sensor {
		t.Errorf("sensor = %q, want %q", c.Sensor, Sensor)
	}
	if c.Source.Agent != AgentParent {
		t.Errorf("empty agent = %q, want normalized to %q", c.Source.Agent, AgentParent)
	}
	if !strings.HasPrefix(c.Signals.Fingerprint, "sha256:") {
		t.Errorf("fingerprint %q missing sha256: prefix", c.Signals.Fingerprint)
	}
	if c.Signals.Fingerprint != "sha256:"+FingerprintHash("some drain") {
		t.Errorf("fingerprint = %q, not the wrapped drain hash", c.Signals.Fingerprint)
	}
	if c.EmittedAt != "2026-07-28T04:12:09.123456789Z" {
		t.Errorf("emitted_at = %q, want RFC3339Nano UTC", c.EmittedAt)
	}
	if c.ID != ID("sess", 3, 9, FingerprintHash("some drain")) {
		t.Errorf("ID not derived from the span fields: %q", c.ID)
	}
}

// TestNew_LLMFreeInvariant is the structural guard: the marshaled row carries
// ONLY contract keys — no claim/text/fact field can ride a candidate, because the
// struct has no such field. If someone adds one, this fails loudly.
func TestNew_LLMFreeInvariant(t *testing.T) {
	c := New(Source{Session: "s", SeqStart: 1, SeqEnd: 2, Agent: "parent"}, time.Unix(0, 0), 1, 1, "d")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	wantTop := map[string]bool{
		"schema_version": true, "id": true, "sensor": true,
		"emitted_at": true, "source": true, "signals": true,
	}
	for k := range m {
		if !wantTop[k] {
			t.Errorf("unexpected top-level key %q (LLM-free contract admits only signals/pointer fields)", k)
		}
	}
	for k := range wantTop {
		if _, ok := m[k]; !ok {
			t.Errorf("missing contract key %q", k)
		}
	}
	for _, banned := range []string{"claim", "text", "fact", "body", "content"} {
		if strings.Contains(string(b), `"`+banned+`"`) {
			t.Errorf("candidate row leaked a %q field — violates the LLM-free invariant", banned)
		}
	}
}
