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
	if err := spine(&buf, root, "sess-x", nil); err != nil {
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
	if err := spine(&buf, filepath.Join("..", "..", "testdata", "corpus"), "sess-friction", nil); err != nil {
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
	err := spine(&bytes.Buffer{}, t.TempDir(), "nope", nil)
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
			if got := renderArgs(json.RawMessage(tc.input), nil); got != tc.want {
				t.Errorf("renderArgs(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestRenderArgsPlaceholderMapping is the core ferret-5c0 behavior at the
// renderArgs level: a repeated volatile value renders in full on first sight
// (that's what establishes what the token means) and as a short stable token
// on every repeat within the same placeholderTable — and the token reverses
// back to the original value for human-facing output.
func TestRenderArgsPlaceholderMapping(t *testing.T) {
	ph := newPlaceholderTable()
	input := json.RawMessage(`{"file_path":"/Users/vcto/Projects/ferret/cmd/ferret/spine.go"}`)
	const full = "file_path=/Users/vcto/Projects/ferret/cmd/ferret/spine.go"

	if got := renderArgs(input, ph); got != full {
		t.Errorf("first occurrence = %q, want full value %q", got, full)
	}
	for i := 0; i < 3; i++ {
		if got := renderArgs(input, ph); got != "file_path=[P1]" {
			t.Errorf("repeat %d = %q, want file_path=[P1]", i, got)
		}
	}

	// A second, distinct volatile value mints the next token in sequence.
	input2 := json.RawMessage(`{"file_path":"/Users/vcto/Projects/ferret/cmd/ferret/adjudicate.go"}`)
	if got := renderArgs(input2, ph); got != "file_path=/Users/vcto/Projects/ferret/cmd/ferret/adjudicate.go" {
		t.Errorf("first occurrence of second value = %q, want full", got)
	}
	if got := renderArgs(input2, ph); got != "file_path=[P2]" {
		t.Errorf("repeat of second value = %q, want file_path=[P2]", got)
	}

	// Reverse mapping restores the original value — dk must never see a raw
	// token, only the LLM prompt should.
	if got := ph.expand("call referenced file_path=[P1] again"); got != "call referenced "+full+" again" {
		t.Errorf("expand = %q", got)
	}
}

// TestRenderArgsNilPlaceholderTableUnaffected proves the human/non-prompt path
// (nil placeholder table) is untouched by ferret-5c0: the SCOPE GUARDRAIL — no
// mapping, no token, full value every time.
func TestRenderArgsNilPlaceholderTableUnaffected(t *testing.T) {
	input := json.RawMessage(`{"path":"/same/path"}`)
	first := renderArgs(input, nil)
	second := renderArgs(input, nil)
	if first != "path=/same/path" || second != "path=/same/path" {
		t.Errorf("nil placeholder table must render the full value every time, got %q then %q", first, second)
	}
}

// TestSpineTokenizesRepeatedVolatileFieldWhenPlaceholderTableGiven is the
// end-to-end regression the bead's AC asks for: across N tool calls sharing one
// volatile file_path, a prompt-bound render (non-nil placeholder table)
// collapses to one full-value occurrence + token repeats, reverse-mapping
// restores the original, and the human `ferret spine` path (nil table) is
// unaffected — same fixture, full value on every occurrence.
func TestSpineTokenizesRepeatedVolatileFieldWhenPlaceholderTableGiven(t *testing.T) {
	root := t.TempDir()
	const path = "/Users/vcto/Projects/ferret/cmd/ferret/spine.go"
	writeSpineFixture(t, root, "-Users-dev-proj", "sess-tok.jsonl", []string{
		`{"type":"user","sessionId":"sess-tok","message":{"role":"user","content":"read it twice"}}`,
		`{"type":"assistant","sessionId":"sess-tok","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"` + path + `"}}]}}`,
		`{"type":"user","sessionId":"sess-tok","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","is_error":false,"content":"body"}]}}`,
		`{"type":"assistant","sessionId":"sess-tok","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"` + path + `"}}]}}`,
		`{"type":"user","sessionId":"sess-tok","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t2","is_error":false,"content":"body"}]}}`,
	})

	ph := newPlaceholderTable()
	var buf bytes.Buffer
	if err := spine(&buf, root, "sess-tok", ph); err != nil {
		t.Fatalf("spine: %v", err)
	}
	promptOut := buf.String()

	if !strings.Contains(promptOut, "file_path="+path) {
		t.Errorf("first occurrence must render the full value:\n%s", promptOut)
	}
	if !strings.Contains(promptOut, "file_path=[P1]") {
		t.Errorf("repeat occurrence must render the short token:\n%s", promptOut)
	}
	if n := strings.Count(promptOut, path); n != 1 {
		t.Errorf("full value must appear exactly once in the prompt-bound render (repeat must tokenize), got %d:\n%s", n, promptOut)
	}
	if got := ph.expand("file_path=[P1]"); got != "file_path="+path {
		t.Errorf("expand did not restore the original value: %q", got)
	}

	// Human path: same fixture, nil placeholder table — SCOPE GUARDRAIL, must
	// show the full value on EVERY occurrence, unaffected by the mechanism.
	var humanBuf bytes.Buffer
	if err := spine(&humanBuf, root, "sess-tok", nil); err != nil {
		t.Fatalf("spine (human path): %v", err)
	}
	humanOut := humanBuf.String()
	if n := strings.Count(humanOut, path); n != 2 {
		t.Errorf("human path must render the full value every time (nil placeholder table), got %d:\n%s", n, humanOut)
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
