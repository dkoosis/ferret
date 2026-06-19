package score

import (
	"encoding/json"
	"testing"
)

// TestTerminalAction is the unit table for the terminal-action detector: only
// the mutating/terminal VCS subset fires, read-only VCS and non-VCS shells do
// not, non-shell tool tokens are ignored, and the LAST terminal action in the
// shape wins (the action that closed the task).
func TestTerminalAction(t *testing.T) {
	cases := []struct {
		name       string
		shape      []string
		wantSignal string
		wantOK     bool
	}{
		{"commit fires", []string{"Edit", "sh:git_commit"}, "sh:git_commit", true},
		{"push fires", []string{"sh:git_push"}, "sh:git_push", true},
		{"gh pr fires", []string{"sh:gh_pr"}, "sh:gh_pr", true},
		{"merge fires", []string{"sh:git_merge"}, "sh:git_merge", true},
		{"read-only vcs does not fire", []string{"sh:git_status", "sh:git_diff"}, "", false},
		{"non-vcs shell does not fire", []string{"sh:go_test", "sh:rg"}, "", false},
		{"tool tokens ignored", []string{"Read", "Edit", "mcp:trixi.set_nug"}, "", false},
		{"empty shape", nil, "", false},
		{"last terminal action wins", []string{"sh:git_commit", "Edit", "sh:git_push"}, "sh:git_push", true},
		{"read-only between mutations still last-mutation", []string{"sh:git_commit", "sh:git_status"}, "sh:git_commit", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sig, ok := terminalAction(c.shape)
			if sig != c.wantSignal || ok != c.wantOK {
				t.Errorf("terminalAction(%v) = (%q, %v), want (%q, %v)",
					c.shape, sig, ok, c.wantSignal, c.wantOK)
			}
		})
	}
}

// TestLabelOutcomes verifies the post-pass attaches a positive label only to the
// segment that owns a terminal VCS action, leaves the others nil (absence, not a
// negative label), and is byte-stable across repeated runs.
func TestLabelOutcomes(t *testing.T) {
	res := Result{Segments: []Segment{
		{Index: 1, Shape: []string{"Read", "Edit"}},                        // no terminal action
		{Index: 2, Shape: []string{"Edit", "sh:go_test", "sh:git_commit"}}, // ships
		{Index: 3, Shape: []string{"sh:git_status"}},                       // read-only only
	}}
	LabelOutcomes(&res)

	if res.Segments[0].Outcome != nil {
		t.Errorf("seg1 outcome = %+v, want nil (no terminal action)", res.Segments[0].Outcome)
	}
	if got := res.Segments[1].Outcome; got == nil || !got.Positive || got.Signal != "sh:git_commit" {
		t.Errorf("seg2 outcome = %+v, want {Positive:true Signal:sh:git_commit}", got)
	}
	if res.Segments[2].Outcome != nil {
		t.Errorf("seg3 outcome = %+v, want nil (read-only vcs is not a ship)", res.Segments[2].Outcome)
	}

	// idempotent + byte-stable: re-running yields the identical marshaled form.
	first := mustMarshal(t, res)
	LabelOutcomes(&res)
	if second := mustMarshal(t, res); second != first {
		t.Errorf("LabelOutcomes not idempotent:\nfirst  %s\nsecond %s", first, second)
	}
}

// mustMarshal JSON-encodes v or fails the test (errchkjson + a stable helper for
// the byte-stability assertions below).
func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// outcomeFixtureLines is a two-prompt session: task 1 reads + edits then commits
// (ships), task 2 only edits (no terminal action). It exercises per-task
// attribution end to end through SegmentSource.
func outcomeFixtureLines() []string {
	return []string{
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"fix the bug and commit"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"a.go"}},` +
			`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git commit -m fix"}}]}}`,
		`{"type":"user","sessionId":"s","message":{"role":"user","content":"now start the next thing"}}`,
		`{"type":"assistant","sessionId":"s","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"b.go"}}]}}`,
	}
}

// TestSegmentSourceOutcome is the integration contract: the terminal git_commit
// attributes a positive-outcome label to its owning task (the one that ran it),
// the task with no terminal action gets none, and the labeled emission is
// byte-stable across runs.
func TestSegmentSourceOutcome(t *testing.T) {
	src := writeSession(t, outcomeFixtureLines())
	res, err := SegmentSource(src)
	if err != nil {
		t.Fatalf("SegmentSource: %v", err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("want 2 segments, got %d", len(res.Segments))
	}
	if got := res.Segments[0].Outcome; got == nil || !got.Positive || got.Signal != "sh:git_commit" {
		t.Errorf("seg1 outcome = %+v, want shipped via sh:git_commit", got)
	}
	if res.Segments[1].Outcome != nil {
		t.Errorf("seg2 outcome = %+v, want nil (no terminal action)", res.Segments[1].Outcome)
	}

	// byte-stable: the label is a pure function of the transcript.
	want := mustMarshal(t, res)
	for i := range 3 {
		got, gerr := SegmentSource(src)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if mustMarshal(t, got) != want {
			t.Fatalf("run %d not byte-stable", i)
		}
	}
}
