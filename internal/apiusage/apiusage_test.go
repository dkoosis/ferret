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

// TestTotals_ReordersBucketsByPrice_When_Priced pins the finding the report
// exists to show: cache read is nearly all of the tokens and a minority of the
// bill, so a token ranking and a cost ranking disagree.
//
// Fixture: 200k written (1h TTL) against 2m read on Opus ($5/MTok input).
// Tokens: the write is 9.1%. Dollars: write 200k x 2.0 x $5/M = $2.00; read
// 2m x 0.1 x $5/M = $1.00 — 66.7%.
func TestTotals_ReordersBucketsByPrice_When_Priced(t *testing.T) {
	var tot Totals
	tot.Add(&Row{Model: "claude-opus-5", CacheWrite: 200000, Write1h: 200000, CacheRead: 2000000})

	if got := Share(float64(tot.CacheWrite), float64(tot.Tokens())); got > 10 {
		t.Errorf("raw token share = %.1f%%, expected under 10%%", got)
	}
	if got := Share(tot.USDWrite, tot.USD); got < 60 {
		t.Errorf("dollar share = %.1f%%, expected over 60%% — the reordering is the point", got)
	}
	if got, want := tot.USDWrite, 2.0; got != want {
		t.Errorf("USDWrite = %v, want %v (200k x 2.0x x $5/MTok)", got, want)
	}
	if got, want := tot.ReadPerWrite(), 10.0; got != want {
		t.Errorf("ReadPerWrite() = %.1f, want %.1f", got, want)
	}
}

// TestCost_ChargesOneHourWritesAtTwice_When_TTLSplitPresent pins the multiplier
// Codex caught on PR #134: a 1-hour cache write costs 2x base input, not the
// 1.25x a 5-minute write costs. dk's corpus is 99.5% 1-hour writes, so pooling
// them at the 5-minute rate understated write spend by ~60%.
func TestCost_ChargesOneHourWritesAtTwice_When_TTLSplitPresent(t *testing.T) {
	oneHour := Row{Model: "claude-opus-5", CacheWrite: 1000000, Write1h: 1000000}
	fiveMin := Row{Model: "claude-opus-5", CacheWrite: 1000000, Write5m: 1000000}

	gotH, okH := oneHour.Cost()
	gotM, okM := fiveMin.Cost()
	if !okH || !okM {
		t.Fatal("a known model must price")
	}
	if gotH != 10.0 {
		t.Errorf("1h write cost = %v, want 10.00 (1M x 2.0x x $5/MTok)", gotH)
	}
	if gotM != 6.25 {
		t.Errorf("5m write cost = %v, want 6.25 (1M x 1.25x x $5/MTok)", gotM)
	}
}

// TestCost_ChargesUnsplitWritesAtTheCheaperRate_When_TTLAbsent pins the
// conservative fallback for a transcript schema carrying no TTL split: charge
// 5-minute, because the alternative inflates the one number here that is meant
// to be checkable.
func TestCost_ChargesUnsplitWritesAtTheCheaperRate_When_TTLAbsent(t *testing.T) {
	r := Row{Model: "claude-opus-5", CacheWrite: 1000000}
	got, ok := r.Cost()
	if !ok || got != 6.25 {
		t.Errorf("unsplit write cost = %v (priced=%v), want 6.25", got, ok)
	}
}

// TestCost_PricesEachModelSeparately_When_CorpusMixesThem pins the second
// finding from that review: an input-token-equivalent is worth 5x more on Opus
// than on Haiku, so pooling models before ranking mis-orders sessions.
func TestCost_PricesEachModelSeparately_When_CorpusMixesThem(t *testing.T) {
	opus := Row{Model: "claude-opus-5", Output: 1000000}
	haiku := Row{Model: "claude-haiku-4-5-20251001", Output: 1000000} // dated variant: prefix match
	sonnet := Row{Model: "claude-sonnet-5", Output: 1000000}

	o, _ := opus.Cost()
	h, okH := haiku.Cost()
	s, _ := sonnet.Cost()
	if !okH {
		t.Fatal("a dated model id must resolve by prefix")
	}
	if o != 25.0 || s != 15.0 || h != 5.0 {
		t.Errorf("costs opus=%v sonnet=%v haiku=%v, want 25/15/5 per MTok output", o, s, h)
	}
}

// TestCost_RefusesToGuess_When_ModelUnknown pins that an unpriced model is
// reported, never absorbed: "<synthetic>" turns are not billed at all, and a
// fabricated price is indistinguishable from a measured one inside a total.
func TestCost_RefusesToGuess_When_ModelUnknown(t *testing.T) {
	r := Row{Model: "<synthetic>", Output: 1000, CacheRead: 5000}
	if _, ok := r.Cost(); ok {
		t.Error("an unknown model must not price")
	}
	var tot Totals
	tot.Add(&r)
	if tot.USD != 0 {
		t.Errorf("USD = %v, want 0 — unpriced calls stay out of spend", tot.USD)
	}
	if tot.Unpriced != 1 || tot.UnpricedTok != 6000 {
		t.Errorf("Unpriced=%d tokens=%d, want 1/6000 — the exclusion must be visible", tot.Unpriced, tot.UnpricedTok)
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

// TestAggregate_RanksSessionsByDollars_When_LedgerMixesModels pins that the
// ranking key is money, not calls and not tokens: the session with far fewer
// tokens on a pricier model must lead.
func TestAggregate_RanksSessionsByDollars_When_LedgerMixesModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Artifact)
	rows := []Row{
		{Session: "cheap", Model: "claude-haiku-4-5", CacheRead: 20000000},                               // 20m x 0.1 x $1/M = $2.00
		{Session: "cheap", Model: "claude-haiku-4-5", CacheRead: 20000000},                               // $2.00
		{Session: "pricey", Model: "claude-opus-5", Output: 1000000, Agent: "ag1"},                       // 1m x $25/M = $25.00
		{Session: "pricey", Model: "claude-sonnet-5", CacheWrite: 100000, Write1h: 100000, Thinking: 10}, // 100k x 2.0 x $3/M = $0.60
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
		t.Errorf("Rows[0] = %q, want pricey — ranking is dollars, not tokens", rep.Rows[0].Session)
	}
	if got := rep.Rows[0].Totals.USD; got != 25.6 {
		t.Errorf("pricey USD = %v, want 25.60 ($25 Opus output + $0.60 Sonnet 1h write)", got)
	}
	if got := rep.Rows[0].Agents; got != 2 {
		t.Errorf("pricey Agents = %d, want 2 (main thread + ag1)", got)
	}
	if got, want := rep.Totals.Calls, 4; got != want {
		t.Errorf("Totals.Calls = %d, want %d", got, want)
	}
	if len(rep.Models) != 3 || rep.Models[0].Model != "claude-opus-5" {
		t.Errorf("models not split/ranked by spend as expected: %+v", rep.Models)
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
