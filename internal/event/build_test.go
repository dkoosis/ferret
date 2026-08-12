package event

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/snipeusage"
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

// toolResultErrContent is toolResultContent with is_error set — a failed call
// that still carries a result body (an error message).
func toolResultErrContent(uuid, id, contentJSON string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-06-10T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":true,"content":%s}]}}`,
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

// ingestWithUsage is ingest with a snipeusage.Index wired in via SetUsage —
// used by the usage-join enrichment tests (sn-r1do.2). Also returns the
// Builder's Stats so a test can assert on UsageJoined.
func ingestWithUsage(t *testing.T, src transcript.Source, idx *snipeusage.Index) ([]*Event, *Stats) {
	t.Helper()
	b := NewBuilder()
	b.SetUsage(idx)
	var evs []*Event
	if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
		t.Fatal(err)
	}
	return evs, b.Stats
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

// TestSwallowedErrorMarksLeftArmAndRecordsOK is the ferret-cax wiring test:
// `cmd 2>/dev/null || fallback` exits 0, so Status stays ok and no misfire is
// recorded — the failure is only visible through Swallow, which must land on
// the left arm (the command whose error was discarded) and not on the
// fallback that ran in its place.
func TestSwallowedErrorMarksLeftArmAndRecordsOK(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"bd show x 2>/dev/null || bd list"}`),
		toolResult("u2", "t1", false),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (split compound)", len(evs))
	}
	want := map[string]bool{"bd_show": true, "bd_list": false}
	for _, ev := range evs {
		if ev.Status != StatusOK {
			t.Errorf("%s: status = %q, want %q — the chain exits with the fallback's code",
				ev.Action, ev.Status, StatusOK)
		}
		if w, ok := want[ev.Action]; !ok {
			t.Errorf("unexpected action %q", ev.Action)
		} else if ev.Swallow != w {
			t.Errorf("%s: Swallow = %v, want %v", ev.Action, ev.Swallow, w)
		}
	}
}

// TestUnswallowedCompoundLeavesSwallowUnset pins the negative: an `&&` chain
// with the same redirect still surfaces its failure, so nothing is marked.
func TestUnswallowedCompoundLeavesSwallowUnset(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"bd show x 2>/dev/null && bd list"}`),
		toolResult("u2", "t1", true),
	)
	evs := ingest(t, src)
	for _, ev := range evs {
		if ev.Swallow {
			t.Errorf("%s: Swallow = true, want false — an && chain still reports its failure", ev.Action)
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

// TestTrixiAskSearchCaptureQueryAndResults is the CLI-recall half of the
// retrieval episode contract (ferret-bbp.20): a query-mode `trixi ask`/`trixi
// search` shell call captures its query string plus the returned nug ids
// (ordered, deduped, lowercase 12-hex tokens scraped from the plain-text CLI
// output — no JSON envelope to parse, unlike the MCP get_nug arm), while a
// `trixi get <id>` by-id fetch captures neither (mirrors get_nug by-id).
func TestTrixiAskSearchCaptureQueryAndResults(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"trixi ask \"memory design question\""}`),
		toolResultContent("u2", "t1", `"Found 2 nugs:\n- 03447c82d743 (score 0.91) memory design notes\n- 1fe653da94e6 (score 0.5) older note\n"`),
		toolUse("u3", "t2", "Bash", `{"command":"trixi search \"attribution hop\" --nugs"}`),
		toolResultContent("u4", "t2", `"1 hit: 1fe653da94e6\n"`),
		toolUse("u5", "t3", "Bash", `{"command":"trixi get 1fe653da94e6"}`),
		toolResultContent("u6", "t3", `"id: 1fe653da94e6\nbody: older note\n"`),
	)
	evs := ingest(t, src)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}

	ask := evs[0]
	if ask.Query != "memory design question" {
		t.Errorf("ask.Query = %q, want %q", ask.Query, "memory design question")
	}
	wantHits := []NugHit{{ID: "03447c82d743"}, {ID: "1fe653da94e6"}}
	if len(ask.Results) != len(wantHits) {
		t.Fatalf("ask.Results = %+v, want %+v", ask.Results, wantHits)
	}
	for i, w := range wantHits {
		if ask.Results[i] != w {
			t.Errorf("ask.Results[%d] = %+v, want %+v (rank = first-appearance order)", i, ask.Results[i], w)
		}
	}

	search := evs[1]
	if search.Query != "attribution hop" {
		t.Errorf("search.Query = %q, want %q (flag stripped)", search.Query, "attribution hop")
	}
	if want := []NugHit{{ID: "1fe653da94e6"}}; len(search.Results) != 1 || search.Results[0] != want[0] {
		t.Errorf("search.Results = %+v, want %+v", search.Results, want)
	}

	get := evs[2]
	if get.Query != "" || get.Results != nil {
		t.Errorf("trixi get by-id captured query=%q results=%+v, want neither (mirrors get_nug by-id)", get.Query, get.Results)
	}
}

// TestTrixiGetFailedEchoNotCountedAsHit pins the AC4 edge case: a failed `trixi
// get <id>` echoes the queried id back in its own error text ("Resource not
// found"). Because trixi_get never sets Query (by-id fetch, no episode root),
// the echo must never be mistaken for a served hit.
func TestTrixiGetFailedEchoNotCountedAsHit(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"trixi get 0e29b02fa955"}`),
		toolResultContent("u2", "t1", `"Resource not found: 0e29b02fa955 (dangling superseded pointer)\n"`),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Query != "" || evs[0].Results != nil {
		t.Errorf("failed get = query %q results %+v, want neither (echoed id is not a served hit)", evs[0].Query, evs[0].Results)
	}
}

// TestTrixiCompoundQueryAttribution pins AC4's compound-command attribution
// rule: two query-mode trixi calls chained in ONE Bash tool_use share one
// tool_result. Per the bead's documented rule, the shared result's hit ids are
// extracted once and attributed to EACH query-mode event in the chain — the
// alternative (splitting the shared text) has no principled boundary since the
// CLI output carries no per-command delimiter.
func TestTrixiCompoundQueryAttribution(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"trixi ask \"first q\" && trixi search \"second q\""}`),
		toolResultContent("u2", "t1", `"03447c82d743\n1fe653da94e6\n"`),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (compound split)", len(evs))
	}
	wantHits := []NugHit{{ID: "03447c82d743"}, {ID: "1fe653da94e6"}}
	if evs[0].Query != "first q" || evs[1].Query != "second q" {
		t.Errorf("queries = %q, %q, want %q, %q", evs[0].Query, evs[1].Query, "first q", "second q")
	}
	for i, ev := range evs {
		if len(ev.Results) != len(wantHits) {
			t.Fatalf("evs[%d].Results = %+v, want %+v (shared hits attributed to each query event)", i, ev.Results, wantHits)
		}
		for j, w := range wantHits {
			if ev.Results[j] != w {
				t.Errorf("evs[%d].Results[%d] = %+v, want %+v", i, j, ev.Results[j], w)
			}
		}
	}
}

