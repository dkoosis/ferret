package mine

import (
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// Misfire ranking (ferret-ct1) — the missing front half of the fixes/
// substitution ledger loop (kuv.15): a corpus-wide rollup of repeated command
// failures, so a routine that misfires the same way across many sessions
// (the jq/`bd show '.[0]'` saga — 152 errors across 130 transcripts before a
// manual tools.md fix, ferret-67o) surfaces automatically instead of waiting
// for a human to notice the pattern by hand.
//
// The key is Event.Action — for shell events this is already the
// shellnorm-normalized command token (internal/shellnorm), and for tool
// events it is the tool name; both are stable, low-cardinality identifiers of
// "the same misfire happening again." Ranking and repair-pair detection reuse
// signal the ingest pipeline already computes: Event.Status (fail | cfail
// marks a misfire, mirroring FailedActions in internal/score/friction.go) and
// Event.Retry (a later same-key success within the retry window — the
// existing repair tell, set once in internal/event/build.go's finish()).
// This package does not recompute Retry; it only reads it.

// MisfireRow is one Action key's corpus-wide failure ranking.
type MisfireRow struct {
	Key      string  `json:"key"`      // Event.Action — shellnorm token or tool name
	Fails    int     `json:"fails"`    // events with Status fail|cfail
	FailSess int     `json:"failSess"` // distinct sessions with ≥1 failure of this key — the "spread"
	Calls    int     `json:"calls"`    // all tool|shell events with this key, any status
	FailRate float64 `json:"failRate"` // Fails / Calls
	Score    float64 `json:"score"`    // Fails × FailSess — the corpus-wide rank
}

// RepairPair is a failed call paired with the succeeded variant that fixed it,
// aggregated by (key, failed form, fixed form). Detection rides Event.Retry:
// a same-key success inside the retry window after a failure is the repair
// tell. The "form" fields come from Event.Detail — populated with the raw
// (truncated) command text for shell events, but NOT for tool events (tool
// events carry no raw invocation text in this build, only Target). When
// neither side has Detail text, FailedRaw/FixedRaw are both "" and the pair
// collapses to a key-level count — a v1 fallback per the bead spec: still
// useful ("this key fails and gets retried N times") even without seeing the
// exact before/after text.
type RepairPair struct {
	Key       string `json:"key"`
	FailedRaw string `json:"failedRaw,omitempty"`
	FixedRaw  string `json:"fixedRaw,omitempty"`
	Count     int    `json:"count"`
}

// SwallowRow is one Action key's ranking of misfires that are structurally
// invisible to Status (ferret-cax). A guess-chain written as
// `cmd 2>/dev/null || fallback` discards the error text and exits with the
// fallback's code, so the tool_result carries no is_error flag and
// internal/event/build.go's resolve() records the call as ok — it never
// reaches MisfireRow at all. shellnorm.Swallows recovers the shape from the
// command text at ingest (Event.Swallow), which is what this table counts.
//
// Swallows is a FLOOR on hidden failures, not a count of them: the shape says
// the left arm's failure *would* be invisible, never that it failed on this
// run. The left arm's return code is exactly the thing the construct throws
// away, so no pass over the corpus can separate "swallowed a failure" from
// "succeeded quietly." A key that ranks high here is one where the corpus
// cannot answer how often it misfires — which is the finding.
type SwallowRow struct {
	Key         string  `json:"key"`         // Event.Action, "sh:"-prefixed — same key space as MisfireRow
	Swallows    int     `json:"swallows"`    // shell events whose command swallowed its own errors
	SwallowSess int     `json:"swallowSess"` // distinct sessions with ≥1 swallow of this key — the "spread"
	Calls       int     `json:"calls"`       // all shell events with this key, swallowing or not
	SwallowRate float64 `json:"swallowRate"` // Swallows / Calls
	Score       float64 `json:"score"`       // Swallows × SwallowSess — mirrors MisfireRow.Score
	Exemplar    string  `json:"exemplar,omitempty"`
}

// MisfireReport is the whole ranked bundle: the corpus's misfire table, the
// repair pairs pulled from it, and the swallowed-error table — the misfires
// the first two tables can never see.
type MisfireReport struct {
	Rows      []MisfireRow `json:"rows"`
	Repairs   []RepairPair `json:"repairs"`
	Swallowed []SwallowRow `json:"swallowed"`
}

// misfireAgg accumulates one Action key's totals as the corpus walk streams
// events into it. swallows/swallowSess ride the same walk — a swallow is a
// property of the command text, orthogonal to the event's Status, so both
// tables are filled in one pass.
type misfireAgg struct {
	fails        int
	calls        int
	failSess     map[string]struct{}
	swallows     int
	swallowSess  map[string]struct{}
	swallowFirst string // first swallowing command seen, as the row's exemplar
}

// failKey tracks the most recent unresolved failure's raw Detail per
// (session, Action, Target) — the same composite key internal/event/build.go's
// finish() uses to compute Retry, so a matched Retry=true success here is
// guaranteed to correspond to the failure captured under this key.
type failKey struct {
	session string
	action  string
	target  string
}

// MineMisfires walks events in file order and produces the ranked misfire
// table + repair pairs. Events are expected in the order event.Read yields
// them (Seq-ordered per source file); repair-pair matching is keyed per
// session so interleaving across sessions in the stream does not confuse it.
func MineMisfires(events []event.Event) MisfireReport {
	aggs := map[string]*misfireAgg{}
	pending := map[failKey]string{} // → last fail's Detail (raw text, possibly "")
	pairCounts := map[string]*RepairPair{}

	for i := range events {
		ev := &events[i]
		if ev.Kind != event.KindTool && ev.Kind != event.KindShell {
			continue
		}
		key := misfireKey(ev)
		a := aggs[key]
		if a == nil {
			a = &misfireAgg{failSess: map[string]struct{}{}, swallowSess: map[string]struct{}{}}
			aggs[key] = a
		}
		a.calls++

		observeStatus(a, pending, pairCounts, key, ev)
		observeSwallow(a, ev)
	}

	return MisfireReport{
		Rows:      rankMisfires(aggs),
		Repairs:   rankRepairs(pairCounts),
		Swallowed: rankSwallows(aggs),
	}
}

// observeSwallow folds one event's swallowed-error tell into its aggregate.
// Event.Swallow is the authoritative signal — computed at ingest, where the
// full command line still exists. The text re-check is a compatibility path
// for a corpus ingested before that field existed: it only ever fires on the
// forms whose whole command survives into Detail intact (the no-segment "sh"
// event and the parse-fallback segment), because a normal compound is split
// and its `||` is gone. Cheap by construction — the substring guard means the
// bash parser runs only for the handful of commands that mention /dev/null.
func observeSwallow(a *misfireAgg, ev *event.Event) {
	if ev.Kind != event.KindShell {
		return // tool events carry no command text to swallow anything with
	}
	swallowed := ev.Swallow || (strings.Contains(ev.Detail, devNull) && shellnorm.Swallows(ev.Detail))
	if !swallowed {
		return
	}
	a.swallows++
	a.swallowSess[ev.Session] = struct{}{}
	if a.swallowFirst == "" {
		a.swallowFirst = ev.Detail
	}
}

// devNull guards observeSwallow's fallback parse — see its doc comment.
const devNull = "/dev/null"

// observeStatus folds one event's outcome into its command aggregate and the
// pending-fail → repair-pair state machine. cfail's failing segment is unknown
// (compound chain), so it counts as a failure but is not a safe repair-pair
// anchor — mirrors finish()'s own rule that cfail neither opens nor closes the
// retry window.
func observeStatus(a *misfireAgg, pending map[failKey]string, pairCounts map[string]*RepairPair, key string, ev *event.Event) {
	fk := failKey{session: ev.Session, action: ev.Action, target: ev.Target}
	switch ev.Status {
	case event.StatusFail, event.StatusCFail:
		a.fails++
		a.failSess[ev.Session] = struct{}{}
		if ev.Status == event.StatusFail {
			pending[fk] = ev.Detail
		}
	case event.StatusOK:
		if ev.Retry {
			if failedRaw, ok := pending[fk]; ok {
				recordRepair(pairCounts, key, failedRaw, ev.Detail)
				delete(pending, fk)
			}
		} else {
			delete(pending, fk)
		}
	}
}

// misfireKey mirrors burn.go's burnKey — shell events get an "sh:" prefix on
// their normalized command so a shell command never collides with a
// same-named tool in the misfire aggregation.
func misfireKey(ev *event.Event) string {
	if ev.Kind == event.KindShell {
		return "sh:" + ev.Action
	}
	return ev.Action
}

// recordRepair folds one observed (failed → fixed) instance into the
// aggregate count for its (key, failedRaw, fixedRaw) triple. Empty
// failedRaw/fixedRaw is a valid triple — the key-level fallback the doc
// comment on RepairPair describes.
func recordRepair(pairCounts map[string]*RepairPair, key, failedRaw, fixedRaw string) {
	sig := key + "\x00" + failedRaw + "\x00" + fixedRaw
	p := pairCounts[sig]
	if p == nil {
		p = &RepairPair{Key: key, FailedRaw: failedRaw, FixedRaw: fixedRaw}
		pairCounts[sig] = p
	}
	p.Count++
}

// rankMisfires projects the per-key aggregates into the sorted MisfireRow
// table: Score = Fails × FailSess descending, with deterministic tie-breaks
// so repeated runs are byte-stable.
func rankMisfires(aggs map[string]*misfireAgg) []MisfireRow {
	rows := make([]MisfireRow, 0, len(aggs))
	for key, a := range aggs {
		if a.fails == 0 {
			continue // no misfire signal — not a ranking candidate
		}
		row := MisfireRow{
			Key:      key,
			Fails:    a.fails,
			FailSess: len(a.failSess),
			Calls:    a.calls,
			FailRate: misfireRatio(a.fails, a.calls),
		}
		row.Score = float64(row.Fails) * float64(row.FailSess)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Fails != y.Fails {
			return x.Fails > y.Fails
		}
		if x.FailSess != y.FailSess {
			return x.FailSess > y.FailSess
		}
		return x.Key < y.Key
	})
	return rows
}

