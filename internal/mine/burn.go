package mine

import (
	"sort"

	"github.com/dkoosis/ferret/internal/event"
)

// Render-cost model constants (ferret-cax, grounded in ccp-3s1c).
//
// These are a MODEL, not a measurement. The ccp-3s1c finding (14 days, 656
// sessions) measured the thing they are calibrated against: returned output
// bytes are not where context goes — 36MB total, mean 0.7KB/call — while the
// per-call chrome the transcript renders around every call is paid on every
// single call, cheap output or not. A Bash call renders roughly six or seven
// lines (the echoed command, an output preview, the permission-classifier
// line); a native tool call (Read/Grep/Edit) renders ONE collapsed line. What
// was measured is the disparity; the numbers below are our approximation of
// it, in rendered bytes, so the chrome term composes with event.Bytes instead
// of living in an incomparable unit.
//
// Treat them as a dial, not a constant of nature: if the transcript renderer
// changes its framing, these move.
const (
	// renderedLineBytes approximates one rendered line's cost in bytes — a
	// terminal-ish width, the unit that turns "lines of chrome" into something
	// summable with event.Bytes.
	renderedLineBytes = 80

	// shellChromeLines is the per-call line count a shell (Bash) call renders
	// before any output: echoed command text, the preview frame, and the
	// permission-classifier line. ~6-7 lines observed; 6 is the conservative
	// pick — this model should not out-shout the byte term by rounding up.
	shellChromeLines = 6

	// toolChromeLines is the per-call line count a native tool call renders.
	// Read/Grep/Edit collapse to a single line in the transcript.
	toolChromeLines = 1

	// previewCapBytes bounds how much of a call's returned output the reader
	// actually pays for. Output past the preview is collapsed behind a fold,
	// so raw event.Bytes overstates rendered cost for big outputs: a 200KB
	// Read and a 3KB Read cost the reader about the same rendered space.
	previewCapBytes = 2048
)

// BurnRow is one normalized command's corpus-wide burn: how much measured
// context its calls cost, aggregated across every session that called it.
//
// Two costs live here, deliberately side by side. OutBytes is what the call
// RETURNED; RenderCost is what the reader PAID to have it in the transcript.
// They disagree, and the disagreement is the point — see Burn's doc comment.
type BurnRow struct {
	Key          string  `json:"key"`      // shellnorm/tool key: "sh:git_commit", "Read", "mcp__trixi__get_nug", ...
	OutBytes     int     `json:"outBytes"` // summed per-event measured bytes
	Calls        int     `json:"calls"`
	BytesPerCall float64 `json:"bytesPerCall"`
	Sessions     int     `json:"sessions"`

	// RenderCost is the modeled rendered-byte cost of this key's calls: a
	// per-call chrome constant by kind plus the preview-capped share of each
	// call's returned bytes. The default ranking key.
	RenderCost int `json:"renderCost"`
	// RenderPerCall is RenderCost / Calls — the per-call toll, which is what a
	// "should I stop running this?" decision actually turns on.
	RenderPerCall float64 `json:"renderPerCall"`

	sessions map[string]struct{} // distinct-session accumulator; collapsed into Sessions at result time
}

// BurnResult is the corpus-wide ranked burn table plus the totals it was
// computed over.
type BurnResult struct {
	Rows     []BurnRow `json:"rows"`
	Events   int       `json:"events"`   // tool/shell events folded (prompts excluded — no Action to key on)
	Sessions int       `json:"sessions"` // distinct sessions in the corpus
}

// Burn streams the ingested events artifact once, groups tool/shell events by
// their normalized command key, and ranks the groups by modeled render cost —
// the "what's eating my context" tune-up list (ferret-nrr, ferret-cax).
//
// Burn should rank what the reader pays, and the reader pays per call
// rendered, not per byte returned.
//
// That is a correction, not a preference. The original ranking was total
// OutBytes, which the ccp-3s1c measurement showed is the wrong quantity for
// this whole class of waste: a cheap-output, high-count command sinks to the
// bottom of a byte ranking while being a top burner in rendered lines
// (`git status --short`, 733 calls over the measured fortnight, ranks near
// zero on bytes). Rows now sort by RenderCost — chrome per call plus the
// preview-capped byte share — and the byte columns stay visible beside it so
// both orderings stay readable in one table.
//
// The key is the event's Action field, which ingestion (internal/event/build.go
// fromToolUse) already normalizes: a shell segment carries its shellnorm.Split
// command (git_commit, bd_show, ...), a tool call carries its tool name. This
// mirrors Summarize's addAction in stats.go, including its "sh:" prefix on
// shell keys so a shell command never collides with a same-named tool.
//
// The byte cost per row is the sum of each event's Bytes field, which
// internal/event/build.go accounts as tool_use input + the event's share of
// its tool_result content — output-dominated for the read/list/search-heavy
// commands this ranking exists to surface (event.go's Bytes doc comment).
// Plain aggregation over an existing field — no algorithm to cite.
//
// Rows sort by total RenderCost descending, then OutBytes descending, then
// Key ascending — a two-deep tie-break so repeated runs are byte-stable even
// when two keys model to the same rendered cost.
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
		r.OutBytes += ev.Bytes
		r.RenderCost += renderCost(ev)
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
			r.BytesPerCall = float64(r.OutBytes) / float64(r.Calls)
			r.RenderPerCall = float64(r.RenderCost) / float64(r.Calls)
		}
		res.Rows = append(res.Rows, *r)
	}
	sort.Slice(res.Rows, func(i, j int) bool { return burnLess(&res.Rows[i], &res.Rows[j]) })
	return res, nil
}

// burnLess is the row ordering: modeled render cost descending (what the
// reader pays), then returned bytes descending, then key ascending. Extracted
// from the sort.Slice closure so the three-way tie-break reads as a ladder and
// stays under the complexity ceiling as more columns land.
//
// Pointer receivers, not values: BurnRow carries a map field, and a
// value-range/value-param over it trips rangeValCopy on a hot struct.
func burnLess(a, b *BurnRow) bool {
	if a.RenderCost != b.RenderCost {
		return a.RenderCost > b.RenderCost
	}
	if a.OutBytes != b.OutBytes {
		return a.OutBytes > b.OutBytes
	}
	return a.Key < b.Key
}

// renderCost models one event's cost to the reader in approximate rendered
// bytes: a per-call chrome constant chosen by kind, plus the share of the
// call's returned bytes that actually renders before the preview folds.
//
// The cap is applied PER CALL, never to a summed total — a key's rendered cost
// is the sum of its calls' capped previews, so 733 small calls out-render one
// enormous one. That per-call application is the whole mechanism by which this
// ranking differs from the byte ranking.
func renderCost(ev *event.Event) int {
	return chromePerCall(ev.Kind) + min(ev.Bytes, previewCapBytes)
}

// chromePerCall returns the modeled per-call chrome cost in rendered bytes for
// an event kind. Shell calls render the command echo, an output preview frame,
// and a permission-classifier line; native tool calls collapse to one line.
// Unknown kinds fall back to the cheap (tool) constant — a model should not
// invent burn for a kind it has never seen.
func chromePerCall(kind string) int {
	if kind == event.KindShell {
		return shellChromeLines * renderedLineBytes
	}
	return toolChromeLines * renderedLineBytes
}

// burnKey mirrors stats.go's addAction: shell events get an "sh:" prefix on
// their normalized command so a shell command (e.g. "sh:make") never
// collides with a same-named tool key.
func burnKey(ev *event.Event) string {
	if ev.Kind == event.KindShell {
		return "sh:" + ev.Action
	}
	return ev.Action
}