// TestTrixiFailedCallYieldsNoHits pins the review fix: a FAILED trixi ask/search
// whose error text happens to carry a 12-hex token (a session id, trace hash,
// truncated digest) must not fabricate hits — parseHexHits is a plain-text scan,
// so hit extraction is gated on StatusOK.
func TestTrixiFailedCallYieldsNoHits(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"trixi ask \"memory design\""}`),
		toolResultErrContent("u2", "t1", `"error: kg daemon unreachable (trace 03447c82d743)\n"`),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Status != StatusFail {
		t.Fatalf("status = %q, want %q", evs[0].Status, StatusFail)
	}
	if evs[0].Query != "memory design" {
		t.Errorf("Query = %q, want %q (query still captured from the call)", evs[0].Query, "memory design")
	}
	if evs[0].Results != nil {
		t.Errorf("Results = %+v, want nil — a failed call must not scrape hits from its error text", evs[0].Results)
	}
}

// TestTrixiQueryShellEscapes pins the review fix: a double-quoted query with
// shell escapes beyond \" (here \$ and \\) is stored as the value the CLI
// received, not its escaped source.
func TestTrixiQueryShellEscapes(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"trixi ask \"cost \\$5 path\\\\name\""}`),
		toolResultContent("u2", "t1", `"no hits\n"`),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if want := `cost $5 path\name`; evs[0].Query != want {
		t.Errorf("Query = %q, want %q (shell escapes decoded)", evs[0].Query, want)
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

// TestUsageJoinEnrichesSnipeCall mirrors TestSnipeApproxMarker's shape for the
// sn-r1do.2 usage.jsonl join: a snipe call with a session+action+window
// matching usage row gets Rung/CandidateCount/TriedRungs/IndexState copied
// onto the Event, and Stats.UsageJoined is incremented.
func TestUsageJoinEnrichesSnipeCall(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"snipe sym Foo"}`),
		toolResultContent("u2", "t1", `"def Foo(...) at foo.go:10"`),
	)
	idx := snipeusage.NewIndex([]snipeusage.Record{
		{
			TS: "2026-06-10T10:00:04Z", Command: "sym", SessionKey: "sess",
			Rung: "exact", CandidateCount: 3, IndexState: "fresh",
			TriedRungs: []string{"lookup:name"},
		},
	})
	evs, stats := ingestWithUsage(t, src, idx)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Rung != "exact" || ev.CandidateCount != 3 || ev.IndexState != "fresh" {
		t.Errorf("Rung=%q CandidateCount=%d IndexState=%q, want exact/3/fresh", ev.Rung, ev.CandidateCount, ev.IndexState)
	}
	if len(ev.TriedRungs) != 1 || ev.TriedRungs[0] != "lookup:name" {
		t.Errorf("TriedRungs = %+v, want [lookup:name]", ev.TriedRungs)
	}
	if stats.UsageJoined != 1 {
		t.Errorf("Stats.UsageJoined = %d, want 1", stats.UsageJoined)
	}
}

// TestUsageJoinNonSnipeUntouched proves the enrichment is gated on isSnipe,
// same as Approx: a non-snipe shell Event never gets the usage fields even
// when a same-session usage Index is present (a coincidental session+ts
// collision must not leak a snipe row onto an unrelated command).
func TestUsageJoinNonSnipeUntouched(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"rg Foo"}`),
		toolResultContent("u2", "t1", `"hit"`),
	)
	idx := snipeusage.NewIndex([]snipeusage.Record{
		{TS: "2026-06-10T10:00:04Z", Command: "sym", SessionKey: "sess", Rung: "exact"},
	})
	evs, stats := ingestWithUsage(t, src, idx)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Rung != "" {
		t.Errorf("non-snipe event got Rung %q, want empty", evs[0].Rung)
	}
	if stats.UsageJoined != 0 {
		t.Errorf("Stats.UsageJoined = %d, want 0 (rg is not snipe)", stats.UsageJoined)
	}
}

