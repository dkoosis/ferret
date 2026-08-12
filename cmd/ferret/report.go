package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- report (Finding projection) ----

var (
	errBadKind    = errors.New("bad --kind (want routine|friction|loop|noise)")
	errMaxBytesMD = errors.New("--max-bytes is not supported with --format md (whole-document output; use --limit)")
)

func cmdReport() error {
	cmd := &CLI.Report
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 30
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON, fmtMD); err != nil {
		return err
	}
	if c.format == fmtMD && c.maxBytes > 0 {
		return errMaxBytesMD
	}
	switch cmd.Kind {
	case "", string(mine.KindRoutine), string(mine.KindFriction), string(mine.KindLoop), string(mine.KindNoise):
	default:
		return fmt.Errorf("%w: %q", errBadKind, cmd.Kind)
	}
	if err := validateSeqParams(cmd.MinSupport, cmd.MaxGap, cmd.MaxLen); err != nil {
		return err
	}
	if cmd.Order < 1 {
		return errOrder
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	// Surprise (per-session predictability) splits the routine bucket: a recurring
	// motif whose host sessions are outlying-surprising (≥ 1σ above the corpus mean)
	// is friction to fix, not a routine to script. Computed once over the same corpus
	// and routed into Findings' kind assignment — same backoff model the cohesion
	// scorer trains. FrictionCut's σ margin keeps average-surprise routines (which a
	// bare mean would mislabel) on the routine side.
	sscores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: cmd.Order, MinToks: reportSurpriseMinToks})
	surpriseIdx := mine.SurpriseIndex(sscores)
	surpriseCut := mine.FrictionCut(sscores)
	findings, capped := mineFindings(corpus, cmd.MinSupport, cmd.MaxGap, cmd.MaxLen, cmd.Order, cmd.Top, surpriseIdx, surpriseCut)
	if cmd.Kind != "" {
		filtered := findings[:0:0]
		for _, f := range findings {
			if string(f.Kind) == cmd.Kind {
				filtered = append(filtered, f)
			}
		}
		findings = filtered
	} else {
		// Default view drops noise — it's frequent but not actionable.
		drop := findings[:0:0]
		for _, f := range findings {
			if f.Kind != mine.KindNoise {
				drop = append(drop, f)
			}
		}
		findings = drop
	}

	// --since-fixes joins each finding to the fix ledger by motif key, turning
	// the report from a fresh snapshot into a before→after on recorded fixes.
	// A nil index means the flag is off; an empty (non-nil) index means it is on
	// but no fix has been recorded yet.
	var fixIdx map[string]fixes.Entry
	if cmd.SinceFixes {
		entries, err := fixes.Load(fixes.Path(c.data))
		if err != nil {
			return err
		}
		fixIdx = fixes.Index(entries)
	}

	// A wontfix/watch verdict suppresses its motif from the actionable list: the
	// motif was adjudicated and deliberately not fixed, so re-surfacing it as a
	// fresh candidate every scan would re-litigate a closed call. They are pulled
	// out here (and shown separately with their reason) so the loop's memory
	// holds across scans. Only meaningful under --since-fixes (nil index = off).
	var suppressed []*mine.Finding
	if fixIdx != nil {
		keep := findings[:0:0]
		for _, f := range findings {
			if e, ok := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]; ok && e.Suppressed() {
				suppressed = append(suppressed, f)
				continue
			}
			keep = append(keep, f)
		}
		findings = keep
		warnLensDivergence(fixIdx, corpus, findings, suppressed, l.Name())
	}

	// De-island the dialogue + Hop2 retrieval scorers (ferret-bbp.6): roll each
	// session's human-side Outcome + worst Hop2 grade onto the findings that recur
	// in it, so report/out surface them beside burn. Pure read of the same events
	// the corpus was built from; keyed identically to the corpus streams.
	dlgIdx, err := streamDialogueIndex(c.eventsPath())
	if err != nil {
		return err
	}
	mine.AttachDialogue(findings, corpus, dlgIdx, cmd.MaxGap)

	// AttachOddsRatio (ferret-qus) folds the gated odds-ratio-vs-outcome signal
	// onto each finding — a SECOND signal beside burn, not a replacement: burn
	// stays the sort key above and throughout every render path below. Reuses
	// the same dlgIdx dialogue index AttachDialogue just consumed.
	mine.AttachOddsRatio(findings, corpus, dlgIdx, cmd.MaxGap, mine.MinOddsRatioSupport)

	if c.format == fmtJSON {
		type jf struct {
			Motif    []string `json:"motif"`
			Kind     string   `json:"kind"`
			Action   string   `json:"action"`
			Count    int      `json:"count"`
			Sessions int      `json:"sessions"`
			FailRate float64  `json:"failRate"`
			Burn     int      `json:"burn"`
			SideBurn int      `json:"sideBurn"` // slice of burn from subagent sidechain streams
			Surprise float64  `json:"surprise"`
			Outcome  string   `json:"outcome,omitempty"`
			Hop2     string   `json:"hop2,omitempty"`
			Hop1     string   `json:"hop1,omitempty"`
			Repairs  int      `json:"repairs,omitempty"`
			Accepts  int      `json:"accepts,omitempty"`
			// OddsRatio/OddsRatioN (ferret-qus): the odds-ratio-vs-outcome second
			// signal alongside burn. OddsRatio omits when mine.AttachOddsRatio
			// suppressed it (support below mine.MinOddsRatioSupport) — nil pointer,
			// not a spurious number. OddsRatioN is the support count (a+b), emitted
			// whenever it's nonzero even if the ratio itself was suppressed, so a
			// thin-sample motif still reads "n=3" instead of vanishing.
			OddsRatio    *float64 `json:"oddsRatio,omitempty"`
			OddsRatioN   int      `json:"oddsRatioN,omitempty"`
			Evidence     string   `json:"evidence"`
			Fixed        bool     `json:"fixed,omitempty"`
			Fix          string   `json:"fix,omitempty"`
			FixedAt      string   `json:"fixedAt,omitempty"`
			BaselineBurn int      `json:"baselineBurn,omitempty"`
		}
		rows := make([]jf, 0, len(findings))
		for i, f := range findings {
			if c.limit > 0 && i >= c.limit {
				break
			}
			row := jf{
				Motif: corpus.Tokens(f.IDs), Kind: string(f.Kind), Action: string(f.Action),
				Count: f.Count, Sessions: f.Sessions, FailRate: f.FailRate,
				Burn: f.Burn, SideBurn: f.SideBurn, Surprise: f.Surprise,
				Outcome: f.Outcome, Hop2: f.Hop2, Hop1: f.Hop1, Repairs: f.Repairs, Accepts: f.Accepts,
				OddsRatio: f.OutcomeOddsRatio, OddsRatioN: f.OddsRatioSupport,
				Evidence: exemplar(corpus, f.ExStream, f.ExSeq),
			}
			if e, ok := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]; ok {
				row.Fixed, row.Fix = true, e.Fix
				row.FixedAt, row.BaselineBurn = e.AddedAt.Format("2006-01-02"), e.BaselineBurn
			}
			rows = append(rows, row)
		}
		payload := map[string]any{
			keyLens: l.Name(), "findings": rows,
			keyTotal: len(findings), keyTruncated: len(rows) < len(findings) || capped,
		}
		if sup := suppressedRows(fixIdx, corpus, suppressed); len(sup) > 0 {
			payload["suppressed"] = sup
		}
		return out.JSON(os.Stdout, payload)
	}

	// --format md renders the human cost report: the same findings projected into
	// out.MDFinding (motif + exemplar resolved to strings) and grouped into the
	// 💸/🔁/✅ sections. The renderer owns layout; main only flattens + caps.
	if c.format == fmtMD {
		rows := make([]out.MDFinding, 0, len(findings))
		for i, f := range findings {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, out.MDFinding{
				Motif: corpus.Tokens(f.IDs), Kind: string(f.Kind), Action: string(f.Action),
				Count: f.Count, Sessions: f.Sessions, FailRate: f.FailRate,
				Burn: f.Burn, SideBurn: f.SideBurn,
				Outcome: f.Outcome, Hop2: f.Hop2, Repairs: f.Repairs, Accepts: f.Accepts,
				OddsRatio: f.OutcomeOddsRatio, OddsRatioN: f.OddsRatioSupport,
				Evidence: exemplar(corpus, f.ExStream, f.ExSeq),
			})
		}
		return out.Markdown(os.Stdout, l.Name(), len(findings), rows)
	}

	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ report: motifs classified into an action verb, ranked by burn — measured tokens the",
		"≡ motif's member calls cost: each call's tool input + its own tool_result, never the whole turn.",
		"≡ burn spans ALL streams incl. subagent sidechains; side = share of burn from subagent streams (--no-sidechain excludes them).",
		"≡ surp = mean bits/tok of the sessions a motif recurs in: a high-surp routine is friction (fix it), low-surp is routine (script it).",
		legendMarks)
	if fixIdx != nil {
		sink.Head("≡ since-fixes: [fixed DATE burn BASE→NOW ↓/↑/=] annotates motifs in the ledger (↓ = fix landed).")
	}
	sink.Head("report lens=%s findings=%d (min-support=%d order=%d)",
		l.Name(), len(findings), cmd.MinSupport, cmd.Order)
	emptyNote(sink, len(findings), "findings")
	if capped {
		sink.Head("‡ seqs hit the 10000-pattern cap — raise --min-support")
	}
	for _, f := range findings {
		row := fmt.Sprintf("%-8s %-8s burn=%-8d side=%3.0f%% n=%-5d sess=%-4d fail=%2.0f%% surp=%4.1f  %s  ex: %s",
			f.Kind, f.Action, f.Burn, sideShare(f)*100, f.Count, f.Sessions, f.FailRate*100, f.Surprise,
			strings.Join(corpus.Tokens(f.IDs), " ⇝ "), exemplar(corpus, f.ExStream, f.ExSeq))
		if ann, ok := sinceFixAnnotation(fixIdx, corpus.Tokens(f.IDs), f.Burn); ok {
			row += ann
		}
		row += reportDialogueNote(f)
		sink.Row("%s", row)
	}
	if len(suppressed) > 0 {
		sink.Head("⊘ suppressed=%d (adjudicated wontfix/watch — not actionable, kept for memory)", len(suppressed))
		for _, f := range suppressed {
			e := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]
			sink.Row("⊘ %-8s %s  [%s]", e.Disp(),
				strings.Join(corpus.Tokens(f.IDs), " ⇝ "), suppressReason(e))
		}
	}
	// Legal moves, not a plan (DK-AXI rule 11): a motif's burn is gross cost —
	// the merged view prices how much of it bought nothing, and the ledger is
	// where a decided fix goes.
	if len(findings) > 0 {
		sink.NextHead("ferret friction", "ferret fixes add --motif <tokens> --fix <action>")
	}
	return nil
}

