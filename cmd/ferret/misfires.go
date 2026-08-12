package main

import (
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
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
}

// cmdMisfires wires the kong flags to mine.MineMisfires over the canonical
// events artifact — the same load-then-mine shape as cmdGates (gates.go).
func cmdMisfires(cmd MisfiresCmd) error {
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
	rep := mine.MineMisfires(events)
	if c.format == fmtJSON {
		return writeMisfiresJSON(os.Stdout, rep, c.limit)
	}
	return writeMisfiresText(os.Stdout, rep, c.limit, c.maxBytes)
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
	for _, row := range rep.Rows {
		if !sink.Row("%-24s  fails=%-4d sessions=%-4d calls=%-4d fail-rate=%.2f  score=%.0f",
			row.Key, row.Fails, row.FailSess, row.Calls, row.FailRate, row.Score) {
			break
		}
	}

	writeRepairSection(sink, rep.Repairs)
	writeSwallowSection(sink, rep.Swallowed)
	// Legal moves, not a plan (DK-AXI rule 11): price these failures against
	// the other detectors, or record a repair pair as a substitution.
	if len(rep.Rows) > 0 {
		sink.NextHead("ferret friction --source misfire", "ferret fixes sub --from <failed> --to <fixed>")
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
		if !repairRow(sink, p) {
			return
		}
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
		if !swallowRow(sink, row) {
			return
		}
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