// TestUsageJoinNoMatchStaysUnenriched covers both no-usage-loaded (nil index,
// the default) and loaded-but-no-matching-row: either way Rung stays "" —
// the documented "empty means unjoined" contract (event.go's Rung comment).
func TestUsageJoinNoMatchStaysUnenriched(t *testing.T) {
	t.Run("no usage index set (nil, the default)", func(t *testing.T) {
		src := writeTranscript(t,
			toolUse("u1", "t1", "Bash", `{"command":"snipe sym Foo"}`),
			toolResultContent("u2", "t1", `"def Foo(...) at foo.go:10"`),
		)
		evs := ingest(t, src) // NewBuilder() default: b.usage == nil
		if evs[0].Rung != "" {
			t.Errorf("Rung = %q, want empty with no usage index", evs[0].Rung)
		}
	})
	t.Run("usage index set but empty — no row anywhere", func(t *testing.T) {
		src := writeTranscript(t,
			toolUse("u1", "t1", "Bash", `{"command":"snipe sym Foo"}`),
			toolResultContent("u2", "t1", `"def Foo(...) at foo.go:10"`),
		)
		evs, stats := ingestWithUsage(t, src, snipeusage.NewIndex(nil))
		if evs[0].Rung != "" {
			t.Errorf("Rung = %q, want empty with an empty usage index", evs[0].Rung)
		}
		if stats.UsageJoined != 0 {
			t.Errorf("Stats.UsageJoined = %d, want 0", stats.UsageJoined)
		}
	})
	t.Run("usage index set, row present but outside the join window", func(t *testing.T) {
		src := writeTranscript(t,
			toolUse("u1", "t1", "Bash", `{"command":"snipe sym Foo"}`),
			toolResultContent("u2", "t1", `"def Foo(...) at foo.go:10"`),
		)
		idx := snipeusage.NewIndex([]snipeusage.Record{
			// tool_result ts is 2026-06-10T10:00:05Z; this row is 5 minutes off.
			{TS: "2026-06-10T10:05:05Z", Command: "sym", SessionKey: "sess", Rung: "exact"},
		})
		evs, stats := ingestWithUsage(t, src, idx)
		if evs[0].Rung != "" {
			t.Errorf("Rung = %q, want empty — candidate is outside the join window", evs[0].Rung)
		}
		if stats.UsageJoined != 0 {
			t.Errorf("Stats.UsageJoined = %d, want 0", stats.UsageJoined)
		}
	})
}

