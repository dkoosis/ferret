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
	win, err := corpusWindow(cmd.Since, cmd.Until)
	if err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	s, err := mine.Summarize(c.eventsPath(), cmd.By, win)
	if err != nil {
		return err
	}
	if c.format == fmtJSON {
		return summaryJSON(c, s)
	}
	return summaryText(c, s)
}

// summaryJSON emits the buckets, the top actions, and the Bash-against-native
// mix (ferret-2mf) at the top level. ratio is null when the window held no
// native file-tool call. The window's counts appear only when a window was set.
func summaryJSON(c *common, s *mine.Summary) error {
	total := len(s.Buckets)
	capBuckets := s.Buckets
	if c.limit > 0 && len(capBuckets) > c.limit {
		capBuckets = capBuckets[:c.limit]
	}
	m := s.Shell
	return out.JSON(os.Stdout, withDateWindow(s.Window, s.Kept, map[string]any{
		"by": s.By, "buckets": capBuckets,
		keyTotal: total, keyTruncated: len(capBuckets) < total,
		"topActions":          s.TopActions,
		"bashCalls":           m.BashCalls,
		"nativeCalls":         m.NativeCalls,
		"ratio":               m.Ratio,
		"bashBytes":           m.BashBytes,
		"bashBytesPerSession": m.BashBytesPerSession,
		keySessions:           m.Sessions,
		"unkeyedBashCalls":    m.UnkeyedBashCalls,
	}))
}

// shellLine renders the Bash-against-native mix. A call counts once however
// many segments it split into; bytes are Bash results, not tokens.
func shellLine(m mine.ShellMix) string {
	ratio := "n/a"
	if m.Ratio != nil {
		ratio = fmt.Sprintf("%.2f", *m.Ratio)
	}
	return fmt.Sprintf("shell bash_calls=%d native_calls=%d ratio=%s bash_bytes_per_session=%.0f · bash_bytes=%d sessions=%d",
		m.BashCalls, m.NativeCalls, ratio, m.BashBytesPerSession, m.BashBytes, m.Sessions)
}

func summaryText(c *common, s *mine.Summary) error {
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ summary: corpus health — event volume, failure and retry rates per "+s.By+".",
		"≡ fail = action errored · cfail = inside a failed compound · unpaired = call without result.",
		"≡ cwd = harness cwd-reset tells (ferret-cax) — count, not a rate; a floor on hidden resets.",
		"≡ shell = Bash calls (a compound or pipe counts once) per native file-tool call: Read Edit MultiEdit Write NotebookEdit Grep Glob.")
	tag := ""
	if s.Window.Set() {
		tag = " " + windowStreams(s.Window, s.Kept)
	}
	sink.Head("summary by=%s buckets=%d%s", s.By, len(s.Buckets), tag)
	sink.Head("%s", shellLine(s.Shell))
	if n := s.Shell.UnkeyedBashCalls; n > 0 {
		sink.Head("≡ %d of %d bash calls have no tool_use id (ingested before ferret-2mf): grouped by transcript line; re-ingest for exact counts.",
			n, s.Shell.BashCalls)
	}
	for _, b := range s.Buckets {
		sink.Row("%8d ev %5d sess fail=%.1f%% cfail=%.1f%% retry=%.1f%% unpaired=%.1f%% cwd=%-4d  %s",
			b.Events, b.Sessions, pct(b.Fails, b.Events), pct(b.CFails, b.Events), pct(b.Retries, b.Events), pct(b.Unpaired, b.Events), b.CwdResets, b.Key)
	}
	if s.By == "corpus" && len(s.TopActions) > 0 {
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
