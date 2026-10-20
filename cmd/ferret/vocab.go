package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- vocab (ferret-mta) ----

// vocabRow is one distinct token under the lens: how often it occurs and in
// how many streams.
type vocabRow struct {
	Token    string `json:"token"`
	Count    int    `json:"count"`
	Sessions int    `json:"sessions"`
}

func cmdVocab() error {
	cmd := &CLI.Vocab
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	// The default is every token: the point is to see how a command tokenizes.
	applyDefaultLimit(c, 0)
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	rows := vocabRows(corpus, cmd.Substring)

	if c.format == fmtJSON {
		shown := rows
		if c.limit > 0 && len(shown) > c.limit {
			shown = shown[:c.limit]
		}
		return out.JSON(os.Stdout, withWindow(map[string]any{
			keyLens: l.Name(), "tokens": shown, keyTotal: len(rows), keyTruncated: len(shown) < len(rows),
		}, corpus))
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ vocab: the distinct tokens under the lens, as the miners see them, most frequent first.",
		"≡ N occ / M sessions = N occurrences in M sessions; a session is a stream (project/session@agent).",
		legendMarks)
	head := "vocab lens=" + l.Name()
	if cmd.Substring != "" {
		head += fmt.Sprintf(" contains=%q", cmd.Substring)
	}
	sink.Head("%s streams=%d tokens=%d", head, len(corpus.Streams), len(rows))
	windowHead(sink, corpus)
	emptyNote(sink, len(rows), "tokens")
	for _, r := range rows {
		sink.Row("  %s  %d occ / %d sessions", r.Token, r.Count, r.Sessions)
	}
	return nil
}

// vocabRows counts each vocab id over the streams, after the lens flags have
// rewritten them, so !, ? and + forms count as their own tokens. An id no
// stream holds (a token collapse replaced everywhere) is left out, as is one
// whose text lacks sub. Sorted by occurrences, then by token.
func vocabRows(c *mine.Corpus, sub string) []vocabRow {
	count := make([]int, len(c.Vocab))
	sessions := make([]int, len(c.Vocab))
	last := make([]int, len(c.Vocab)) // stream index + 1 that last counted the id
	for si, st := range c.Streams {
		for _, t := range st {
			count[t.ID]++
			if last[t.ID] != si+1 {
				last[t.ID] = si + 1
				sessions[t.ID]++
			}
		}
	}
	rows := []vocabRow{} // non-nil: JSON emits "tokens": [] on no match
	for id, tok := range c.Vocab {
		if count[id] == 0 || !strings.Contains(tok, sub) {
			continue
		}
		rows = append(rows, vocabRow{tok, count[id], sessions[id]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Token < rows[j].Token
	})
	return rows
}
