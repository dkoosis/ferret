package mine

import (
	"sort"

	"github.com/dkoosis/ferret/internal/event"
)

// BurnRow is one normalized command's corpus-wide burn: how much measured
// context its calls cost, aggregated across every session that called it.
type BurnRow struct {
	Key          string  `json:"key"`      // shellnorm/tool key: "sh:git_commit", "Read", "mcp__trixi__get_nug", ...
	OutBytes     int     `json:"outBytes"` // summed per-event measured bytes
	Calls        int     `json:"calls"`
	BytesPerCall float64 `json:"bytesPerCall"`
	Sessions     int     `json:"sessions"`

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
// their normalized command key, and ranks the groups by total measured byte
// cost — the "what's eating my context" tune-up list (ferret-nrr).
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
// Rows sort by total OutBytes descending; ties break on Key ascending so
// repeated runs are byte-stable.
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
		}
		res.Rows = append(res.Rows, *r)
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		if res.Rows[i].OutBytes != res.Rows[j].OutBytes {
			return res.Rows[i].OutBytes > res.Rows[j].OutBytes
		}
		return res.Rows[i].Key < res.Rows[j].Key
	})
	return res, nil
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
