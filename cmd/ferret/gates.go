package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/gates"
	"github.com/dkoosis/ferret/internal/out"
)

// cmdGates wires the kong CLI flags to gates extraction over the canonical
// events artifact. It is the deterministic, LLM-free spike: load events, mine
// gate checks, emit per-gate rejection sets + pairwise overlap ratio ω +
// confirmed friction loops. ferret owns only the extraction + arithmetic; the
// LLM fallback for UNKNOWN gate verdicts is analyst-side and not wired here.
func cmdGates() error {
	c, err := fromCommonFlags(CLI.Gates.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return err
	}
	rep := gates.Extract(events)
	if c.format == fmtJSON {
		return writeGatesJSON(os.Stdout, rep)
	}
	return writeGatesText(os.Stdout, rep)
}

// loadEvents streams the events artifact into a slice. The gate miner is a pure
// function over the full event set (it sorts checks by session+seq itself), so
// it needs the events materialized rather than streamed.
func loadEvents(path string) ([]event.Event, error) {
	var events []event.Event
	err := event.Read(path, func(ev *event.Event) error {
		events = append(events, *ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// writeGatesJSON emits the Report (schema is the analyst-ingestable contract,
// kept in the conformance.go writeConformanceJSON shape).
func writeGatesJSON(w io.Writer, rep gates.Report) error {
	return out.JSON(w, map[string]any{
		"gates":    rep.Gates,
		"overlaps": rep.Overlaps,
		"loops":    rep.Loops,
		"checks":   len(rep.Checks),
	})
}

// writeGatesText emits the dense human/analyst rendering: per-gate verdict
// tallies + rejection counts, the pairwise ω table (the redundant-gate signal),
// and the confirmed friction loops.
func writeGatesText(w io.Writer, rep gates.Report) error {
	bw := bufio.NewWriter(w)

	about := func(lines ...string) {
		for _, ln := range lines {
			fmt.Fprintln(bw, ln)
		}
	}
	about(
		"≡ gates: deterministic review-gate mining. a gate (code-review/plan-review/precommit/QA)",
		"≡ check is classified APPROVED/NEEDS_REVISION/ESCALATE by regex over tool_result text; a",
		"≡ rejection is ground-truth friction. ω = Jaccard over rejected sessions — high ω = REDUNDANT",
		"≡ gate (a burn to cut), low ω = complementary. source: claude-code-log-analyzer + overlap-ratio.")

	if len(rep.Gates) == 0 {
		fmt.Fprintln(bw, "gates: no gate checks found in corpus (looked for code-review/plan-review/precommit/QA tool calls)")
		return bw.Flush()
	}

	fmt.Fprintf(bw, "gates (%d checks across %d gate(s)):\n", len(rep.Checks), len(rep.Gates))
	for _, gs := range rep.Gates {
		fmt.Fprintf(bw, "  %-12s checks=%d  approved=%d revisions=%d escalated=%d unknown=%d  rejected-sessions=%d\n",
			gs.Gate, gs.Checks, gs.Approved, gs.Revisions, gs.Escalated, gs.Unknown, len(gs.Rejections))
	}

	fmt.Fprintln(bw, "overlap ratio ω (Jaccard over rejected sessions):")
	if len(rep.Overlaps) == 0 {
		fmt.Fprintln(bw, "  (none — fewer than two gates rejected any session)")
	}
	for _, ov := range rep.Overlaps {
		fmt.Fprintf(bw, "  %-12s × %-12s ω=%.2f  shared=%d/union=%d  %s\n",
			ov.A, ov.B, ov.Omega, ov.Shared, ov.Union, omegaVerdict(ov.Omega))
	}

	fmt.Fprintf(bw, "confirmed friction loops (%d): rejection runs split at APPROVED boundaries\n", len(rep.Loops))
	for _, lp := range rep.Loops {
		status := "resolved"
		if !lp.Resolved {
			status = "UNRESOLVED"
		}
		fmt.Fprintf(bw, "  %-12s %-12s revisions=%d  (%s)\n", lp.Session, lp.Gate, lp.Revisions, status)
	}
	return bw.Flush()
}

// omegaCut is the redundancy threshold for the human verdict: at/above it two
// gates reject mostly the same sessions, so one is a candidate to cut. It is an
// advisory label only — the spike's deliverable is whether ω discriminates on
// the real corpus, not a hard cut rule.
const omegaCut = 0.5

// omegaVerdict labels a pairwise ω as the cost-leak read: redundant (cut one) or
// complementary (both earn their cost).
func omegaVerdict(omega float64) string {
	if omega >= omegaCut {
		return "⚠ redundant (candidate to cut)"
	}
	return "complementary"
}
