package mine

import (
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// overcountFactor is the disclosed threshold ferret-wmb's AC calls for: an
// at:* row whose serialized record bytes exceed its content-bearing bytes by
// more than this factor is marked Overcount. Chosen from the two measured
// extremes in the bead's Evidence section: at:hook_success runs ~12.2x
// record/content-only (88.3MB vs 7.24MB corpus-wide), at:skill_listing checks
// out at ~1.2x (21.0KB vs 17,430B, genuinely mostly content). 2x sits cleanly
// between them — the row that measurably IS mostly overhead stays flagged,
// the row that measurably IS mostly content does not.
const overcountFactor = 2.0

// BurnRow is one normalized command's corpus-wide burn: how much measured
// context its calls cost, aggregated across every session that called it.
//
// The quantity is CONTEXT BYTES — what enters the model's request body
// (tool_use input + tool_result content) — because that is what costs money,
// fills the window, and can be reduced and re-measured. Screen rendering
// (chrome, folds, wrapping, terminal width) is a display artifact with no
// context cost and is out of scope (ferret-noj, dk 2026-08-14).
type BurnRow struct {
	Key          string  `json:"key"`   // shellnorm/tool key: "sh:git_commit", "Read", "at:skill_listing", ...
	Bytes        int     `json:"bytes"` // summed per-event measured context bytes (InBytes+OutBytes) — the ranking key
	Calls        int     `json:"calls"`
	BytesPerCall float64 `json:"bytesPerCall"` // the per-call toll a "should I stop running this?" decision turns on
	Sessions     int     `json:"sessions"`
	// ContentBytes is the row's summed content-bearing bytes (event.Event.
	// ContentBytes, KindAttach only) — zero and not meaningful for a tool/shell
	// row, which carries no comparable content-only figure. It exists to
	// DISCLOSE how much of Bytes a model actually sees; Bytes stays the whole
	// serialized record and stays the ranking key (ferret-wmb).
	ContentBytes int `json:"contentBytes,omitempty"`
	// Overcount marks an at:* row whose Bytes exceed ContentBytes by more than
	// overcountFactor — the record carries far more than the content a model
	// actually sees (routing metadata, unsurfaced stdout, ...). Never set for a
	// tool/shell row: those have no comparable content-only figure to disclose
	// against. This is disclosure, not re-accounting: Bytes is unchanged either
	// way (ferret-wmb Rules — the fix is not a per-class content allowlist).
	Overcount bool `json:"overcount,omitempty"`

	sessions map[string]struct{} // distinct-session accumulator; collapsed into Sessions at result time
}

// BurnResult is the corpus-wide ranked burn table plus the totals it was
// computed over.
type BurnResult struct {
	Rows     []BurnRow `json:"rows"`
	Events   int       `json:"events"`   // tool/shell/attach events folded (prompts excluded — no Action to key on)
	Sessions int       `json:"sessions"` // distinct sessions in the corpus
}

// Burn streams the ingested events artifact once, groups tool/shell/attachment
// events by their normalized key, and ranks the groups by measured context
// bytes — the "what's eating my context" tune-up list (ferret-nrr, ferret-cax,
// ferret-rfc, ferret-noj).
//
// Burn ranks CONTEXT BYTES, and nothing else. That is a correction to two
// earlier models, both retired here:
//
//   - Ranking by returned bytes alone missed that a cheap-output command called
//     733 times still pays 733 times. It doesn't: the per-call sum IS the byte
//     sum, so counting bytes already prices repetition correctly.
//   - The ferret-cax render model added a per-call chrome constant (shell 6
//     lines × 80B, tool 1 line × 80B) and clipped each call's output at a
//     2048-byte "preview cap". Both terms are deleted. Chrome is terminal
//     framing that never enters the request — it fabricated 56.8 MB, 35% of
//     the model's own total. The preview cap discarded 53% of real bytes to
//     model a fold that costs the model nothing; worse, the harness has
//     ALREADY truncated tool_result before it reaches the transcript (Bash
//     caps at 30,000 chars: max observed 29,723 over 60,550 calls, zero at or
//     above the cap), so a modeled cap charges truncation twice. Read is not
//     capped in practice at all — max 89,352 B / 1,672 lines over 13,236
//     calls — which is why no single global cap was ever right.
//
// Measured over 3,923 transcripts / 1.4 GB / ~148,706 tool+shell events. The
// old model's ranking correlated 0.951 with a plain call count and only 0.864
// with real context bytes; its top-20 overlap with the measured ranking was
// 14/20, mis-ranking mcp__claude-in-chrome__computer from #2 measured to #44
// modeled (19.3 MB over 505 calls).
//
// There are no calibrated constants left in this file. The cost of an event is
// the bytes it put in the request, which internal/event/build.go already
// accounts in Event.Bytes — the input/output split (Event.InBytes/OutBytes,
// ferret-e4g) lives one level down; this table still ranks on the sum.
//
// The key is the event's Action field, which ingestion (internal/event/build.go
// fromToolUse) already normalizes: a shell segment carries its shellnorm.Split
// command (git_commit, bd_show, ...), a tool call carries its tool name, an
// attachment carries its class. This mirrors Summarize's addAction in stats.go,
// including its "sh:" prefix so a shell command never collides with a
// same-named tool.
//
// Rows sort by Bytes descending, then Key ascending — a two-deep tie-break
// so repeated runs are byte-stable even when two keys cost the same.
func Burn(eventsPath string) (*BurnResult, error) {
	rows := map[string]*BurnRow{}
	sessions := map[string]struct{}{}
	events := 0

	err := event.Read(eventsPath, func(ev *event.Event) error {
		if ev.Kind == event.KindPrompt {
			return nil // no Action to key on
		}
		events++
		sessions[ev.Session] = struct{}{}

		key := burnKey(ev)
		r, ok := rows[key]
		if !ok {
			r = &BurnRow{Key: key, sessions: map[string]struct{}{}}
			rows[key] = r
		}
		r.Calls++
		r.Bytes += ev.Bytes
		r.ContentBytes += ev.ContentBytes
		r.sessions[ev.Session] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}

	res := &BurnResult{Events: events, Sessions: len(sessions)}
	res.Rows = make([]BurnRow, 0, len(rows))
	for _, r := range rows {
		r.Sessions = len(r.sessions)
		if r.Calls > 0 {
			r.BytesPerCall = float64(r.Bytes) / float64(r.Calls)
		}
		r.Overcount = isOvercounted(r.Key, r.Bytes, r.ContentBytes)
		res.Rows = append(res.Rows, *r)
	}
	sort.Slice(res.Rows, func(i, j int) bool { return burnLess(&res.Rows[i], &res.Rows[j]) })
	return res, nil
}

// burnLess is the row ordering: measured context bytes descending, then key
// ascending. Extracted from the sort.Slice closure so the tie-break reads as a
// ladder and stays under the complexity ceiling as more columns land.
//
// Pointer receivers, not values: BurnRow carries a map field, and a
// value-range/value-param over it trips rangeValCopy on a hot struct.
func burnLess(a, b *BurnRow) bool {
	if a.Bytes != b.Bytes {
		return a.Bytes > b.Bytes
	}
	return a.Key < b.Key
}

// burnKey mirrors stats.go's addAction: shell events get an "sh:" prefix on
// their normalized command so a shell command (e.g. "sh:make") never
// collides with a same-named tool key. Attachment classes take "at:" for the
// same reason (ferret-rfc) — and because the prefix is what makes a row's
// nature legible in the ranking: "at:skill_listing" is a config you change
// once, where "Read" is a call you make less often.
func burnKey(ev *event.Event) string {
	switch ev.Kind {
	case event.KindShell:
		return "sh:" + ev.Action
	case event.KindAttach:
		return "at:" + ev.Action
	}
	return ev.Action
}

// isOvercounted decides whether a row's disclosed content trails its
// record bytes by more than overcountFactor. Scoped to "at:" keys only — a
// tool/shell row has no comparable content-only figure, so it can never be
// marked no matter how its (always-zero) ContentBytes compares to Bytes.
//
// contentBytes == 0 with bytes > 0 is the maximal case (ferret-wmb AC): a
// record that discloses no content at all is, by definition, more than
// overcountFactor times its content — there is no finite ratio to compute, so
// it is marked directly rather than dividing by zero.
func isOvercounted(key string, bytes, contentBytes int) bool {
	if !strings.HasPrefix(key, "at:") || bytes == 0 {
		return false
	}
	if contentBytes == 0 {
		return true
	}
	return float64(bytes) > overcountFactor*float64(contentBytes)
}
