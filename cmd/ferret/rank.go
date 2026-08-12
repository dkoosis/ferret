package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- rank (cohesion-scored review queue) ----

func cmdRank() error {
	cmd := &CLI.Rank
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
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
	pats, capped := mine.MineSeqs(corpus, mine.SeqOpts{
		MinSupport: cmd.MinSupport, MaxGap: cmd.MaxGap, MaxLen: cmd.MaxLen, MaxPatterns: 10000,
	})
	opts := mine.DefaultRankOpts()
	opts.Order = cmd.Order
	cards, noise := mine.RankPatterns(corpus, pats, opts)

	byBucket := map[string][]*mine.Card{}
	overflow := 0
	for _, card := range cards {
		if cmd.Top > 0 && len(byBucket[card.Bucket]) >= cmd.Top {
			overflow++
			continue
		}
		byBucket[card.Bucket] = append(byBucket[card.Bucket], card)
	}

	if c.format == fmtJSON {
		type jc struct {
			Tokens   []string `json:"tokens"`
			Support  int      `json:"support"`
			Bits     float64  `json:"bits"`
			Score    float64  `json:"score"`
			Folded   int      `json:"folded"`
			Variants int      `json:"variants"`
			Exemplar string   `json:"exemplar"`
		}
		buckets := map[string][]jc{}
		for _, b := range mine.Buckets {
			rows := make([]jc, 0, len(byBucket[b]))
			for _, card := range byBucket[b] {
				rows = append(rows, jc{
					corpus.Tokens(card.IDs), card.Support, card.Bits,
					card.Score, card.Folded, card.Variants, exemplar(corpus, card.ExStream, card.ExSeq),
				})
			}
			buckets[b] = rows
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "order": cmd.Order, "buckets": buckets,
			"noise": noise, keyTotal: len(cards),
			keyTruncated: overflow > 0 || capped,
		})
	}
	sink := out.NewSink(os.Stdout, 0, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ rank: mined seqs deduped + scored into review buckets. Columns: sessions · bits",
		"≡ (predictability of the chain — lower = tighter habit) · score (review priority).",
		legendMarks)
	sink.Head("rank lens=%s patterns=%d → cards=%d noise=%d (min-support=%d order=%d top=%d)",
		l.Name(), len(pats), len(cards), noise, cmd.MinSupport, cmd.Order, cmd.Top)
	if capped {
		sink.Head("‡ seqs hit the 10000-pattern cap — raise --min-support")
	}
	desc := map[string]string{
		mine.BucketFriction: "fail-marked",
		mine.BucketLoop:     "revisits a step",
		mine.BucketScript:   "low-entropy chains — automation candidates",
		mine.BucketWatch:    "frequent, not yet classifiable",
	}
	for _, b := range mine.Buckets {
		if len(byBucket[b]) == 0 {
			continue
		}
		sink.Head("%s (%s):", strings.ToUpper(b), desc[b])
		for _, card := range byBucket[b] {
			fold := ""
			if card.Variants > 0 {
				fold = fmt.Sprintf(" (+%d variants)", card.Variants)
			} else if card.Folded > 0 {
				fold = fmt.Sprintf(" (+%d folded)", card.Folded)
			}
			sink.Row("%5ds %4.1fb %6.1f  %s%s  ex: %s",
				card.Support, card.Bits, card.Score,
				strings.Join(corpus.Tokens(card.IDs), " ⇝ "), fold,
				exemplar(corpus, card.ExStream, card.ExSeq))
		}
	}
	if overflow > 0 {
		sink.Head("… %d more cards past --top %d", overflow, cmd.Top)
	}
	return nil
}
