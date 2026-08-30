package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// FrictionCmd is the kong-ready flag struct for `ferret friction` (ferret-q6y)
// — the combined friction op. Answering "which tool calls are worth fixing?"
// used to take five commands (ingest → report --kind friction → burn →
// misfires → polling) and a cross-join done by eye. This runs all four
// detectors over one events load and emits ONE table ranked by estimated
// wasted bytes.
//
// The DK-AXI operating contract (kg reference.decision 03447c82d743) names
// combined operations as the single largest agent-ergonomics win: one command
// that does the work of three. This is that command for the friction lane.
//
// Registration (cmd/ferret/main.go): a `Friction FrictionCmd` field on CLI, a
// synopsis line in the kong.Description block, and a `case "friction"` arm in
// the dispatch switch — the same three edits every subcommand takes.
type FrictionCmd struct {
	CommonFlags
	LensFlags
	Source   string `help:"Only this source: poll|misfire|motif (default: all)." name:"source"`
	NoMotifs bool   `help:"Skip the motif leg (sequence mining) — much faster on a large corpus." name:"no-motifs"`
}

// cmdFriction renders `ferret friction`. Load order matters for cost: the
// three cheap detectors ride one events load, and the motif leg (sequence
// mining over a lensed corpus, the expensive one) is skipped entirely under
// --no-motifs or --source poll|misfire.
func cmdFriction(cmd *FrictionCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 20)
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := validateWasteSource(cmd.Source); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}

	rep, err := mergeFriction(c, cmd)
	if err != nil {
		return err
	}
	if cmd.Source != "" {
		rep = filterWasteReport(rep, mine.WasteSource(cmd.Source))
	}

	if c.format == fmtJSON {
		return writeFrictionJSON(os.Stdout, rep, c.limit)
	}
	return writeFrictionText(os.Stdout, rep, c.limit, c.maxBytes)
}

// mergeFriction runs the detectors and hands them to mine.MergeWaste. Burn
// streams the artifact itself (it takes a path, not a slice); misfires and
// polling share one in-memory load, mirroring cmdMisfires/cmdPolling.
func mergeFriction(c *common, cmd *FrictionCmd) (mine.WasteReport, error) {
	burn, err := mine.Burn(c.eventsPath())
	if err != nil {
		return mine.WasteReport{}, err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return mine.WasteReport{}, err
	}
	corpus, motifs, err := frictionMotifs(c, cmd)
	if err != nil {
		return mine.WasteReport{}, err
	}
	return mine.MergeWaste(burn, mine.MineMisfires(events), mine.MinePolling(events), corpus, motifs), nil
}

// frictionMotifs runs the motif leg with the report command's mining defaults,
// so a motif's waste here is measured the same way `ferret report` measures its
// burn. Returns (nil, nil) when the leg is skipped — MergeWaste reads the
// corpus only when there are motifs to render.
func frictionMotifs(c *common, cmd *FrictionCmd) (*mine.Corpus, []*mine.Finding, error) {
	if cmd.NoMotifs || (cmd.Source != "" && cmd.Source != string(mine.WasteMotif)) {
		return nil, nil, nil
	}
	lo := fromLensFlags(cmd.LensFlags)
	corpus, _, err := lo.corpus(c.eventsPath())
	if err != nil {
		return nil, nil, err
	}
	sscores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: reportOrder, MinToks: reportSurpriseMinToks})
	motifs, _ := mineFindings(corpus, reportMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop,
		mine.SurpriseIndex(sscores), mine.FrictionCut(sscores))
	return corpus, motifs, nil
}

// validateWasteSource rejects an unknown --source rather than silently
// emitting an empty table — DK-AXI contract rule 3 (a dropped flag is worse
// than a fatal one).
func validateWasteSource(src string) error {
	switch src {
	case "", string(mine.WasteRepeat), string(mine.WasteFail), string(mine.WasteMotif):
		return nil
	}
	return fmt.Errorf("%w: %q (want poll|misfire|motif)", errBadSource, src)
}

// filterWasteReport narrows the merged table to one detector and re-derives
// the totals from what survived. Filtering rows without re-summing would print
// a header that counts detectors the table no longer shows — `--source poll`
// reporting misfire waste. Per-source subtotals are re-derived too, so the
// breakdown always describes the rows on screen.
//
// TotalBytes is left alone on purpose: it is the corpus denominator, not a
// property of the selected rows.
func filterWasteReport(rep mine.WasteReport, src mine.WasteSource) mine.WasteReport {
	kept := rep.Rows[:0]
	rep.TotalWasted = 0
	rep.BySource = map[mine.WasteSource]int{}
	for _, r := range rep.Rows {
		if r.Source != src {
			continue
		}
		rep.TotalWasted += r.WastedBytes
		rep.BySource[r.Source] += r.WastedBytes
		kept = append(kept, r)
	}
	rep.Rows = kept
	return rep
}

