package event

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/transcript"
)

// writeTranscript drops lines into a temp .jsonl and returns its Source.
func writeTranscript(t *testing.T, lines ...string) transcript.Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sess.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return transcript.Source{Path: p, Project: "proj", Session: "sess"}
}

func toolUse(uuid, id, name, inputJSON string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":"2026-06-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`,
		uuid, id, name, inputJSON)
}

func toolResult(uuid, id string, isError bool) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-06-10T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":%v}]}}`,
		uuid, id, isError)
}

func toolResultContent(uuid, id, contentJSON string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-06-10T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%s}]}}`,
		uuid, id, contentJSON)
}

func ingest(t *testing.T, src transcript.Source) []*Event {
	t.Helper()
	b := NewBuilder()
	var evs []*Event
	if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestPairingAndStatus(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`),
		toolResult("u2", "t1", false),
		toolUse("u3", "t2", "Edit", `{"file_path":"a.go"}`),
		toolResult("u4", "t2", true),
		toolUse("u5", "t3", "Grep", `{"pattern":"x"}`), // never resolved
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	for i, want := range []string{StatusOK, StatusFail, StatusNone} {
		if evs[i].Status != want {
			t.Errorf("event %d (%s) status = %q, want %q", i, evs[i].Action, evs[i].Status, want)
		}
	}
	if evs[0].Target != "a.go" || evs[2].Target != "x" {
		t.Errorf("targets = %q, %q; want a.go, x", evs[0].Target, evs[2].Target)
	}
}

func TestBurnBytesCaptureInputPlusResult(t *testing.T) {
	input := `{"file_path":"a.go"}` // 20 bytes of tool_use input
	content := `"0123456789"`       // 12 bytes of tool_result payload (quotes included)
	src := writeTranscript(t,
		toolUse("u1", "t1", "Read", input),
		toolResultContent("u2", "t1", content),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	want := len(input) + len(content)
	if evs[0].Bytes != want {
		t.Errorf("Bytes = %d, want %d (input %d + result %d)", evs[0].Bytes, want, len(input), len(content))
	}
}

// TestGetNugQueryEpisodeCapture is the Decision-0 contract (ferret-sq.d0): a
// query-mode get_nug call captures its full query plus the returned nug ids and
// per-query scores in rank (result) order, a hit missing a score gets zero, a
// by-id fetch captures neither, and an errored/non-array result yields no hits.
func TestGetNugQueryEpisodeCapture(t *testing.T) {
	// result body = array-of-blocks whose text is the get_nug hit JSON array.
	hits := `[{"type":"text","text":"[{\"id\":\"aaa\",\"score\":0.5},{\"id\":\"bbb\",\"score\":0.3},{\"id\":\"ccc\"}]"}]`
	src := writeTranscript(t,
		toolUse("u1", "t1", toolGetNug, `{"query":"memory design","limit":5}`),
		toolResultContent("u2", "t1", hits),
		toolUse("u3", "t2", toolGetNug, `{"id":"1fe653da94e4"}`), // by-id fetch
		toolResultContent("u4", "t2", `"some nug body"`),
		toolUse("u5", "t3", toolGetNug, `{"query":"timed out one"}`),
		toolResultContent("u6", "t3", `"The operation timed out."`), // error → no hits
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}

	q := evs[0]
	if q.Query != "memory design" {
		t.Errorf("query = %q, want %q", q.Query, "memory design")
	}
	want := []NugHit{{ID: "aaa", Score: 0.5}, {ID: "bbb", Score: 0.3}, {ID: "ccc"}}
	if len(q.Results) != len(want) {
		t.Fatalf("results = %+v, want %+v", q.Results, want)
	}
	for i, w := range want {
		if q.Results[i] != w {
			t.Errorf("result[%d] = %+v, want %+v (rank order + score)", i, q.Results[i], w)
		}
	}

	if byID := evs[1]; byID.Query != "" || byID.Results != nil {
		t.Errorf("by-id fetch captured query=%q results=%+v, want neither", byID.Query, byID.Results)
	}
	if errored := evs[2]; errored.Query != "timed out one" || errored.Results != nil {
		t.Errorf("errored episode = query %q / results %+v, want query set, no hits", errored.Query, errored.Results)
	}
}

func TestBurnBytesSplitAcrossCompoundSegments(t *testing.T) {
	// One result resolves a 2-segment chain: its payload is split across both.
	content := `"01234567"` // 10 bytes → 5 each
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"a && b"}`),
		toolResultContent("u2", "t1", content),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	for _, ev := range evs {
		// each segment: its own command-segment bytes + half the result payload
		if ev.Bytes <= len(content)/2 {
			t.Errorf("segment %q Bytes = %d, want > %d (seg input + result share)", ev.Action, ev.Bytes, len(content)/2)
		}
	}
}

func TestBurnBytesConservedAcrossCompoundSegments(t *testing.T) {
	// 10-byte payload across 3 segments: integer division gives 3 each = 9,
	// dropping 1 byte. The remainder must be carried so no bytes vanish from burn.
	content := `"01234567"` // 10 bytes
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"a && b && c"}`),
		toolResultContent("u2", "t1", content),
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	var resultBytes int
	for _, ev := range evs {
		// each segment's bytes = its own command-segment len + a share of the payload
		resultBytes += ev.Bytes - len(ev.Detail)
	}
	if resultBytes != len(content) {
		t.Errorf("result bytes distributed = %d, want %d (remainder dropped)", resultBytes, len(content))
	}
}

