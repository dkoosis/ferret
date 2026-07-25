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

// TestWriteProposalsText_ShellQuotesExample_When_ConfessionCarriesMetacharacters
// guards against command injection via copy-paste (Codex flagged this shape on
// PR #93, though its own claimed fix never actually landed — this is the real
// one): a confessed-waste sentence is transcript-derived text, not a literal
// ferret authored, so it can carry $(...), backticks, or a bare " that a naive
// %q-quoted "confirm:" line would let the shell interpret when dk pastes it.
func TestWriteProposalsText_ShellQuotesExample_When_ConfessionCarriesMetacharacters(t *testing.T) {
	root := t.TempDir()
	evil := `I did a redundant ask after a get miss - search alone would have sufficed ` +
		"$(touch /tmp/pwned) `touch /tmp/pwned2` and it's bad"
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-evil.jsonl", []string{
		`{"type":"user","sessionId":"sess-evil","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","sessionId":"sess-evil","message":{"role":"assistant","content":[` +
			`{"type":"text","text":` + mustJSONString(t, evil) + `}]}}`,
	})

	var buf bytes.Buffer
	if err := runFixesProposals(&buf, root, "sess-evil", fmtText); err != nil {
		t.Fatalf("runFixesProposals: %v", err)
	}
	out := buf.String()
	confirmLine := ""
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "confirm:") {
			confirmLine = line
			break
		}
	}
	if confirmLine == "" {
		t.Fatalf("no confirm: line in output:\n%s", out)
	}
	wantExample := "'" + strings.Replace(evil, "'", `'\''`, 1) + "'"
	if !strings.Contains(confirmLine, "--example "+wantExample) {
		t.Errorf("--example arg not single-quoted as one literal:\nwant substring: %s\ngot: %s", wantExample, confirmLine)
	}
}

// TestShellQuote_RoundTrips_MetacharactersAndApostrophes is shellQuote's own
// unit coverage: every output must be safe to pass, unmodified, to `sh -c`.
func TestShellQuote_RoundTrips_MetacharactersAndApostrophes(t *testing.T) {
	cases := []string{
		"plain",
		"$(rm -rf /)",
		"`whoami`",
		"it's got an apostrophe",
		"a; b && c || d",
		"",
	}
	for _, s := range cases {
		q := shellQuote(s)
		if len(q) < 2 || q[0] != '\'' || q[len(q)-1] != '\'' {
			t.Errorf("shellQuote(%q) = %q, want outer single quotes", s, q)
		}
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
