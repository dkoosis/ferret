package fixes

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSessionBaselineRoundTrip: the session and occurrence baselines persist in
// the ledger row and load back.
func TestSessionBaselineRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	want := Entry{Motif: "Skill,sh:git_add", Fix: "sdlc-xxx", AddedAt: time.Now(), BaselineBurn: 900,
		Lens: "cmd", BaselineSessions: 64, BaselineCount: 211}
	if err := Append(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %v, %v", got, err)
	}
	if got[0].BaselineSessions != 64 || got[0].BaselineCount != 211 {
		t.Errorf("loaded sessions/count = %d/%d, want 64/211", got[0].BaselineSessions, got[0].BaselineCount)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"baselineSessions":64`, `"baselineCount":211`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("row %s missing %s", raw, key)
		}
	}
}

// TestPreChangeRowLoadsWithoutSessionBaseline: a ledger row written before the
// session fields existed still loads, carries no session baseline, writes none
// back (omitzero), and yields no session arrow.
func TestPreChangeRowLoadsWithoutSessionBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixes.jsonl")
	old := `{"motif":"Edit!,Read","fix":"hookify","addedAt":"2026-06-12T00:00:00Z","baselineBurn":253000,"lens":"tool"}` + "\n"
	if err := appendRaw(path, old); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("Load = %v, %v", got, err)
	}
	e := got[0]
	if e.HasSessionBaseline() || e.BaselineSessions != 0 || e.BaselineCount != 0 {
		t.Errorf("old row has a session baseline: %+v", e)
	}
	if arr := (Annotation{Entry: e, Current: 1, CurrentSessions: 5}).SessionsArrow(); arr != "" {
		t.Errorf("old row SessionsArrow = %q, want empty", arr)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "baselineSessions") || strings.Contains(string(raw), "baselineCount") {
		t.Errorf("zero baselines must be omitted, got %s", raw)
	}
}

// TestSplitMotifKeyInvertsMotifKey: the key splits back to the exact tokens,
// including comma- and backslash-bearing ones.
func TestSplitMotifKeyInvertsMotifKey(t *testing.T) {
	for _, toks := range [][]string{
		{"Edit!", "Read"},
		{"a,b", "c"},
		{"a", "b,c"},
		{`x\y`, "z"},
		{"solo"},
		{"Skill", "sh:git_add", "sh:git_commit -q -F"},
	} {
		got := SplitMotifKey(MotifKey(toks))
		if !slices.Equal(got, toks) {
			t.Errorf("SplitMotifKey(MotifKey(%q)) = %q", toks, got)
		}
	}
}

// TestSessionsArrowIsIndependentOfBurn: sessions get their own arrow; burn
// rising while sessions fall (or the reverse) must not cross-contaminate.
func TestSessionsArrowIsIndependentOfBurn(t *testing.T) {
	e := Entry{BaselineBurn: 1000, BaselineSessions: 64}
	cases := []struct {
		burn, sess      int
		wantBurn, wantS string
	}{
		{500, 80, "↓", "↑"},
		{2000, 10, "↑", "↓"},
		{1000, 64, "=", "="},
	}
	for _, tc := range cases {
		a := Annotation{Entry: e, Current: tc.burn, CurrentSessions: tc.sess}
		if a.Arrow() != tc.wantBurn || a.SessionsArrow() != tc.wantS {
			t.Errorf("burn=%d sess=%d: arrows %q/%q, want %q/%q",
				tc.burn, tc.sess, a.Arrow(), a.SessionsArrow(), tc.wantBurn, tc.wantS)
		}
	}
}
