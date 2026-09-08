package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// HomeCmd backs bare `ferret` (ferret-4vx, rescoped from the c91-era compact
// status view — `ferret status` still carries that view, unchanged).
//
// Bare `ferret` is the measurement scoreboard: the top routines (`report
// --lens cmd`, by burn), priced single-call waste (`friction`, misfire ∪
// poll), and a burn-delta per recorded fix. No new detector or scorer — this
// composes and caps the same two pipelines `ferret report` and `ferret
// friction` already compute (internal/mine/scoreboard.go). [local]: no model
// call, so `--offline` changes nothing about this command's output.
type HomeCmd struct {
	CommonFlags
}

// scoreboardCap is the row cap per category (routines, waste) — three, per
// the bead: dk lands one or two fixes a session, and a longer list gets
// re-read at full cost every run (SKILL.md rationale, cited in the bead's
// prior decisions).
const scoreboardCap = 3

// homeLens is the lens the scoreboard's routine ranking always uses — the
// same "cmd" lens the bead names, not the corpus-command default("tool").
// Bare `ferret` has no --lens flag: the whole point is one fixed, comparable
// view, not a knob.
const homeLens = "cmd"

// homeMinSupport is the recurrence floor for the routines ranking, and it is
// deliberately looser than `report`'s reportMinSupport (20).
//
// Measured on the live corpus 2026-09-07 (22,328 events / 118 sessions), at
// order=3: min-support 20 yields 0 findings, 10 yields 4, 5 yields 20. So the
// report default renders bare `ferret`'s headline section EMPTY on a real
// corpus of this size — the front page would show only its waste half.
//
// The two commands want different floors because they answer different
// questions. `report` is the exhaustive audit: a high floor keeps it honest,
// and a reader who wants more passes --min-support. The scoreboard shows the
// top 3 and nothing else, so its floor only decides whether those 3 rows
// exist; the cap, not the floor, is what protects the reader here.
//
// This does not change `report`'s output — reportMinSupport is untouched and
// `ferret report` is byte-identical (golden diff). It changes which findings
// the scoreboard asks for.
const homeMinSupport = 10

// cmdHome renders the scoreboard, or falls back to the existing status block
// when the corpus is missing/stale/era-drifted. The freshness gate runs
// FIRST and reuses status.go's Ready/Stale/EraDrift verbatim — a stale corpus
// must never render numbers next to `Δ since fixes`, since a fix's "after"
// figure would be measuring against transcripts the corpus hasn't ingested
// yet.
func cmdHome(cmd *HomeCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}

	st := readStatus(c)
	if !st.Ready || st.Stale || st.EraDrift {
		if c.format == fmtJSON {
			return writeStatusJSON(os.Stdout, st)
		}
		return writeStatusText(os.Stdout, st, c.maxBytes)
	}

	sb, corpus, err := buildHomeScoreboard(c)
	if err != nil {
		return err
	}
	if c.format == fmtJSON {
		return writeHomeJSON(os.Stdout, st, sb)
	}
	return writeHomeText(os.Stdout, corpus, st, sb, c.maxBytes)
}

// buildHomeScoreboard runs the report pipeline (lens forced to cmd) and the
// friction merge (motif leg off — mergeFriction then emits exactly poll ∪
// misfire, the union the bead asks for; the motif leg's territory is already
// covered by the routines ranking above) and folds the fix ledger over both
// through mine.BuildScoreboard. The corpus is returned alongside so the text
// renderer can resolve exemplar session ids without re-building it.
func buildHomeScoreboard(c *common) (mine.Scoreboard, *mine.Corpus, error) {
	lo := &lensOpts{lens: homeLens}
	corpus, _, err := lo.corpus(c.eventsPath())
	if err != nil {
		return mine.Scoreboard{}, nil, err
	}

	sscores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: reportOrder, MinToks: reportSurpriseMinToks})
	findings, _ := mineFindings(corpus, homeMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop,
		mine.SurpriseIndex(sscores), mine.FrictionCut(sscores))
	// report's default view (no --kind) drops noise; the scoreboard mirrors it
	// so routines[].key matches `report --lens cmd --limit 3` exactly.
	kept := findings[:0:0]
	for _, f := range findings {
		if f.Kind != mine.KindNoise {
			kept = append(kept, f)
		}
	}
	findings = kept

	// noMotifs=true: skip the (expensive) motif leg entirely. Its territory is
	// already the routines ranking above; the waste side only needs poll ∪
	// misfire, which is exactly what MergeWaste emits with a nil corpus/motifs.
	waste, err := mergeFriction(c, lo, true, "")
	if err != nil {
		return mine.Scoreboard{}, nil, err
	}

	entries, err := fixes.Load(fixes.Path(c.data))
	if err != nil {
		return mine.Scoreboard{}, nil, err
	}

	return mine.BuildScoreboard(corpus, findings, waste, entries, scoreboardCap), corpus, nil
}

