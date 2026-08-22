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

// TestBurn_RanksByContextBytesDescending_When_MultipleKeysPresent pins the
// ferret-noj ranking: rows sort by measured context bytes, so the key that put
// more into the request body leads — no chrome constant, no preview cap, no
// modeled term of any kind between the corpus and the order.
//
// Fixture arithmetic, spelled out:
//
//	Read          2 tool calls,  150 bytes
//	sh:git_commit 3 shell calls,  35 bytes
//
// Under the retired ferret-cax render model sh:git_commit won this comparison
// 1475 -> 310, purely on 3 x 480 bytes of fabricated shell chrome. Chrome never
// entered the request, so Read leads now.
func TestBurn_RanksByContextBytesDescending_When_MultipleKeysPresent(t *testing.T) {
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
	if res.Rows[0].Key != "Read" {
		t.Errorf("Rows[0].Key = %q, want %q (more context bytes ranks first — shell chrome is not a cost)", res.Rows[0].Key, "Read")
	}
	if res.Rows[1].Key != "sh:git_commit" {
		t.Errorf("Rows[1].Key = %q, want %q", res.Rows[1].Key, "sh:git_commit")
	}
	// A shell call is priced exactly like a tool call: its bytes, nothing added.
	if res.Rows[1].OutBytes != 35 {
		t.Errorf("sh:git_commit OutBytes = %d, want 35 — no per-call chrome constant survives", res.Rows[1].OutBytes)
	}
}

// TestBurn_ChargesMeasuredBytes_When_KindAndVolumeVary is the cost table. It
// asserts the PER-TOOL result caps that were actually measured, rather than the
// single global 2048-byte cap ferret-cax applied to every key alike.
//
// The caps below are properties of the HARNESS, observed in the corpus, not
// dials in this file. The harness truncates tool_result before the transcript
// records it, so the recorded bytes are already post-cap: burn must charge them
// whole. Re-applying a cap here would charge the same truncation twice, and no
// single value could be right anyway — Bash and Read cap three orders of
// magnitude apart.
func TestBurn_ChargesMeasuredBytes_When_KindAndVolumeVary(t *testing.T) {
	const (
		// bashResultCap is the Bash tool_result ceiling: 30,000 characters.
		// Max observed 29,723 over 60,550 calls, zero at or above 30,000
		// (3,923-transcript corpus, ferret-noj).
		bashResultCap = 30000
		// bashResultMax is the largest Bash result the corpus actually holds.
		bashResultMax = 29723
		// readResultMax is the largest Read result observed over 13,236 calls
		// (89,352 B / 1,672 lines). Read is NOT capped in practice — the
		// 2,000-line default never bound — which is why one global cap was
		// always wrong.
		readResultMax = 89352
		// retiredGlobalCap is ferret-cax's previewCapBytes. It discarded 53% of
		// real bytes; every case below must exceed it and still be charged whole.
		retiredGlobalCap = 2048
	)
	tests := []struct {
		name          string
		lines         string
		key           string
		wantOutBytes  int
		wantPerCall   float64
		wantCallCount int
	}{
		{
			name:          "a shell call is charged its bytes and no chrome",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"shell","act":"git_status","b":100}`,
			key:           "sh:git_status",
			wantOutBytes:  100,
			wantPerCall:   100,
			wantCallCount: 1,
		},
		{
			name:          "a tool call is charged its bytes and no chrome",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":100}`,
			key:           "Read",
			wantOutBytes:  100,
			wantPerCall:   100,
			wantCallCount: 1,
		},
		{
			name:          "a Bash result at its measured cap is charged whole, not clipped to the retired global cap",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"shell","act":"git_log","b":29723}`,
			key:           "sh:git_log",
			wantOutBytes:  bashResultMax,
			wantPerCall:   bashResultMax,
			wantCallCount: 1,
		},
		{
			name:          "a Read result far past Bash's cap is charged whole — Read is uncapped in practice",
			lines:         `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":89352}`,
			key:           "Read",
			wantOutBytes:  readResultMax,
			wantPerCall:   readResultMax,
			wantCallCount: 1,
		},
		{
			name: "repetition is priced by summing calls, never by a per-call constant",
			lines: `{"i":1,"p":"proj","s":"s1","k":"tool","act":"Read","b":89352}
{"i":2,"p":"proj","s":"s1","k":"tool","act":"Read","b":89352}`,
			key:           "Read",
			wantOutBytes:  2 * readResultMax,
			wantPerCall:   readResultMax,
			wantCallCount: 2,
		},
		{
			name: "a zero-output shell call costs nothing — chrome was fabricated burn",
			lines: `{"i":1,"p":"proj","s":"s1","k":"shell","act":"git_status"}
{"i":2,"p":"proj","s":"s1","k":"shell","act":"git_status"}`,
			key:           "sh:git_status",
			wantOutBytes:  0,
			wantPerCall:   0,
			wantCallCount: 2,
		},
	}

	// Guard the guard: the caps must stay ordered the way the corpus measured
	// them, or the cases above stop proving that no single cap fits.
	if retiredGlobalCap >= bashResultMax || bashResultMax >= bashResultCap || bashResultCap >= readResultMax {
		t.Fatalf("measured caps out of order: global=%d bashMax=%d bashCap=%d readMax=%d",
			retiredGlobalCap, bashResultMax, bashResultCap, readResultMax)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Burn(writeBurnFixture(t, tt.lines+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			row := findBurnRow(t, res, tt.key)
			if row.OutBytes != tt.wantOutBytes {
				t.Errorf("OutBytes = %d, want %d", row.OutBytes, tt.wantOutBytes)
			}
			if row.BytesPerCall != tt.wantPerCall {
				t.Errorf("BytesPerCall = %v, want %v", row.BytesPerCall, tt.wantPerCall)
			}
			if row.Calls != tt.wantCallCount {
				t.Errorf("Calls = %d, want %d", row.Calls, tt.wantCallCount)
			}
		})
	}
}

// TestBurn_BreaksByteTiesOnKey_When_CostsMatch pins the tie-break that keeps
// repeated runs byte-stable: equal context bytes falls to key ascending.
func TestBurn_BreaksByteTiesOnKey_When_CostsMatch(t *testing.T) {
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
			t.Fatalf("row order = %v, want %v (bytes desc → key asc)", got, want)
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
			got.Rows[i].BytesPerCall != res.Rows[i].BytesPerCall {
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

// TestBurn_ChargesAttachmentsFullBytes_When_PayloadIsLarge pins the ferret-rfc
// coverage: an attachment enters context whole, so it is charged whole. Under
// ferret-cax this needed an explicit exemption from the 2048-byte preview cap;
// ferret-noj deleted the cap for every kind, so the exemption is now the rule
// and this test guards against a cap creeping back in.
func TestBurn_ChargesAttachmentsFullBytes_When_PayloadIsLarge(t *testing.T) {
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
	if got.OutBytes != big {
		t.Errorf("OutBytes = %d, want %d — an attachment is charged the bytes it injected", got.OutBytes, big)
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
