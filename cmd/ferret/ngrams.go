package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

var errBadRange = usage("bad --n range (gram length must be ≥ 2; 1-gram frequency = summary top actions)")

// ---- ngrams ----

func cmdNgrams() error {
	cmd := &CLI.Ngrams
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 30)
	lo := fromLensFlags(cmd.LensFlags)
	minN, maxN, err := parseRange(cmd.N)
	if err != nil {
		return err
	}
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	grams := mine.Filter(mine.CountGrams(corpus, minN, maxN), cmd.MinCount, cmd.MinSessions)

	if c.format == fmtJSON {
		type jg struct {
			Tokens   []string `json:"tokens"`
			Count    int      `json:"count"`
			Sessions int      `json:"sessions"`
			Exemplar string   `json:"exemplar"`
		}
		rows := make([]jg, 0, len(grams))
		for i, g := range grams {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, jg{corpus.Tokens(g.IDs), g.Count, g.Sessions, exemplar(corpus, g.ExStream, g.ExSeq)})
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "n": cmd.N, "grams": rows,
			keyTotal: len(grams), keyTruncated: len(rows) < len(grams),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ ngrams: exact action sequences repeated verbatim (no gaps). High count across many",
		"≡ sessions = a habitual routine — script/skill candidate. Nx/Ms = N occurrences in M sessions.",
		legendMarks)
	sink.Head("ngrams lens=%s n=%s streams=%d grams=%d (min-count=%d min-sessions=%d)",
		l.Name(), cmd.N, len(corpus.Streams), len(grams), cmd.MinCount, cmd.MinSessions)
	emptyNote(sink, len(grams), "grams")
	for _, g := range grams {
		sink.Row("%5dx/%-4ds %s  ex: %s",
			g.Count, g.Sessions, strings.Join(corpus.Tokens(g.IDs), " → "), exemplar(corpus, g.ExStream, g.ExSeq))
	}
	return nil
}

// parseRange parses "N" or "LO-HI" gram-length bounds for --n, rejecting
// anything below a 2-gram (a 1-gram is just token frequency, already covered
// by 'summary top actions') or an inverted/malformed range.
func parseRange(s string) (int, int, error) {
	if a, b, ok := strings.Cut(s, "-"); ok {
		lo, err1 := strconv.Atoi(a)
		hi, err2 := strconv.Atoi(b)
		if err1 != nil || err2 != nil || lo < 2 || hi < lo {
			return 0, 0, fmt.Errorf("%w: %q", errBadRange, s)
		}
		return lo, hi, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 2 {
		return 0, 0, fmt.Errorf("%w: %q", errBadRange, s)
	}
	return n, n, nil
}
