package main

import (
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
)

// ContainsFlags filter a mined table to the rows that touch one token
// (ferret-f6i). ngrams, seqs and graph embed it; CommonFlags does not, so
// commands that would ignore the flag never offer it.
type ContainsFlags struct {
	Contains string `help:"Keep only rows where some token holds this substring (case-sensitive); counts and order do not change." name:"contains"`
}

// tokenMatch is the set of vocab ids whose rendered token holds a --contains
// substring, worked out once per run. A nil tokenMatch keeps every row.
type tokenMatch map[uint32]bool

func newTokenMatch(c *mine.Corpus, sub string) tokenMatch {
	if sub == "" {
		return nil
	}
	m := tokenMatch{}
	for id, tok := range c.Vocab {
		if strings.Contains(tok, sub) {
			m[uint32(id)] = true
		}
	}
	return m
}

// any reports whether some id matches; a nil tokenMatch matches everything.
func (m tokenMatch) any(ids ...uint32) bool {
	if m == nil {
		return true
	}
	for _, id := range ids {
		if m[id] {
			return true
		}
	}
	return false
}

// keepMatching drops the rows whose ids hold no matching token, keeping the
// rest in order. ids reads a row's token ids.
func keepMatching[T any](m tokenMatch, rows []T, ids func(T) []uint32) []T {
	if m == nil {
		return rows
	}
	kept := rows[:0:0]
	for i := range rows {
		if m.any(ids(rows[i])...) {
			kept = append(kept, rows[i])
		}
	}
	return kept
}

// containsTag is the header fragment naming an active --contains filter.
func containsTag(sub string) string {
	if sub == "" {
		return ""
	}
	return " contains=" + sub
}
