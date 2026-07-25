package fixes

import "testing"

// selfAuditFixtureText mirrors the ferret-kuv.16 evidence session (43ad3b27,
// synthetic — dk's live session isn't a repo fixture): an assistant self-audit
// passage with a call-count confession plus two named substitution
// admissions, one per waste archetype this package detects.
const selfAuditFixtureText = `Floor was 4; I spent 6 tool calls this task. ` +
	`Two waste items worth naming. ` +
	`I did a redundant second ask after a get miss - search alone would have sufficed. ` +
	`I also split one nug read into head -120 then sed 120,220 where one full get was known-needed.`

// ordinaryFixtureText is a normal assistant turn with no self-audit content —
// the negative golden the bead's acceptance criteria requires (zero
// proposals on a session with no self-audit text).
const ordinaryFixtureText = `Let me read the file and implement the fix, then run the tests to confirm.`

// TestDetectProposals_ReturnsBothArchetypes_When_SelfAuditConfessesKnownWaste
// is the positive golden: a session self-audit naming both confessed-waste
// archetypes yields exactly one proposal per archetype, each with the
// intent+better fields the confession itself names.
func TestDetectProposals_ReturnsBothArchetypes_When_SelfAuditConfessesKnownWaste(t *testing.T) {
	got := DetectProposals("43ad3b27", selfAuditFixtureText)
	if len(got) != 2 {
		t.Fatalf("got %d proposals, want 2: %+v", len(got), got)
	}

	var sawAskAfterMiss, sawSplitRead bool
	for _, p := range got {
		if p.Session != "43ad3b27" {
			t.Errorf("proposal %+v: session = %q, want 43ad3b27", p, p.Session)
		}
		switch p.IntentClass {
		case "ask-after-miss":
			sawAskAfterMiss = true
			if p.Better != "search alone" {
				t.Errorf("ask-after-miss better = %q, want %q", p.Better, "search alone")
			}
		case "split-read":
			sawSplitRead = true
			if p.Better != "one full get" {
				t.Errorf("split-read better = %q, want %q", p.Better, "one full get")
			}
		default:
			t.Errorf("unexpected intentClass %q", p.IntentClass)
		}
	}
	if !sawAskAfterMiss {
		t.Error("missing ask-after-miss proposal (redundant ask after a get miss -> search alone)")
	}
	if !sawSplitRead {
		t.Error("missing split-read proposal (split nug read -> one full get)")
	}
}

// TestDetectProposals_ReturnsNil_When_NoSelfAuditTell is the negative golden:
// ordinary assistant text with no self-audit signal yields zero proposals,
// even though it names tools (read/fix) that in isolation are harmless.
func TestDetectProposals_ReturnsNil_When_NoSelfAuditTell(t *testing.T) {
	got := DetectProposals("normalsess", ordinaryFixtureText)
	if got != nil {
		t.Errorf("got %d proposals, want nil: %+v", len(got), got)
	}
}

// TestDetectProposals_ReturnsNil_When_WasteWordsButNoSpecificArchetype covers
// a sentence that trips the self-audit gate (contains "wasted") but doesn't
// match either specific waste archetype — the gate alone isn't sufficient,
// bias precision over recall.
func TestDetectProposals_ReturnsNil_When_WasteWordsButNoSpecificArchetype(t *testing.T) {
	got := DetectProposals("sess", "That was a wasted turn, moving on to the next task now.")
	if got != nil {
		t.Errorf("got %d proposals, want nil (no specific archetype matched): %+v", len(got), got)
	}
}

// TestSelfAuditTell reports the three self-audit signal shapes fire, and
// ordinary text doesn't.
func TestSelfAuditTell(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"call-count comparison", "Floor was 4; I spent 6.", true},
		{"waste word: redundant", "That second call was redundant.", true},
		{"waste word: should have", "I shouldn't have re-read the file.", true},
		{"numbered waste list", "1. wasted a call re-reading the same file\n2. moved on", true},
		{"ordinary text", ordinaryFixtureText, false},
		{"empty text", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelfAuditTell(tc.text); got != tc.want {
				t.Errorf("SelfAuditTell(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestProposal_ToSubstitution_CarriesFieldsExactly ensures the Substitution
// projection carries IntentClass/Better/Example/Session verbatim, since that
// is the exact shape dk's confirm step (`ferret fixes sub`) records — WrongTool
// is intentionally left blank (regex can't reliably isolate it from prose).
func TestProposal_ToSubstitution_CarriesFieldsExactly(t *testing.T) {
	p := Proposal{
		IntentClass: "ask-after-miss",
		Better:      "search alone",
		Example:     "example sentence",
		Session:     "sess1",
		Tell:        "redundant-ask-after-miss",
	}
	sub := p.ToSubstitution()
	if sub.IntentClass != p.IntentClass || sub.Better != p.Better || sub.Example != p.Example || sub.Session != p.Session {
		t.Errorf("ToSubstitution() = %+v, want fields matching %+v", sub, p)
	}
	if sub.WrongTool != "" {
		t.Errorf("WrongTool = %q, want empty (not derivable from prose)", sub.WrongTool)
	}
}
