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

	// Window records what a --since/--until window kept; nil when none was set.
	Window *Window
}

// Window is the outcome of Options.Since/Until on one Build: the bounds and
// how many streams survived, so a report header can show the denominator.
type Window struct {
	Since, Until time.Time // zero = unbounded on that side
	Total        int       // streams the lens built before the window
	Kept         int       // streams whose start fell inside the window
	NoTime       int       // streams dropped because no event carried a timestamp
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

	// Since and Until keep only streams whose start (the earliest timestamped
	// event of the stream, over every event, not just the lens's tokens) lies in
	// [Since, Until]. Zero = unbounded on that side. Until is an instant, so the
	// caller passes end-of-day for an inclusive date. A stream with no timestamp
	// has no start: it is dropped when either bound is set, kept otherwise.
	Since, Until time.Time
}

func (o Options) windowed() bool { return !o.Since.IsZero() || !o.Until.IsZero() }

// inWindow reports whether a stream starting at start is kept.
func (o Options) inWindow(start time.Time) bool {
	if start.IsZero() {
		return false
	}
	return (o.Since.IsZero() || !start.Before(o.Since)) &&
		(o.Until.IsZero() || !start.After(o.Until))
}

// Build streams the events artifact through a lens into a Corpus.
// Streams are keyed (project, session, agent): subagent transcripts are
// separate streams — interleaving them into the parent would fabricate
// sequences that never happened.
func Build(eventsPath string, l lens.Lens, opts Options) (*Corpus, error) {
	c := &Corpus{}
	intern := map[string]uint32{}
	streamIdx := map[string]int{}
	windowed := opts.windowed()
	var starts map[string]time.Time // stream key → earliest timestamp seen
	if windowed {
		starts = map[string]time.Time{}
	}

	err := event.Read(eventsPath, func(ev *event.Event) error {
		if opts.NoSidechain && ev.Sidechain {
			return nil
		}
		key := ""
		if windowed {
			// Judge a stream by every event it has, not only the ones this lens
			// tokenizes: its start must not move with the lens.
			key = streamKey(ev)
			noteStart(starts, key, ev.Time)
		}
		tok, ok := l.Token(ev)
		if !ok {
			return nil
		}
		if opts.MarkFail {
			tok += failMark(ev.Status)
		}
		if key == "" {
			key = streamKey(ev)
		}
		si := c.stream(streamIdx, key, ev.Sidechain)
		c.Streams[si] = append(c.Streams[si], Tok{ID: c.intern(intern, tok), Seq: ev.Seq, Bytes: ev.Bytes})
		return nil
	})
	if err != nil {
		return nil, err
	}

	if windowed {
		intern = c.applyWindow(opts, starts)
	}
	if opts.Collapse {
		c.collapse(intern)
	}
	return c, nil
}

// failMark is the suffix a failed-action token carries under Options.MarkFail.
func failMark(status string) string {
	switch status {
	case event.StatusFail:
		return "!"
	case event.StatusCFail:
		return "?" // part of a failed compound chain; failing segment unknown
	}
	return ""
}

// stream returns the index of the stream keyed key, opening it on first sight.
func (c *Corpus) stream(streamIdx map[string]int, key string, sidechain bool) int {
	si, ok := streamIdx[key]
	if !ok {
		si = len(c.Streams)
		streamIdx[key] = si
		c.Streams = append(c.Streams, nil)
		c.StreamKeys = append(c.StreamKeys, key)
		c.Sidechain = append(c.Sidechain, sidechain)
	}
	return si
}

func streamKey(ev *event.Event) string { return ev.Project + "/" + ev.Session + "@" + ev.Agent }

// noteStart keeps the earliest non-zero timestamp seen for key.
func noteStart(starts map[string]time.Time, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	if cur, ok := starts[key]; !ok || t.Before(cur) {
		starts[key] = t
	}
}

// applyWindow drops the streams that start outside the window, records the
// counts in c.Window, and rebuilds the vocab from the survivors so a token seen
// only in dropped streams does not linger. It returns the fresh intern map for
// the later collapse pass.
func (c *Corpus) applyWindow(opts Options, starts map[string]time.Time) map[string]uint32 {
	w := &Window{Since: opts.Since, Until: opts.Until, Total: len(c.Streams)}
	oldVocab := c.Vocab
	c.Vocab = nil
	intern := map[string]uint32{}
	remap := map[uint32]uint32{}

	streams, keys, side := c.Streams[:0], c.StreamKeys[:0], c.Sidechain[:0]
	for si, st := range c.Streams {
		key, sc := c.StreamKeys[si], c.Sidechain[si]
		start, timed := starts[key]
		if !timed {
			w.NoTime++
		}
		if !opts.inWindow(start) {
			continue
		}
		for i := range st {
			id, ok := remap[st[i].ID]
			if !ok {
				id = c.intern(intern, oldVocab[st[i].ID])
				remap[st[i].ID] = id
			}
			st[i].ID = id
		}
		streams, keys, side = append(streams, st), append(keys, key), append(side, sc)
	}
	c.Streams, c.StreamKeys, c.Sidechain = streams, keys, side
	w.Kept = len(streams)
	c.Window = w
	return intern
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
