// Package mine builds token streams from events and runs the v0 miners:
// n-grams, directly-follows graph, summary stats.
package mine

import (
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
)

// Tok is one token occurrence in a stream.
type Tok struct {
	ID    uint32
	Seq   int // event Seq of the occurrence (run start, if collapsed)
	Bytes int // measured context cost of the occurrence (summed over a collapsed run)
}

// Corpus is the tokenized view of the whole events artifact under one lens.
type Corpus struct {
	Streams    [][]Tok
	StreamKeys []string // "project/session@agent" per stream
	Vocab      []string // id → token
}

func (c *Corpus) Tokens(ids []uint32) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = c.Vocab[id]
	}
	return out
}

// Options control the cross-cutting stream transforms.
type Options struct {
	MarkFail    bool // append "!" to failed-action tokens
	Collapse    bool // run-length collapse: read,read,read → read+
	NoSidechain bool
}

// Build streams the events artifact through a lens into a Corpus.
// Streams are keyed (project, session, agent): subagent transcripts are
// separate streams — interleaving them into the parent would fabricate
// sequences that never happened.
func Build(eventsPath string, l lens.Lens, opts Options) (*Corpus, error) {
	c := &Corpus{}
	intern := map[string]uint32{}
	streamIdx := map[string]int{}

	err := event.Read(eventsPath, func(ev *event.Event) error {
		if opts.NoSidechain && ev.Sidechain {
			return nil
		}
		tok, ok := l.Token(ev)
		if !ok {
			return nil
		}
		if opts.MarkFail {
			switch ev.Status {
			case event.StatusFail:
				tok += "!"
			case event.StatusCFail:
				tok += "?" // part of a failed compound chain; failing segment unknown
			}
		}
		key := ev.Project + "/" + ev.Session + "@" + ev.Agent
		si, ok := streamIdx[key]
		if !ok {
			si = len(c.Streams)
			streamIdx[key] = si
			c.Streams = append(c.Streams, nil)
			c.StreamKeys = append(c.StreamKeys, key)
		}
		c.Streams[si] = append(c.Streams[si], Tok{ID: c.intern(intern, tok), Seq: ev.Seq, Bytes: ev.Bytes})
		return nil
	})
	if err != nil {
		return nil, err
	}

	if opts.Collapse {
		c.collapse(intern)
	}
	return c, nil
}

// intern returns the stable id for tok, growing the vocab on first sight.
func (c *Corpus) intern(intern map[string]uint32, tok string) uint32 {
	if id, ok := intern[tok]; ok {
		return id
	}
	id := uint32(len(c.Vocab)) //nolint:gosec // vocab is distinct tokens; nowhere near 2^32
	intern[tok] = id
	c.Vocab = append(c.Vocab, tok)
	return id
}

// collapse rewrites runs of an identical token (length ≥ 2) into a single
// "tok+" token — the cheapest and most effective trivia suppressor.
//
// This is run-length encoding applied to token streams: Golomb, "Run-Length
// Encodings", IEEE Trans. Information Theory 12(3), 1966.
func (c *Corpus) collapse(intern map[string]uint32) {
	plus := map[uint32]uint32{} // id → id of "tok+"
	for si, st := range c.Streams {
		out := st[:0]
		for i := 0; i < len(st); {
			j := i + 1
			for j < len(st) && st[j].ID == st[i].ID {
				j++
			}
			if j-i >= 2 {
				// Collapsed token carries the whole run's measured cost: a
				// read+ that absorbed five reads burned all five reads' bytes.
				bytes := 0
				for k := i; k < j; k++ {
					bytes += st[k].Bytes
				}
				out = append(out, Tok{ID: c.plusID(plus, intern, st[i].ID), Seq: st[i].Seq, Bytes: bytes})
			} else {
				out = append(out, st[i])
			}
			i = j
		}
		c.Streams[si] = out
	}
}

// plusID returns (caching) the id of the "tok+" variant of id.
func (c *Corpus) plusID(plus map[uint32]uint32, intern map[string]uint32, id uint32) uint32 {
	if pid, ok := plus[id]; ok {
		return pid
	}
	pid := c.intern(intern, c.Vocab[id]+"+")
	plus[id] = pid
	return pid
}
