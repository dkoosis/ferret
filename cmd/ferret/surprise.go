package main

import (
	"os"
	"slices"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- surprise (PPM-lite) ----

func cmdSurprise() error {
	cmd := &CLI.Surprise
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 20
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
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
	scores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: cmd.Order, MinToks: cmd.MinToks})

	mean := 0.0
	for _, s := range scores {
		mean += s.Bits
	}
	if len(scores) > 0 {
		mean /= float64(len(scores))
	}
	routine, thrash := splitSurprise(scores, c.limit)

	if c.format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "order": cmd.Order, "meanBits": mean,
			"routine": routine, "thrash": thrash,
			keyTotal: len(scores), keyTruncated: len(routine)+len(thrash) < len(scores),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit+2, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ surprise: how predictable each session is to a model trained on all your sessions",
		"≡ (order-N context model). Low bits/tok = rote routine worth scripting; high = novel work or thrash.")
	sink.Head("surprise lens=%s order=%d streams=%d mean=%.2f bits/tok (low=routine/scriptable, high=thrash)",
		l.Name(), cmd.Order, len(scores), mean)
	sink.Head("most routine:")
	for _, s := range routine {
		if !sink.Row("%6.2f bits %5d toks  %s", s.Bits, s.Toks, s.Stream) {
			break
		}
	}
	sink.Head("most surprising:")
	for _, s := range slices.Backward(thrash) {
		if !sink.Row("%6.2f bits %5d toks  %s", s.Bits, s.Toks, s.Stream) {
			break
		}
	}
	return nil
}

// splitSurprise partitions the lo→hi sorted surprise scores into the most
// routine (low bits/tok) and most surprising (high bits/tok) sections,
// capping each at limit/2. The two sections must never overlap: on a small
// corpus the naive "first half" / "last half" slices share their middle, so
// the same streams render under both "most routine" and "most surprising"
// (ferret-045). Both the text and JSON paths consume this, so they stay in
// parity by construction.
func splitSurprise(scores []mine.StreamScore, limit int) (routine, thrash []mine.StreamScore) {
	half := limit / 2
	if half < 1 {
		half = 10
	}
	// Partition at the midpoint so routine ⊆ [0,mid) and thrash ⊆ [mid,n):
	// the two slices can never share an element, even when the corpus is
	// smaller than the limit. Each side is then capped at half.
	mid := len(scores) / 2
	routine = scores[:mid]
	if len(routine) > half {
		routine = routine[:half]
	}
	thrash = scores[mid:]
	if len(thrash) > half {
		thrash = thrash[len(thrash)-half:]
	}
	return routine, thrash
}
