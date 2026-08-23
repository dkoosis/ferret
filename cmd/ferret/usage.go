package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dkoosis/ferret/internal/apiusage"
	"github.com/dkoosis/ferret/internal/out"
)

// UsageCmd is the kong-ready flag struct for `ferret usage` — the API token
// ledger the harness itself wrote, read back (ferret-x2v).
//
// Every other ferret command counts bytes ferret measured. This one reports
// tokens the API billed, which makes it the only ferret number an outside
// source can contradict: run `/usage` for the same window and the totals must
// agree. That check is the point — a measurement nothing can falsify is not a
// measurement.
// errNoLedger marks a corpus ingested before the token ledger existed — a state
// with a one-command fix, not a failure of the run.
var errNoLedger = errors.New("no API token ledger in this corpus")

type UsageCmd struct {
	CommonFlags
}

// cmdUsage renders `ferret usage`.
func cmdUsage(cmd *UsageCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 10)
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}

	path := filepath.Join(c.data, apiusage.Artifact)
	rep, err := apiusage.Aggregate(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w at %s — this corpus was ingested before the ledger existed; run 'ferret ingest' to capture it", errNoLedger, path)
		}
		return err
	}

	if c.format == fmtJSON {
		return writeUsageJSON(os.Stdout, rep, c.limit)
	}
	return writeUsageText(os.Stdout, rep, c.limit, c.maxBytes)
}

// writeUsageJSON emits the ledger bundle, pre-capping session rows to limit.
func writeUsageJSON(w io.Writer, rep *apiusage.Report, limit int) error {
	total := len(rep.Rows)
	rows := rep.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return out.JSON(w, map[string]any{
		"totals": rep.Totals, "sessions": rep.Sessions, "models": rep.Models, "rows": rows,
		"readPerWrite": rep.Totals.ReadPerWrite(), "thinkingSharePct": rep.Totals.ThinkingShare(),
		"weighted": rep.Totals.Weighted(),
		keyTotal:   total, keyTruncated: len(rows) < total,
	})
}

// writeUsageText renders the ledger: raw buckets, the same buckets priced, then
// the sessions that cost the most.
//
// Both views are printed because they disagree and the disagreement IS the
// finding — cache write is a few percent of tokens and about a third of the
// bill, so a token budget and a money budget rank the same corpus differently.
func writeUsageText(w io.Writer, rep *apiusage.Report, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	t := &rep.Totals
	weighted := t.Weighted()

	about(sink,
		"≡ usage: the API's own token ledger, read back from the transcripts — measured spend, not bytes ferret counted.",
		"≡ RECONCILE: run /usage for the same window. These totals must agree; if they don't, the capture is wrong and nothing below is trustworthy.",
		fmt.Sprintf("≡ weighted = input-token-equivalents at the posted ratios (input %g, cache-write %g, cache-read %g, output %g). VERIFY the ratios against current pricing before acting on a share.",
			apiusage.WeightInput, apiusage.WeightCacheWrite, apiusage.WeightCacheRead, apiusage.WeightOutput),
		"≡ thinking is a SUBSET of output, never added to it.")
	sink.Head("usage calls=%d sessions=%d tokens=%s weighted=%s",
		t.Calls, rep.Sessions, humanCount(t.Tokens()), humanCount(int64(weighted)))
	if t.Calls == 0 {
		emptyNote(sink, 0, "API calls")
		return nil
	}

	// The bucket table goes through Head, not Row: it is a fixed four-line
	// accounting of the whole corpus, not a ranking, so --limit must govern the
	// session list below it rather than being spent on structure. Head still
	// counts toward --max-bytes, so the output budget stays honest.
	sink.Head("bucket        tokens    %%tokens        weighted   %%weighted")
	for _, b := range []struct {
		name   string
		tokens int64
		weight float64
	}{
		{"input", t.Input, apiusage.WeightInput},
		{"cache-write", t.CacheWrite, apiusage.WeightCacheWrite},
		{"cache-read", t.CacheRead, apiusage.WeightCacheRead},
		{"output", t.Output, apiusage.WeightOutput},
	} {
		wt := b.weight * float64(b.tokens)
		sink.Head("%-12s %8s   %6.1f%%   %13s   %6.1f%%", b.name, humanCount(b.tokens),
			apiusage.Share(float64(b.tokens), float64(t.Tokens())), humanCount(int64(wt)), apiusage.Share(wt, weighted))
	}
	sink.Head("cache: %.1f reads per written token · writes 1h=%s 5m=%s · thinking=%.1f%% of output",
		t.ReadPerWrite(), humanCount(t.Write1h), humanCount(t.Write5m), t.ThinkingShare())

	sink.Head("costliest sessions (weighted):")
	for i := range rep.Rows {
		r := &rep.Rows[i]
		if !sink.Row("  %10s  %5d calls  %2d ag  cw=%-8s cr=%-8s out=%-8s %s",
			humanCount(int64(r.Weighted)), r.Totals.Calls, r.Agents,
			humanCount(r.Totals.CacheWrite), humanCount(r.Totals.CacheRead), humanCount(r.Totals.Output),
			shortSession(r.Session)) {
			break
		}
	}
	sink.NextHead("ferret burn", "ferret friction")
	return nil
}

// humanCount renders a token count glance-readably. Tokens are counted in the
// millions and billions here; raw digits are unreadable at that scale.
func humanCount(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1000*1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1fm", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.2fb", float64(n)/1e9)
	}
}
