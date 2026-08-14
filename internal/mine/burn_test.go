package mine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeBurnFixture writes raw event JSONL lines to a temp events.jsonl and
// returns its path — mirrors stream_test.go's inline-JSONL fixture style.
func writeBurnFixture(t *testing.T, lines string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBurn_RanksByRenderCostDescending_When_MultipleKeysPresent pins the
// ferret-cax ranking flip and the reason for it: rows sort by modeled render
// cost, so the high-count/low-byte shell key outranks the low-count/high-byte
// tool key — the exact inversion the ccp-3s1c measurement demanded, and the
// exact case the old total-OutBytes ranking got backwards.
//
// Fixture arithmetic, spelled out because the flip is the whole point:
//
//	Read          2 tool calls, 150 out-bytes → 2*80 chrome + 150 = 310 rend
//	sh:git_commit 3 shell calls, 35 out-bytes → 3*480 chrome + 35 = 1475 rend
//
// Read wins on bytes 150→35; sh:git_commit wins on render 1475→310, and
// render is what ranks.
func TestBurn_RanksByRenderCostDescending_When_MultipleKeysPresent(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":100}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":50}
{"i":3,"p":"proj","s":"s2","k":"shell","act":"git_commit","b":10}
{"i":4,"p":"proj","s":"s2","k":"shell","act":"git_commit","b":20}
{"i":5,"p":"proj","s":"s3","k":"shell","act":"git_commit","b":5}
`
	path := writeBurnFixture(t, lines)

	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2 (Read, sh:git_commit): %+v", len(res.Rows), res.Rows)
	}
	if res.Rows[0].Key != "sh:git_commit" {
		t.Errorf("Rows[0].Key = %q, want %q (higher render cost ranks first)", res.Rows[0].Key, "sh:git_commit")
	}
	if res.Rows[1].Key != "Read" {
		t.Errorf("Rows[1].Key = %q, want %q", res.Rows[1].Key, "Read")
	}
	// The byte ordering must remain *readable* even though it no longer ranks:
	// the row that lost the ranking still carries the larger OutBytes.
	if res.Rows[1].OutBytes <= res.Rows[0].OutBytes {
		t.Errorf("expected the lower-ranked row to still show more out-bytes (byte columns stay visible): %+v", res.Rows)
	}
}

// TestBurn_ComputesRenderCost_When_KindAndBytesVary is the cost-model table:
// per-kind chrome constants, the per-call preview cap, and the per-call
// division into RenderPerCall. Each case is one key's worth of events so the
// expected arithmetic stays inspectable.
func TestBurn_ComputesRenderCost_When_KindAndBytesVary(t *testing.T) {
	const (
		toolChrome  = toolChromeLines * renderedLineBytes  // 80
		shellChrome = shellChromeLines * renderedLineBytes // 480
	)
	tests := []struct {
		name          string
		lines         string
		key           string
		wantRender    int
		wantPerCall   float64
		wantOutBytes  int
		wantCallCount int
	}{
		{
			name:          "tool call pays one collapsed line of chrome",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":100}`,
			key:           "Read",
			wantRender:    toolChrome + 100,
			wantPerCall:   toolChrome + 100,
			wantOutBytes:  100,
			wantCallCount: 1,
		},
		{
			name:          "shell call pays the full command-echo-plus-classifier chrome",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"shell","act":"git_status","b":100}`,
			key:           "sh:git_status",
			wantRender:    shellChrome + 100,
			wantPerCall:   shellChrome + 100,
			wantOutBytes:  100,
			wantCallCount: 1,
		},
		{
			name:          "output past the preview cap is collapsed, not charged",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":999999}`,
			key:           "Read",
			wantRender:    toolChrome + previewCapBytes,
			wantPerCall:   toolChrome + previewCapBytes,
			wantOutBytes:  999999,
			wantCallCount: 1,
		},
		{
			name: "the preview cap applies per call, never to the summed total",
			lines: `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":999999}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":999999}`,
			key:           "Read",
			wantRender:    2 * (toolChrome + previewCapBytes),
			wantPerCall:   toolChrome + previewCapBytes,
			wantOutBytes:  1999998,
			wantCallCount: 2,
		},
		{
			name: "a zero-output shell call still costs its chrome",
			lines: `{"i":1,"p":"proj","s":"s1","k":"shell","act":"git_status"}
{"i":2,"p":"proj","s":"s1","k":"shell","act":"git_status"}`,
			key:           "sh:git_status",
			wantRender:    2 * shellChrome,
			wantPerCall:   shellChrome,
			wantOutBytes:  0,
			wantCallCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Burn(writeBurnFixture(t, tt.lines+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			row := findBurnRow(t, res, tt.key)
			if row.RenderCost != tt.wantRender {
				t.Errorf("RenderCost = %d, want %d", row.RenderCost, tt.wantRender)
			}
			if row.RenderPerCall != tt.wantPerCall {
				t.Errorf("RenderPerCall = %v, want %v", row.RenderPerCall, tt.wantPerCall)
			}
			if row.OutBytes != tt.wantOutBytes {
				t.Errorf("OutBytes = %d, want %d (byte accounting must be untouched by the model)", row.OutBytes, tt.wantOutBytes)
			}
			if row.Calls != tt.wantCallCount {
				t.Errorf("Calls = %d, want %d", row.Calls, tt.wantCallCount)
			}
		})
	}
}

