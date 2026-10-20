package main

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- around (ferret-hp5) ----

// aroundSeqTop caps the gapped-sequence tail; the neighbour tables take --limit.
const aroundSeqTop = 10

// neighbour is one row of a before/after table: the path from the token out
// to the token d steps away, with the counts of the (d+1)-gram that spells it.
type neighbour struct {
	dist     int
	path     []uint32 // gram ids, in stream order
	far      uint32   // the token d steps away
	count    int
	sessions int
}

func cmdAround() error {
	cmd := &CLI.Around
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	applyDefaultLimit(c, 15)
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if cmd.Window < 1 || cmd.Window > 3 {
		return fmt.Errorf("%w: %d", errBadWindow, cmd.Window)
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	id, ok, err := resolveToken(corpus, cmd.Token)
	if err != nil {
		return err
	}
	if !ok {
		if c.format == fmtJSON {
			return out.JSON(os.Stdout, map[string]any{keyLens: l.Name(), "token": cmd.Token, "matches": 0})
		}
		fmt.Printf("0 matches for %s\n", cmd.Token)
		return nil
	}

	before, after := neighbours(mine.CountGrams(corpus, 2, cmd.Window+1), id)
	pats, capped := mine.MineSeqs(corpus, mine.SeqOpts{
		MinSupport: reportMinSupport, MaxGap: reportMaxGap, MaxLen: 5, MaxPatterns: 10000,
		Keep: func(ids []uint32) bool { return slices.Contains(ids, id) },
	})
	seqTotal := len(pats)
	if len(pats) > aroundSeqTop {
		pats = pats[:aroundSeqTop]
	}

	if c.format == fmtJSON {
		b, a := aroundJSON(corpus, before, c.limit), aroundJSON(corpus, after, c.limit)
		return out.JSON(os.Stdout, withWindow(map[string]any{
			keyLens: l.Name(), "token": corpus.Vocab[id], "window": cmd.Window, "streams": len(corpus.Streams),
			"before": b, "after": a, "seqs": seqsJSON(corpus, pats),
			"beforeTotal": len(before), "afterTotal": len(after), "seqsTotal": seqTotal, "seqsCapped": capped,
			keyTruncated: len(b) < len(before) || len(a) < len(after) || len(pats) < seqTotal || capped,
		}, corpus))
	}
	aroundText(corpus, l.Name(), id, cmd.Window, before, after, pats, capped, c)
	return nil
}

// aroundText prints the before and after tables, then the seqs tail.
func aroundText(corpus *mine.Corpus, lensName string, id uint32, window int,
	before, after []neighbour, pats []*mine.SeqPattern, capped bool, c *common,
) {
	sink := out.NewSink(os.Stdout, 0, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ around: the tokens just before and after one token, then the gapped routines through it.",
		"≡ Nx/Ms = N occurrences in M sessions; a session is a stream (project/session@agent). dN = N steps away.",
		legendMarks)
	sink.Head("around %s lens=%s window=%d streams=%d", corpus.Vocab[id], lensName, window, len(corpus.Streams))
	windowHead(sink, corpus)
	aroundTable(sink, corpus, "before", before, c.limit)
	aroundTable(sink, corpus, "after", after, c.limit)
	sink.Head("seqs through it (min-support=%d max-gap=%d, top %d)", reportMinSupport, reportMaxGap, aroundSeqTop)
	if capped {
		sink.Head("‡ search hit the 10000-pattern cap — the list may miss patterns")
	}
	emptyNote(sink, len(pats), "seqs")
	for _, p := range pats {
		sink.Row("%5ds %s  ex: %s",
			p.Support, strings.Join(corpus.Tokens(p.IDs), " ⇝ "), exemplar(corpus, p.ExStream, p.ExSeq))
	}
}

var errBadWindow = usage("--window must be 1, 2 or 3")

// resolveToken finds the vocab id for tok: an exact match wins, else the one
// token that holds tok as a substring. No match is not an error (ok=false); a
// substring that matches several tokens is, and names them.
func resolveToken(c *mine.Corpus, tok string) (uint32, bool, error) {
	var hits []uint32
	for id, v := range c.Vocab {
		if v == tok {
			return uint32(id), true, nil
		}
		if strings.Contains(v, tok) {
			hits = append(hits, uint32(id))
		}
	}
	switch len(hits) {
	case 0:
		return 0, false, nil
	case 1:
		return hits[0], true, nil
	}
	return 0, false, fmt.Errorf("%w: %q matches %d tokens: %s",
		errAmbiguousToken, tok, len(hits), strings.Join(c.Tokens(hits), " · "))
}

var errAmbiguousToken = usage("ambiguous token; pass one of the matches exactly")

// neighbours reads the raw gram map — not mine.Filter, whose closed-gram
// suppression would hide a 2-gram whose 3-gram keeps 80% of its count, and
// that 2-gram is the answer here. A gram that ends with id gives a before row;
// one that starts with id gives an after row.
func neighbours(grams map[string]*mine.Gram, id uint32) (before, after []neighbour) {
	for _, g := range grams {
		d := len(g.IDs) - 1
		if g.IDs[d] == id {
			before = append(before, neighbour{d, g.IDs, g.IDs[0], g.Count, g.Sessions})
		}
		if g.IDs[0] == id {
			after = append(after, neighbour{d, g.IDs, g.IDs[d], g.Count, g.Sessions})
		}
	}
	sortNeighbours(before)
	sortNeighbours(after)
	return before, after
}

// sortNeighbours orders rows nearest first, then by sessions, then count; ids
// last so ties are deterministic (map order isn't).
func sortNeighbours(ns []neighbour) {
	sort.Slice(ns, func(i, j int) bool {
		a, b := ns[i], ns[j]
		if a.dist != b.dist {
			return a.dist < b.dist
		}
		if a.sessions != b.sessions {
			return a.sessions > b.sessions
		}
		if a.count != b.count {
			return a.count > b.count
		}
		return slices.Compare(a.path, b.path) < 0
	})
}

// aroundTable prints one neighbour table, at most limit rows per distance.
func aroundTable(sink *out.Sink, c *mine.Corpus, name string, ns []neighbour, limit int) {
	sink.Head("%s (%d)", name, len(ns))
	emptyNote(sink, len(ns), "neighbours")
	shown, dropped := 0, 0
	for i, n := range ns {
		if i > 0 && ns[i-1].dist != n.dist {
			shown = 0
		}
		if limit > 0 && shown >= limit {
			dropped++
			continue
		}
		shown++
		row := c.Vocab[n.far]
		if n.dist > 1 {
			row += "   via " + strings.Join(c.Tokens(n.path), " → ")
		}
		sink.Row("%5dx/%-4ds d%d %s", n.count, n.sessions, n.dist, row)
	}
	if dropped > 0 {
		sink.Head("… +%d more %s rows (raise --limit)", dropped, name)
	}
}

type aroundRow struct {
	Dist     int      `json:"dist"`
	Token    string   `json:"token"`
	Path     []string `json:"path"`
	Count    int      `json:"count"`
	Sessions int      `json:"sessions"`
}

func aroundJSON(c *mine.Corpus, ns []neighbour, limit int) []aroundRow {
	rows := make([]aroundRow, 0, len(ns))
	shown := 0
	for i, n := range ns {
		if i > 0 && ns[i-1].dist != n.dist {
			shown = 0
		}
		if limit > 0 && shown >= limit {
			continue
		}
		shown++
		rows = append(rows, aroundRow{n.dist, c.Vocab[n.far], c.Tokens(n.path), n.count, n.sessions})
	}
	return rows
}

func seqsJSON(c *mine.Corpus, pats []*mine.SeqPattern) []map[string]any {
	rows := make([]map[string]any, 0, len(pats))
	for _, p := range pats {
		rows = append(rows, map[string]any{
			"tokens": c.Tokens(p.IDs), "support": p.Support, "exemplar": exemplar(c, p.ExStream, p.ExSeq),
		})
	}
	return rows
}
