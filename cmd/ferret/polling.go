package main

import (
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// PollingCmd is the kong-ready flag struct for `ferret polling` (ferret-cax):
// a corpus-wide ranking of exact-duplicate shell commands repeated inside one
// session — the polling loops (`loto inbox` ×932, `git status --short` ×733)
// that burn.go's normalized ranking and the sequence miners both miss. Emitting
// the ranking is all this command does; replacing a poll with a watch, a hook,
// or a cached read is a human call.
//
// Registration is left to the primary (write-set boundary): add
//
//	Polling PollingCmd `cmd:"" help:"Rank exact-duplicate commands repeated within a session." name:"polling"`
//
// to the CLI struct in cmd/ferret/main.go, and
//
//	case "polling":
//		err = cmdPolling(CLI.Polling)
//
// to the dispatch switch, mirroring every other subcommand.
type PollingCmd struct {
	CommonFlags
}

// cmdPolling wires the kong flags to mine.MinePolling over the canonical
// events artifact — the same load-then-mine shape as cmdMisfires (misfires.go).
func cmdPolling(cmd PollingCmd) error {
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
	rep := mine.MinePolling(events)
	if c.format == fmtJSON {
		return writePollingJSON(os.Stdout, rep, c.limit)
	}
	return writePollingText(os.Stdout, rep, c.limit, c.maxBytes)
}

// writePollingJSON emits the ranked table as a single JSON document, pre-capping
// rows to limit (0 = unlimited) — mirrors writeMisfiresJSON; the out.JSON
// contract ignores row limits itself, so the cap happens here.
func writePollingJSON(w io.Writer, rep mine.PollingReport, limit int) error {
	total := len(rep.Rows)
	rows := rep.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return out.JSON(w, map[string]any{
		keyRows:      rows,
		keySessions:  rep.Sessions,
		keyTotal:     total,
		keyTruncated: len(rows) < total,
	})
}

// writePollingText emits the dense human/analyst rendering: one row per polled
// command, worst-session count first in the eye path since that is the number
// that decides whether a fix is worth building.
func writePollingText(w io.Writer, rep mine.PollingReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ polling: exact-duplicate shell commands repeated inside a single session — the",
		"≡ loops a watch, a hook, or a cached read replaces. Unit is the raw command text,",
		"≡ not the normalized token: `git status --short` and `git status -sb` share a burn",
		"≡ key but are different polls. repeats = summed runs over sessions that polled (≥2",
		"≡ runs); max = worst single session; score = repeats × sessions, so a habit spread",
		"≡ across sessions outranks one runaway session. key joins to the burn table.",
		"≡ Command text is truncated at ingest — two long commands sharing a prefix merge.")

	sink.Head("polling rows=%d sessions=%d", len(rep.Rows), rep.Sessions)
	for _, row := range rep.Rows {
		if !sink.Row("max=%-5d repeats=%-6d sessions=%-4d score=%-8.0f %-20s  %q",
			row.MaxPerSession, row.TotalRepeats, row.Sessions, row.Score, row.Key, row.Command) {
			break
		}
	}
	return nil
}