// TestBurn_BreaksRenderTiesOnBytesThenKey_When_CostsMatch pins the two-deep
// tie-break that keeps repeated runs byte-stable: equal render cost falls to
// out-bytes descending, and an equal-on-both pair falls to key ascending.
func TestBurn_BreaksRenderTiesOnBytesThenKey_When_CostsMatch(t *testing.T) {
	// Three tool keys, one call each. alpha/beta are identical on both cost
	// columns (key breaks the tie); gamma matches their render cost but is
	// capped, so it carries far more out-bytes and must outrank both.
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"beta","b":2048}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"alpha","b":2048}
{"i":3,"p":"proj","s":"s1","k":"tool","act":"gamma","b":900000}
`
	res, err := Burn(writeBurnFixture(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(res.Rows))
	for i := range res.Rows {
		got = append(got, res.Rows[i].Key)
	}
	want := []string{"gamma", "alpha", "beta"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("row order = %v, want %v (render tie → out-bytes desc → key asc)", got, want)
		}
	}
}

// findBurnRow returns the row for key, failing the test if it is absent —
// pointer-returning so callers never copy a BurnRow (it carries a map field).
func findBurnRow(t *testing.T, res *BurnResult, key string) *BurnRow {
	t.Helper()
	for i := range res.Rows {
		if res.Rows[i].Key == key {
			return &res.Rows[i]
		}
	}
	t.Fatalf("no row for key %q: %+v", key, res.Rows)
	return nil
}

// TestBurn_AggregatesCallsBytesPerCallAndSessions_When_KeyRecursAcrossSessions
// pins the four required columns: calls, out-bytes, bytes/call, and distinct
// session count — the shell key here recurs across two sessions (s2, s3) so
// Sessions must read 2, not the 3-call count.
func TestBurn_AggregatesCallsBytesPerCallAndSessions_When_KeyRecursAcrossSessions(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":100}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":50}
{"i":3,"p":"proj","s":"s2","k":"shell","act":"git_commit","b":10}
{"i":4,"p":"proj","s":"s2","k":"shell","act":"git_commit","b":20}
{"i":5,"p":"proj","s":"s3","k":"shell","act":"git_commit","b":5}
`
	path := writeBurnFixture(t, lines)

	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}

	var read, gitCommit *BurnRow
	for i := range res.Rows {
		switch res.Rows[i].Key {
		case "Read":
			read = &res.Rows[i]
		case "sh:git_commit":
			gitCommit = &res.Rows[i]
		}
	}
	if read == nil || gitCommit == nil {
		t.Fatalf("missing expected rows: %+v", res.Rows)
	}

	if read.Calls != 2 || read.OutBytes != 150 || read.BytesPerCall != 75 || read.Sessions != 1 {
		t.Errorf("Read row = %+v, want calls=2 outBytes=150 bytesPerCall=75 sessions=1", *read)
	}
	if gitCommit.Calls != 3 || gitCommit.OutBytes != 35 || gitCommit.Sessions != 2 {
		t.Errorf("sh:git_commit row = %+v, want calls=3 outBytes=35 sessions=2", *gitCommit)
	}
	wantBPC := 35.0 / 3.0
	if gitCommit.BytesPerCall != wantBPC {
		t.Errorf("sh:git_commit.BytesPerCall = %v, want %v", gitCommit.BytesPerCall, wantBPC)
	}

	if res.Events != 5 {
		t.Errorf("Events = %d, want 5", res.Events)
	}
	if res.Sessions != 3 {
		t.Errorf("Sessions = %d, want 3", res.Sessions)
	}
}

