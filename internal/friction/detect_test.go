package friction

import (
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

func failShell(seq int, session, cmd, detail string) event.Event {
	return event.Event{Seq: seq, Session: session, Kind: event.KindShell, Action: cmd, Detail: detail, Status: event.StatusFail}
}

func TestFrictionText(t *testing.T) {
	cases := []struct {
		name   string
		ev     event.Event
		want   string
		wantOK bool
	}{
		{
			"failed shell joins action and detail",
			event.Event{Action: "go_test", Detail: "go test ./internal/mine", Status: event.StatusFail},
			"go_test go test ./internal/mine", true,
		},
		{
			"compound-fail (cfail) is excluded — failing segment unknown",
			event.Event{Action: "make_check", Detail: "make check", Status: event.StatusCFail},
			"", false,
		},
		{
			"failed tool falls back to target",
			event.Event{Action: "Read", Target: "/x/y.go", Status: event.StatusFail, Kind: event.KindTool},
			"Read /x/y.go", true,
		},
		{
			"action only when no detail or target",
			event.Event{Action: "git_push", Status: event.StatusFail},
			"git_push", true,
		},
		{"ok status is not friction", event.Event{Action: "go_test", Status: event.StatusOK}, "", false},
		{"none status is not friction", event.Event{Action: "go_test", Status: event.StatusNone}, "", false},
		{"empty failed event yields nothing", event.Event{Status: event.StatusFail}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := FrictionText(c.ev)
			if ok != c.wantOK || got != c.want {
				t.Errorf("FrictionText() = (%q,%v), want (%q,%v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestDetectorFlagsSecondOccurrence is the bead's central contract: the first
// sighting records the signature silently; the second matching event is flagged.
func TestDetectorFlagsSecondOccurrence(t *testing.T) {
	d := NewDetector(nil)

	// First occurrence (session A) — records the signature, emits nothing.
	if _, ok := d.Observe(failShell(3, "sessA", "go_test", "go test ./internal/mine")); ok {
		t.Fatal("first occurrence must not be flagged")
	}
	// Second occurrence (session B), different volatile path — must flag.
	m, ok := d.Observe(failShell(9, "sessB", "go_test", "go test ./internal/score"))
	if !ok {
		t.Fatal("second occurrence must be flagged")
	}
	if m.Occurrence != 2 {
		t.Errorf("Occurrence = %d, want 2", m.Occurrence)
	}
	if m.Session != "sessB" || m.Seq != 9 {
		t.Errorf("location = %s#%d, want sessB#9", m.Session, m.Seq)
	}
	if m.Fingerprint == "" {
		t.Error("match record must carry the fingerprint")
	}
}

// TestSeededSignatureFlagsFirstFreshEvent proves a pre-seeded known signature is
// treated as already-seen: its FIRST fresh sighting is occurrence 2 (a repeat).
func TestSeededSignatureFlagsFirstFreshEvent(t *testing.T) {
	seed := Signature{Fingerprint: Fingerprint("go_test go test /Users/a/mine"), Label: "test loop"}
	d := NewDetector([]Signature{seed})

	m, ok := d.Observe(failShell(1, "sessC", "go_test", "go test /Users/b/score"))
	if !ok {
		t.Fatal("a seeded signature's first fresh sighting must be flagged")
	}
	if m.Occurrence != 2 {
		t.Errorf("Occurrence = %d, want 2", m.Occurrence)
	}
	if m.Label != "test loop" {
		t.Errorf("Label = %q, want carried from seed", m.Label)
	}
}

// TestSignaturesReturnsSeededAndLearned proves the registry the CLI persists
// includes both seeded fingerprints and those learned from the stream, so the
// learned corpus is durable across runs (Bug 1).
func TestSignaturesReturnsSeededAndLearned(t *testing.T) {
	seed := Signature{Fingerprint: "seeded-fp", Label: "known"}
	d := NewDetector([]Signature{seed})
	d.Observe(failShell(1, "s", "go_test", "go test /Users/a/mine")) // learns a new fp

	sigs := d.Signatures()
	got := make(map[string]string, len(sigs))
	for _, s := range sigs {
		got[s.Fingerprint] = s.Label
	}
	if _, ok := got["seeded-fp"]; !ok {
		t.Error("Signatures() dropped the seeded fingerprint")
	}
	if _, ok := got[Fingerprint("go_test go test /Users/a/mine")]; !ok {
		t.Error("Signatures() dropped the learned fingerprint")
	}
}

func TestScan(t *testing.T) {
	events := []event.Event{
		failShell(1, "s", "go_test", "go test ./a"),         // 1st: silent
		{Seq: 2, Action: "go_test", Status: event.StatusOK}, // not friction
		failShell(3, "s", "go_test", "go test ./b"),         // 2nd: flag
		failShell(4, "s", "go_test", "go test ./c"),         // 3rd: flag
		failShell(5, "s", "git_push", "git push"),           // 1st distinct: silent
	}
	got := Scan(nil, events)
	if len(got) != 2 {
		t.Fatalf("Scan returned %d matches, want 2", len(got))
	}
	if got[0].Occurrence != 2 || got[1].Occurrence != 3 {
		t.Errorf("occurrences = %d,%d want 2,3", got[0].Occurrence, got[1].Occurrence)
	}
}