// TestResumedForkDuplicateUseFreshResult covers the orphaned-pair byte loss:
// a resumed session copies a tool_use (same UUID → deduped, never parked) but
// its tool_result carries a fresh UUID, reaches resolve, finds nothing pending,
// and its bytes silently vanish from burn. Bytes must be conserved.
func TestResumedForkDuplicateUseFreshResult(t *testing.T) {
	use := toolUse("dupuse", "t1", "Read", `{"file_path":"a.go"}`)
	// File 1: original use + result, fully counted.
	src1 := writeTranscript(t, use, toolResult("r1", "t1", false))
	// File 2: resumed — same use UUID (deduped), but a NEW result UUID + payload.
	content := `"abcdefghij"` // 12 bytes of returned context
	src2 := writeTranscript(t, use, toolResultContent("r2", "t1", content))

	b := NewBuilder()
	var evs []*Event
	for _, src := range []transcript.Source{src1, src2} {
		if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	_ = evs
	// The orphaned fresh result must not inflate Unpaired (it is a result, not a use).
	if b.Stats.Unpaired != 0 {
		t.Errorf("Stats.Unpaired = %d, want 0 (a stray result must not be counted as an unpaired use)", b.Stats.Unpaired)
	}
	// The fresh result's bytes must be accounted, not silently dropped.
	if b.Stats.OrphanBytes != len(content) {
		t.Errorf("Stats.OrphanBytes = %d, want %d (fresh result payload lost)", b.Stats.OrphanBytes, len(content))
	}
}

// TestResumedForkDuplicateResultFreshUse covers the symmetric inflation case:
// the tool_result is deduped (same UUID), but the tool_use is fresh. The use is
// parked, never resolved, and counted as Unpaired — inflating the reported
// unpaired rate even though it WAS resolved in the original file.
func TestResumedForkDuplicateResultFreshUse(t *testing.T) {
	result := toolResult("dupres", "t1", false)
	src1 := writeTranscript(t, toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`), result)
	// File 2: fresh use UUID (parked), same result UUID (deduped → never resolves it).
	src2 := writeTranscript(t, toolUse("u2", "t1", "Read", `{"file_path":"a.go"}`), result)

	b := NewBuilder()
	var evs []*Event
	for _, src := range []transcript.Source{src1, src2} {
		if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	if b.Stats.Unpaired != 0 {
		t.Errorf("Stats.Unpaired = %d, want 0 (resumed use whose result was deduped should not inflate unpaired)", b.Stats.Unpaired)
	}
}

func TestCompoundFailureIsCFailNotFail(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./... && go build ./..."}`),
		toolResult("u2", "t1", true),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (split compound)", len(evs))
	}
	for _, ev := range evs {
		if !ev.Compound {
			t.Errorf("%s: Compound = false, want true", ev.Action)
		}
		if ev.Status != StatusCFail {
			t.Errorf("%s: status = %q, want %q — failing segment is unknown", ev.Action, ev.Status, StatusCFail)
		}
	}
}

