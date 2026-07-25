package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// proposalsSelfAuditLine is one assistant turn's content, synthesized to
// mirror the ferret-kuv.16 evidence session (43ad3b27, synthetic — dk's live
// session isn't a repo fixture): a call-count self-audit plus two named
// substitution admissions, one per waste archetype ferret detects.
const proposalsSelfAuditLine = `Floor was 4; I spent 6 tool calls this task. ` +
	`I did a redundant second ask after a get miss - search alone would have sufficed. ` +
	`I also split one nug read into head -120 then sed 120,220 where one full get was known-needed.`

// TestRunFixesProposals_SurfacesBothConfessions_When_SessionHasSelfAudit is
// the AC-shaped positive golden: on a session whose assistant text confesses
// both waste archetypes, ferret surfaces >=1 proposed substitution per
// archetype with intent+better fields matching the confession, and NOTHING
// is auto-recorded into the substitution ledger (fixes.RecordSub is never
// called from this path).
func TestRunFixesProposals_SurfacesBothConfessions_When_SessionHasSelfAudit(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-waste.jsonl", []string{
		`{"type":"user","sessionId":"sess-waste","message":{"role":"user","content":"reproduce the recall bug"}}`,
		`{"type":"assistant","sessionId":"sess-waste","message":{"role":"assistant","content":[` +
			`{"type":"text","text":` + mustJSONString(t, proposalsSelfAuditLine) + `}]}}`,
	})

	var buf bytes.Buffer
	if err := runFixesProposals(&buf, root, "sess-waste", fmtText); err != nil {
		t.Fatalf("runFixesProposals: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ask-after-miss -> search alone") {
		t.Errorf("missing ask-after-miss -> search alone proposal:\n%s", out)
	}
	if !strings.Contains(out, "split-read -> one full get") {
		t.Errorf("missing split-read -> one full get proposal:\n%s", out)
	}
	if !strings.Contains(out, "count=2") {
		t.Errorf("header should report count=2:\n%s", out)
	}
	// Confirm-loop only: never record automatically. The rendered command is a
	// TEMPLATE dk runs by hand — this path itself must not touch the ledger.
	if strings.Contains(out, "recorded substitution") || strings.Contains(out, "bumped substitution") {
		t.Errorf("proposals output must not read as an auto-recorded ledger write:\n%s", out)
	}

	var jbuf bytes.Buffer
	if err := runFixesProposals(&jbuf, root, "sess-waste", fmtJSON); err != nil {
		t.Fatalf("runFixesProposals json: %v", err)
	}
	var res struct {
		Session   string `json:"session"`
		Proposals []struct {
			IntentClass string `json:"intentClass"`
			Better      string `json:"better"`
		} `json:"proposals"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(jbuf.Bytes(), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, jbuf.String())
	}
	if res.Total != 2 || len(res.Proposals) != 2 {
		t.Fatalf("json total/len = %d/%d, want 2/2: %+v", res.Total, len(res.Proposals), res)
	}
}

// TestRunFixesProposals_ReturnsZero_When_SessionHasNoSelfAudit is the negative
// golden the acceptance criteria calls out explicitly: an ordinary session
// with no self-audit text yields zero proposals.
func TestRunFixesProposals_ReturnsZero_When_SessionHasNoSelfAudit(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-clean.jsonl", []string{
		`{"type":"user","sessionId":"sess-clean","message":{"role":"user","content":"add a helper function"}}`,
		`{"type":"assistant","sessionId":"sess-clean","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Done, added the helper and ran the tests."}]}}`,
	})

	var buf bytes.Buffer
	if err := runFixesProposals(&buf, root, "sess-clean", fmtText); err != nil {
		t.Fatalf("runFixesProposals: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "count=0") {
		t.Errorf("expected count=0 for a session with no self-audit text:\n%s", out)
	}

	var jbuf bytes.Buffer
	if err := runFixesProposals(&jbuf, root, "sess-clean", fmtJSON); err != nil {
		t.Fatalf("runFixesProposals json: %v", err)
	}
	var res struct {
		Proposals []any `json:"proposals"`
		Total     int   `json:"total"`
	}
	if err := json.Unmarshal(jbuf.Bytes(), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, jbuf.String())
	}
	if res.Total != 0 || len(res.Proposals) != 0 {
		t.Errorf("json total/len = %d/%d, want 0/0", res.Total, len(res.Proposals))
	}
}

// mustJSONString marshals s as a JSON string literal, so an inline transcript
// fixture line can embed arbitrary text (quotes, dashes) without hand-escaping.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
