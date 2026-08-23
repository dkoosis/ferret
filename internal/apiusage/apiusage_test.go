package apiusage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTotals_KeepsBucketsDisjoint_When_Folding pins the accounting rule the
// rest of the package depends on: the four token buckets are disjoint and
// summable, thinking is a subset of output and must never be added to it.
func TestTotals_KeepsBucketsDisjoint_When_Folding(t *testing.T) {
	var tot Totals
	tot.Add(&Row{Input: 10, CacheWrite: 100, CacheRead: 1000, Output: 5, Thinking: 3})
	tot.Add(&Row{Input: 10, CacheWrite: 100, CacheRead: 1000, Output: 5, Thinking: 2})

	if got, want := tot.Tokens(), int64(2230); got != want {
		t.Errorf("Tokens() = %d, want %d — thinking must not enter the sum", got, want)
	}
	if got, want := tot.Calls, 2; got != want {
		t.Errorf("Calls = %d, want %d", got, want)
	}
	if got, want := tot.ThinkingShare(), 50.0; got != want {
		t.Errorf("ThinkingShare() = %.1f, want %.1f", got, want)
	}
}

// TestTotals_ReordersBucketsByPrice_When_Weighted pins the finding the report
// exists to show: cache write is a sliver of tokens and a large share of cost,
// so a token ranking and a cost ranking disagree.
//
// Fixture: 200 written tokens against 2000 read. By raw tokens the write is
// 9.1%; weighted (1.25 vs 0.1) it is 250 of 450 — 55.6%.
func TestTotals_ReordersBucketsByPrice_When_Weighted(t *testing.T) {
	tot := Totals{CacheWrite: 200, CacheRead: 2000}

	rawShare := Share(float64(tot.CacheWrite), float64(tot.Tokens()))
	costShare := Share(WeightCacheWrite*float64(tot.CacheWrite), tot.Weighted())
	if rawShare > 10 {
		t.Errorf("raw token share = %.1f%%, expected under 10%%", rawShare)
	}
	if costShare < 50 {
		t.Errorf("weighted share = %.1f%%, expected over 50%% — the reordering is the point", costShare)
	}
	if got, want := tot.ReadPerWrite(), 10.0; got != want {
		t.Errorf("ReadPerWrite() = %.1f, want %.1f", got, want)
	}
}

// TestTotals_ReturnsZero_When_NothingWritten pins the div-by-zero guards: a
// corpus with no cache writes reports 0, not NaN or a panic.
func TestTotals_ReturnsZero_When_NothingWritten(t *testing.T) {
	var tot Totals
	if got := tot.ReadPerWrite(); got != 0 {
		t.Errorf("ReadPerWrite() = %v, want 0", got)
	}
	if got := tot.ThinkingShare(); got != 0 {
		t.Errorf("ThinkingShare() = %v, want 0", got)
	}
	if got := Share(1, 0); got != 0 {
		t.Errorf("Share(1, 0) = %v, want 0", got)
	}
}

// TestAggregate_RanksSessionsByWeightedCost_When_LedgerHasRows pins that the
// ranking key is money, not calls: the session with fewer calls but expensive
// buckets must lead.
func TestAggregate_RanksSessionsByWeightedCost_When_LedgerHasRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Artifact)
	rows := []Row{
		{Session: "cheap", Model: "m1", CacheRead: 100000},               // 10000 weighted, 1 call
		{Session: "cheap", Model: "m1", CacheRead: 100000},               // 10000 weighted
		{Session: "pricey", Model: "m1", Output: 8000, Agent: "ag1"},     // 40000 weighted
		{Session: "pricey", Model: "m2", CacheWrite: 1000, Thinking: 10}, // 1250 weighted
	}
	if err := Write(path, rows); err != nil {
		t.Fatal(err)
	}

	rep, err := Aggregate(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 2 {
		t.Fatalf("Sessions = %d, want 2", rep.Sessions)
	}
	if rep.Rows[0].Session != "pricey" {
		t.Errorf("Rows[0] = %q, want pricey — ranking is weighted cost, not call count", rep.Rows[0].Session)
	}
	if got := rep.Rows[0].Agents; got != 2 {
		t.Errorf("pricey Agents = %d, want 2 (main thread + ag1)", got)
	}
	if got, want := rep.Totals.Calls, 4; got != want {
		t.Errorf("Totals.Calls = %d, want %d", got, want)
	}
	if len(rep.Models) != 2 || rep.Models[0].Model != "m1" {
		t.Errorf("models not split/ranked as expected: %+v", rep.Models)
	}
}

// TestAggregate_ReportsMissingArtifact_When_LedgerAbsent pins that a corpus
// ingested before the ledger existed reports itself as such — os.IsNotExist
// must survive, since the CLI turns it into the re-ingest instruction.
func TestAggregate_ReportsMissingArtifact_When_LedgerAbsent(t *testing.T) {
	_, err := Aggregate(filepath.Join(t.TempDir(), Artifact))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want a not-exist error the CLI can recognize", err)
	}
}

// TestRead_SkipsMalformedLine_When_ArtifactPartiallyCorrupt pins that one bad
// line costs one row, not the whole corpus read.
func TestRead_SkipsMalformedLine_When_ArtifactPartiallyCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), Artifact)
	if err := os.WriteFile(path, []byte("{\"s\":\"a\",\"out\":1}\n{not json}\n\n{\"s\":\"b\",\"out\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := Read(path, func(*Row) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("read %d rows, want 2 (one malformed line skipped, blank line ignored)", n)
	}
}
