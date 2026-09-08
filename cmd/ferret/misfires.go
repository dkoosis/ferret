package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// errByRequiresReasons and errByUnsupported are the ferret-cf9 --by
// validation failures — static sentinels (err113) wrapped with the
// offending value below.
var (
	errByRequiresReasons = errors.New("--by requires --reasons")
	errByUnsupported     = errors.New(`--by not supported; only "week"`)
)

// MisfiresCmd is the kong-ready flag struct for `ferret misfires` (ferret-ct1):
// a corpus-wide ranking of repeated command failures + the repair pairs that
// resolved them, feeding the existing fixes/substitution ledger loop
// (internal/fixes, `ferret fixes sub`) — this command only emits the ranking,
// it does not write to the ledger.
//
// Registration is left to the primary (write-set boundary): add
//
//	Misfires MisfiresCmd `cmd:"" help:"Rank repeated command misfires + repair pairs corpus-wide." name:"misfires"`
//
// to the CLI struct in cmd/ferret/main.go, and
//
//	case "misfires":
//		err = cmdMisfires(CLI.Misfires)
//
// to the dispatch switch, mirroring every other subcommand.
type MisfiresCmd struct {
	CommonFlags
	// Reasons and Key add the ferret-54q breakdown: without --reasons,
	// cmdMisfires' output is unchanged (byte-identical) from before either
	// flag existed.
	Reasons bool   `help:"Break each ranked key's failures down by captured error-text reason." name:"reasons"`
	Key     string `help:"Scope --reasons to one misfire key (Event.Action); omitted = every ranked key." name:"key"`
	// By adds the ferret-cf9 time axis on top of --reasons: without --by,
	// output is unchanged from before this flag existed. Only "week" is
	// supported (the bead's one named axis); requires --reasons because a
	// week-bucketed histogram is a breakdown OF the reason rows, not a
	// standalone view.
	By string `help:"Bucket --reasons by an ISO time axis (supported: week); requires --reasons." name:"by"`
}

// cmdMisfires wires the kong flags to mine.MineMisfires over the canonical
// events artifact — the same load-then-mine shape as cmdGates (gates.go).
// --reasons switches to the mine.MineReasons breakdown instead (ferret-54q);
// the plain ranking below it is untouched either way.
func cmdMisfires(cmd MisfiresCmd) error {
	if err := validateMisfiresBy(cmd); err != nil {
		return err
	}
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return err
	}
	if cmd.By != "" {
		rep := mine.MineReasonsByWeek(events, cmd.Key)
		if c.format == fmtJSON {
			return writeReasonsWeeklyJSON(os.Stdout, rep)
		}
		return writeReasonsWeeklyText(os.Stdout, rep, c.limit, c.maxBytes)
	}
	if cmd.Reasons {
		rep := mine.MineReasons(events, cmd.Key)
		if c.format == fmtJSON {
			return writeReasonsJSON(os.Stdout, rep)
		}
		return writeReasonsText(os.Stdout, rep, c.limit, c.maxBytes)
	}
	rep := mine.MineMisfires(events)
	if c.format == fmtJSON {
		return writeMisfiresJSON(os.Stdout, rep, c.limit)
	}
	return writeMisfiresText(os.Stdout, rep, c.limit, c.maxBytes)
}

// validateMisfiresBy enforces the bead's Rule up front, before touching the
// corpus: --by without --reasons is refused by name, not silently ignored;
// an unsupported --by value is refused the same way ("week" is the only
// axis this bead builds).
func validateMisfiresBy(cmd MisfiresCmd) error {
	if cmd.By == "" {
		return nil
	}
	if !cmd.Reasons {
		return fmt.Errorf("%w: got --by %q", errByRequiresReasons, cmd.By)
	}
	if cmd.By != "week" {
		return fmt.Errorf("%w: got %q", errByUnsupported, cmd.By)
	}
	return nil
}

// writeMisfiresJSON emits the ranked bundle as a single JSON document, pre-
// capping rows and repairs to limit (0 = unlimited) — mirrors writeBurnJSON;
// the out.JSON contract ignores row limits itself, so the cap happens here.
func writeMisfiresJSON(w io.Writer, rep mine.MisfireReport, limit int) error {
	rowsTotal, repairsTotal, swallowedTotal := len(rep.Rows), len(rep.Repairs), len(rep.Swallowed)
	rows, repairs, swallowed := rep.Rows, rep.Repairs, rep.Swallowed
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	if limit > 0 && len(repairs) > limit {
		repairs = repairs[:limit]
	}
	if limit > 0 && len(swallowed) > limit {
		swallowed = swallowed[:limit]
	}
	return out.JSON(w, map[string]any{
		keyRows:          rows,
		"repairs":        repairs,
		"swallowed":      swallowed,
		keyTotal:         rowsTotal,
		"repairsTotal":   repairsTotal,
		"swallowedTotal": swallowedTotal,
	})
}

