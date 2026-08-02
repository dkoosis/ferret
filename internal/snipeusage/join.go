package snipeusage

import (
	"sort"
	"time"
)

// usageJoinWindow is the max allowed distance between a usage.jsonl row's
// TS (stamped after the call completes, telemetry.go:167) and the canonical
// Event's resolve() ts (the tool_result timestamp) for the two to be
// considered the same call. 10s: RFC3339's 1s granularity plus CC's
// tool_use/tool_result round-trip overhead — a coarser analogue of
// build.go's existing retryWindow. One-line tune if a real corpus shows
// otherwise.
const usageJoinWindow = 10 * time.Second

// group holds one (SessionKey, action) bucket's records, stably sorted by
// parsed TS (earliest first), plus a per-record consumed flag so a matched
// record is claimed exactly once. There is deliberately NO cursor: Match
// calls for one group do NOT arrive in `at` order. Ingest calls b.File()
// once per source transcript and walks files in filepath.WalkDir LEXICAL
// order (transcript.Walk), and subagent transcripts inherit their parent
// session's SessionKey (transcript/walk.go) — so one group's records span
// several files walked by path, not by time. A cursor that only advanced
// would skip records once an out-of-order file supplied a larger `at`,
// silently mis-joining Rung/CandidateCount. Match instead scans the
// remaining unconsumed records every call, which is correct regardless of
// call order.
type group struct {
	records  []Record
	consumed []bool
}

// Index groups Records by (SessionKey, "snipe_"+Command) for the join
// Match performs. Matched/Attempted count every Match call so an ingest run
// can report a corpus-level join rate (Builder.Stats.UsageJoined /
// UsageRecords) without inspecting individual events.
//
// Index is NOT goroutine-safe: Match mutates group.consumed and the
// Matched/Attempted counters without synchronization. Today's only caller
// (ingest.go's strictly sequential file loop) never races; a hypothetical
// parallel ingest would need external locking.
type Index struct {
	groups    map[string]*group
	Matched   int
	Attempted int
}

// key mirrors shellnorm's "snipe_" + subcommand action-name convention
// (norm.go's subcmdTools/fromCall), so a Record's own group key and an
// Event's Action string are directly comparable.
func key(sessionKey, action string) string {
	return sessionKey + "\x00" + action
}

// NewIndex builds a lookup Index from a flat slice of Records (e.g. from
// ReadGlob). Each group's records are stably sorted by parsed TS so Match's
// "earliest record within the window" scan is a single forward pass; ties
// (and unparseable TS) keep their original file order, which is exactly the
// emit-order pairing rule the bead's "two identical repeated calls" case
// depends on.
func NewIndex(records []Record) *Index {
	idx := &Index{groups: map[string]*group{}}
	for i := range records {
		r := records[i]
		k := key(r.SessionKey, "snipe_"+r.Command)
		g := idx.groups[k]
		if g == nil {
			g = &group{}
			idx.groups[k] = g
		}
		g.records = append(g.records, r)
	}
	for _, g := range idx.groups {
		recs := g.records
		sort.SliceStable(recs, func(i, j int) bool {
			ti, oki := parseTS(recs[i].TS)
			tj, okj := parseTS(recs[j].TS)
			if !oki || !okj {
				return false // unparseable TS: leave relative order (stable sort keeps file order)
			}
			return ti.Before(tj)
		})
		g.consumed = make([]bool, len(recs))
	}
	return idx
}

// Match looks up the usage Record for one (session, action) call completing
// at `at`. It claims the earliest unconsumed record whose TS falls within
// usageJoinWindow of `at`, marks it consumed so a later call can't re-match
// it, and returns it. Because it scans the remaining records fresh each
// call and never advances a shared cursor, the result is independent of the
// ORDER Match is called in — the load-bearing property once a group's rows
// come from more than one transcript file (see group's doc). Returns
// (Record{}, false) when the group is empty/unknown or no unconsumed record
// falls inside the window.
func (idx *Index) Match(session, action string, at time.Time) (Record, bool) {
	idx.Attempted++
	g := idx.groups[key(session, action)]
	if g == nil {
		return Record{}, false
	}
	for i := range g.records {
		if g.consumed[i] {
			continue
		}
		ts, ok := parseTS(g.records[i].TS)
		if !ok {
			continue // unparseable TS: never matchable, but a later call might parse a different row
		}
		diff := at.Sub(ts)
		if diff > usageJoinWindow {
			// Record is older than the window reaches from THIS `at`. Don't
			// consume it — a different Match with a smaller `at` (calls arrive
			// out of order) may still legitimately claim it.
			continue
		}
		if diff < -usageJoinWindow {
			// Records are sorted ascending by TS, so this one and every record
			// after it are too far in the future for `at`. Stop.
			break
		}
		g.consumed[i] = true
		idx.Matched++
		return g.records[i], true
	}
	return Record{}, false
}

func parseTS(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
