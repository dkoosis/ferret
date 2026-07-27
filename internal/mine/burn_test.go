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

// TestBurn_RanksByTotalOutBytesDescending_When_MultipleKeysPresent pins the
// core AC: events group by their normalized command key (shellnorm segment
// for shell, tool name for tool), sum to a per-key OutBytes total, and rows
// sort by that total descending with a stable key tie-break.
func TestBurn_RanksByTotalOutBytesDescending_When_MultipleKeysPresent(t *testing.T) {
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
	// Read totals 150, sh:git_commit totals 35 — Read must rank first.
	if res.Rows[0].Key != "Read" {
		t.Errorf("Rows[0].Key = %q, want %q (higher total out-bytes ranks first)", res.Rows[0].Key, "Read")
	}
	if res.Rows[1].Key != "sh:git_commit" {
		t.Errorf("Rows[1].Key = %q, want %q", res.Rows[1].Key, "sh:git_commit")
	}
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
			got.Rows[i].Calls != res.Rows[i].Calls || got.Rows[i].Sessions != res.Rows[i].Sessions {
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
