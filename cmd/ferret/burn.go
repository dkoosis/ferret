package main

import (
	"fmt"
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
//	} `cmd:"" help:"Ranked corpus-wide context burn per normalized command."`
//
// and dispatches alongside the other subcommands' switch arm:
//
//	case "burn":
//	    err = cmdBurn(&CLI.Burn)
type BurnCmd struct {
	CommonFlags
}

// cmdBurn renders `ferret burn`: rows are normalized commands (shellnorm key
// for shell, tool name for tool, at:class for attachments), ranked by each
// row's TOTAL measured context bytes across the whole ingested corpus —
// tool_use input plus tool_result content, summed over every call (ferret-noj).
// Summing over calls is what prices repetition: many small calls out-burn one
// huge one without any modeled per-call constant. Mirrors cmdSummary's shape
// (fromCommonFlags → ensureData → mine.* → text/json render).
func cmdBurn(cmd *BurnCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 20)
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
		"≡ bytes = event.Bytes (tool_use input + tool_result content) summed over every call — measured, not modeled. This is what enters the request body, so it is what ranks (ferret-noj).",
		"≡ bytes/call is the per-call toll a \"should I stop running this?\" decision turns on; the two columns disagree when a cheap command is called constantly.",
		"≡ shell rows are shellnorm-normalized (sh:git_commit, ...), tool rows keyed by tool name, attachment classes by at:class.",
		"≡ an at:* row's bytes are the WHOLE serialized attachment record, not the content a model actually sees — that is deliberate (ferret-rfc), not a bug. A row marked ⚠ over-count exceeds its disclosed content bytes by more than 2x; read its bytes as an upper bound on injected context, not the figure itself (ferret-wmb).")
	sink.Head("burn events=%d sessions=%d rows=%d", res.Events, res.Sessions, len(res.Rows))
	emptyNote(sink, len(res.Rows), "commands")
	for i := range res.Rows {
		r := &res.Rows[i] // index-range: BurnRow carries a map field, value-range trips rangeValCopy
		sink.Row("%10s bytes  %8s/call  %6d calls  %4d sess  %s%s",
			humanBytes(r.Bytes), humanBytes(int(r.BytesPerCall)), r.Calls, r.Sessions, r.Key, overcountNote(r))
	}
	// Legal moves, not a plan (DK-AXI rule 11): gross cost is not waste — the
	// merged view says how much of it bought nothing.
	if len(res.Rows) > 0 {
		sink.NextHead("ferret friction")
	}
	return nil
}

// overcountNote renders the ferret-wmb disclosure suffix for a row the miner
// flagged Overcount — empty string for every other row. contentBytes == 0 has
// no finite ratio to print (see mine.isOvercounted), so it states the
// disclosed content directly instead of dividing by zero.
func overcountNote(r *mine.BurnRow) string {
	if !r.Overcount {
		return ""
	}
	if r.ContentBytes == 0 {
		return "  ⚠ over-count: 0B disclosed content in this record"
	}
	ratio := float64(r.Bytes) / float64(r.ContentBytes)
	return fmt.Sprintf("  ⚠ over-count %.1fx record/content (content %s of %s record)",
		ratio, humanBytes(r.ContentBytes), humanBytes(r.Bytes))
}
