package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/reach"
)

// reachMark is the per-opportunity glyph: ✓ store-reached, ✗ a miss.
func reachMark(o reach.Opportunity) string {
	if o.Reached() {
		return "✓"
	}
	return "✗"
}

// ratePct renders a 0..1 rate as a whole-percent string.
func ratePct(r float64) string { return fmt.Sprintf("%.0f%%", r*100) }

// writeReachText emits the dense human scorecard.
func writeReachText(sink *out.Sink, r reach.Report) {
	about(sink,
		"≡ reach: at recall opportunities (dk asking what's known/decided, or re-orienting),",
		"≡ did Claude reach the trixi store FIRST (store) vs grep/gh/none. reach-rate = store/n.",
		"≡ Phase 1 transcript-only; RU (was the result used?) is the tx-kji6 telemetry join.")

	sink.Head("reach %s..%s  n=%d  sessions=%d",
		r.Since.Format(dateLayout), r.Until.Format(dateLayout), r.N, r.Sessions)
	sink.Head("reach-rate  store=%s (%d/%d)  · kg[+beads]=%s  · fail=%d/%d",
		ratePct(r.ReachRate), r.Reached, r.N, ratePct(r.KgRate), r.Failures, r.N)
	sink.Head("channels    store=%d beads=%d grep=%d gh=%d none=%d",
		r.Reached, r.Beads, r.Grep, r.Gh, r.None)
	sink.Head("class       recall=%d reorient=%d", r.Recall, r.Reorient)
	if r.DecodeErrs > 0 {
		sink.Head("decode-errs %d", r.DecodeErrs)
	}
	if r.N == 0 {
		return
	}
	sink.Head("opportunities (✓ store-reached · ✗ miss):")
	for i := range r.Opportunities {
		o := &r.Opportunities[i]
		sink.Row("  %s %-8s %-5s %s/%s  [%s→%s] %s",
			reachMark(*o), o.Class, o.Reach,
			shortProject(o.Project), shortSession(o.Session), o.Cue, firedOrDash(*o), o.Text)
	}
}

// writeReachMD emits ONE appendable markdown block for the Inquiry ledger:
// claudish-dense, self-dating, safe to paste at the end of the running log.
func writeReachMD(w io.Writer, r reach.Report, limit int) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "### reach %s..%s · n=%d\n",
		r.Since.Format(dateLayout), r.Until.Format(dateLayout), r.N)
	fmt.Fprintf(bw, "reach-rate **store=%s** (%d/%d) · kg[+beads]=%s · fail=%d/%d · sessions=%d\n",
		ratePct(r.ReachRate), r.Reached, r.N, ratePct(r.KgRate), r.Failures, r.N, r.Sessions)
	fmt.Fprintf(bw, "channels: store=%d beads=%d grep=%d gh=%d none=%d · class: recall=%d reorient=%d",
		r.Reached, r.Beads, r.Grep, r.Gh, r.None, r.Recall, r.Reorient)
	if r.DecodeErrs > 0 {
		fmt.Fprintf(bw, " · decode-errs=%d", r.DecodeErrs)
	}
	fmt.Fprintln(bw)
	shown := r.Opportunities
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for i := range shown {
		o := &shown[i]
		fmt.Fprintf(bw, "- %s %s %s/%s [%s→%s] %q\n",
			reachMark(*o), o.Class, shortProject(o.Project), shortSession(o.Session), o.Cue, firedOrDash(*o), o.Text)
	}
	if n := len(r.Opportunities) - len(shown); n > 0 {
		fmt.Fprintf(bw, "- … +%d more (raise --limit)\n", n)
	}
	return bw.Flush()
}

func firedOrDash(o reach.Opportunity) string {
	if o.FiredTool == "" {
		return "·"
	}
	return o.FiredTool
}

// shortSession trims a session UUID to its leading segment for legible output.
func shortSession(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// shortProject strips the CC project-dir path prefix from a slug
// (-Users-vcto-Projects-trixi → trixi) for a legible ledger row, keeping
// internal hyphens (cc-plugins, obsidian-track-changes) intact.
func shortProject(p string) string {
	const marker = "-Projects-"
	if i := strings.LastIndex(p, marker); i >= 0 {
		return p[i+len(marker):]
	}
	return p
}
