package mine

import (
	"sort"

	"github.com/dkoosis/ferret/internal/event"
)

// Polling detection (ferret-cax item 2) — the repeat axis ferret was blind to.
// burn.go ranks *normalized* commands corpus-wide (every `git status …` folds
// into one sh:git_status row) and ngram.go/prefixspan.go rank repeated
// *sequences*; neither answers "the same literal command ran 932 times inside
// one session." That shape — `loto inbox` ×932, `git status --short` ×733 over
// 14 days — is polling: a loop asking the same question over and over, which a
// watch, a hook, or a cached read replaces outright.
//
// The unit is therefore the raw command text within one session, not the
// normalized token: `git status --short` and `git status -sb` are the same
// shellnorm key but different polls, and only the raw form tells you which one
// to replace. The normalized Action key rides along on each row so a reader can
// join back to the burn table.

// PollingRow is one exact command's corpus-wide polling profile. Command is the
// dedup unit; Key is the shellnorm token it normalizes to, carried so a reader
// can join this table to the burn ranking.
type PollingRow struct {
	Command       string  `json:"command"`       // Event.Detail — the exact (truncated) raw command text
	Key           string  `json:"key"`           // Event.Action with burn.go's "sh:" prefix — the normalized join key
	TotalRepeats  int     `json:"totalRepeats"`  // summed per-session run counts, over sessions that polled (count ≥ 2)
	Sessions      int     `json:"sessions"`      // distinct sessions that polled this command — the "spread"
	MaxPerSession int     `json:"maxPerSession"` // worst single session's run count — the headline number
	Score         float64 `json:"score"`         // TotalRepeats × Sessions — the corpus-wide rank
}

// PollingReport is the ranked polling table plus the corpus denominator a
// reader needs to size it ("14 repeats across 3 of 400 sessions" reads very
// differently from "3 of 4").
type PollingReport struct {
	Rows     []PollingRow `json:"rows"`
	Sessions int          `json:"sessions"` // distinct sessions containing ≥1 shell event
}

// pollKey is the dup unit: one exact raw command inside one session. Two
// distinct long commands whose Detail truncates to the same prefix collide
// under this key — accepted for a ranking (they share a prefix, so they are
// near-certainly the same routine), and never load-bearing since the output is
// advisory, not an automated rewrite.
type pollKey struct {
	session string
	command string
}

// pollAgg accumulates one command's corpus-wide totals once the per-session
// counts are known.
type pollAgg struct {
	key           string // normalized Action key, first-seen wins (see minePollingCounts)
	totalRepeats  int
	sessions      int
	maxPerSession int
}

// pollingMin is the run count at which repetition becomes polling. Two is the
// floor the bead names: a single repeat is already a signal that the answer was
// re-asked, and the ranking (repeats × sessions) buries weak rows on its own —
// a higher threshold would only hide the long tail without changing the head.
const pollingMin = 2

// MinePolling walks events and ranks exact-duplicate shell commands repeated
// within a single session. Only shell events with raw command text (Detail)
// participate: tool events carry no invocation text in this build, so they
// would all collapse into one empty-string bucket.
//
// Two passes over the same walk: count (session, command) pairs, then roll the
// qualifying pairs up per command. Event order does not matter to the counts —
// only to which normalized Action key a command records, which uses first-seen
// order so repeated runs are byte-stable.
func MinePolling(events []event.Event) PollingReport {
	counts, keys, sessions := minePollingCounts(events)
	return PollingReport{
		Rows:     rankPolling(aggregatePolling(counts, keys)),
		Sessions: sessions,
	}
}

// minePollingCounts does the single walk: per-(session, command) run counts,
// the first-seen normalized key per command, and the distinct shell-session
// count for the report's denominator.
func minePollingCounts(events []event.Event) (map[pollKey]int, map[string]string, int) {
	counts := map[pollKey]int{}
	keys := map[string]string{}
	sessions := map[string]struct{}{}

	for i := range events {
		ev := &events[i]
		if ev.Kind != event.KindShell || ev.Detail == "" {
			continue
		}
		sessions[ev.Session] = struct{}{}
		counts[pollKey{session: ev.Session, command: ev.Detail}]++
		if _, seen := keys[ev.Detail]; !seen {
			keys[ev.Detail] = pollingKey(ev)
		}
	}
	return counts, keys, len(sessions)
}

// aggregatePolling rolls the per-session counts up per command, keeping only
// sessions that actually polled (count ≥ pollingMin). A command run once in
// each of fifty sessions is normal use, not polling, and contributes nothing.
func aggregatePolling(counts map[pollKey]int, keys map[string]string) map[string]*pollAgg {
	aggs := map[string]*pollAgg{}
	for pk, n := range counts {
		if n < pollingMin {
			continue
		}
		a := aggs[pk.command]
		if a == nil {
			a = &pollAgg{key: keys[pk.command]}
			aggs[pk.command] = a
		}
		a.totalRepeats += n
		a.sessions++
		if n > a.maxPerSession {
			a.maxPerSession = n
		}
	}
	return aggs
}

// pollingKey mirrors burn.go's burnKey so the Key column joins straight to the
// burn table. Only shell events reach here, but the shape is kept identical to
// its sibling on purpose — one convention for "the normalized command key".
func pollingKey(ev *event.Event) string {
	if ev.Kind == event.KindShell {
		return "sh:" + ev.Action
	}
	return ev.Action
}

// rankPolling projects the per-command aggregates into the sorted table:
// Score = TotalRepeats × Sessions descending (mirroring rankMisfires' fails ×
// sessions shape — volume alone would let one runaway session outrank a habit
// that repeats everywhere), with deterministic tie-breaks so repeated runs are
// byte-stable.
func rankPolling(aggs map[string]*pollAgg) []PollingRow {
	rows := make([]PollingRow, 0, len(aggs))
	for command, a := range aggs {
		row := PollingRow{
			Command:       command,
			Key:           a.key,
			TotalRepeats:  a.totalRepeats,
			Sessions:      a.sessions,
			MaxPerSession: a.maxPerSession,
		}
		row.Score = float64(row.TotalRepeats) * float64(row.Sessions)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.TotalRepeats != y.TotalRepeats {
			return x.TotalRepeats > y.TotalRepeats
		}
		if x.MaxPerSession != y.MaxPerSession {
			return x.MaxPerSession > y.MaxPerSession
		}
		return x.Command < y.Command
	})
	return rows
}