func TestSingleSegmentFailureIsFail(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./..."}`),
		toolResult("u2", "t1", true),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Status != StatusFail {
		t.Errorf("status = %q, want %q", evs[0].Status, StatusFail)
	}
}

func TestDuplicateUUIDDedup(t *testing.T) {
	// Resumed sessions copy history: the same uuid in a second file must not double-count.
	line1 := toolUse("dup", "t1", "Read", `{"file_path":"a.go"}`)
	src1 := writeTranscript(t, line1, toolResult("r1", "t1", false))
	src2 := writeTranscript(t, line1) // copied history

	b := NewBuilder()
	var evs []*Event
	for _, src := range []transcript.Source{src1, src2} {
		if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (uuid dedup across files)", len(evs))
	}
	if b.Stats.Deduped != 1 {
		t.Errorf("Stats.Deduped = %d, want 1", b.Stats.Deduped)
	}
}

func TestPromptDetection(t *testing.T) {
	src := writeTranscript(t,
		`{"type":"user","uuid":"p1","timestamp":"2026-06-10T10:00:00Z","message":{"role":"user","content":"fix the bug"}}`,
		`{"type":"user","uuid":"p2","isMeta":true,"message":{"role":"user","content":"injected context"}}`,
		toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`),
		toolResult("u2", "t1", false), // tool_result user line: not a prompt
	)
	evs := ingest(t, src)
	prompts := 0
	var got string
	for _, ev := range evs {
		if ev.Kind == KindPrompt {
			prompts++
			got = ev.Prompt
		}
	}
	if prompts != 1 {
		t.Errorf("prompts = %d, want 1 (meta and tool_result lines are not prompts)", prompts)
	}
	if got != "fix the bug" {
		t.Errorf("prompt text = %q, want %q (full text captured at ingestion)", got, "fix the bug")
	}
}

func TestPromptTextCapture(t *testing.T) {
	// Multi-text-block user turn with messy whitespace: capture is full but
	// whitespace-collapsed, and never truncated.
	long := strings.Repeat("x", 5000)
	src := writeTranscript(t,
		`{"type":"user","uuid":"p1","timestamp":"2026-06-10T10:00:00Z","message":{"role":"user","content":[`+
			`{"type":"text","text":"  no,\n\n  not that  "},`+
			`{"type":"text","text":"`+long+`"}]}}`,
	)
	evs := ingest(t, src)
	if len(evs) != 1 || evs[0].Kind != KindPrompt {
		t.Fatalf("events = %d, want 1 prompt", len(evs))
	}
	want := "no, not that " + long
	if evs[0].Prompt != want {
		t.Errorf("prompt text len=%d, want len=%d (full, collapsed, untruncated)", len(evs[0].Prompt), len(want))
	}
}

func TestRetryAttribution(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"go test ./..."}`),
		toolResult("u2", "t1", true),
		toolUse("u3", "t2", "Bash", `{"command":"go test ./..."}`),
		toolResult("u4", "t2", false),
		toolUse("u5", "t3", "Bash", `{"command":"go test ./..."}`),
		toolResult("u6", "t3", false),
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	if !evs[1].Retry {
		t.Error("second go_test follows a failure — Retry should be true")
	}
	if evs[2].Retry {
		t.Error("third go_test follows a success — the retry window is closed")
	}
}

func TestSidechainFlag(t *testing.T) {
	src := writeTranscript(t,
		`{"type":"assistant","uuid":"s1","isSidechain":true,"timestamp":"2026-06-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}`,
	)
	evs := ingest(t, src)
	if len(evs) != 1 || !evs[0].Sidechain {
		t.Fatalf("want 1 sidechain event, got %+v", evs)
	}
}