// writeMisfiresText emits the dense human/analyst rendering: the misfire
// table (command key, failure count, sessions, fail rate — the bead's named
// columns) followed by the repair pairs pulled from it.
func writeMisfiresText(w io.Writer, rep mine.MisfireReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ misfires: corpus-wide ranking of repeated command failures. key = Event.Action",
		"≡ (shellnorm token for shell events, tool name for tool events); score = fails × ",
		"≡ sessions — a command that fails the same way across many sessions ranks highest.",
		"≡ repair pairs: a failed call followed by a same-key success (Event.Retry) — the",
		"≡ existing repair tell. Feeds the fixes/substitution ledger loop (ferret fixes sub);",
		"≡ this command only ranks, it does not write to the ledger.",
		"≡ swallowed: `cmd 2>/dev/null || fallback` — the error text is discarded and the",
		"≡ chain exits with the fallback's code, so no is_error ever fires and the rows",
		"≡ above cannot see these at all. Counts are a FLOOR on hidden failures: the shape",
		"≡ proves a failure would be invisible, never that one happened.")

	sink.Head("misfires rows=%d repairs=%d", len(rep.Rows), len(rep.Repairs))
	emptyNote(sink, len(rep.Rows), "misfiring commands")
	for _, row := range rep.Rows {
		sink.Row("%-24s  fails=%-4d sessions=%-4d calls=%-4d fail-rate=%.2f  score=%.0f",
			row.Key, row.Fails, row.FailSess, row.Calls, row.FailRate, row.Score)
	}

	writeRepairSection(sink, rep.Repairs)
	writeSwallowSection(sink, rep.Swallowed)
	// Legal moves, not a plan (DK-AXI rule 11): price these failures against
	// the other detectors, or record a repair pair as a substitution.
	if len(rep.Rows) > 0 {
		sink.NextHead("ferret friction --source misfire", "ferret fixes sub --intent <class> --wrong <tool> --better <template>")
	}
	return nil
}

// writeRepairSection emits the failed→fixed pairs under their own header, or
// nothing when none were detected. Extracted from writeMisfiresText with its
// swallow sibling to keep that function under the gocognit ceiling.
func writeRepairSection(sink *out.Sink, repairs []mine.RepairPair) {
	if len(repairs) == 0 {
		return
	}
	sink.Head("repair pairs (failed → fixed):")
	for _, p := range repairs {
		repairRow(sink, p)
	}
}

// writeSwallowSection emits the swallowed-error table — the misfires the rows
// above structurally cannot see — or nothing when none were detected.
func writeSwallowSection(sink *out.Sink, swallowed []mine.SwallowRow) {
	if len(swallowed) == 0 {
		return
	}
	sink.Head("swallowed errors (invisible to is_error — floor on hidden misfires):")
	for _, row := range swallowed {
		swallowRow(sink, row)
	}
}

// swallowRow renders one swallowed-error row, appending the exemplar command
// only when ingest captured one — the shape is the finding, and the exemplar
// is what makes it actionable (it names the chain to rewrite). Returns the
// sink's keep-going signal.
func swallowRow(sink *out.Sink, row mine.SwallowRow) bool {
	line := "%-24s  swallowed=%-4d sessions=%-4d calls=%-4d rate=%.2f  score=%.0f"
	args := []any{row.Key, row.Swallows, row.SwallowSess, row.Calls, row.SwallowRate, row.Score}
	if row.Exemplar != "" {
		line += "  %q"
		args = append(args, row.Exemplar)
	}
	return sink.Row(line, args...)
}

// repairRow renders one repair pair, falling back to the key-level form when
// neither side captured raw command text. Returns the sink's keep-going signal.
func repairRow(sink *out.Sink, p mine.RepairPair) bool {
	if p.FailedRaw == "" && p.FixedRaw == "" {
		return sink.Row("%-24s  count=%d  (key-level — no raw command text captured for this key)", p.Key, p.Count)
	}
	return sink.Row("%-24s  count=%d  %q → %q", p.Key, p.Count, p.FailedRaw, p.FixedRaw)
}

// writeReasonsJSON emits the ferret-54q reason breakdown as a single JSON
// document — no row cap: a corpus-wide reason list is small (one row per
// distinct error-text bucket per key), unlike the row-heavy tables above.
func writeReasonsJSON(w io.Writer, rep mine.ReasonsReport) error {
	return out.JSON(w, map[string]any{
		"keys":       rep.Keys,
		"failed":     rep.Failed,
		"uncaptured": rep.Uncaptured,
	})
}

