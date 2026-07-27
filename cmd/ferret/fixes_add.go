package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
)

var errFixMotifRequired = errors.New("fixes add: --motif must not be empty")

// cmdFixesAdd records motif→fix in the ledger, capturing the motif's CURRENT
// burn as the baseline. The baseline is measured through the same findings
// pipeline the report uses, so the later 'report --since-fixes' delta is a true
// before→after rather than an eyeballed guess. A motif that isn't currently a
// finding records a 0 baseline (with a stderr note) — the user is recording a
// fix for friction the corpus no longer shows.
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
	// nil surprise index: the baseline only needs each motif's burn (surprise-
	// independent), so the routine/friction split is irrelevant here.
	findings, _ := mineFindings(corpus, reportMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop, nil, 0)
	for _, f := range findings {
		if fixes.MotifKey(corpus.Tokens(f.IDs)) == motif {
			e.BaselineBurn = f.Burn
			break
		}
	}

	if err := fixes.Append(fixes.Path(data), e); err != nil {
		return err
	}
	if e.BaselineBurn == 0 {
		fmt.Fprintf(os.Stderr,
			"ferret: motif %q is not a current finding (lens=%s) — baseline burn recorded as 0\n", motif, cmd.Lens)
	}
	fmt.Printf("recorded fix: %s → %s (baseline burn %d toks)\n", motif, cmd.Fix, e.BaselineBurn)
	return nil
}
