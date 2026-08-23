package event

import (
	"testing"

	"github.com/dkoosis/ferret/internal/apiusage"
)

// usageLine writes one assistant line carrying an API usage ledger. msgID is
// the API response id — several lines of one response repeat it.
func usageLine(uuid, msgID string, in, cw, cr, out, think int) string {
	return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"2026-08-14T10:00:00Z","message":{"role":"assistant","id":"` + msgID +
		`","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":` + itoa(in) +
		`,"cache_creation_input_tokens":` + itoa(cw) + `,"cache_read_input_tokens":` + itoa(cr) +
		`,"output_tokens":` + itoa(out) + `,"output_tokens_details":{"thinking_tokens":` + itoa(think) +
		`},"cache_creation":{"ephemeral_1h_input_tokens":` + itoa(cw) + `,"ephemeral_5m_input_tokens":0}}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// collectLedger runs the builder over the given lines and returns the rows it
// emitted plus its stats.
func collectLedger(t *testing.T, lines ...string) ([]apiusage.Row, *Stats) {
	t.Helper()
	src := writeTranscript(t, lines...)
	b := NewBuilder()
	var rows []apiusage.Row
	b.SetLedger(func(r *apiusage.Row) { rows = append(rows, *r) })
	if err := b.File(src, func(*Event) {}); err != nil {
		t.Fatal(err)
	}
	return rows, b.Stats
}

// TestLedger_CollapsesRepeatLines_When_OneResponseSpansSeveral is the bead's
// load-bearing test. A single API response is written to the transcript as one
// line per content block, each repeating the SAME usage; billing every line
// inflates measured spend by roughly half. Only the message id collapses them —
// the per-line uuid differs, so the existing uuid dedup cannot see it.
func TestLedger_CollapsesRepeatLines_When_OneResponseSpansSeveral(t *testing.T) {
	rows, stats := collectLedger(t,
		usageLine("u1", "msg_A", 2, 1000, 50000, 100, 40),
		usageLine("u2", "msg_A", 2, 1000, 50000, 100, 40), // same response, second block
		usageLine("u3", "msg_A", 2, 1000, 50000, 100, 40), // same response, third block
		usageLine("u4", "msg_B", 2, 500, 51000, 80, 10),
	)

	if len(rows) != 2 {
		t.Fatalf("captured %d rows, want 2 — one per distinct API call, not per line: %+v", len(rows), rows)
	}
	if stats.APICalls != 2 || stats.APIDupes != 2 {
		t.Errorf("APICalls=%d APIDupes=%d, want 2/2", stats.APICalls, stats.APIDupes)
	}
	var tot apiusage.Totals
	for i := range rows {
		tot.Add(&rows[i])
	}
	if tot.CacheWrite != 1500 {
		t.Errorf("CacheWrite = %d, want 1500 — repeat lines must not be billed twice", tot.CacheWrite)
	}
}

// TestLedger_CapturesEveryBucket_When_UsagePresent pins the field mapping,
// including the TTL split: a 5-minute write that expires before its next read
// is a full-price write that bought nothing, so the halves must stay separable.
func TestLedger_CapturesEveryBucket_When_UsagePresent(t *testing.T) {
	rows, _ := collectLedger(t, usageLine("u1", "msg_A", 7, 1000, 50000, 120, 90))
	if len(rows) != 1 {
		t.Fatalf("captured %d rows, want 1", len(rows))
	}
	r := rows[0]
	switch {
	case r.Input != 7:
		t.Errorf("Input = %d, want 7", r.Input)
	case r.CacheWrite != 1000:
		t.Errorf("CacheWrite = %d, want 1000", r.CacheWrite)
	case r.CacheRead != 50000:
		t.Errorf("CacheRead = %d, want 50000", r.CacheRead)
	case r.Output != 120:
		t.Errorf("Output = %d, want 120", r.Output)
	case r.Thinking != 90:
		t.Errorf("Thinking = %d, want 90", r.Thinking)
	case r.Write1h != 1000 || r.Write5m != 0:
		t.Errorf("TTL split = 1h:%d 5m:%d, want 1000/0", r.Write1h, r.Write5m)
	case r.Model != "claude-opus-5":
		t.Errorf("Model = %q, want claude-opus-5", r.Model)
	case r.Session != "sess":
		t.Errorf("Session = %q, want sess", r.Session)
	case r.Time.IsZero():
		t.Error("Time is zero — a ledger row with no timestamp cannot be windowed against /usage")
	}
}

// TestLedger_StaysSilent_When_NoSinkWired pins that the ledger is opt-in: an
// ingest path that never calls SetLedger behaves exactly as before this bead.
func TestLedger_StaysSilent_When_NoSinkWired(t *testing.T) {
	src := writeTranscript(t, usageLine("u1", "msg_A", 2, 10, 20, 30, 0))
	b := NewBuilder()
	if err := b.File(src, func(*Event) {}); err != nil {
		t.Fatal(err)
	}
	if b.Stats.APICalls != 0 {
		t.Errorf("APICalls = %d with no sink wired, want 0", b.Stats.APICalls)
	}
}

// TestLedger_SkipsLines_When_UsageAbsent pins that a line with no usage (a user
// turn, an older transcript schema) contributes nothing rather than a zero row
// — a zero row would dilute every per-call average.
func TestLedger_SkipsLines_When_UsageAbsent(t *testing.T) {
	rows, stats := collectLedger(t,
		`{"type":"assistant","uuid":"u1","timestamp":"2026-08-14T10:00:00Z","message":{"role":"assistant","id":"m1","content":[{"type":"text","text":"hi"}]}}`,
		usageLine("u2", "msg_B", 1, 2, 3, 4, 0),
	)
	if len(rows) != 1 || stats.APICalls != 1 {
		t.Errorf("rows=%d APICalls=%d, want 1/1 — a usage-less line is not a zero-cost call", len(rows), stats.APICalls)
	}
}