// writeReasonsText emits the reason breakdown: one line per ranked key
// listing its top error-text reasons and counts, then the coverage line the
// bead's Rules require — how many failed events in range carry no captured
// error text, so a pre-capture corpus reads as "not captured" rather than
// "no failures".
func writeReasonsText(w io.Writer, rep mine.ReasonsReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ misfires --reasons: each ranked key's failures broken down by captured",
		"≡ error-text reason (event.Err, set once at ingest — never re-scanned from",
		"≡ transcripts here). A hook/guard denial groups by its script name",
		"≡ (hook:<script>), not its message text; every other reason is the first ~70",
		"≡ chars of the tool_result text, whitespace-collapsed.")

	sink.Head("reasons keys=%d failed=%d", len(rep.Keys), rep.Failed)
	emptyNote(sink, len(rep.Keys), "keys with a captured reason")
	for _, kr := range rep.Keys {
		sink.Row("%s", reasonLine(kr))
	}
	writeCoverageNote(sink, rep.Failed, rep.Uncaptured)
	return nil
}

// reasonLine renders one key's ranked reasons as "reason count · reason
// count · …", the shape the bead's Acceptance Criteria names.
func reasonLine(kr mine.KeyReasons) string {
	parts := make([]string, 0, len(kr.Reasons))
	for _, r := range kr.Reasons {
		parts = append(parts, fmt.Sprintf("%s %d", r.Reason, r.Count))
	}
	return fmt.Sprintf("%-24s  %s", kr.Key, strings.Join(parts, " · "))
}

// writeCoverageNote states the report's own coverage: a corpus ingested
// before event.Err existed decodes every old row with Err == "", and without
// this line that reads as "no failures" rather than "not captured" — the
// exact failure the bead's Rules call out. Shared by the flat and --by week
// reasons reports (both carry the same Failed/Uncaptured corpus counters).
func writeCoverageNote(sink *out.Sink, failed, uncaptured int) {
	if failed == 0 {
		sink.Head("coverage: 0 failed events in range")
		return
	}
	if uncaptured == failed {
		sink.Head("coverage: 0/%d failed events carry a captured reason — not captured (pre-ferret-54q ingest), not \"no failures\"; re-ingest to see reasons",
			failed)
		return
	}
	share := float64(uncaptured) / float64(failed) * 100
	sink.Head("coverage: %d/%d failed events (%.0f%%) carry no captured error text",
		uncaptured, failed, share)
}

// writeReasonsWeeklyJSON emits the ferret-cf9 --by week bundle as a single
// JSON document — no row cap, mirroring writeReasonsJSON: a per-key,
// per-reason, per-week histogram over one corpus is bounded by construction
// (weeks × reasons × keys), never row-heavy like the misfire tables.
func writeReasonsWeeklyJSON(w io.Writer, rep mine.WeeklyReasonsReport) error {
	return out.JSON(w, map[string]any{
		"weeks":      rep.Weeks,
		"keys":       rep.Keys,
		"failed":     rep.Failed,
		"uncaptured": rep.Uncaptured,
	})
}

// writeReasonsWeeklyText emits the bead's named view: one ISO-week histogram
// line per (key, reason), explicit zeros and uncaptured markers included, in
// week order — the shape that lets SendMessage's fossil total (340 failures,
// weekly shape 168/129/43/0) read as "already fixed" instead of "still
// burning".
func writeReasonsWeeklyText(w io.Writer, rep mine.WeeklyReasonsReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ misfires --reasons --by week: each ranked key's reasons broken down into",
		"≡ one count per ISO week, in week order. A week with zero occurrences of a",
		"≡ reason prints 0, not a gap — a decayed reason reads as a run to zero rather",
		"≡ than vanishing from the table. \"uncaptured\" marks a week whose failures for",
		"≡ that key carry no captured error text at all (a pre-ferret-54q ingest",
		"≡ window) — read that as \"not captured\", never as a genuine zero.")

	sink.Head("reasons-by-week keys=%d weeks=%d failed=%d", len(rep.Keys), len(rep.Weeks), rep.Failed)
	emptyNote(sink, len(rep.Keys), "keys with a captured reason")
	for _, kr := range rep.Keys {
		for _, rw := range kr.Reasons {
			sink.Row("%s", reasonWeekLine(kr.Key, rw))
		}
	}
	writeCoverageNote(sink, rep.Failed, rep.Uncaptured)
	return nil
}

// reasonWeekLine renders one (key, reason)'s histogram as "key  reason
// total=N  <week>=<count> <week>=<count> …", uncaptured weeks spelled out
// rather than given a numeric placeholder that could be mistaken for a real
// zero.
func reasonWeekLine(key string, rw mine.ReasonWeekly) string {
	parts := make([]string, 0, len(rw.Weeks))
	for _, b := range rw.Weeks {
		if b.Uncaptured {
			parts = append(parts, b.Week+"=uncaptured")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", b.Week, b.Count))
	}
	return fmt.Sprintf("%-24s  %-40s total=%-4d  %s", key, rw.Reason, rw.Total, strings.Join(parts, " "))
}
