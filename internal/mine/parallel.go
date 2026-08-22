package mine

import (
	"slices"
	"sort"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// DefaultIdleGap is how long a session (or an agent within one) may go without
// an event before it stops counting as running. A transcript's first and last
// timestamps are NOT its running window: a session left open overnight would
// otherwise overlap every other session that touched that night and inflate
// every concurrency number. Five minutes is the smallest gap that survives a
// slow model call plus a human's short pause; it is a flag, not a constant, so
// the ranking can be re-measured against a different assumption.
const DefaultIdleGap = 5 * time.Minute

// ParallelOptions tunes the sweep. Zero value means DefaultIdleGap.
type ParallelOptions struct {
	IdleGap time.Duration
}

// LevelRow is one concurrency level's share of the corpus: how long that many
// threads ran at once, and how many context bytes were spent while they did.
//
// BytesPctAtOrAbove is the cumulative column and the one that answers the
// question this analysis exists for — "63% of usage happened while 4+ ran in
// parallel" is a single cell (N=4) of it.
type LevelRow struct {
	N                 int     `json:"n"`
	Hours             float64 `json:"hours"`
	HoursPct          float64 `json:"hoursPct"`
	Events            int     `json:"events"`
	Bytes             int64   `json:"bytes"`
	BytesPct          float64 `json:"bytesPct"`
	BytesPctAtOrAbove float64 `json:"bytesPctAtOrAbove"`
}

// ConcurrencyProfile is one kind of parallelism, measured two ways: by time
// (how long N ran at once) and by cost (how many bytes were spent while N ran).
// They disagree whenever the parallel stretches are the expensive ones, which
// is the whole finding — a small share of hours can carry most of the spend.
type ConcurrencyProfile struct {
	Max              int        `json:"max"`
	TimeWeightedMean float64    `json:"timeWeightedMean"`
	ByteWeightedMean float64    `json:"byteWeightedMean"`
	Levels           []LevelRow `json:"levels"`
}

// ParallelResult is the corpus-wide parallelism tax, split the two ways dk
// required (ferret-04d, 2026-08-14): the split is the deliverable, because a
// single "4+ concurrent" number cannot say whether the cost belongs to running
// four windows or to one session fanning out four subagents, and those have
// opposite remedies.
//
//   - Window concurrency: distinct session ids overlapping in wall-clock.
//   - Fan-out concurrency: distinct agent ids inside ONE session.
//
// The discriminator matters: agent_id is the ONLY parent-vs-subagent tell —
// session_id and transcript_path are SHARED between a parent and its subagents
// (boot.md live trap), so keying fan-out on anything else silently measures
// nothing.
type ParallelResult struct {
	Sessions    int     `json:"sessions"`
	Events      int     `json:"events"`  // timed events folded into the sweep
	Untimed     int     `json:"untimed"` // events with no timestamp, excluded
	Bytes       int64   `json:"bytes"`
	SpanHours   float64 `json:"spanHours"`   // first to last event in the corpus
	ActiveHours float64 `json:"activeHours"` // hours with at least one session running
	IdleGapMin  float64 `json:"idleGapMinutes"`

	Window ConcurrencyProfile `json:"window"`
	Fanout ConcurrencyProfile `json:"fanout"`
}

// parallelRec is one timed event reduced to the three fields the sweep needs.
// Held in memory (a corpus of ~150k events costs a few MB) so the artifact is
// read once: both profiles need a per-event concurrency lookup that cannot be
// computed until every session's blocks are known.
type parallelRec struct {
	t     time.Time
	agent string // "" = main thread
	bytes int
}

// block is one uninterrupted stretch of activity for one thread — a session for
// the window profile, a (session, agent) pair for fan-out. Inclusive at both
// ends: a single-event thread is a zero-length block that still owns its
// instant, so its bytes are attributed and its duration is honestly zero.
type block struct{ start, end time.Time }

// Parallel measures the two concurrency axes across the ingested corpus.
//
// Both profiles come from the same sweep: activity blocks per thread, a
// change-point timeline over them, then every event's bytes charged to the
// concurrency level in force at its timestamp. Nothing is modeled or fitted —
// the inputs are timestamps and measured context bytes.
//
// Untimed events are excluded rather than guessed at (Event.Time is advisory;
// some event types carry none) and counted in Untimed so the exclusion is
// visible instead of quietly shrinking the denominator.
func Parallel(eventsPath string, opts ParallelOptions) (*ParallelResult, error) {
	gap := opts.IdleGap
	if gap <= 0 {
		gap = DefaultIdleGap
	}

	bySession := map[string][]parallelRec{}
	res := &ParallelResult{IdleGapMin: gap.Minutes()}
	err := event.Read(eventsPath, func(ev *event.Event) error {
		if ev.Time.IsZero() {
			res.Untimed++
			return nil
		}
		res.Events++
		res.Bytes += int64(ev.Bytes)
		bySession[ev.Session] = append(bySession[ev.Session], parallelRec{t: ev.Time, agent: ev.Agent, bytes: ev.Bytes})
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Sessions = len(bySession)
	if res.Events == 0 {
		return res, nil
	}

	for _, recs := range bySession {
		sort.Slice(recs, func(i, j int) bool { return recs[i].t.Before(recs[j].t) })
	}
	res.Window, res.SpanHours, res.ActiveHours = windowProfile(bySession, gap, res.Bytes)
	res.Fanout = fanoutProfile(bySession, gap, res.Bytes)
	return res, nil
}

// windowProfile measures wall-clock overlap between distinct sessions, and
// returns the corpus span and active hours alongside it since both fall out of
// the same timeline.
func windowProfile(bySession map[string][]parallelRec, gap time.Duration, totalBytes int64) (ConcurrencyProfile, float64, float64) {
	all := make([]block, 0, len(bySession))
	var first, last time.Time
	for _, recs := range bySession {
		all = append(all, blocksOf(recs, gap)...)
		if first.IsZero() || recs[0].t.Before(first) {
			first = recs[0].t
		}
		if end := recs[len(recs)-1].t; end.After(last) {
			last = end
		}
	}
	tl := newTimeline(all)

	acc := newLevelAcc()
	acc.addHours(tl.hoursByLevel())
	for _, recs := range bySession {
		for i := range recs {
			acc.addEvent(tl.levelAt(recs[i].t), recs[i].bytes)
		}
	}
	return acc.profile(totalBytes), last.Sub(first).Hours(), acc.totalHours()
}

// fanoutProfile measures overlap between agents INSIDE each session, keyed on
// agent id — the only parent-vs-subagent discriminator. Hours sum across
// sessions (they are session-hours, not wall-clock hours), which is why a
// fan-out hour total can exceed the corpus span.
func fanoutProfile(bySession map[string][]parallelRec, gap time.Duration, totalBytes int64) ConcurrencyProfile {
	acc := newLevelAcc()
	for _, recs := range bySession {
		tl := newTimeline(agentBlocks(recs, gap))
		acc.addHours(tl.hoursByLevel())
		for i := range recs {
			acc.addEvent(tl.levelAt(recs[i].t), recs[i].bytes)
		}
	}
	return acc.profile(totalBytes)
}

// agentBlocks splits one session's records by agent id and gap-splits each,
// so a subagent that ran for two minutes owns two minutes of the session, not
// the session's whole span.
func agentBlocks(recs []parallelRec, gap time.Duration) []block {
	byAgent := map[string][]parallelRec{}
	for i := range recs {
		byAgent[recs[i].agent] = append(byAgent[recs[i].agent], recs[i])
	}
	out := make([]block, 0, len(byAgent))
	for _, ar := range byAgent {
		out = append(out, blocksOf(ar, gap)...) // already time-sorted: recs was sorted before the split
	}
	return out
}

// blocksOf turns one thread's time-sorted records into activity blocks, cutting
// wherever it went quiet for longer than gap. Without the cut, "running" would
// mean "open", and an idle window would look like a working one.
func blocksOf(recs []parallelRec, gap time.Duration) []block {
	if len(recs) == 0 {
		return nil
	}
	cur := block{start: recs[0].t, end: recs[0].t}
	var out []block
	for i := 1; i < len(recs); i++ {
		if recs[i].t.Sub(cur.end) > gap {
			out = append(out, cur)
			cur = block{start: recs[i].t, end: recs[i].t}
			continue
		}
		cur.end = recs[i].t
	}
	return append(out, cur)
}

// timeline is a set of blocks prepared for two questions: how long N ran at
// once, and what N was at one instant.
//
// The instant lookup counts opens and closes directly rather than searching the
// duration segments, because a zero-length block (a one-event thread) produces
// no segment at all yet still owns its instant — searching segments would
// report level 0 at a timestamp where a thread demonstrably ran.
type timeline struct {
	opens  []time.Time // sorted block starts
	closes []time.Time // sorted block ends
	blocks []block
}

func newTimeline(blocks []block) *timeline {
	tl := &timeline{blocks: blocks, opens: make([]time.Time, 0, len(blocks)), closes: make([]time.Time, 0, len(blocks))}
	for _, b := range blocks {
		tl.opens = append(tl.opens, b.start)
		tl.closes = append(tl.closes, b.end)
	}
	sort.Slice(tl.opens, func(i, j int) bool { return tl.opens[i].Before(tl.opens[j]) })
	sort.Slice(tl.closes, func(i, j int) bool { return tl.closes[i].Before(tl.closes[j]) })
	return tl
}

// levelAt is how many blocks cover t, with both ends inclusive: started at or
// before t, minus ended strictly before t.
func (tl *timeline) levelAt(t time.Time) int {
	started := sort.Search(len(tl.opens), func(i int) bool { return tl.opens[i].After(t) })
	ended := sort.Search(len(tl.closes), func(i int) bool { return !tl.closes[i].Before(t) })
	return started - ended
}

// hoursByLevel sweeps the change points and returns how many hours ran at each
// level ≥ 1. Zero-length blocks contribute no time, only an instant.
func (tl *timeline) hoursByLevel() map[int]float64 {
	type point struct {
		t     time.Time
		delta int
	}
	pts := make([]point, 0, 2*len(tl.blocks))
	for _, b := range tl.blocks {
		pts = append(pts, point{b.start, +1}, point{b.end, -1})
	}
	sort.Slice(pts, func(i, j int) bool {
		if !pts[i].t.Equal(pts[j].t) {
			return pts[i].t.Before(pts[j].t)
		}
		return pts[i].delta > pts[j].delta // open before close at the same instant
	})

	hours := map[int]float64{}
	level := 0
	var cur time.Time
	for i, p := range pts {
		if i > 0 && level > 0 && p.t.After(cur) {
			hours[level] += p.t.Sub(cur).Hours()
		}
		level += p.delta
		cur = p.t
	}
	return hours
}

// levelAcc accumulates hours, events and bytes per concurrency level, then
// renders the ranked profile.
type levelAcc struct {
	hours  map[int]float64
	bytes  map[int]int64
	events map[int]int
}

func newLevelAcc() *levelAcc {
	return &levelAcc{hours: map[int]float64{}, bytes: map[int]int64{}, events: map[int]int{}}
}

func (a *levelAcc) addHours(h map[int]float64) {
	for lvl, v := range h {
		a.hours[lvl] += v
	}
}

func (a *levelAcc) addEvent(level, bytes int) {
	if level < 1 {
		level = 1 // an event proves its own thread ran; guards a clock-skew edge
	}
	a.events[level]++
	a.bytes[level] += int64(bytes)
}

func (a *levelAcc) totalHours() float64 {
	var sum float64
	for _, v := range a.hours {
		sum += v
	}
	return sum
}

// profile renders the accumulated levels ascending, filling both percentage
// columns and the cumulative at-or-above column that answers "what share of the
// spend happened while N or more ran at once".
func (a *levelAcc) profile(totalBytes int64) ConcurrencyProfile {
	levels := make([]int, 0, len(a.hours)+len(a.bytes))
	seen := map[int]bool{}
	for lvl := range a.hours {
		levels, seen[lvl] = append(levels, lvl), true
	}
	for lvl := range a.bytes {
		if !seen[lvl] {
			levels = append(levels, lvl)
		}
	}
	sort.Ints(levels)

	totalHours := a.totalHours()
	var p ConcurrencyProfile
	var byteWeighted float64
	for _, lvl := range levels {
		row := LevelRow{N: lvl, Hours: a.hours[lvl], Events: a.events[lvl], Bytes: a.bytes[lvl]}
		row.HoursPct = pct(row.Hours, totalHours)
		row.BytesPct = pct(float64(row.Bytes), float64(totalBytes))
		p.TimeWeightedMean += float64(lvl) * row.Hours
		byteWeighted += float64(lvl) * float64(row.Bytes)
		p.Levels = append(p.Levels, row)
		if lvl > p.Max {
			p.Max = lvl
		}
	}
	if totalHours > 0 {
		p.TimeWeightedMean /= totalHours
	}
	if totalBytes > 0 {
		p.ByteWeightedMean = byteWeighted / float64(totalBytes)
	}
	// Cumulative from the tail: share of bytes spent at this level or higher.
	var running float64
	for i, row := range slices.Backward(p.Levels) {
		running += row.BytesPct
		p.Levels[i].BytesPctAtOrAbove = running
	}
	return p
}

func pct(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * part / whole
}
