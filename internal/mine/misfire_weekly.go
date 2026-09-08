package mine

import (
	"fmt"
	"sort"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// ISO-week bucketing (ferret-cf9) — the missing time axis behind
// MineReasons: a reason count alone cannot tell "still burning" from
// "already fixed". SendMessage's 340 JSON-parse failures looked like the top
// misfire in the corpus; their weekly shape was 168/129/43/0 across August —
// the fix had already landed and the total was a fossil. isoWeek/weekRange
// below are the one reusable bucketing primitive (no such helper existed
// anywhere in the repo before this bead); MineReasonsByWeek is the
// reasons-specific caller. A second temporal --by reuses the primitives, not
// this function.

// isoWeek returns the ISO 8601 week label for t (e.g. "2026-W31").
func isoWeek(t time.Time) string {
	year, week := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// mondayOf returns the UTC midnight of the Monday starting t's ISO week —
// the per-week anchor weekRange steps by.
func mondayOf(t time.Time) time.Time {
	t = t.UTC()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // ISO: Monday=1 ... Sunday=7
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(wd - 1))
}

// weekRange returns every ISO week label spanned by times, earliest to
// latest inclusive, chronological order, with no gaps — the explicit-zero
// backbone a histogram is filled against so a decayed reason reads as a run
// to zero rather than a gap. Zero times are ignored (Event.Time is
// advisory; some event kinds carry none); returns nil when no time is set.
func weekRange(times []time.Time) []string {
	var earliest, latest time.Time
	for _, t := range times {
		if t.IsZero() {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
		if latest.IsZero() || t.After(latest) {
			latest = t
		}
	}
	if earliest.IsZero() {
		return nil
	}
	var out []string
	end := mondayOf(latest)
	for wk := mondayOf(earliest); !wk.After(end); wk = wk.AddDate(0, 0, 7) {
		out = append(out, isoWeek(wk))
	}
	return out
}

// WeekBucket is one ISO week's count for a reason under a key. Count is
// explicit — a week with no occurrences prints 0, not a gap. Uncaptured
// marks a week whose failed events for this key carry no captured Err text
// at all (every failure that week predates error capture): the bead's Rules
// require that read as "uncaptured", never as a genuine zero, because the
// true count for that week is unknowable, not zero.
type WeekBucket struct {
	Week       string `json:"week"`
	Count      int    `json:"count"`
	Uncaptured bool   `json:"uncaptured,omitempty"`
}

// ReasonWeekly is one reason's per-week histogram under a key, ordered by
// the shared week axis (WeeklyReasonsReport.Weeks); Total is the reason's
// own sum across weeks, the same figure ReasonRow.Count reports flat.
type ReasonWeekly struct {
	Reason string       `json:"reason"`
	Total  int          `json:"total"`
	Weeks  []WeekBucket `json:"weeks"`
}

// KeyReasonsWeekly is one misfire key's reasons, each with its own weekly
// histogram, sharing one ISO-week axis.
type KeyReasonsWeekly struct {
	Key     string         `json:"key"`
	Reasons []ReasonWeekly `json:"reasons"`
}

// WeeklyReasonsReport is the --reasons --by week bundle: the shared week
// axis, each key's per-reason histograms, and the same corpus coverage
// counters MineReasons reports (Failed/Uncaptured) — so the coverage note
// reads the same way regardless of --by.
type WeeklyReasonsReport struct {
	Weeks      []string           `json:"weeks"` // ISO week labels, chronological, shared axis
	Keys       []KeyReasonsWeekly `json:"keys"`
	Failed     int                `json:"failed"`
	Uncaptured int                `json:"uncaptured"`
}

// weekAgg accumulates one (key, week)'s failure/uncaptured totals — the
// per-week visibility a bucket needs to tell a genuine zero from a
// pre-capture week it cannot see into.
type weekAgg struct {
	failed     int
	uncaptured int
}

// MineReasonsByWeek is MineReasons' temporal sibling: the same key→reason
// grouping over event.Err, additionally bucketed into ISO weeks derived from
// Event.Time. Events with a zero Time (advisory field; rare) are excluded
// from the weekly buckets and the shared week axis, since there is nothing
// to bucket them by — they are still absent from Failed/Uncaptured here,
// mirroring the fact this function only ever sees fail|cfail tool/shell
// events reasonCandidate already gates.
func MineReasonsByWeek(events []event.Event, keyFilter string) WeeklyReasonsReport {
	reasonCounts := map[string]map[string]map[string]int{} // key -> reason -> week -> count
	weekTotals := map[string]map[string]*weekAgg{}         // key -> week -> agg
	var times []time.Time
	failed, uncaptured := 0, 0

	for i := range events {
		ev := &events[i]
		key, ok := reasonCandidate(ev, keyFilter)
		if !ok || ev.Time.IsZero() {
			continue
		}
		failed++
		times = append(times, ev.Time)
		wk := isoWeek(ev.Time)

		agg := weekAggFor(weekTotals, key, wk)
		agg.failed++

		if ev.Err == "" {
			uncaptured++
			agg.uncaptured++
			continue
		}
		reasonCountsFor(reasonCounts, key, reasonFor(ev.Err))[wk]++
	}

	weeks := weekRange(times)
	return WeeklyReasonsReport{
		Weeks:      weeks,
		Keys:       buildKeyReasonsWeekly(reasonCounts, weekTotals, weeks),
		Failed:     failed,
		Uncaptured: uncaptured,
	}
}

// weekAggFor returns key/week's aggregate, creating the intermediate maps
// and the aggregate itself on first use.
func weekAggFor(weekTotals map[string]map[string]*weekAgg, key, week string) *weekAgg {
	wt := weekTotals[key]
	if wt == nil {
		wt = map[string]*weekAgg{}
		weekTotals[key] = wt
	}
	agg := wt[week]
	if agg == nil {
		agg = &weekAgg{}
		wt[week] = agg
	}
	return agg
}

// reasonCountsFor returns key/reason's week->count map, creating the
// intermediate maps on first use.
func reasonCountsFor(reasonCounts map[string]map[string]map[string]int, key, reason string) map[string]int {
	rc := reasonCounts[key]
	if rc == nil {
		rc = map[string]map[string]int{}
		reasonCounts[key] = rc
	}
	wc := rc[reason]
	if wc == nil {
		wc = map[string]int{}
		rc[reason] = wc
	}
	return wc
}

// buildKeyReasonsWeekly projects the accumulated counts into key-ascending
// KeyReasonsWeekly rows. Only keys with at least one captured reason appear
// — mirroring sortedKeyReasons/MineReasons: a key whose failures are all
// uncaptured contributes to the top-level Failed/Uncaptured counters but has
// no reason to rank, flat or by week.
func buildKeyReasonsWeekly(reasonCounts map[string]map[string]map[string]int, weekTotals map[string]map[string]*weekAgg, weeks []string) []KeyReasonsWeekly {
	keys := make([]string, 0, len(reasonCounts))
	for k := range reasonCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]KeyReasonsWeekly, 0, len(keys))
	for _, k := range keys {
		out = append(out, KeyReasonsWeekly{Key: k, Reasons: buildReasonWeekly(reasonCounts[k], weekTotals[k], weeks)})
	}
	return out
}

