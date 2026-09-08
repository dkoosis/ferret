package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/floor"
)

// TestRunFloor_SkipsSubagentTranscripts guards the scope boundary: a subagent
// transcript carries no SessionStart hook (the fork's own transcript starts
// mid-task, verified against a live corpus), so it must never be scanned as
// its own session floor.
func TestRunFloor_SkipsSubagentTranscripts(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-main.jsonl", []string{
		`{"type":"attachment","attachment":{"type":"hook_success","stdout":"banner"}}`,
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
	})
	subDir := filepath.Join(root, "-Users-dev-proj", "sess-main", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-x1.jsonl"),
		[]byte(`{"type":"user","message":{"role":"user","content":"the whole subagent task prompt"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := runFloor(root, "", "")
	if err != nil {
		t.Fatalf("runFloor: %v", err)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("Sessions = %d, want 1 (subagent transcript must be skipped)", len(rep.Sessions))
	}
	if rep.Sessions[0].Session != "sess-main" {
		t.Errorf("Sessions[0].Session = %q, want %q", rep.Sessions[0].Session, "sess-main")
	}
}

// TestRunFloor_MeasuresRuleFilesFromDisk pins the disclosed rule-file path:
// both tiers are read straight off disk via the supplied globs.
func TestRunFloor_MeasuresRuleFilesFromDisk(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-a.jsonl", []string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
	})
	rulesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rulesDir, "r1.md"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := runFloor(root, filepath.Join(rulesDir, "*.md"), filepath.Join(rulesDir, "*.md"))
	if err != nil {
		t.Fatalf("runFloor: %v", err)
	}
	if rep.RuleFilesGlobal != 1 || rep.RuleBytesGlobal != 10 {
		t.Errorf("global rule files = (%d, %d), want (1, 10)", rep.RuleFilesGlobal, rep.RuleBytesGlobal)
	}
	if rep.RuleFilesProject != 1 || rep.RuleBytesProject != 10 {
		t.Errorf("project rule files = (%d, %d), want (1, 10)", rep.RuleFilesProject, rep.RuleBytesProject)
	}
}

// floorTestReport builds a small hand-rolled report for the renderer tests
// below — three sessions of very different length sharing the same fixed
// hook cost, so the worst-share ranking has an unambiguous answer.
func floorTestReport() floor.Report {
	return floor.Report{
		Sessions: []floor.Session{
			{Session: "aaaaaaaa-short", Project: "proj", TranscriptBytes: 500, Cat: floor.Category{HookBytes: 300, ItzyBytes: 100, RosterBytes: 100}},
			{Session: "bbbbbbbb-long", Project: "proj", TranscriptBytes: 500000, Cat: floor.Category{HookBytes: 300, ItzyBytes: 100, RosterBytes: 100}},
		},
		RuleFilesGlobal: 8, RuleBytesGlobal: 4000,
		RuleFilesProject: 4, RuleBytesProject: 1000,
	}
}

// TestWriteFloorText_RanksWorstShareFirst pins the point of the command: the
// short session (small denominator, same fixed floor) must render ABOVE the
// long one, and the headline totals line must carry the floor/turn split.
func TestWriteFloorText_RanksWorstShareFirst(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFloorText(&buf, floorTestReport(), 0, 0); err != nil {
		t.Fatalf("writeFloorText: %v", err)
	}
	out := buf.String()

	shortIdx := strings.Index(out, "aaaaaaaa")
	longIdx := strings.Index(out, "bbbbbbbb")
	if shortIdx < 0 || longIdx < 0 {
		t.Fatalf("both sessions must render:\n%s", out)
	}
	if shortIdx > longIdx {
		t.Errorf("short session (worst share) must rank above the long one:\n%s", out)
	}
	for _, want := range []string{"floor", "turn", "rules: global 8 files", "share=", "worst="} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
}

// TestWriteFloorText_EmptyState pins the DK-AXI 0-results rule.
func TestWriteFloorText_EmptyState(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFloorText(&buf, floor.Report{}, 0, 0); err != nil {
		t.Fatalf("writeFloorText: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "0 sessions") {
		t.Errorf("missing explicit empty state:\n%s", out)
	}
}

// TestWriteFloorJSON_CarriesRollupAndRuleFiles guards the JSON contract: a
// consumer must not have to recompute Rollup itself to get the headline share.
func TestWriteFloorJSON_CarriesRollupAndRuleFiles(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFloorJSON(&buf, floorTestReport()); err != nil {
		t.Fatalf("writeFloorJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	totals, ok := doc["totals"].(map[string]any)
	if !ok {
		t.Fatalf("missing totals object:\n%s", buf.String())
	}
	if _, ok := totals["worstSharePct"]; !ok {
		t.Errorf("totals missing worstSharePct:\n%s", buf.String())
	}
	ruleFiles, ok := doc["ruleFiles"].(map[string]any)
	if !ok {
		t.Fatalf("missing ruleFiles object:\n%s", buf.String())
	}
	if _, ok := ruleFiles["global"]; !ok {
		t.Errorf("ruleFiles missing global tier:\n%s", buf.String())
	}
}

// TestApplyFloorLimit pins the three-state --limit contract (unset/N/negative)
// FloorCmd re-derives by hand since it carries no *common receiver.
func TestApplyFloorLimit(t *testing.T) {
	cases := map[int]int{0: floorDefaultLimit, 5: 5, -1: 0}
	for in, want := range cases {
		if got := applyFloorLimit(in); got != want {
			t.Errorf("applyFloorLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestCmdFloor_RejectsBadFormat pins the format-validation contract every
// other ferret command shares.
func TestCmdFloor_RejectsBadFormat(t *testing.T) {
	orig := CLI.Floor.Format
	defer func() { CLI.Floor.Format = orig }()
	CLI.Floor.Format = "yaml"
	if err := cmdFloor(); err == nil {
		t.Error("expected an error for --format yaml")
	}
}