// rankSwallows projects the per-key aggregates into the sorted SwallowRow
// table, using the same Score = count × session-spread rank and the same
// deterministic tie-break ladder as rankMisfires so the two tables read alike.
func rankSwallows(aggs map[string]*misfireAgg) []SwallowRow {
	rows := make([]SwallowRow, 0, len(aggs))
	for key, a := range aggs {
		if a.swallows == 0 {
			continue // nothing hidden under this key
		}
		row := SwallowRow{
			Key:         key,
			Swallows:    a.swallows,
			SwallowSess: len(a.swallowSess),
			Calls:       a.calls,
			SwallowRate: misfireRatio(a.swallows, a.calls),
			Exemplar:    a.swallowFirst,
		}
		row.Score = float64(row.Swallows) * float64(row.SwallowSess)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Swallows != y.Swallows {
			return x.Swallows > y.Swallows
		}
		if x.SwallowSess != y.SwallowSess {
			return x.SwallowSess > y.SwallowSess
		}
		return x.Key < y.Key
	})
	return rows
}

// rankRepairs projects the pair aggregates into a Count-descending,
// key-stable list.
func rankRepairs(pairCounts map[string]*RepairPair) []RepairPair {
	pairs := make([]RepairPair, 0, len(pairCounts))
	for _, p := range pairCounts {
		pairs = append(pairs, *p)
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		x, y := pairs[i], pairs[j]
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		if x.Key != y.Key {
			return x.Key < y.Key
		}
		if x.FailedRaw != y.FailedRaw {
			return x.FailedRaw < y.FailedRaw
		}
		return x.FixedRaw < y.FixedRaw
	})
	return pairs
}

// misfireRatio is a small guarded divide (0/0 = 0) for the fail-rate field.
func misfireRatio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