// buildReasonWeekly ranks one key's reasons by rankReasons (the same
// Count-descending, reason-stable order the flat --reasons report uses) and
// fills each reason's per-week buckets against the shared week axis,
// applying the uncaptured marker from that key's week totals.
func buildReasonWeekly(reasonWeeks map[string]map[string]int, weekTotals map[string]*weekAgg, weeks []string) []ReasonWeekly {
	totals := make(map[string]int, len(reasonWeeks))
	for reason, wc := range reasonWeeks {
		sum := 0
		for _, c := range wc {
			sum += c
		}
		totals[reason] = sum
	}
	ranked := rankReasons(totals)

	out := make([]ReasonWeekly, 0, len(ranked))
	for _, rr := range ranked {
		wc := reasonWeeks[rr.Reason]
		buckets := make([]WeekBucket, 0, len(weeks))
		for _, wk := range weeks {
			buckets = append(buckets, weekBucketFor(wk, wc, weekTotals[wk]))
		}
		out = append(out, ReasonWeekly{Reason: rr.Reason, Total: rr.Count, Weeks: buckets})
	}
	return out
}

// weekBucketFor builds one reason's bucket for week wk: uncaptured when that
// key's week had failures but none of them carried captured error text
// (nothing to attribute this reason's count to, positive or zero), the
// exact count otherwise — including a genuine 0 when the week's captured
// failures simply didn't include this reason.
func weekBucketFor(wk string, reasonWeek map[string]int, agg *weekAgg) WeekBucket {
	b := WeekBucket{Week: wk}
	if agg != nil && agg.failed > 0 && agg.uncaptured == agg.failed {
		b.Uncaptured = true
		return b
	}
	b.Count = reasonWeek[wk]
	return b
}
