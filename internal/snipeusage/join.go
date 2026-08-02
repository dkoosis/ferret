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

// group holds one (SessionKey, action) bucket's records in chronological
// order (file order — telemetry.Emit appends), plus a cursor marking the
// next unconsumed candidate. cursor only ever advances: Match calls for one
// group arrive in non-decreasing `at` order (ferret processes Events in Seq
// order within a session), so a classic two-pointer walk suffices.
type group struct {
	records []Record
	cursor  int
}

// Index groups Records by (SessionKey, "snipe_"+Command) for the join
// Match performs. Matched/Attempted count every Match call so an ingest run
// can report a corpus-level join rate (Builder.Stats.UsageJoined /
// UsageRecords) without inspecting individual events.
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
// ReadGlob). Each group's records are stably sorted by parsed TS —
// normally already file-order-chronological, but a defensive sort keeps
// Match's two-pointer walk correct even if ReadGlob concatenated more than
// one source file out of strict time order; ties keep their original
// (file) order, which is exactly the pairing rule the bead's "two
// identical repeated calls" case depends on.
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
	}
	return idx
}

// Match looks up the usage Record for one (session, action) call completing
// at `at`, doing a greedy nearest-neighbor-in-usageJoinWindow two-pointer
// walk over that group's remaining (unconsumed) records. A matched Record
// is consumed — removed from future consideration — so a second call in
// the same group doesn't re-match an already-claimed row. Returns
// (Record{}, false) when the group is empty/unknown, or no remaining
// candidate falls inside the window.
func (idx *Index) Match(session, action string, at time.Time) (Record, bool) {
	idx.Attempted++
	g := idx.groups[key(session, action)]
	if g == nil {
		return Record{}, false
	}
	for g.cursor < len(g.records) {
		rec := g.records[g.cursor]
		ts, ok := parseTS(rec.TS)
		if !ok {
			g.cursor++ // unparseable TS: skip, never matchable
			continue
		}
		diff := at.Sub(ts)
		if diff > usageJoinWindow {
			// This record is older than the window can reach from `at`, and
			// `at` only grows call-over-call within a group — it will never
			// be reachable again. Drop it (consume without matching) so a
			// dropped-row gap on the usage side doesn't stall the walk.
			g.cursor++
			continue
		}
		if diff < -usageJoinWindow {
			// Nearest candidate is still ahead of the window; don't consume
			// it — a later, closer `at` may still claim it.
			return Record{}, false
		}
		g.cursor++
		idx.Matched++
		return rec, true
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