// TestUsageJoinCompoundChainBothSegments pins Direction point 4: unlike
// Approx (gated to the trailing segment of a compound chain), the usage join
// applies to EVERY snipe segment — each subprocess in the chain emitted its
// own usage.jsonl row, independently joinable by (session, action).
func TestUsageJoinCompoundChainBothSegments(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"snipe def Foo && snipe callers Foo"}`),
		toolResultContent("u2", "t1", `"def result\ncallers result"`),
	)
	idx := snipeusage.NewIndex([]snipeusage.Record{
		{TS: "2026-06-10T10:00:03Z", Command: "def", SessionKey: "sess", Rung: "exact", IndexState: "fresh"},
		{TS: "2026-06-10T10:00:04Z", Command: "callers", SessionKey: "sess", Rung: "fuzzy", IndexState: "fresh"},
	})
	evs, stats := ingestWithUsage(t, src, idx)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (compound split)", len(evs))
	}
	if evs[0].Action != "snipe_def" || evs[0].Rung != "exact" {
		t.Errorf("evs[0] Action=%q Rung=%q, want snipe_def/exact", evs[0].Action, evs[0].Rung)
	}
	if evs[1].Action != "snipe_callers" || evs[1].Rung != "fuzzy" {
		t.Errorf("evs[1] Action=%q Rung=%q, want snipe_callers/fuzzy", evs[1].Action, evs[1].Rung)
	}
	if stats.UsageJoined != 2 {
		t.Errorf("Stats.UsageJoined = %d, want 2 (both segments joined)", stats.UsageJoined)
	}
}

// TestPipeFlagCapturedAtIngest covers the PIPE-ERASURE wiring (ferret-cax
// item 3): a piped Bash call carries Event.Pipe forward from
// shellnorm.Segment.Piped — the fact is gone from Detail alone (`rg foo |
// head` collapses to the "rg" segment), so it must be captured here.
func TestPipeFlagCapturedAtIngest(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"rg foo | head"}`),
		toolResult("u2", "t1", false),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if !evs[0].Pipe {
		t.Error("Pipe = false, want true — command came from a pipeline")
	}
}

// TestPipeFlagUnsetOnPlainCall is the negative twin: a bare command never
// gets Pipe set.
func TestPipeFlagUnsetOnPlainCall(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"rg foo"}`),
		toolResult("u2", "t1", false),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Pipe {
		t.Error("Pipe = true, want false for a bare command")
	}
}

// TestLegacyDecodeMissingPiCwKeys pins backward compatibility (ferret-cax):
// a JSONL line from before Pipe/CwdReset existed carries neither the "pi" nor
// the "cw" key, and must decode with both flags false and no error.
func TestLegacyDecodeMissingPiCwKeys(t *testing.T) {
	line := `{"i":1,"p":"proj","s":"sess","k":"shell","act":"rg"}`
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("Unmarshal legacy line: %v", err)
	}
	if ev.Pipe {
		t.Error("Pipe = true decoding a legacy line with no \"pi\" key, want false")
	}
	if ev.CwdReset {
		t.Error("CwdReset = true decoding a legacy line with no \"cw\" key, want false")
	}
}

// TestCwdResetMarksTrailingShellSegment covers the ferret-cax item 5 tell:
// a tool_result containing the harness's cwd-reset notice marks the trailing
// shell segment of the Bash call it belongs to, and counts into Stats.
func TestCwdResetMarksTrailingShellSegment(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"ls"}`),
		toolResultContent("u2", "t1", `"Shell cwd was reset to /Users/x/project"`),
	)
	b := NewBuilder()
	var evs []*Event
	if err := b.File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if !evs[0].CwdReset {
		t.Error("CwdReset = false, want true — result carries the marker")
	}
	if b.Stats.CwdResets != 1 {
		t.Errorf("Stats.CwdResets = %d, want 1", b.Stats.CwdResets)
	}
}

// TestCwdResetOnlyTrailingSegmentOfCompound checks a compound chain only
// flags its last segment — the marker describes the shell's state after the
// whole tool_result, mirroring Approx's trailing-segment attribution rule.
func TestCwdResetOnlyTrailingSegmentOfCompound(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"ls /x && cat f"}`),
		toolResultContent("u2", "t1", `"Shell cwd was reset to /x"`),
	)
	evs := ingest(t, src)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (both segments survive shellnorm)", len(evs))
	}
	if evs[0].CwdReset {
		t.Error("leading segment CwdReset = true, want false")
	}
	if !evs[1].CwdReset {
		t.Error("trailing segment CwdReset = false, want true")
	}
}

// TestCwdResetNotSetOnToolResult is the negative twin: the marker text
// appearing in a non-shell tool's result (e.g. a Read whose file content
// happens to quote the phrase) must not set the flag — the tell is gated to
// KindShell.
func TestCwdResetNotSetOnToolResult(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Read", `{"file_path":"a.go"}`),
		toolResultContent("u2", "t1", `"// contains the string Shell cwd was reset in a comment"`),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].CwdReset {
		t.Error("CwdReset = true, want false — marker in a tool result must not set the tell")
	}
}

// TestCwdResetAbsentWithoutMarker is the plain negative: no marker text, no flag.
func TestCwdResetAbsentWithoutMarker(t *testing.T) {
	src := writeTranscript(t,
		toolUse("u1", "t1", "Bash", `{"command":"ls"}`),
		toolResultContent("u2", "t1", `"total 0"`),
	)
	evs := ingest(t, src)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].CwdReset {
		t.Error("CwdReset = true, want false — no marker present")
	}
}
