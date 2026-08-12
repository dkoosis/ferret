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
//	} `cmd:"" help:"Ranked corpus-wide render burn per normalized command."`
//
// and dispatches alongside the other subcommands' switch arm:
//
//	case "burn":
//	    err = cmdBurn(&CLI.Burn)
type BurnCmd struct {
	CommonFlags
}

// cmdBurn renders `ferret burn`: rows are normalized commands (shellnorm key
// for shell, tool name for tool), ranked by modeled render cost across the
// whole ingested corpus — what the reader pays per call rendered, not per byte
// returned (ferret-cax) — with the output-byte columns kept alongside so the
// old ordering stays readable in the same table. Mirrors cmdSummary's shape
// (fromCommonFlags → ensureData → mine.* → text/json render).
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
		"≡ rend = modeled rendered cost, the ranking key: per-call chrome (shell ≈6 lines of command echo + preview frame + permission line; native tools ≈1 collapsed line) + each call's preview-capped output share. A model, not a measurement (ccp-3s1c).",
		"≡ out-bytes = event.Bytes (tool_use input + tool_result content); kept beside rend because they disagree — a cheap-output, high-count command is near-zero on bytes and a top burner on rend.",
		"≡ shell rows are shellnorm-normalized (sh:git_commit, ...), tool rows keyed by tool name.")
	sink.Head("burn events=%d sessions=%d rows=%d", res.Events, res.Sessions, len(res.Rows))
	for i := range res.Rows {
		r := &res.Rows[i] // index-range: BurnRow carries a map field, value-range trips rangeValCopy
		sink.Row("%10s rend  %8s/call  %10s out  %6d calls  %8s/call  %4d sess  %s",
			humanBytes(r.RenderCost), humanBytes(int(r.RenderPerCall)),
			humanBytes(r.OutBytes), r.Calls, humanBytes(int(r.BytesPerCall)), r.Sessions, r.Key)
	}
	return nil
}
