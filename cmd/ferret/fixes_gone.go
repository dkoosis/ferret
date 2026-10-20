package main

import (
	"cmp"
	"slices"
	"strings"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// goneFix is a ledgered fix whose motif is no longer among the report's mined
// findings — typically because the fix worked and the motif fell below
// min-support. Now is the motif counted straight from the corpus, so the
// outcome the fix aimed for stays visible instead of vanishing with the finding.
type goneFix struct {
	Entry fixes.Entry
	Motif []string
	Now   mine.MotifCount
}

// minedKeys is the set of motif keys among the mined findings, taken before
// the --kind and noise filters so a filtered-out finding is not mistaken for a
// motif that left the findings.
func minedKeys(corpus *mine.Corpus, findings []*mine.Finding) map[string]bool {
	keys := make(map[string]bool, len(findings))
	for _, f := range findings {
		keys[fixes.MotifKey(corpus.Tokens(f.IDs))] = true
	}
	return keys
}

// fixedNotFindings lists the ledger's fix rows whose motif is not among the
// mined findings, each re-counted from the corpus at maxGap. Suppressed
// (wontfix/watch) rows are skipped — they have their own section — and so are
// rows recorded under a different lens, whose tokens mean something else here
// (warnLensDivergence already flags those). A legacy row with no lens is
// skipped when its tokens are absent from this corpus: that may be another lens,
// not a motif that went away. Newest fix first, then by key.
func fixedNotFindings(idx map[string]fixes.Entry, mined map[string]bool, corpus *mine.Corpus, lensName string, maxGap int) []*goneFix {
	var gone []*goneFix
	for key := range idx {
		e := idx[key]
		if mined[key] || e.Suppressed() || (e.Lens != "" && e.Lens != lensName) {
			continue
		}
		toks := fixes.SplitMotifKey(key)
		now := mine.CountMotif(corpus, toks, maxGap)
		if e.Lens == "" && !now.Known {
			continue // legacy row, lens unknown: tokens absent here may just be another lens
		}
		gone = append(gone, &goneFix{Entry: e, Motif: toks, Now: now})
	}
	slices.SortFunc(gone, func(a, b *goneFix) int {
		return cmp.Or(b.Entry.AddedAt.Compare(a.Entry.AddedAt), strings.Compare(a.Entry.Motif, b.Entry.Motif))
	})
	return gone
}

// writeGoneFixes prints the ledgered fixes that left the findings under their
// own heading, each with its baseline→now burn and sessions.
func writeGoneFixes(sink *out.Sink, gone []*goneFix, minSupport int) {
	if len(gone) == 0 {
		return
	}
	sink.Head("✓ fixed, no longer in findings=%d (below min-support=%d or cut by --top; counted from the corpus)",
		len(gone), minSupport)
	for _, g := range gone {
		sink.Row("✓ n=%-5d sess=%-4d %s%s", g.Now.Count, g.Now.Sessions,
			strings.Join(g.Motif, " ⇝ "), fixAnnotation(g.Entry, g.Now.Burn, g.Now.Sessions))
	}
}

// addGoneFixes puts the gone rows into a report's JSON payload under the same
// --limit the findings obey (limit > 0 caps; zero or negative is unlimited, as
// applyDefaultLimit leaves it). When the cap cuts rows it records the full
// count as fixedGoneTotal and marks the payload truncated.
func addGoneFixes(payload map[string]any, gone []*goneFix, limit int) {
	if len(gone) == 0 {
		return
	}
	shown := gone
	if limit > 0 && len(gone) > limit {
		shown = gone[:limit]
		payload[keyTruncated] = true
	}
	payload["fixedGone"] = goneFixRows(shown)
	payload["fixedGoneTotal"] = len(gone)
}

// goneFixRows projects the same rows for --format json.
func goneFixRows(gone []*goneFix) []map[string]any {
	rows := make([]map[string]any, 0, len(gone))
	for _, g := range gone {
		row := map[string]any{
			"motif": g.Motif, "fix": g.Entry.Fix, "fixedAt": g.Entry.AddedAt.Format("2006-01-02"),
			"baselineBurn": g.Entry.BaselineBurn, "burn": g.Now.Burn,
			"sessions": g.Now.Sessions, "count": g.Now.Count,
		}
		if g.Entry.HasSessionBaseline() {
			row["baselineSessions"], row["baselineCount"] = g.Entry.BaselineSessions, g.Entry.BaselineCount
		}
		rows = append(rows, row)
	}
	return rows
}
