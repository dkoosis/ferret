// Package mine builds token streams from events and runs the v0 miners:
// n-grams, directly-follows graph, summary stats.
package mine

import (
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
)

// Tok is one token occurrence in a stream.
type Tok struct {
	ID  uint32
	Seq int // event Seq of the occurrence (run start, if collapsed)
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
	Collapse    bool // run-length collapse: read read read → read+
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
		if opts.MarkFail && ev.Status == event.StatusFail {
			tok += "!"
		}
		key := ev.Project + "/" + ev.Session + "@" + ev.Agent
		si, ok := streamIdx[key]
		if !ok {
			si = len(c.Streams)
			streamIdx[key] = si
			c.Streams = append(c.Streams, nil)
			c.StreamKeys = append(c.StreamKeys, key)
		}
		id, ok := intern[tok]
		if !ok {
			id = uint32(len(c.Vocab))
			intern[tok] = id
			c.Vocab = append(c.Vocab, tok)
		}
		c.Streams[si] = append(c.Streams[si], Tok{ID: id, Seq: ev.Seq})
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

// collapse rewrites runs of an identical token (length ≥ 2) into a single
// "tok+" token — the cheapest and most effective trivia suppressor.
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
				pid, ok := plus[st[i].ID]
				if !ok {
					name := c.Vocab[st[i].ID] + "+"
					if existing, has := intern[name]; has {
						pid = existing
					} else {
						pid = uint32(len(c.Vocab))
						intern[name] = pid
						c.Vocab = append(c.Vocab, name)
					}
					plus[st[i].ID] = pid
				}
				out = append(out, Tok{ID: pid, Seq: st[i].Seq})
			} else {
				out = append(out, st[i])
			}
			i = j
		}
		c.Streams[si] = out
	}
}
