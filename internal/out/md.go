package out

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// MDFinding is one finding flattened for the human markdown report: the motif
// tokens and exemplar are already resolved to strings, so the renderer needs no
// Corpus. main.go projects mine.Findings into this shape — the same
// mine-once-render-twice split the JSON path uses, just a second view.
type MDFinding struct {
	Motif    []string // resolved motif tokens
	Kind     string   // routine | friction | loop | noise
	Action   string   // script | hook | tool-fix | trim
	Count    int      // total occurrences across the corpus
	Sessions int      // distinct streams containing the motif
	FailRate float64  // share of fail-marked member tokens (0..1)
	Burn     int      // measured tokens of context the motif's occurrences cost
	Evidence string   // exemplar location, "sess@pos"
}

// usdPerToken prices burn in dollars for the human report. burn counts the
// re-read CONTEXT (input) tokens a motif drags back, so this is Claude Sonnet
// 4.6 input pricing — $3.00 / 1M tokens (cached 2026-06). It is an
// order-of-magnitude figure on purpose: the report says "~$X", not an invoice.
const usdPerToken = 3.0 / 1_000_000

// mdSection is one report section: the Finding kind it collects, its header,
// the one-line framing under it, and whether to omit the section when empty.
// The three core sections always render (stable, diffable shape); noise is
// optional so it only appears when explicitly requested via --kind noise.
type mdSection struct {
	kind     string
	title    string
	intro    string
	optional bool
}

// mdSections fixes the report order: cost leaks first (the point of the
// report), then churn, then routines as calibration proof, then optional noise.
var mdSections = []mdSection{
	{kind: "friction", title: "💸 Cost leaks", intro: "Friction that drags context back into the model — sorted by measured burn."},
	{kind: "loop", title: "🔁 Context churn", intro: "Loops that revisit a step — trim the redundant context."},
	{kind: "routine", title: "✅ Routines already automated", intro: "Low-entropy chains ferret classifies as routine — calibration proof it reads your real patterns, not noise."},
	{kind: "noise", title: "🔇 Noise", intro: "Frequent but not yet actionable.", optional: true},
}

// Markdown writes the human cost report. Sections render in a fixed order so the
// output diffs cleanly when committed to a repo; within each section findings
// are sorted by burn (heaviest first). Each finding's evidence is tucked into a
// collapsible <details> so the scan line stays terse. total is the pre-cap
// finding count, so a capped findings slice surfaces what it dropped rather than
// truncating silently.
func Markdown(w io.Writer, lens string, total int, findings []MDFinding) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintln(bw, "# ferret cost report")
	fmt.Fprintln(bw)
	fmt.Fprintf(bw, "_lens %s · %d findings · cost ≈ burn × Sonnet input price ($3/1M tok)_\n", lens, total)

	byKind := map[string][]MDFinding{}
	for _, f := range findings {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}

	for _, sec := range mdSections {
		group := byKind[sec.kind]
		if len(group) == 0 && sec.optional {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].Burn > group[j].Burn })

		fmt.Fprintln(bw)
		fmt.Fprintf(bw, "## %s\n\n", sec.title)
		fmt.Fprintf(bw, "%s\n", sec.intro)
		if len(group) == 0 {
			fmt.Fprintln(bw)
			fmt.Fprintln(bw, "_none in this corpus._")
			continue
		}
		fmt.Fprintln(bw)
		for _, f := range group {
			writeMDFinding(bw, f)
		}
	}

	if total > len(findings) {
		fmt.Fprintln(bw)
		fmt.Fprintf(bw, "_… +%d more findings (raise --limit)._\n", total-len(findings))
	}
	return bw.Flush()
}

// writeMDFinding renders one finding as a scannable bullet plus its collapsible
// evidence block.
func writeMDFinding(bw *bufio.Writer, f MDFinding) {
	motif := strings.Join(f.Motif, " ⇝ ")
	fmt.Fprintf(bw, "- **%s** — %s; fix: %s\n", motif, mdCostPhrase(f.Burn, f.Sessions), f.Action)
	fmt.Fprintln(bw, "  <details><summary>evidence</summary>")
	fmt.Fprintln(bw)
	fmt.Fprintf(bw, "  %d occurrences · %.0f%% fail · ex: %s\n", f.Count, f.FailRate*100, f.Evidence)
	fmt.Fprintln(bw)
	fmt.Fprintln(bw, "  </details>")
}

// mdCostPhrase renders "~Nk tokens / ~$X across M sessions" — the
// glance-readable magnitude of one finding's burn.
func mdCostPhrase(burn, sessions int) string {
	return fmt.Sprintf("~%s tokens / ~%s across %d sessions", mdCompactTokens(burn), mdUSD(burn), sessions)
}

// mdCompactTokens renders a token count compactly: 253000 → "253k", 990 → "990".
// Lossy by design — a glance wants magnitude, not the exact figure.
func mdCompactTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n/1000) + "k"
}

// mdUSD renders burn as dollars, flooring at "<$0.01" so a cheap-but-nonzero
// motif never reads as free.
func mdUSD(burn int) string {
	d := float64(burn) * usdPerToken
	if d > 0 && d < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", d)
}
