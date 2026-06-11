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

// fileState is the per-transcript accumulator: events buffer plus the
// tool_use → tool_result pairing maps.
type fileState struct {
	events   []*Event
	pending  map[string][]*Event // tool_use id → events awaiting status
	callTime map[string]time.Time
	seq      int
}

// File processes one transcript and emits its events in file order.
// Events are buffered until EOF because statuses (tool_result) arrive
// after their tool_use; files are small enough (~5MB max) to hold.
func (b *Builder) File(src transcript.Source, emit func(*Event)) error {
	b.Stats.Files++
	st := &fileState{pending: map[string][]*Event{}, callTime: map[string]time.Time{}}

	err := transcript.ReadLines(src.Path, func(line []byte) error {
		st.seq++
		b.Stats.Lines++
		b.consumeLine(src, st, line)
		return nil
	})
	if err != nil {
		return err
	}

	finish(st.events, b.Stats)
	for _, ev := range st.events {
		emit(ev)
	}
	b.Stats.Events += len(st.events)
	return nil
}

// consumeLine decodes one transcript line and folds it into the file state.
// Undecodable or irrelevant lines are counted and dropped.
func (b *Builder) consumeLine(src transcript.Source, st *fileState, line []byte) {
	var probe transcript.Probe
	if err := json.Unmarshal(line, &probe); err != nil {
		b.Stats.DecodeErrs++ // truncated final line of a killed session, etc.
		return
	}
	b.Stats.ByType[probe.Type]++
	if probe.Type != "assistant" && probe.Type != "user" {
		return
	}
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		b.Stats.DecodeErrs++
		return
	}
	if b.isDuplicate(raw.UUID) {
		return
	}
	if raw.Message == nil {
		return
	}
	ts, _ := time.Parse(time.RFC3339, raw.Timestamp)
	switch probe.Type {
	case "assistant":
		b.assistantLine(src, st, &raw, ts)
	case "user":
		b.userLine(src, st, &raw, ts)
	}
}

// isDuplicate dedups by message UUID across the whole ingest: resumed and
// forked sessions copy history into new files. UUIDs are FNV-1a-hashed to
// 8 bytes: the 64-bit birthday bound puts collision odds near 1e-6 even at
// 10M events, and a collision costs one dropped event in a frequency miner.
func (b *Builder) isDuplicate(uuid string) bool {
	if uuid == "" {
		return false
	}
	h := fnv.New64a()
	h.Write([]byte(uuid))
	key := h.Sum64()
	if _, dup := b.seen[key]; dup {
		b.Stats.Deduped++
		return true
	}
	b.seen[key] = struct{}{}
	return false
}

// assistantLine extracts tool_use blocks and parks them pending a status.
func (b *Builder) assistantLine(src transcript.Source, st *fileState, raw *transcript.Raw, ts time.Time) {
	for _, blk := range raw.Message.Content {
		if blk.Type != "tool_use" {
			continue
		}
		evs := b.fromToolUse(src, raw, blk, st.seq, ts)
		st.events = append(st.events, evs...)
		if blk.ID != "" {
			st.pending[blk.ID] = evs
			if !ts.IsZero() {
				st.callTime[blk.ID] = ts
			}
		}
	}
}

// userLine resolves tool_result statuses and detects genuine user prompts.
func (b *Builder) userLine(src transcript.Source, st *fileState, raw *transcript.Raw, ts time.Time) {
	sawResult := false
	sawText := false
	for _, blk := range raw.Message.Content {
		switch blk.Type {
		case "tool_result":
			sawResult = true
			st.resolve(blk, ts)
		case "text":
			if len(blk.Text) > 0 {
				sawText = true
			}
		}
	}
	if sawText && !sawResult && !raw.IsMeta {
		st.events = append(st.events, &Event{
			Seq: st.seq, Time: ts,
			Project: src.Project, Session: session(src, raw), Agent: src.Agent,
			Sidechain: raw.IsSidechain,
			Kind:      KindPrompt, Action: "prompt",
			Version: raw.Version,
		})
		b.Stats.Prompts++
	}
}

// resolve applies a tool_result's status and latency to its pending events.
// A failed compound chain gets cfail, not fail: the result says the invocation
// failed, not which segment — fail on every segment would invent friction.
func (st *fileState) resolve(blk transcript.Block, ts time.Time) {
	evs := st.pending[blk.ToolUseID]
	status := StatusOK
	if blk.IsError != nil && *blk.IsError {
		status = StatusFail
		if len(evs) > 1 {
			status = StatusCFail
		}
	}
	for _, ev := range evs {
		ev.Status = status
		if ct, ok := st.callTime[blk.ToolUseID]; ok && !ts.IsZero() {
			ev.DurMS = ts.Sub(ct).Milliseconds()
		}
	}
	delete(st.pending, blk.ToolUseID)
	delete(st.callTime, blk.ToolUseID)
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
		switch ev.Status {
		case StatusFail:
			if !ev.Time.IsZero() {
				lastFail[key] = ev.Time
			}
		case StatusOK:
			// Success closes the retry window: a later organic call to the
			// same action+target is not a retry of the original failure.
			delete(lastFail, key)
			// StatusCFail neither opens nor closes the window: the failing
			// segment is unknown, so no per-action attribution is safe.
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
