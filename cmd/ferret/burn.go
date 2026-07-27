package main

import (
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// BurnCmd is the kong-ready flag struct for `ferret burn` — a ranked table of
// context burn per normalized command over the whole corpus (ferret-nrr).
// The primary wires registration into CLI (cmd/ferret/main.go):
//
//	Burn struct {
//	    CommonFlags
//	} `cmd:"" help:"Ranked corpus-wide output-byte burn per normalized command."`
//
// and dispatches alongside the other subcommands' switch arm:
//
//	case "burn":
//	    err = cmdBurn(&CLI.Burn)
type BurnCmd struct {
	CommonFlags
}

// cmdBurn renders `ferret burn`: rows are normalized commands (shellnorm key
// for shell, tool name for tool), ranked by total measured output-byte cost
// across the whole ingested corpus, with calls/bytes-per-call/session-count
// alongside. Mirrors cmdSummary's shape (fromCommonFlags → ensureData →
// mine.* → text/json render).
func cmdBurn(cmd *BurnCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 20
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	res, err := mine.Burn(c.eventsPath())
	if err != nil {
		return err
	}

	if c.format == fmtJSON {
		return writeBurnJSON(os.Stdout, res, c.limit)
	}
	return writeBurnText(os.Stdout, res, c.limit, c.maxBytes)
}

// writeBurnJSON emits the burn bundle as a single JSON document, pre-capping
// rows to limit (0 = unlimited) — the out.JSON contract ignores row limits
// itself, so the cap happens here.
func writeBurnJSON(w io.Writer, res *mine.BurnResult, limit int) error {
	total := len(res.Rows)
	rows := res.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return out.JSON(w, map[string]any{
		"events": res.Events, "sessions": res.Sessions, "rows": rows,
		keyTotal: total, keyTruncated: len(rows) < total,
	})
}

// writeBurnText renders the human-readable ranked table via out.Sink, which
// self-enforces limit/maxBytes and reports truncation in its tail line.
func writeBurnText(w io.Writer, res *mine.BurnResult, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ burn: ranked context cost per normalized command across the whole corpus — the tune-up list.",
		"≡ out-bytes = event.Bytes (tool_use input + tool_result content, output-dominated for read/list/search calls); shell rows are shellnorm-normalized (sh:git_commit, ...), tool rows keyed by tool name.")
	sink.Head("burn events=%d sessions=%d rows=%d", res.Events, res.Sessions, len(res.Rows))
	for _, r := range res.Rows {
		sink.Row("%10s out  %6d calls  %8s/call  %4d sess  %s",
			humanBytes(r.OutBytes), r.Calls, humanBytes(int(r.BytesPerCall)), r.Sessions, r.Key)
	}
	return nil
}
