package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// qualitySessionLines is a two-task session: task 1 is tight (Read→Edit, one fresh
// kind per call), task 2 is a stuck read⇄search loop that runs to the boundary —
// enough to exercise both axes through the CLI.
func qualitySessionLines() []string {
	return []string{
		`{"type":"user","sessionId":"q","message":{"role":"user","content":"fix the typo"}}`,
		`{"type":"assistant","sessionId":"q","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a"}},` +
			`{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"a"}}]}}`,
		`{"type":"user","sessionId":"q","message":{"role":"user","content":"now find where it is used"}}`,
		`{"type":"assistant","sessionId":"q","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"b"}},` +
			`{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"rg foo"}},` +
			`{"type":"tool_use","id":"t5","name":"Read","input":{"file_path":"c"}},` +
			`{"type":"tool_use","id":"t6","name":"Bash","input":{"command":"rg bar"}},` +
			`{"type":"tool_use","id":"t7","name":"Read","input":{"file_path":"d"}},` +
			`{"type":"tool_use","id":"t8","name":"Bash","input":{"command":"rg baz"}}]}}`,
	}
}

func runQualitySession(t *testing.T, lines []string, format string) string {
	t.Helper()
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "q.jsonl", lines)
	var buf bytes.Buffer
	if err := qualitySession(&buf, root, "q", format); err != nil {
		t.Fatalf("qualitySession: %v", err)
	}
	return buf.String()
}

// TestQualitySessionJSON asserts the per-task axes contract: task 1 scores high on
// both axes, task 2 (a stuck loop) scores low efficiency and low adaptivity.
func TestQualitySessionJSON(t *testing.T) {
	out := runQualitySession(t, qualitySessionLines(), fmtJSON)
	var got struct {
		Session string `json:"session"`
		Tasks   []struct {
			Index      int     `json:"index"`
			Efficiency float64 `json:"efficiency"`
			Adaptivity float64 `json:"adaptivity"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Session != "q" {
		t.Errorf("session = %q, want q", got.Session)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2: %s", len(got.Tasks), out)
	}
	if got.Tasks[0].Efficiency != 1.0 || got.Tasks[0].Adaptivity != 1.0 {
		t.Errorf("task 1 = eff %v adapt %v, want 1/1 (tight, no churn)", got.Tasks[0].Efficiency, got.Tasks[0].Adaptivity)
	}
	if got.Tasks[1].Efficiency >= got.Tasks[0].Efficiency {
		t.Errorf("task 2 efficiency %v should be below task 1 %v (thrash)", got.Tasks[1].Efficiency, got.Tasks[0].Efficiency)
	}
	if got.Tasks[1].Adaptivity != 0.2 {
		t.Errorf("task 2 adaptivity = %v, want 0.2 (stuck loop to boundary)", got.Tasks[1].Adaptivity)
	}
}

// TestQualitySessionText checks the human rendering carries the about-line, a
// per-task row, and the roll-up.
func TestQualitySessionText(t *testing.T) {
	out := runQualitySession(t, qualitySessionLines(), "text")
	for _, want := range []string{
		"quality session=q project=-Users-dev-proj",
		"reference-free per-task axes (TRACE)",
		"[task 1] eff=1.00 adapt=1.00",
		"--- tasks=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
}

// TestQualitySessionDeterministic is the byte-stability gate: identical input →
// identical output across repeated runs, both formats.
func TestQualitySessionDeterministic(t *testing.T) {
	lines := qualitySessionLines()
	for _, format := range []string{"text", fmtJSON} {
		first := runQualitySession(t, lines, format)
		for i := range 5 {
			if got := runQualitySession(t, lines, format); got != first {
				t.Fatalf("format %s not byte-stable on run %d", format, i)
			}
		}
	}
}

// corpusShapeLines builds a single-task session of shape Read→Edit whose Edit
// reads `pad` bytes of input, so two sessions with different pad cluster as k=2
// with a measurable cost spread.
func corpusShapeLines(pad string) []string {
	return []string{
		`{"type":"user","sessionId":"c","message":{"role":"user","content":"recurring shape"}}`,
		`{"type":"assistant","sessionId":"c","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"r","name":"Read","input":{"file_path":"a"}},` +
			`{"type":"tool_use","id":"e","name":"Edit","input":{"file_path":"a","pad":"` + pad + `"}}]}}`,
	}
}

// TestQualityCorpusClusters asserts the corpus pass^k path clusters same-shape
// tasks across sessions (k=2 here) and emits the consistency block.
func TestQualityCorpusClusters(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-projA", "a.jsonl", corpusShapeLines("x"))
	writeSpineFixture(t, root, "-Users-dev-projB", "b.jsonl", corpusShapeLines(strings.Repeat("y", 200)))

	var buf bytes.Buffer
	if err := qualityCorpus(&buf, root, fmtJSON); err != nil {
		t.Fatalf("qualityCorpus: %v", err)
	}
	var got struct {
		Sessions int `json:"sessions"`
		Tasks    int `json:"tasks"`
		Clusters []struct {
			Shape       []string `json:"shape"`
			K           int      `json:"k"`
			Consistency float64  `json:"consistency"`
			Spread      float64  `json:"spread"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.Sessions != 2 || got.Tasks != 2 {
		t.Errorf("sessions=%d tasks=%d, want 2/2", got.Sessions, got.Tasks)
	}
	if len(got.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1 (both sessions share Read,Edit)", len(got.Clusters))
	}
	c := got.Clusters[0]
	if c.K != 2 {
		t.Errorf("cluster K = %d, want 2", c.K)
	}
	if c.Spread <= 0 {
		t.Errorf("spread = %v, want > 0 (the two attempts differ in cost)", c.Spread)
	}
	if c.Consistency < 0 || c.Consistency > 1 {
		t.Errorf("consistency %v out of [0,1]", c.Consistency)
	}
}
