// Package mine builds token streams from events and runs the v0 miners:
// n-grams, directly-follows graph, summary stats.
package mine

import (
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
)

// Tok is one token occurrence in a stream.
type Tok struct {
	ID    uint32
	Seq   int // event Seq of the occurrence (run start, if collapsed)
	Bytes int // measured context cost of the occurrence (summed over a collapsed run)
}

// sameCall reports whether two tokens are segments of the SAME compound bash
// invocation. The ingest gives every segment of one tool_use line that line's
// Seq, so two adjacent tokens with an equal Seq are within-call segments — not
// a cross-call transition. The transition-counting miners (follows, n-grams,
// PrefixSpan) use this to avoid reading one atomic `a && b && c` call as a
// multi-step routine, which roughly doubled top burn rankings (ferret-07s).
func sameCall(a, b Tok) bool { return a.Seq == b.Seq }

// spansMultipleCalls reports whether a run of stream positions covers at least
// two distinct calls (distinct Seqs). A contiguous gram or a matched sequence
// confined to one call is not a cross-call routine and must not be mined.
func spansMultipleCalls(toks []Tok) bool {
	for i := 1; i < len(toks); i++ {
		if !sameCall(toks[i-1], toks[i]) {
			return true
		}
	}
	return false
}

// Corpus is the tokenized view of the whole events artifact under one lens.
type Corpus struct {
	Streams    [][]Tok
	StreamKeys []string // "project/session@agent" per stream
	Sidechain  []bool   // per stream: true = subagent sidechain transcript
	Vocab      []string // id → token
	Window     Window   // the --since/--until window the streams were kept by
}

// Window keeps only streams whose start falls in [Since, Until]. A stream's
// start is its earliest timestamped event; a zero bound is open. Build fills
// Total and Untimed so a header can show what the window kept and dropped.
type Window struct {
	Since   time.Time
	Until   time.Time // inclusive: the caller passes the last instant it wants
	Total   int       // streams before the window was applied
	Untimed int       // streams dropped because no event carried a timestamp
}

// Set reports whether either bound is given.
func (w Window) Set() bool { return !w.Since.IsZero() || !w.Until.IsZero() }

func (w Window) contains(t time.Time) bool {
	return !t.IsZero() &&
		(w.Since.IsZero() || !t.Before(w.Since)) &&
		(w.Until.IsZero() || !t.After(w.Until))
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
	Window      Window // Since/Until only; a zero Window keeps every stream
}

// Build streams the events artifact through a lens into a Corpus.
// Streams are keyed (project, session, agent): subagent transcripts are
// separate streams — interleaving them into the parent would fabricate
// sequences that never happened.
func Build(eventsPath string, l lens.Lens, opts Options) (*Corpus, error) {
	c := &Corpus{}
	intern := map[string]uint32{}
	streamIdx := map[string]int{}
	windowed := opts.Window.Set()
	start := map[string]time.Time{} // stream key → earliest event time, any token or none

	err := event.Read(eventsPath, func(ev *event.Event) error {
		if opts.NoSidechain && ev.Sidechain {
			return nil
		}
		key := ev.Project + "/" + ev.Session + "@" + ev.Agent
		if windowed {
			noteStart(start, key, ev.Time)
		}
		tok, ok := l.Token(ev)
		if !ok {
			return nil
		}
		if opts.MarkFail {
			tok += failMark(ev.Status)
		}
		si, ok := streamIdx[key]
		if !ok {
			si = len(c.Streams)
			streamIdx[key] = si
			c.Streams = append(c.Streams, nil)
			c.StreamKeys = append(c.StreamKeys, key)
			c.Sidechain = append(c.Sidechain, ev.Sidechain)
		}
		c.Streams[si] = append(c.Streams[si], Tok{ID: c.intern(intern, tok), Seq: ev.Seq, Bytes: ev.Bytes})
		return nil
	})
	if err != nil {
		return nil, err
	}

	if windowed {
		c.window(opts.Window, start, intern)
	}
	if opts.Collapse {
		c.collapse(intern)
	}
	return c, nil
}

// failMark is the suffix --mark-fail appends for a failed status.
func failMark(st string) string {
	switch st {
	case event.StatusFail:
		return "!"
	case event.StatusCFail:
		return "?" // part of a failed compound chain; failing segment unknown
	}
	return ""
}

// noteStart keeps the earliest non-zero time seen for a stream key.
func noteStart(start map[string]time.Time, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	if cur, ok := start[key]; !ok || t.Before(cur) {
		start[key] = t
	}
}

// window drops every stream whose start lies outside w, and every stream with
// no timestamp at all, recording the counts on c.Window. It then drops from
// Vocab (and intern) every token only the dropped streams held, so a lookup by
// token text cannot find one the window removed.
func (c *Corpus) window(w Window, start map[string]time.Time, intern map[string]uint32) {
	w.Total, w.Untimed = len(c.Streams), 0
	kept := 0
	for si, key := range c.StreamKeys {
		t, ok := start[key]
		if !ok {
			w.Untimed++
			continue
		}
		if !w.contains(t) {
			continue
		}
		c.Streams[kept], c.StreamKeys[kept], c.Sidechain[kept] = c.Streams[si], key, c.Sidechain[si]
		kept++
	}
	c.Streams, c.StreamKeys, c.Sidechain = c.Streams[:kept], c.StreamKeys[:kept], c.Sidechain[:kept]
	c.Window = w
	c.compactVocab(intern)
}

// compactVocab renumbers the ids the streams hold, in first-sight order, and
// rebuilds Vocab and intern to hold only those tokens.
func (c *Corpus) compactVocab(intern map[string]uint32) {
	const unseen = ^uint32(0)
	remap := make([]uint32, len(c.Vocab))
	for i := range remap {
		remap[i] = unseen
	}
	var vocab []string
	for _, st := range c.Streams {
		for i := range st {
			old := st[i].ID
			if remap[old] == unseen {
				remap[old] = uint32(len(vocab)) //nolint:gosec // bounded by the old vocab size
				vocab = append(vocab, c.Vocab[old])
			}
			st[i].ID = remap[old]
		}
	}
	clear(intern)
	for id, tok := range vocab {
		intern[tok] = uint32(id)
	}
	c.Vocab = vocab
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
		out := make([]Tok, 0, len(st))
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