// writeFrictionJSON emits the merged bundle as one JSON document, pre-capping
// rows to limit (0 = unlimited) — out.JSON ignores row limits itself, so the
// cap happens here. totalWasted stays uncapped on purpose: it describes every
// row the merge produced, not the page of them shown. bySource rides along
// because it is the sound figure — see WasteReport on why the sum is only an
// upper bound.
func writeFrictionJSON(w io.Writer, rep mine.WasteReport, limit int) error {
	total := len(rep.Rows)
	rows := rep.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return out.JSON(w, map[string]any{
		keyRows:       rows,
		"totalWasted": rep.TotalWasted,
		"bySource":    rep.BySource,
		"totalBytes":  rep.TotalBytes,
		"events":      rep.Events,
		keySessions:   rep.Sessions,
		keyTotal:      total,
		keyTruncated:  len(rows) < total,
	})
}

// writeFrictionText renders the merged ranking. Waste first in the eye path —
// it is the number a "should I fix this?" decision turns on — then the spread,
// then the gross cost the waste is a slice of.
func writeFrictionText(w io.Writer, rep mine.WasteReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ friction: the four detectors (polling, misfires, motif findings, burn) merged into one",
		"≡ ranking. waste = context bytes spent on calls that bought nothing: repeats past",
		"≡ the first per session (poll), calls that failed (misfire), motif occurrences past the",
		"≡ first per session (motif). burn contributes no rows of its own — it prices the others,",
		"≡ and each row carries its key's GROSS bytes beside the waste. Which occurrences bought",
		"≡ nothing is the detectors' judgment. The price is measured for misfire rows (the failing",
		"≡ calls' own bytes) and MODELED for poll and motif rows (occurrences x the key's mean",
		"≡ bytes/call, successes included) — a modeled row is an estimate, not a receipt.",
		"≡ Swallowed-error rows are excluded because a swallow count is a floor, not a count.",
		"≡ The per-source subtotals are sound; their SUM is an upper bound, because the detectors",
		"≡ overlap (one failing repeated command is charged by both poll and misfire, and a motif",
		"≡ can charge the same calls again). ✗ read it as a share of the corpus total.")
	// Legal moves, not a plan (DK-AXI rule 11): each detector's own command
	// holds the detail this table summarizes. Head-style so the hints survive
	// row truncation — the truncated case is when a reader most needs them.
	sink.NextHead("ferret polling", "ferret misfires", "ferret report --kind friction", "ferret burn")
	sink.Head("friction rows=%d waste≤%s [%s]  gross=%s  events=%d sessions=%d",
		len(rep.Rows), humanBytes(rep.TotalWasted), wasteSplit(rep),
		humanBytes(rep.TotalBytes), rep.Events, rep.Sessions)
	if len(rep.Rows) == 0 {
		sink.Head("0 rows — no estimable waste in this corpus")
		return nil
	}
	// No early break on a refused Row: Sink counts every refusal, so breaking
	// at the first one makes Close report "+1 more" no matter how many rows
	// were actually dropped. Feeding the whole slice is what makes the tail
	// line honest (burn.go does the same; polling/misfires still break —
	// ferret-09f).
	for i := range rep.Rows {
		r := &rep.Rows[i]
		sink.Row("%10s waste  %5dx  %4d sess  %10s gross  %-8s %s%s",
			humanBytes(r.WastedBytes), r.Occurrences, r.Sessions, humanBytes(r.GrossBytes),
			r.Source, r.Key, detailSuffix(r.Detail))
	}
	return nil
}

// wasteSplit renders the per-source subtotals — the figures that are actually
// sound, since rows within one detector are disjoint. Fixed source order so
// repeated runs are byte-stable (map iteration is not).
func wasteSplit(rep mine.WasteReport) string {
	parts := make([]string, 0, 3)
	for _, src := range []mine.WasteSource{mine.WasteRepeat, mine.WasteFail, mine.WasteMotif} {
		if n := rep.BySource[src]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", src, humanBytes(n)))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

// detailSuffix appends a poll row's raw command text — its unit is the exact
// text, not the normalized key, so the key alone under-identifies it.
func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return "  " + strconv.Quote(detail)
}