// suppressReason renders a wontfix/watch entry's recorded justification: the Fix
// field (which holds the reason for a non-fix verdict) plus any Note.
func suppressReason(e fixes.Entry) string {
	if e.Note == "" {
		return e.Fix
	}
	return e.Fix + " — " + e.Note
}

// suppressedRows projects the suppressed findings into JSON rows carrying the
// motif, its verdict, and the recorded reason — the actionable list omits them,
// but the report still reports what was adjudicated and why.
func suppressedRows(idx map[string]fixes.Entry, corpus *mine.Corpus, suppressed []*mine.Finding) []map[string]any {
	if len(suppressed) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(suppressed))
	for _, f := range suppressed {
		toks := corpus.Tokens(f.IDs)
		e := idx[fixes.MotifKey(toks)]
		rows = append(rows, map[string]any{
			"motif":       toks,
			"disposition": e.Disp(),
			"reason":      suppressReason(e),
			"fixedAt":     e.AddedAt.Format("2006-01-02"),
		})
	}
	return rows
}

// warnLensDivergence flags fix-ledger entries that matched no finding this run
// AND were recorded under a different lens than the report is using. Lens
// transforms change the token strings (mark-fail appends '!', collapse merges
// runs), so the join key shifts and the fix silently fails to annotate — the
// exact re-litigation the ledger exists to prevent. A single stderr line points
// the operator at the lens mismatch rather than leaving the miss invisible.
func warnLensDivergence(idx map[string]fixes.Entry, corpus *mine.Corpus, kept, suppressed []*mine.Finding, lensName string) {
	matched := make(map[string]bool, len(kept)+len(suppressed))
	for _, f := range kept {
		matched[fixes.MotifKey(corpus.Tokens(f.IDs))] = true
	}
	for _, f := range suppressed {
		matched[fixes.MotifKey(corpus.Tokens(f.IDs))] = true
	}
	n := 0
	for key, e := range idx {
		if !matched[key] && e.Lens != "" && e.Lens != lensName {
			n++
		}
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr,
			"ferret: %d fix-ledger entr%s recorded under a different lens matched no finding under lens=%s — "+
				"re-run report with the lens you recorded the fix under, or re-record the fix\n",
			n, plural(n, "y", "ies"), lensName)
	}
}