// writeHomeJSON emits {routines, waste, delta, belowCut, status} — the
// scoreboard plus the freshness view it passed, so a JSON consumer never has
// to make a second call to confirm the numbers are fresh.
func writeHomeJSON(w io.Writer, st status, sb mine.Scoreboard) error {
	return out.JSON(w, map[string]any{
		"routines": sb.Routines,
		"waste":    sb.Waste,
		"delta":    sb.Delta,
		"belowCut": sb.BelowCut,
		"status":   st,
	})
}

// writeHomeText renders the ≤40-line scoreboard. Data rows go through
// sink.Row, so --max-bytes truncates them and Close prints the `… +K more`
// notice — the header and the section labels stay Head, since a budget that
// eats the label before the rows would hand the reader unlabelled numbers.
// Every row carries its own `next:` command — a legal move, not a plan (DK-AXI rule 11) — because this
// is the terse, no-routing-prose view: the built-in usage report is where
// recommendations live now (decision feb7162e18b9).
func writeHomeText(w io.Writer, corpus *mine.Corpus, st status, sb mine.Scoreboard, maxBytes int) error {
	sink := out.NewSink(w, 0, maxBytes)
	defer sink.Close()

	sink.Head("ferret %s · %d events · %d sessions · fresh", st.Data, st.Events, st.Sessions)

	if len(sb.Routines) == 0 && len(sb.Waste) == 0 && len(sb.Delta) == 0 {
		sink.Head("0 routines, 0 waste rows — nothing ranked yet")
	}

	if len(sb.Routines) > 0 {
		sink.Head("routines (cmd lens, by burn — one helper each):")
		for i, r := range sb.Routines {
			sink.Row("%d  %10s  n=%-5d sess=%-4d side=%3.0f%%  %-40s next: ferret tokens --session %s",
				i+1, humanBytes(r.BurnBytes), r.Count, r.Sessions, sideSharePct(r), r.Key,
				exemplarSession(corpus, r.ExStream, r.ExSeq))
		}
	}

	if len(sb.Waste) > 0 {
		sink.Head("waste (priced, by wastedBytes):")
		for i, r := range sb.Waste {
			sink.Row("%d  %10s  %-8s %5dx/%d sess  %-24s next: %s",
				i+1, humanBytes(r.WastedBytes), r.Source, r.Occurrences, r.Sessions, r.Key, wasteNextCmd(r))
		}
	}

	if len(sb.Delta) > 0 {
		sink.Head("Δ since fixes:")
		for _, d := range sb.Delta {
			sink.Row("  %s  fix %s  %s → %s (%s)",
				d.Key, d.FixedAt, humanBytes(d.BeforeBytes), humanBytes(d.AfterBytes), deltaPct(d))
		}
	}

	if sb.BelowCut > 0 {
		sink.Head("… %d more below the cut: ferret report --lens cmd · ferret friction", sb.BelowCut)
	}
	sink.NextHead("ferret status", "ferret fixes add --motif <tokens> --fix <action>")
	return nil
}

// sideSharePct is side burn's share of total burn as a percentage — the same
// "subagent sidechain slice" figure report.go's sideShare renders, recomputed
// here because mine.ScoreboardRoutine carries the two totals, not the ratio.
// Both are bytes, so the ratio is unit-independent.
func sideSharePct(r mine.ScoreboardRoutine) float64 {
	if r.BurnBytes == 0 {
		return 0
	}
	return 100 * float64(r.SideBurnBytes) / float64(r.BurnBytes)
}

// exemplarSession resolves a routine's exemplar occurrence to just the
// session id `ferret tokens --session` takes — exemplar() (main.go) renders
// "session@seq"; the seq half is for a human reading `ferret tokens`' own
// output, not for the flag.
func exemplarSession(c *mine.Corpus, stream, seq int) string {
	ex := exemplar(c, stream, seq)
	if before, _, ok := strings.Cut(ex, "@"); ok {
		return before
	}
	return ex
}

// wasteNextCmd names the single legal drill-down for a waste row's source —
// the detector command that owns the detail (ferret misfires / ferret
// polling). Neither command takes a --key flag today, so the hint names the
// command, not a pre-filtered call to a flag that doesn't exist.
func wasteNextCmd(r mine.WasteRow) string {
	switch r.Source {
	case mine.WasteFail:
		return "ferret misfires"
	case mine.WasteRepeat:
		return "ferret polling"
	default:
		return "ferret friction"
	}
}

// deltaPct renders a fix's burn change as a signed percentage — the same
// direction fixes.Annotation.Arrow reports, spelled as a number instead of a
// glyph so the text and JSON views agree on magnitude, not just direction.
func deltaPct(d mine.ScoreboardDelta) string {
	if d.BeforeBytes == 0 {
		return "n/a"
	}
	pct := 100 * float64(d.AfterBytes-d.BeforeBytes) / float64(d.BeforeBytes)
	return fmt.Sprintf("%+.0f%%", pct)
}
