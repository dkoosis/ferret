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
		"≡ RU (Phase 2, transcript): of the store reaches, was the retrieved nug USED? RU=used/(used+unused).")

	sink.Head("reach %s..%s  n=%d  sessions=%d",
		r.Since.Format(dateLayout), r.Until.Format(dateLayout), r.N, r.Sessions)
	sink.Head("reach-rate  store=%s (%d/%d)  · kg[+beads]=%s  · fail=%d/%d",
		ratePct(r.ReachRate), r.Reached, r.N, ratePct(r.KgRate), r.Failures, r.N)
	sink.Head("channels    store=%d beads=%d grep=%d gh=%d none=%d",
		r.Reached, r.Beads, r.Grep, r.Gh, r.None)
	sink.Head("RU          %s", ruLine(r))
	sink.Head("class       recall=%d reorient=%d", r.Recall, r.Reorient)
	if r.DecodeErrs > 0 {
		sink.Head("decode-errs %d", r.DecodeErrs)
	}
	if r.N == 0 {
		return
	}
	sink.Head("opportunities (✓ store-reached · ✗ miss · RU: used/unused/inconc):")
	for i := range r.Opportunities {
		o := &r.Opportunities[i]
		sink.Row("  %s %-8s %-5s%s %s/%s  [%s→%s] %s",
			reachMark(*o), o.Class, o.Reach, ruTag(*o),
			shortProject(o.Project), shortSession(o.Session), o.Cue, firedOrDash(*o), o.Text)
	}
}

// ruLine renders the RU summary: the verdict tally always, the rate only once
// there are enough store reaches to trust it (else "insufficient").
func ruLine(r reach.Report) string {
	tally := fmt.Sprintf("used=%d unused=%d inconc=%d", r.Used, r.Unused, r.Inconclusive)
	if r.RUInsufficient {
		return fmt.Sprintf("%s  · insufficient data (%d store-reach%s, need ≥%d)",
			tally, r.Reached, plural(r.Reached, "", "es"), reach.RUMinN)
	}
	if r.RUDenom == 0 {
		return tally + "  · RU=n/a (0 gradable)"
	}
	return fmt.Sprintf("%s  · RU=%s (%d/%d)", tally, ratePct(r.RURate), r.Used, r.RUDenom)
}

// ruTag renders a compact RU verdict for a store-reach row (blank for non-store).
func ruTag(o reach.Opportunity) string {
	switch o.RU {
	case reach.RUUsed:
		return " ✓used "
	case reach.RUUnused:
		return " ✗unused"
	case reach.RUInconclusive:
		return " ?inconc"
	default:
		return "       "
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
	fmt.Fprintf(bw, "channels: store=%d beads=%d grep=%d gh=%d none=%d · class: recall=%d reorient=%d\n",
		r.Reached, r.Beads, r.Grep, r.Gh, r.None, r.Recall, r.Reorient)
	fmt.Fprintf(bw, "RU: %s", ruLine(r))
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
		fmt.Fprintf(bw, "- %s %s%s %s/%s [%s→%s] %q\n",
			reachMark(*o), o.Class, mdRUTag(*o), shortProject(o.Project), shortSession(o.Session), o.Cue, firedOrDash(*o), o.Text)
	}
	if n := len(r.Opportunities) - len(shown); n > 0 {
		fmt.Fprintf(bw, "- … +%d more (raise --limit)\n", n)
	}
	return bw.Flush()
}

// mdRUTag renders the store-reach RU verdict inline in the markdown row (blank
// for non-store opportunities).
func mdRUTag(o reach.Opportunity) string {
	if o.RU == reach.RUNone {
		return ""
	}
	return " (RU:" + string(o.RU) + ")"
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
