package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSearchFindsLiteralAcrossSessions is the core contract: a literal value
// shared in two different sessions — once in a user prompt, once inside a
// tool_result — is found in both, tagged by kind, and matched literally (no
// regex, no case-fold).
func TestSearchFindsLiteralAcrossSessions(t *testing.T) {
	root := t.TempDir()
	const email = "dave.k+test@example.com"
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-a.jsonl", []string{
		`{"type":"user","sessionId":"sess-a","message":{"role":"user","content":"my address is ` + email + ` thanks"}}`,
	})
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-b.jsonl", []string{
		`{"type":"user","sessionId":"sess-b","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"grep found ` + email + ` in config"}]}}`,
	})

	var buf bytes.Buffer
	if err := runSearch(&buf, root, email, searchOpts{format: "text", limit: 0, context: 0}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"sess-a", "sess-b", "[user]", "[rslt]", email} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	// Literal semantics: a regex metacharacter query must not match as a regex,
	// and matching is case-sensitive.
	for _, q := range []string{"da.e", "DAVE"} {
		var b bytes.Buffer
		if err := runSearch(&b, root, q, searchOpts{format: "text", limit: 0, context: 0}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "hits=0") {
			t.Errorf("query %q should match nothing (literal, case-sensitive), got:\n%s", q, b.String())
		}
	}
}

func TestSearchSnippetWindowsLargeEntry(t *testing.T) {
	const needle = "NEEDLE-EMAIL"
	pad := strings.Repeat("x", 3000)
	tail := strings.Repeat("y", 3000)

	// Buried mid-string: window is bounded and cut on both sides.
	mid := snippet(pad+needle+tail, needle)
	if !strings.Contains(mid, needle) {
		t.Fatalf("snippet dropped the match: %q", mid)
	}
	if len([]rune(mid)) > 400 {
		t.Errorf("snippet not bounded: %d runes", len([]rune(mid)))
	}
	if !strings.HasPrefix(mid, "…") || !strings.HasSuffix(mid, "…") {
		t.Errorf("expected … on both cut sides, got %q…%q", mid[:6], mid[len(mid)-6:])
	}

	// Match at the very start: no leading marker (marker only where text was cut).
	head := snippet(needle+tail, needle)
	if strings.HasPrefix(head, "…") {
		t.Errorf("no text was cut before the match; unexpected leading …: %q", head[:12])
	}
	if !strings.HasSuffix(head, "…") {
		t.Errorf("tail was cut; expected trailing …: %q", head)
	}

	// Match at the very end: trailing marker absent.
	end := snippet(pad+needle, needle)
	if strings.HasSuffix(end, "…") {
		t.Errorf("no text was cut after the match; unexpected trailing …: %q", end[len(end)-12:])
	}
}

func TestSearchShowsContext(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-c.jsonl", []string{
		`{"type":"user","sessionId":"sess-c","message":{"role":"user","content":"alpha"}}`,
		`{"type":"assistant","sessionId":"sess-c","message":{"role":"assistant","content":[{"type":"text","text":"bravo"}]}}`,
		`{"type":"user","sessionId":"sess-c","message":{"role":"user","content":"charlie MATCHHERE"}}`,
		`{"type":"assistant","sessionId":"sess-c","message":{"role":"assistant","content":[{"type":"text","text":"delta"}]}}`,
		`{"type":"user","sessionId":"sess-c","message":{"role":"user","content":"echo"}}`,
	})

	var buf bytes.Buffer
	if err := runSearch(&buf, root, "MATCHHERE", searchOpts{format: "text", limit: 0, context: 2}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"alpha", "bravo", "delta", "echo", "-2", "-1", "+1", "+2"} {
		if !strings.Contains(out, want) {
			t.Errorf("context output missing %q\n%s", want, out)
		}
	}
	mustPrecede(t, out, "alpha", "bravo")
	mustPrecede(t, out, "bravo", "delta")
	mustPrecede(t, out, "delta", "echo")
}

func TestSearchContextAtTranscriptStart(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-d.jsonl", []string{
		`{"type":"user","sessionId":"sess-d","message":{"role":"user","content":"MATCHHERE first"}}`,
		`{"type":"user","sessionId":"sess-d","message":{"role":"user","content":"second"}}`,
	})
	var buf bytes.Buffer
	if err := runSearch(&buf, root, "MATCHHERE", searchOpts{format: "text", limit: 0, context: 2}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "second") {
		t.Errorf("expected after-context %q\n%s", "second", out)
	}
	if strings.Contains(out, "-1") || strings.Contains(out, "-2") {
		t.Errorf("no before-context exists; unexpected -N marker\n%s", out)
	}
}