// TestBurn_ExcludesPromptEvents_When_StreamContainsPrompts pins that prompt
// events (no Action to key on) are counted in neither Events nor any row —
// folding them in would fabricate a bogus "" or "prompt" burn row.
func TestBurn_ExcludesPromptEvents_When_StreamContainsPrompts(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"prompt","act":"prompt","q":"hello"}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":10}
`
	path := writeBurnFixture(t, lines)

	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 1 {
		t.Errorf("Events = %d, want 1 (prompt excluded)", res.Events)
	}
	if len(res.Rows) != 1 || res.Rows[0].Key != "Read" {
		t.Errorf("Rows = %+v, want single Read row", res.Rows)
	}
}

// TestBurn_RoundTripsThroughJSON_When_ResultMarshaled pins the AC's json
// format requirement: BurnResult (and its BurnRow rows) must marshal and
// unmarshal without loss — the unexported per-row session-set accumulator
// must not leak into or block the encoding.
func TestBurn_RoundTripsThroughJSON_When_ResultMarshaled(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":10}
{"i":2,"p":"proj","s":"s2","k":"shell","act":"git_commit","b":20}
`
	path := writeBurnFixture(t, lines)

	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got BurnResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Rows) != len(res.Rows) {
		t.Fatalf("round-tripped Rows len = %d, want %d", len(got.Rows), len(res.Rows))
	}
	for i := range res.Rows {
		if got.Rows[i].Key != res.Rows[i].Key || got.Rows[i].OutBytes != res.Rows[i].OutBytes ||
			got.Rows[i].Calls != res.Rows[i].Calls || got.Rows[i].Sessions != res.Rows[i].Sessions ||
			got.Rows[i].RenderCost != res.Rows[i].RenderCost || got.Rows[i].RenderPerCall != res.Rows[i].RenderPerCall {
			t.Errorf("round-tripped Rows[%d] = %+v, want %+v", i, got.Rows[i], res.Rows[i])
		}
	}
	if got.Events != res.Events || got.Sessions != res.Sessions {
		t.Errorf("round-tripped totals = events=%d sessions=%d, want events=%d sessions=%d",
			got.Events, got.Sessions, res.Events, res.Sessions)
	}
}

// TestBurn_ReturnsError_When_EventsPathMissing pins the error path: a
// nonexistent artifact must surface an error, not a silently empty result.
func TestBurn_ReturnsError_When_EventsPathMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := Burn(filepath.Join(dir, "does-not-exist.jsonl"))
	if err == nil {
		t.Fatal("Burn() error = nil, want non-nil for missing events path")
	}
}

// TestBurn_ChargesAttachmentsFullBytes_When_PayloadExceedsPreviewCap pins the
// ferret-rfc exemption: an attachment does not fold behind a preview, so the
// 2048-byte cap must not apply to it. A real skill_listing averages 20,321
// bytes; capping it would rank the corpus's second-largest context injector at
// roughly a tenth of its cost.
func TestBurn_ChargesAttachmentsFullBytes_When_PayloadExceedsPreviewCap(t *testing.T) {
	const big = 20321 // measured mean skill_listing size over 3,617 records
	path := writeBurnFixture(t, `{"i":1,"s":"s1","k":"attach","act":"skill_listing","b":20321}
`)
	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	got := res.Rows[0]
	if got.Key != "at:skill_listing" {
		t.Errorf("key = %q, want at:skill_listing (prefixed so a class cannot collide with a tool name)", got.Key)
	}
	if got.RenderCost != big {
		t.Errorf("RenderCost = %d, want %d — attachments are exempt from previewCapBytes and from chrome", got.RenderCost, big)
	}
}

// An attachment key must be able to outrank a tool key. Before ferret-rfc no
// attachment reached Burn at all; with the preview cap applied it still could
// not win. This is the end-to-end statement of the bug.
func TestBurn_AttachmentOutranksTool_When_ItCostsMore(t *testing.T) {
	path := writeBurnFixture(t, `{"i":1,"s":"s1","k":"attach","act":"skill_listing","b":20321}
{"i":2,"s":"s1","k":"tool","act":"Read","b":9000}
`)
	res, err := Burn(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if res.Rows[0].Key != "at:skill_listing" {
		t.Errorf("top key = %q, want at:skill_listing — a 20KB injection must outrank a 9KB Read that folds at 2048", res.Rows[0].Key)
	}
}
