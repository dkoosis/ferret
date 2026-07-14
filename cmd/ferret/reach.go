package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/reach"
	"github.com/dkoosis/ferret/internal/transcript"
)

const dateLayout = "2006-01-02"

var (
	errBadSince    = errors.New("bad --since (want YYYY-MM-DD)")
	errBadUntil    = errors.New("bad --until (want YYYY-MM-DD)")
	errReachWindow = errors.New("--since must not be after --until")
)

// cmdReach mines recall-opportunity moments across the transcript corpus over a
// date window and reports the reach-rate: did the agent reach for the trixi store
// FIRST at recall moments, or fall back to grep/gh/none. The keystone metric for
// epic tx-qw86; Phase 1 is transcript-only (no telemetry join).
func cmdReach() error {
	cmd := &CLI.Reach
	if cmd.Format != fmtText && cmd.Format != fmtJSON && cmd.Format != fmtMD {
		return fmt.Errorf("%w: %q (want text|json|md)", errBadFormat, cmd.Format)
	}
	win, err := resolveWindow(cmd.Since, cmd.Until)
	if err != nil {
		return err
	}
	root := cmd.Root
	if root == "" {
		if root, err = defaultRoot(); err != nil {
			return err
		}
	}
	rep, err := runReach(root, cmd.Project, cmd.Session, win)
	if err != nil {
		return err
	}
	return emitReach(os.Stdout, rep, cmd.Format, cmd.Limit, cmd.MaxBytes)
}

// resolveWindow parses the --since/--until flags. An empty window defaults to the
// trailing 7 days (the weekly cadence): reports feed a rolling 3-week judgment.
func resolveWindow(since, until string) (reach.Window, error) {
	var w reach.Window
	now := time.Now()
	if until == "" {
		w.Until = endOfDay(now)
	} else {
		t, err := time.ParseInLocation(dateLayout, until, time.Local)
		if err != nil {
			return w, fmt.Errorf("%w: %q", errBadUntil, until)
		}
		w.Until = endOfDay(t)
	}
	if since == "" {
		w.Since = startOfDay(now.AddDate(0, 0, -7))
	} else {
		t, err := time.ParseInLocation(dateLayout, since, time.Local)
		if err != nil {
			return w, fmt.Errorf("%w: %q", errBadSince, since)
		}
		w.Since = startOfDay(t)
	}
	if w.Since.After(w.Until) {
		return w, errReachWindow
	}
	return w, nil
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func endOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 23, 59, 59, int(time.Second-time.Nanosecond), t.Location())
}

// runReach walks the transcript root, scans each in-window main-session
// transcript for recall opportunities, and rolls them up. Subagent transcripts
// carry no dk prompts and are skipped. A file whose last write predates the
// window can hold no in-window turn, so it is stat-skipped — the corpus is large
// and the window is usually days.
func runReach(root, project, session string, win reach.Window) (reach.Report, error) {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return reach.Report{}, err
	}
	var all []reach.Opportunity
	decodeErrs := 0
	for _, src := range srcs {
		if skipSource(src, project, session, win) {
			continue
		}
		opps, derr, scanErr := reach.ScanSource(src, win)
		if scanErr != nil {
			continue // a single unreadable transcript is skipped, not fatal
		}
		decodeErrs += derr
		all = append(all, opps...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp.Before(all[j].Timestamp) })
	rep := reach.Rollup(all, win, decodeErrs)
	reach.JoinTelemetry(&rep) // Phase-2 seam (tx-kji6): no-op in Phase 1
	return rep, nil
}

// skipSource reports whether a transcript is out of scope: a subagent stream (no
// dk prompts), a project/session filter miss, or a file whose last write predates
// the window (so it can hold no in-window turn — a cheap stat-level prefilter over
// a large corpus).
func skipSource(src transcript.Source, project, session string, win reach.Window) bool {
	if src.Agent != "" {
		return true
	}
	if project != "" && !containsFold(src.Project, project) {
		return true
	}
	if session != "" && !sessionMatches(src.Session, session) {
		return true
	}
	if !win.Since.IsZero() {
		if fi, err := os.Stat(src.Path); err == nil && fi.ModTime().Before(win.Since) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return sub == "" || strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// emitReach renders the report in the requested format.
func emitReach(w io.Writer, rep reach.Report, format string, limit, maxBytes int) error {
	switch format {
	case fmtJSON:
		return out.JSON(w, rep)
	case fmtMD:
		return writeReachMD(w, rep, limit)
	default:
		sink := out.NewSink(w, limit, maxBytes)
		defer sink.Close()
		writeReachText(sink, rep)
		return nil
	}
}
