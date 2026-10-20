package main

import (
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- seqs (PrefixSpan) ----

func cmdSeqs() error {
	cmd := &CLI.Seqs
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 30)
	lo := fromLensFlags(cmd.LensFlags).window(cmd.WindowFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if err := validateSeqParams(cmd.MinSupport, cmd.MaxGap, cmd.MaxLen); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	opts := mine.SeqOpts{
		MinSupport: cmd.MinSupport, MaxGap: cmd.MaxGap, MaxLen: cmd.MaxLen, MaxPatterns: 10000,
	}
	// --contains filters inside the miner, so the pattern cap counts only matching rows.
	if m := newTokenMatch(corpus, cmd.Contains); m != nil {
		opts.Keep = func(ids []uint32) bool { return m.any(ids...) }
	}
	pats, capped := mine.MineSeqs(corpus, opts)

	if c.format == fmtJSON {
		type jp struct {
			Tokens   []string `json:"tokens"`
			Support  int      `json:"support"`
			Exemplar string   `json:"exemplar"`
		}
		rows := make([]jp, 0, len(pats))
		for i, p := range pats {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, jp{corpus.Tokens(p.IDs), p.Support, exemplar(corpus, p.ExStream, p.ExSeq)})
		}
		return out.JSON(os.Stdout, withWindow(map[string]any{
			keyLens: l.Name(), "maxGap": cmd.MaxGap, "patterns": rows,
			keyTotal: len(pats), keyTruncated: len(rows) < len(pats) || capped,
		}, corpus))
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ seqs: ordered subsequences that recur with up to max-gap other actions between steps",
		"≡ (PrefixSpan) — habits that survive interruptions. Ns = pattern appears in N sessions. ⇝ = gap allowed.",
		legendMarks)
	sink.Head("seqs lens=%s%s streams=%d patterns=%d (min-support=%d max-gap=%d max-len=%d)",
		l.Name(), containsTag(cmd.Contains), len(corpus.Streams), len(pats), cmd.MinSupport, cmd.MaxGap, cmd.MaxLen)
	windowHead(sink, corpus)
	if capped {
		sink.Head("‡ search hit the 10000-pattern cap — raise --min-support")
	}
	for _, p := range pats {
		sink.Row("%5ds %s  ex: %s",
			p.Support, strings.Join(corpus.Tokens(p.IDs), " ⇝ "), exemplar(corpus, p.ExStream, p.ExSeq))
	}
	return nil
}