func TestSearchCapsHitsAndSignalsMore(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-e.jsonl", []string{
		`{"type":"user","sessionId":"sess-e","message":{"role":"user","content":"MATCH one"}}`,
		`{"type":"user","sessionId":"sess-e","message":{"role":"user","content":"MATCH two"}}`,
		`{"type":"user","sessionId":"sess-e","message":{"role":"user","content":"MATCH three"}}`,
		`{"type":"user","sessionId":"sess-e","message":{"role":"user","content":"MATCH four"}}`,
		`{"type":"user","sessionId":"sess-e","message":{"role":"user","content":"MATCH five"}}`,
	})

	var capped bytes.Buffer
	if err := runSearch(&capped, root, "MATCH", searchOpts{format: "text", limit: 2, context: 0}); err != nil {
		t.Fatal(err)
	}
	out := capped.String()
	if n := strings.Count(out, "── "); n != 2 {
		t.Errorf("expected 2 hit blocks, got %d\n%s", n, out)
	}
	if !strings.Contains(out, "hits=5") {
		t.Errorf("header should report total hits=5\n%s", out)
	}
	if !strings.Contains(out, "… +3 more hits (raise --limit)") {
		t.Errorf("missing more-hits tail\n%s", out)
	}

	var all bytes.Buffer
	if err := runSearch(&all, root, "MATCH", searchOpts{format: "text", limit: 0, context: 0}); err != nil {
		t.Fatal(err)
	}
	out = all.String()
	if n := strings.Count(out, "── "); n != 5 {
		t.Errorf("limit 0 should show all 5, got %d\n%s", n, out)
	}
	if strings.Contains(out, "more hits") {
		t.Errorf("limit 0 must not signal more hits\n%s", out)
	}
}

func TestSearchJSONShape(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-f.jsonl", []string{
		`{"type":"user","sessionId":"sess-f","message":{"role":"user","content":"MATCH one"}}`,
		`{"type":"user","sessionId":"sess-f","message":{"role":"user","content":"MATCH two"}}`,
		`{"type":"user","sessionId":"sess-f","message":{"role":"user","content":"MATCH three"}}`,
		`{"type":"user","sessionId":"sess-f","message":{"role":"user","content":"MATCH four"}}`,
		`{"type":"user","sessionId":"sess-f","message":{"role":"user","content":"MATCH five"}}`,
	})

	var buf bytes.Buffer
	if err := runSearch(&buf, root, "MATCH", searchOpts{format: "json", limit: 2, context: 2}); err != nil {
		t.Fatal(err)
	}
	var got searchResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Query != "MATCH" {
		t.Errorf("query = %q, want MATCH", got.Query)
	}
	if got.Total != 5 {
		t.Errorf("total = %d, want 5", got.Total)
	}
	if !got.Truncated {
		t.Error("truncated = false, want true")
	}
	if len(got.Hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(got.Hits))
	}
	h := got.Hits[0]
	if h.Session == "" || h.Project == "" || h.Line == 0 || h.Kind == "" {
		t.Errorf("hit missing location fields: %+v", h)
	}
	if !strings.Contains(h.Snippet, "MATCH") {
		t.Errorf("hit snippet missing match: %q", h.Snippet)
	}
	if got.Hits[1].Before == nil || got.Hits[1].After == nil {
		t.Errorf("before/after should be present arrays: %+v", got.Hits[1])
	}
}

func TestSearchValidation(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		format  string
		limit   int
		context int
		want    error
	}{
		{"empty query", "", "text", 20, 2, errSearchQueryRequired},
		{"whitespace query", "   ", "text", 20, 2, errSearchQueryRequired},
		{"bad format", "x", "xml", 20, 2, errBadFormat},
		{"negative context", "x", "text", 20, -1, errSearchBadContext},
		{"negative limit", "x", "text", -1, 2, errSearchBadLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			CLI.Search.Query = tc.query
			CLI.Search.Format = tc.format
			CLI.Search.Limit = tc.limit
			CLI.Search.Context = tc.context
			CLI.Search.Root = t.TempDir()
			if err := cmdSearch(); !errors.Is(err, tc.want) {
				t.Errorf("cmdSearch() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSearchNoMatch(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-g.jsonl", []string{
		`{"type":"user","sessionId":"sess-g","message":{"role":"user","content":"nothing to see"}}`,
	})
	var buf bytes.Buffer
	if err := runSearch(&buf, root, "absent-value", searchOpts{format: "text", limit: 20, context: 2}); err != nil {
		t.Fatalf("no-match should not error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "hits=0") {
		t.Errorf("expected hits=0 header\n%s", out)
	}
	if strings.Contains(out, "── ") {
		t.Errorf("no-match should emit no hit blocks\n%s", out)
	}
}
