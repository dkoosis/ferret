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
		return writeMisfiresJSON(os.Stdout, rep)
	}
	return writeMisfiresText(os.Stdout, rep, c.limit, c.maxBytes)
}

// writeMisfiresJSON emits the ranked bundle as a single JSON document — the
// analyst / ledger-loop ingestable contract (out.JSON, like every other JSON
// command in this package).
func writeMisfiresJSON(w io.Writer, rep mine.MisfireReport) error {
	return out.JSON(w, map[string]any{
		"rows":         rep.Rows,
		"repairs":      rep.Repairs,
		keyTotal:       len(rep.Rows),
		"repairsTotal": len(rep.Repairs),
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
		"≡ this command only ranks, it does not write to the ledger.")

	sink.Head("misfires rows=%d repairs=%d", len(rep.Rows), len(rep.Repairs))
	for _, row := range rep.Rows {
		if !sink.Row("%-24s  fails=%-4d sessions=%-4d calls=%-4d fail-rate=%.2f  score=%.0f",
			row.Key, row.Fails, row.FailSess, row.Calls, row.FailRate, row.Score) {
			break
		}
	}

	if len(rep.Repairs) > 0 {
		sink.Head("repair pairs (failed → fixed):")
		for _, p := range rep.Repairs {
			if !repairRow(sink, p) {
				break
			}
		}
	}
	return nil
}

// repairRow renders one repair pair, falling back to the key-level form when
// neither side captured raw command text. Returns the sink's keep-going signal.
func repairRow(sink *out.Sink, p mine.RepairPair) bool {
	if p.FailedRaw == "" && p.FixedRaw == "" {
		return sink.Row("%-24s  count=%d  (key-level — no raw command text captured for this key)", p.Key, p.Count)
	}
	return sink.Row("%-24s  count=%d  %q → %q", p.Key, p.Count, p.FailedRaw, p.FixedRaw)
}