// plural picks the singular or plural suffix for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sinceFixAnnotation joins one finding's motif to the fix ledger index,
// returning the human-readable annotation suffix and whether a fix matched.
// Pure (no corpus/disk) so the motif-keyed join + burn-delta formatting is
// unit-testable. A nil index (flag off) yields no match.
func sinceFixAnnotation(idx map[string]fixes.Entry, motif []string, currentBurn int) (string, bool) {
	e, ok := idx[fixes.MotifKey(motif)]
	if !ok {
		return "", false
	}
	a := fixes.Annotation{Entry: e, Current: currentBurn}
	return fmt.Sprintf("  [fixed %s burn %s→%s %s]",
		e.AddedAt.Format("2006-01-02"), compactBurn(e.BaselineBurn), compactBurn(currentBurn), a.Arrow()), true
}

// sideShare is the fraction of a finding's burn drawn from subagent sidechain
// streams — the pool label that keeps an all-streams burn from being read as
// main-session cost (ferret-9j3). Zero-burn findings read as 0.
func sideShare(f *mine.Finding) float64 {
	if f.Burn == 0 {
		return 0
	}
	return float64(f.SideBurn) / float64(f.Burn)
}

// compactBurn renders a token count compactly for inline annotations: 253000 →
// "253k", 990 → "990". Lossy by design — an annotation wants a glance-readable
// magnitude, not an exact figure (the JSON output carries the precise numbers).
func compactBurn(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n/1000) + "k"
}
