package event

import (
	"encoding/json"
	"hash/fnv"
	"time"

	"github.com/dkoosis/ferret/internal/shellnorm"
	"github.com/dkoosis/ferret/internal/transcript"
)

const (
	detailMax   = 160
	retryWindow = 120 * time.Second
)

// Builder converts transcript files into canonical events.
// The uuid seen-set spans the whole ingest: resumed/forked sessions copy
// history into new files and would otherwise double-count.
type Builder struct {
	seen  map[uint64]struct{}
	Stats *Stats
}

func NewBuilder() *Builder {
	return &Builder{seen: map[uint64]struct{}{}, Stats: NewStats()}
}

// File processes one transcript and emits its events in file order.
// Events are buffered until EOF because statuses (tool_result) arrive
// after their tool_use; files are small enough (~5MB max) to hold.
func (b *Builder) File(src transcript.Source, emit func(*Event)) error {
	b.Stats.Files++
	var events []*Event
	pending := map[string][]*Event{} // tool_use id → events awaiting status
	callTime := map[string]time.Time{}
	seq := 0

	err := transcript.ReadLines(src.Path, func(line []byte) error {
		seq++
		b.Stats.Lines++
		var probe transcript.Probe
		if err := json.Unmarshal(line, &probe); err != nil {
			b.Stats.DecodeErrs++ // truncated final line of a killed session, etc.
			return nil
		}
		b.Stats.ByType[probe.Type]++
		if probe.Type != "assistant" && probe.Type != "user" {
			return nil
		}
		var raw transcript.Raw
		if err := json.Unmarshal(line, &raw); err != nil {
			b.Stats.DecodeErrs++
			return nil
		}
		if raw.UUID != "" {
			h := fnv.New64a()
			h.Write([]byte(raw.UUID))
			key := h.Sum64()
			if _, dup := b.seen[key]; dup {
				b.Stats.Deduped++
				return nil
			}
			b.seen[key] = struct{}{}
		}
		ts, _ := time.Parse(time.RFC3339, raw.Timestamp)
		if raw.Message == nil {
			return nil
		}

		switch probe.Type {
		case "assistant":
			for _, blk := range raw.Message.Content {
				if blk.Type != "tool_use" {
					continue
				}
				evs := b.fromToolUse(src, &raw, blk, seq, ts)
				events = append(events, evs...)
				if blk.ID != "" {
					pending[blk.ID] = evs
					if !ts.IsZero() {
						callTime[blk.ID] = ts
					}
				}
			}
		case "user":
			sawResult := false
			sawText := false
			for _, blk := range raw.Message.Content {
				switch blk.Type {
				case "tool_result":
					sawResult = true
					status := StatusOK
					if blk.IsError != nil && *blk.IsError {
						status = StatusFail
					}
					for _, ev := range pending[blk.ToolUseID] {
						ev.Status = status
						if ct, ok := callTime[blk.ToolUseID]; ok && !ts.IsZero() {
							ev.DurMS = ts.Sub(ct).Milliseconds()
						}
					}
					delete(pending, blk.ToolUseID)
					delete(callTime, blk.ToolUseID)
				case "text":
					if len(blk.Text) > 0 {
						sawText = true
					}
				}
			}
			if sawText && !sawResult && !raw.IsMeta {
				events = append(events, &Event{
					Seq: seq, Time: ts,
					Project: src.Project, Session: session(src, &raw), Agent: src.Agent,
					Sidechain: raw.IsSidechain,
					Kind:      KindPrompt, Action: "prompt",
					Version: raw.Version,
				})
				b.Stats.Prompts++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	finish(events, b.Stats)
	for _, ev := range events {
		emit(ev)
	}
	b.Stats.Events += len(events)
	return nil
}

// finish resolves unpaired statuses and marks retries, in file order.
func finish(events []*Event, stats *Stats) {
	lastFail := map[string]time.Time{}
	for _, ev := range events {
		if ev.Kind != KindPrompt && ev.Status == "" {
			ev.Status = StatusNone // interruption/compaction — not a failure
			stats.Unpaired++
		}
		key := ev.Action + "\x00" + ev.Target
		if ft, ok := lastFail[key]; ok && !ev.Time.IsZero() && ev.Time.Sub(ft) <= retryWindow {
			ev.Retry = true
		}
		if ev.Status == StatusFail && !ev.Time.IsZero() {
			lastFail[key] = ev.Time
		}
	}
}

func (b *Builder) fromToolUse(src transcript.Source, raw *transcript.Raw, blk transcript.Block, seq int, ts time.Time) []*Event {
	base := Event{
		Seq: seq, Time: ts,
		Project: src.Project, Session: session(src, raw), Agent: src.Agent,
		Sidechain: raw.IsSidechain,
		Skill:     raw.Skill, Plugin: raw.Plugin, MCP: raw.MCPServer,
		Version: raw.Version,
	}
	var input map[string]any
	_ = json.Unmarshal(blk.Input, &input)

	if blk.Name == "Bash" {
		cmd, _ := input["command"].(string)
		segs, fb := shellnorm.Split(cmd)
		if fb {
			b.Stats.Fallback++
		}
		if len(segs) == 0 {
			ev := base
			ev.Kind = KindShell
			ev.Action = "sh"
			ev.Detail = trunc(cmd, detailMax)
			return []*Event{&ev}
		}
		out := make([]*Event, 0, len(segs))
		for _, seg := range segs {
			ev := base
			ev.Kind = KindShell
			ev.Action = seg.Cmd
			ev.Detail = trunc(seg.Raw, detailMax)
			ev.Compound = len(segs) > 1
			out = append(out, &ev)
		}
		return out
	}

	ev := base
	ev.Kind = KindTool
	ev.Action = blk.Name
	ev.Target = target(input)
	return []*Event{&ev}
}

// target pulls the most identifying input field, by priority.
var targetKeys = []string{"file_path", "path", "notebook_path", "pattern", "query", "url", "skill", "subagent_type"}

func target(input map[string]any) string {
	for _, k := range targetKeys {
		if v, ok := input[k].(string); ok && v != "" {
			return trunc(v, detailMax)
		}
	}
	return ""
}

func session(src transcript.Source, raw *transcript.Raw) string {
	if raw.SessionID != "" {
		return raw.SessionID
	}
	return src.Session
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
