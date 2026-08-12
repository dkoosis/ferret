package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
)

func TestWriteProposeTextRendersActionable(t *testing.T) {
	res := analyst.ProposeResult{
		Session: "abc123",
		Model:   "claude-sonnet-4-6",
		Proposals: []analyst.Proposal{
			{Task: 4, Kind: analyst.ProposeDeContext, Proposal: "replace full-file Reads with snipe pack", Why: "ow=0.69, 22KB output", Confidence: "high"},
			{Task: 2, Kind: analyst.ProposeAutomate, Proposal: "script the git add→commit→push chain", Why: "fixed sequence, 0 pivots", Confidence: "medium"},
			{Task: 7, Kind: analyst.ProposeNone, Proposal: "novel debugging, no recurring shape", Why: "one-off", Confidence: "low"},
		},
	}
	var buf bytes.Buffer
	if err := writeProposeText(&buf, res); err != nil {
		t.Fatalf("writeProposeText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "proposals=3 actionable=2") {
		t.Errorf("header should report 3 proposals / 2 actionable:\n%s", out)
	}
	if !strings.Contains(out, "snipe pack") || !strings.Contains(out, "git add→commit→push") {
		t.Errorf("actionable proposals missing:\n%s", out)
	}
	if !strings.Contains(out, "⚙") || !strings.Contains(out, "✂") {
		t.Errorf("lever glyphs missing:\n%s", out)
	}
	// The declined (none) proposal must NOT surface as an actionable row.
	if strings.Contains(out, "novel debugging") {
		t.Errorf("declined proposal leaked into actionable output:\n%s", out)
	}
	// AXI #9: actionable proposals chain to the fix ledger (ferret-8bb). The hint
	// is a fill-in template — labeled as such, with bare (unquoted) placeholders so
	// it can't paste into a shell and record literal <tokens>/<action> values.
	// Wording moved behind out.Sink.NextHead (ferret-nh6) so every command emits
	// one `next:` shape — the label now follows the prefix instead of splitting
	// it. The template's content and its bare placeholders are what this pins,
	// and both are unchanged.
	if !strings.Contains(out, "next: (template — fill from a proposal above) ferret fixes add --motif <tokens> --fix <action>") {
		t.Errorf("actionable output missing/altered next template line:\n%s", out)
	}
	if strings.Contains(out, `--motif "<tokens>"`) {
		t.Errorf("quoted placeholders would paste as a runnable command recording bogus ledger values:\n%s", out)
	}
}

func TestWriteProposeTextNoActionable(t *testing.T) {
	res := analyst.ProposeResult{
		Session:   "abc123",
		Model:     "claude-sonnet-4-6",
		Proposals: []analyst.Proposal{{Task: 1, Kind: analyst.ProposeNone, Proposal: "no fix", Why: "one-off"}},
	}
	var buf bytes.Buffer
	if err := writeProposeText(&buf, res); err != nil {
		t.Fatalf("writeProposeText: %v", err)
	}
	if !strings.Contains(buf.String(), "no actionable fixes proposed") {
		t.Errorf("expected no-actionable line:\n%s", buf.String())
	}
	// No actionable proposals → nothing to chain to (ferret-8bb).
	if strings.Contains(buf.String(), "next:") {
		t.Errorf("empty proposal set emitted a next line:\n%s", buf.String())
	}
}
