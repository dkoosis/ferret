package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpineFixture drops a transcript at root/<slug>/<name> (transcript.Walk
// needs ≥2 path segments) so spine() can resolve it by session prefix.
func writeSpineFixture(t *testing.T, root, slug, name string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSpineEmitsOrderedSpine is the end-to-end shape check: an inline transcript
// with a prompt, thinking, assistant text, a tool call, a tool result, and a
// meta line that must be dropped. The spine must emit each kind in transcript
// order, with the result collapsed to status+size and the meta noise absent.
func TestSpineEmitsOrderedSpine(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-x.jsonl", []string{
		`{"type":"user","sessionId":"sess-x","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","sessionId":"sess-x","message":{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"I should grep first."},` +
			`{"type":"text","text":"Let me search."},` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"rg foo"}}]}}`,
		`{"type":"user","sessionId":"sess-x","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","is_error":false,"content":"matched 3 lines"}]}}`,
		`{"type":"user","isMeta":true,"sessionId":"sess-x","message":{"role":"user","content":"SYSTEM REMINDER noise"}}`,
	})

	var buf bytes.Buffer
	if err := spine(&buf, root, "sess-x"); err != nil {
		t.Fatalf("spine: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"session=sess-x project=-Users-dev-proj",
		"[user] do the thing",
		"[think] I should grep first.",
		"[asst] Let me search.",
		"[call] Bash  command=rg foo",
		"[rslt] ok",
		"prompts=1 thinking=1 text=1 calls=1 results=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("spine output missing %q\n---\n%s", want, out)
		}
	}

	// Meta lines are noise, not intent — they must never reach the spine, and the
	// meta "prompt" must not inflate the prompt count (asserted =1 above).
	if strings.Contains(out, "SYSTEM REMINDER") {
		t.Errorf("meta line leaked into spine:\n%s", out)
	}

	// Order is the contract: prompt → thinking → assistant text → call → result.
	mustPrecede(t, out, "[user]", "[think]")
	mustPrecede(t, out, "[think]", "[asst]")
	mustPrecede(t, out, "[asst]", "[call]")
	mustPrecede(t, out, "[call]", "[rslt]")
}

// TestSpineFrictionFixtureCounts runs the shipped friction fixture (1 prompt, 7
// tool calls, 7 results, 3 of them failures) and checks the call/result kinds
// and counts — proving spine reads the on-disk corpus layout, not just inline.
func TestSpineFrictionFixtureCounts(t *testing.T) {
	var buf bytes.Buffer
	if err := spine(&buf, filepath.Join("..", "..", "testdata", "corpus"), "sess-friction"); err != nil {
		t.Fatalf("spine: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"[user] the build is broken, fix it",
		"[call] Read  file_path=internal/parse/parse.go",
		"[call] Bash  command=go test ./...",
		"[rslt] FAIL",
		"prompts=1 thinking=0 text=0 calls=7 results=7",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("friction spine missing %q\n---\n%s", want, out)
		}
	}
}

// TestSpineNoMatch: an unresolvable session prefix is a loud error, not an empty
// spine — silence would read as "this session had no activity".
func TestSpineNoMatch(t *testing.T) {
	err := spine(&bytes.Buffer{}, t.TempDir(), "nope")
	if !errors.Is(err, errSpineNoMatch) {
		t.Errorf("err = %v, want errSpineNoMatch", err)
	}
}

// TestResolveSpineSourcePrefersRootSession: a session and its own subagent
// transcript share a session id. The root-session file (no agent) must be the
// one emitted, and the two files must count as ONE distinct session (so the
// ambiguity warning does not misfire).
func TestResolveSpineSourcePrefersRootSession(t *testing.T) {
	root := t.TempDir()
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-explore.jsonl", []string{
		`{"type":"user","sessionId":"sess-explore","message":{"role":"user","content":"hi"}}`,
	})
	writeSpineFixture(t, root, filepath.Join("-Users-dev-proj", "sess-explore", "subagents"),
		"agent-explore-01.jsonl", []string{
			`{"type":"user","sessionId":"sess-explore","message":{"role":"user","content":"sub"}}`,
		})

	src, distinct, err := resolveSpineSource(root, "sess-explore")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if src.Agent != "" {
		t.Errorf("chose subagent file (agent=%q), want root session", src.Agent)
	}
	if distinct != 1 {
		t.Errorf("distinct sessions = %d, want 1 (session + its subagent are one session)", distinct)
	}
}

// TestRenderArgs: salient-key extraction keeps the spine readable — the bash
// command, the file path — and unquotes JSON strings; a non-object input renders
// raw; empty input is empty.
func TestRenderArgs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bash command", `{"command":"go test ./..."}`, "command=go test ./..."},
		{"read file_path", `{"file_path":"internal/x.go"}`, "file_path=internal/x.go"},
		{"grep pattern preferred over path", `{"pattern":"func Foo","path":"x"}`, "pattern=func Foo"},
		{"multiline command collapses", `{"command":"a\nb"}`, "command=a b"},
		{"non-object renders raw", `"bare string"`, `"bare string"`},
		{"empty input is empty", ``, ""},
		{"no salient key falls back to object", `{"zzz":1}`, `{"zzz":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderArgs(json.RawMessage(tc.input)); got != tc.want {
				t.Errorf("renderArgs(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestTruncateRunes: caps at the rune budget with a drop marker and never splits
// a multibyte rune (a byte-level cut would corrupt the output).
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("short", 10); got != "short" {
		t.Errorf("under cap mutated: %q", got)
	}
	if got := truncateRunes("abcdef", 3); got != "abc…(+3)" {
		t.Errorf("truncateRunes = %q, want abc…(+3)", got)
	}
	// Multibyte: 6 runes capped at 3 must keep 3 whole runes, not 3 bytes.
	if got := truncateRunes("日本語あいう", 3); got != "日本語…(+3)" {
		t.Errorf("multibyte truncate = %q, want 日本語…(+3)", got)
	}
}

// TestHumanBytes: glance-readable magnitudes across the B/KB/MB boundaries.
func TestHumanBytes(t *testing.T) {
	for in, want := range map[int]string{
		0:       "0B",
		512:     "512B",
		1024:    "1.0KB",
		1536:    "1.5KB",
		1048576: "1.0MB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// mustPrecede asserts the first occurrence of a comes before the first of b.
func mustPrecede(t *testing.T, s, a, b string) {
	t.Helper()
	ia, ib := strings.Index(s, a), strings.Index(s, b)
	if ia < 0 || ib < 0 {
		t.Fatalf("ordering check: %q (%d) or %q (%d) missing", a, ia, b, ib)
	}
	if ia >= ib {
		t.Errorf("expected %q before %q (got %d >= %d)", a, b, ia, ib)
	}
}
