package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// WindowFlags narrow a corpus to the streams that started in a date window
// (ferret-pr4). Only commands that build their whole result from one windowed
// corpus embed it; friction and the rest do not, so they never offer a flag
// they would ignore.
type WindowFlags struct {
	Since string `help:"Keep only streams that started on or after this date, YYYY-MM-DD (a stream with no timestamp is dropped)." name:"since"`
	Until string `help:"Keep only streams that started on or before this date, YYYY-MM-DD, inclusive." name:"until"`
}

// window sets the corpus window on lo and returns it.
func (lo *lensOpts) window(wf WindowFlags) *lensOpts {
	lo.since, lo.until = wf.Since, wf.Until
	return lo
}

// corpusWindow resolves the corpus commands' --since/--until (ferret-pr4) into
// the instants mine.Options takes. Both are YYYY-MM-DD, the spelling reach
// uses; --until is inclusive, so it becomes the end of that day. An empty flag
// leaves its side unbounded (the zero time), and no flags at all leave the
// corpus untouched.
func corpusWindow(since, until string) (from, to time.Time, err error) {
	if since != "" {
		t, err := parseDateFlag(since, errBadSince)
		if err != nil {
			return from, to, err
		}
		from = startOfDay(t)
	}
	if until != "" {
		t, err := parseDateFlag(until, errBadUntil)
		if err != nil {
			return from, to, err
		}
		to = endOfDay(t)
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return from, to, errReachWindow
	}
	return from, to, nil
}

// windowLine renders what a window kept, or "" when the corpus is unwindowed.
// The first number is the kept stream count the command's own header repeats;
// the total is what the window was cut from, so a count reads against its
// denominator.
func windowLine(c *mine.Corpus) string {
	w := c.Window
	if w == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("window")
	if !w.Since.IsZero() {
		fmt.Fprintf(&b, " since=%s", w.Since.Format(dateLayout))
	}
	if !w.Until.IsZero() {
		fmt.Fprintf(&b, " until=%s", w.Until.Format(dateLayout))
	}
	fmt.Fprintf(&b, " kept=%d of %d streams · dropped no-timestamp=%d", w.Kept, w.Total, w.NoTime)
	return b.String()
}

// windowHead prints the window line under a text report's header. It prints
// nothing without a window, so an unwindowed report stays byte-identical.
func windowHead(sink *out.Sink, c *mine.Corpus) {
	if ln := windowLine(c); ln != "" {
		sink.Head("%s", ln)
	}
}

// keyStreamWindow is the JSON key for the window's counts. It is not "window":
// around already uses that key for its --window step count.
const keyStreamWindow = "streamWindow"

// withWindow adds the window's counts to a JSON payload, and only then: an
// unwindowed payload keeps today's exact keys.
func withWindow(m map[string]any, c *mine.Corpus) map[string]any {
	if w := c.Window; w != nil {
		win := map[string]any{"kept": w.Kept, "total": w.Total, "dropped_no_timestamp": w.NoTime}
		if !w.Since.IsZero() {
			win["since"] = w.Since.Format(dateLayout)
		}
		if !w.Until.IsZero() {
			win["until"] = w.Until.Format(dateLayout)
		}
		m[keyStreamWindow] = win
	}
	return m
}