// TestTruncRuneBoundary verifies trunc never splits a multibyte UTF-8 rune.
// A CJK string is all 3-byte runes, so a byte cut at a non-multiple-of-3
// offset lands mid-rune; trunc must walk back to a boundary.
func TestTruncRuneBoundary(t *testing.T) {
	s := strings.Repeat("世", 10) // 10 runes x 3 bytes = 30 bytes
	for n := 0; n <= len(s); n++ {
		got := trunc(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("trunc(%q, %d) = %q: not valid UTF-8", s, n, got)
		}
		if strings.ContainsRune(got, '�') {
			t.Fatalf("trunc(%q, %d) = %q: contains U+FFFD", s, n, got)
		}
		if len(got) > n {
			t.Fatalf("trunc(%q, %d) = %q: length %d exceeds cut %d", s, n, got, len(got), n)
		}
	}
}

// snipeApproxBit returns the Approx bit of the (single) snipe segment among
// evs, or false if no snipe segment is present.
func snipeApproxBit(evs []*Event) bool {
	for _, ev := range evs {
		if ev.Kind == KindShell && (ev.Action == "snipe" || strings.HasPrefix(ev.Action, "snipe_")) {
			return ev.Approx
		}
	}
	return false
}

// anyApprox reports whether any event carries the fallback bit — used to prove
// non-snipe results never get tagged.
func anyApprox(evs []*Event) bool {
	for _, ev := range evs {
		if ev.Approx {
			return true
		}
	}
	return false
}

// TestSnipeApproxMarker covers the tool-internal signal: snipe emits a single
// trailing "~approx" token on fuzzy/semantic fallback; silence means it served
// an exact match. ferret parses that token from the tool_result body it already
// holds and sets Event.Approx on the snipe segment only.
func TestSnipeApproxMarker(t *testing.T) {
	tests := []struct {
		name    string
		command string
		content string // raw JSON for the tool_result content field
		want    bool   // expected Approx on the snipe segment
	}{
		{
			name:    "fallback marker present",
			command: "snipe callers Foo",
			content: `"caller a\ncaller b\n~approx"`,
			want:    true,
		},
		{
			name:    "served exact, no marker",
			command: "snipe callers Foo",
			content: `"caller a\ncaller b\n"`,
			want:    false,
		},
		{
			name:    "marker as suffix of a word is not a trailing token",
			command: "snipe callers Foo",
			content: `"result~approx"`,
			want:    false,
		},
		{
			name:    "array-form content with trailing marker",
			command: "snipe pack Foo",
			content: `[{"type":"text","text":"sym dump\n~approx"}]`,
			want:    true,
		},
		{
			name:    "marker with trailing whitespace still counts",
			command: "snipe search Foo",
			content: `"hit\n~approx\n"`,
			want:    true,
		},
		{
			name:    "compound chain: snipe is the trailing segment",
			command: "git status && snipe callers Foo",
			content: `"clean\nhit\n~approx"`,
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := writeTranscript(t,
				toolUse("u1", "t1", "Bash", fmt.Sprintf(`{"command":%q}`, tc.command)),
				toolResultContent("u2", "t1", tc.content),
			)
			evs := ingest(t, src)
			if got := snipeApproxBit(evs); got != tc.want {
				t.Errorf("snipe Approx = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSnipeApproxNonSnipeUntagged proves the bit is gated on snipe: a non-snipe
// result whose body happens to end in the marker must never be tagged, and in a
// compound chain only a trailing snipe segment is tagged (not an earlier one).
func TestSnipeApproxNonSnipeUntagged(t *testing.T) {
	t.Run("non-snipe body ending in marker", func(t *testing.T) {
		src := writeTranscript(t,
			toolUse("u1", "t1", "Bash", `{"command":"rg Foo"}`),
			toolResultContent("u2", "t1", `"hit\n~approx"`),
		)
		if anyApprox(ingest(t, src)) {
			t.Error("non-snipe event tagged Approx; marker must be snipe-gated")
		}
	})
	t.Run("snipe not trailing segment: trailing token belongs to rg", func(t *testing.T) {
		src := writeTranscript(t,
			toolUse("u1", "t1", "Bash", `{"command":"snipe callers Foo && rg Foo"}`),
			toolResultContent("u2", "t1", `"hit\n~approx"`),
		)
		// The ~approx trails rg's output, not snipe's, so nothing is tagged.
		if anyApprox(ingest(t, src)) {
			t.Error("tagged Approx when snipe was not the trailing segment")
		}
	})
}
