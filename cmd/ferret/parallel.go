package main

import (
	"io"
	"os"
	"time"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ParallelCmd is the kong-ready flag struct for `ferret parallel` — the
// parallelism tax (ferret-04d). Every other ferret detector measures bytes
// WITHIN a transcript and is structurally blind to how many transcripts were
// live at once; this one reads the timestamps across the corpus instead.
type ParallelCmd struct {
	CommonFlags
	IdleGap time.Duration `help:"A thread stops counting as running after this long without an event." default:"5m" name:"idle-gap"`
}

// cmdParallel renders `ferret parallel`. Mirrors cmdBurn's shape
// (fromCommonFlags → ensureData → mine.* → text/json render).
func cmdParallel(cmd *ParallelCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 40) // two profiles share one sink's row budget
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	res, err := mine.Parallel(c.eventsPath(), mine.ParallelOptions{IdleGap: cmd.IdleGap})
	if err != nil {
		return err
	}

	if c.format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	return writeParallelText(os.Stdout, res, c.limit, c.maxBytes)
}

// writeParallelText renders both profiles into one sink. The two tables are
// deliberately identical in shape so the reader can compare them column by
// column — which of the two carries the spend is the question the report exists
// to answer, and it is settled by comparing one cell against its twin.
func writeParallelText(w io.Writer, res *mine.ParallelResult, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ parallel: how much context was spent while N threads ran at once — the cost axis inside-a-transcript detectors cannot see.",
		"≡ window = distinct sessions overlapping in wall-clock. fan-out = distinct agent ids inside ONE session (agent_id is the only parent-vs-subagent tell; session_id is shared).",
		"≡ %bytes@N+ is cumulative: the share of all measured context bytes spent while N or more ran. That column is the finding; %time is context for it.",
		"≡ a thread stops counting as running after --idle-gap without an event, so an open-but-idle window never inflates the overlap.")
	sink.Head("parallel sessions=%d events=%d untimed=%d bytes=%s span=%.1fh active=%.1fh idle-gap=%.0fm",
		res.Sessions, res.Events, res.Untimed, humanBytes(int(res.Bytes)), res.SpanHours, res.ActiveHours, res.IdleGapMin)
	emptyNote(sink, res.Events, "timed events")

	writeParallelProfile(sink, "window (distinct sessions overlapping in wall-clock)", &res.Window)
	writeParallelProfile(sink, "fan-out (distinct agents inside one session; hours are session-hours)", &res.Fanout)
	if res.Events > 0 {
		sink.NextHead("ferret burn")
	}
	return nil
}

// writeParallelProfile emits one profile's headline and level table.
func writeParallelProfile(sink *out.Sink, title string, p *mine.ConcurrencyProfile) {
	sink.Head("%s: max=%d mean=%.2f by time, %.2f by bytes", title, p.Max, p.TimeWeightedMean, p.ByteWeightedMean)
	for i := range p.Levels {
		r := &p.Levels[i]
		sink.Row("  N=%-3d %8.1fh %5.1f%% time   %10s %5.1f%% bytes   %5.1f%% bytes@N+   %7d events",
			r.N, r.Hours, r.HoursPct, humanBytes(int(r.Bytes)), r.BytesPct, r.BytesPctAtOrAbove, r.Events)
	}
}
