package main

import (
	"fmt"
	"os"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

var errBadBy = usage("bad --by (want corpus|project|session)")

// ---- summary ----

func cmdSummary() error {
	cmd := &CLI.Summary
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 20)
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	switch cmd.By {
	case "corpus", "project", "session":
	default:
		return fmt.Errorf("%w: %q", errBadBy, cmd.By)
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	s, err := mine.Summarize(c.eventsPath(), cmd.By)
	if err != nil {
		return err
	}
	if c.format == fmtJSON {
		total := len(s.Buckets)
		capBuckets := s.Buckets
		if c.limit > 0 && len(capBuckets) > c.limit {
			capBuckets = capBuckets[:c.limit]
		}
		return out.JSON(os.Stdout, map[string]any{
			"by": s.By, "buckets": capBuckets,
			keyTotal: total, keyTruncated: len(capBuckets) < total,
			"topActions": s.TopActions,
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ summary: corpus health — event volume, failure and retry rates per "+cmd.By+".",
		"≡ fail = action errored · cfail = inside a failed compound · unpaired = call without result.",
		"≡ cwd = harness cwd-reset tells (ferret-cax) — count, not a rate; a floor on hidden resets.")
	sink.Head("summary by=%s buckets=%d", s.By, len(s.Buckets))
	for _, b := range s.Buckets {
		sink.Row("%8d ev %5d sess fail=%.1f%% cfail=%.1f%% retry=%.1f%% unpaired=%.1f%% cwd=%-4d  %s",
			b.Events, b.Sessions, pct(b.Fails, b.Events), pct(b.CFails, b.Events), pct(b.Retries, b.Events), pct(b.Unpaired, b.Events), b.CwdResets, b.Key)
	}
	if cmd.By == "corpus" && len(s.TopActions) > 0 {
		sink.Head("top actions:")
		for i, a := range s.TopActions {
			if i >= 15 {
				break
			}
			sink.Row("%8dx fail=%.1f%%  %s", a.Count, pct(a.Fails, a.Count), a.Action)
		}
	}
	return nil
}
