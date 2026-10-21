package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
)

var errFixMotifRequired = usage("fixes add: --motif must not be empty")

// cmdFixesAdd records motif→fix in the ledger, capturing the motif's CURRENT
// burn, session count and occurrence count as the baseline. They are measured
// with the same gapped match the report prices a finding with (reportMaxGap), so
// the later 'report --since-fixes' delta is a true before→after rather than an
// eyeballed guess. A motif that is not a finding (below min-support) still gets
// its counted baseline; only a motif the corpus never shows records 0 (with a
// stderr note).
func cmdFixesAdd() error {
	cmd := &CLI.Fixes.Add
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cmd.Motif) == "" {
		return errFixMotifRequired
	}
	// Canonicalize the user's --motif through the SAME key path the report uses
	// (split on comma, trim each token, escape, rejoin) so a spaced or
	// comma-bearing motif stores the exact key the report later computes from the
	// corpus tokens — the join cannot drift between write and read.
	motif := fixes.MotifKey(fixes.ParseMotif(cmd.Motif))

	disp := cmd.Disposition
	e := fixes.Entry{Motif: motif, Fix: cmd.Fix, Note: cmd.Note, AddedAt: time.Now(), Disposition: disp, Lens: cmd.Lens}

	// Only a fix captures a baseline: a wontfix/watch verdict suppresses the
	// motif from the report rather than measuring a delta, so mining its current
	// burn would be wasted work (and a misleading non-zero baseline on a row that
	// never computes a delta).
	if e.Suppressed() {
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			return err
		}
		fmt.Printf("recorded %s: %s — %s (suppressed from report, no baseline)\n", disp, motif, cmd.Fix)
		return nil
	}

	c := &common{data: data, format: "text"}
	if err := c.ensureData(); err != nil {
		return err
	}
	lo := &lensOpts{lens: cmd.Lens}
	corpus, _, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	// Count the motif straight from the corpus (the same gapped match the report
	// measures a finding with) instead of looking it up among the mined findings:
	// a motif below the report's min-support is not a finding, yet it still has a
	// true burn, session count and occurrence count to record.
	n := mine.CountMotif(corpus, fixes.ParseMotif(cmd.Motif), reportMaxGap)
	e.BaselineBurn, e.BaselineSessions, e.BaselineCount = n.Burn, n.Sessions, n.Count

	if err := fixes.Append(fixes.Path(data), e); err != nil {
		return err
	}
	if n.Count == 0 {
		fmt.Fprintf(os.Stderr,
			"ferret: motif %q does not occur in the corpus (lens=%s) — baseline recorded as 0\n", motif, cmd.Lens)
	}
	fmt.Printf("recorded fix: %s → %s (baseline burn %d toks, %d sessions, %d occurrences)\n",
		motif, cmd.Fix, e.BaselineBurn, e.BaselineSessions, e.BaselineCount)
	return nil
}
