package event

import (
	"encoding/json"
	"hash/fnv"
	"strings"
	"time"
	"unicode/utf8"

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
	seen     map[uint64]struct{}
	resolved map[string]struct{} // tool_use ids whose result we've applied, across the ingest
	Stats    *Stats
}

func NewBuilder() *Builder {
	return &Builder{seen: map[uint64]struct{}{}, resolved: map[string]struct{}{}, Stats: NewStats()}
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

	// A use still pending here whose id was already resolved in an earlier file
	// is a resumed/forked duplicate whose result was deduped. It WAS resolved
	// before, so leaving it status-less would make finish count it unpaired and
	// inflate the reported unpaired rate. Mark it resolved-without-status: it is
	// not friction, just a copied call whose result we already accounted.
	for id, evs := range st.pending {
		if _, done := b.resolved[id]; done {
			for _, ev := range evs {
				ev.Status = StatusOK
			}
			delete(st.pending, id)
		}
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
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
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
	var parts []string
	for i := range raw.Message.Content {
		blk := &raw.Message.Content[i]
		switch blk.Type {
		case "tool_result":
			sawResult = true
			b.resolve(st, blk, ts)
		case "text":
			if body := strings.TrimSpace(blk.Text); body != "" {
				parts = append(parts, body)
			}
		}
	}
	if len(parts) > 0 && !sawResult && !raw.IsMeta {
		st.events = append(st.events, &Event{
			Seq: st.seq, Time: ts,
			Project: src.Project, Session: session(src, raw), Agent: src.Agent,
			Sidechain: raw.IsSidechain,
			Kind:      KindPrompt, Action: "prompt",
			// Full, untruncated prompt text — whitespace-collapsed only (ferret-d01).
			Prompt:  strings.Join(strings.Fields(strings.Join(parts, " ")), " "),
			Version: raw.Version,
		})
		b.Stats.Prompts++
	}
}

// resolve applies a tool_result's status and latency to its pending events.
// A failed compound chain gets cfail, not fail: the result says the invocation
// failed, not which segment — fail on every segment would invent friction.
func (b *Builder) resolve(st *fileState, blk *transcript.Block, ts time.Time) {
	evs := st.pending[blk.ToolUseID]
	if len(evs) == 0 {
		// No pending use for this id. On a resumed/forked transcript the use
		// line was a copied duplicate, dropped at isDuplicate before parking,
		// while this result carries a fresh message UUID and reaches us. Any
		// result arriving here has already passed UUID dedup, so it is genuine
		// returned context — account its bytes rather than dropping them. This
		// is a stray result, not an unpaired use, so Stats.Unpaired is
		// untouched. Record the id so a later forked use of it isn't counted
		// unpaired either.
		b.Stats.OrphanBytes += len(blk.Content)
		b.resolved[blk.ToolUseID] = struct{}{}
		return
	}
	status := StatusOK
	if blk.IsError != nil && *blk.IsError {
		status = StatusFail
		if len(evs) > 1 {
			status = StatusCFail
		}
	}
	// Attribute the result payload's measured size across the (possibly
	// compound) events it resolves — this is real context the call returned.
	// Integer division drops up to n-1 bytes; carry the remainder onto the
	// leading segments so the full payload is conserved in burn.
	n := len(evs)
	share := len(blk.Content) / n
	rem := len(blk.Content) % n
	ct, haveCT := st.callTime[blk.ToolUseID]
	for i, ev := range evs {
		ev.Status = status
		ev.Bytes += share
		if i < rem {
			ev.Bytes++
		}
		if haveCT && !ts.IsZero() {
			ev.DurMS = ts.Sub(ct).Milliseconds()
		}
	}
	// snipe emits a single trailing "~approx" token on fuzzy/semantic fallback
	// (silence = exact match). It is the last token of the result body, so it
	// belongs to the trailing segment of a (possibly compound) invocation;
	// attribute it only when that segment is the snipe call — an earlier snipe
	// in a chain whose output is followed by another command's is not the one
	// the marker describes.
	if last := evs[n-1]; isSnipe(last) && snipeApprox(blk.Content) {
		last.Approx = true
	}
	// A query-mode get_nug event (single, non-compound) captures its returned nug
	// hits in rank order from the result envelope — the retrieval half of the
	// episode (ferret-sq.d0). An errored/empty result yields no hits.
	if n == 1 && evs[0].Query != "" {
		evs[0].Results = parseNugHits(blk.Content)
	}
	b.resolved[blk.ToolUseID] = struct{}{}
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

func (b *Builder) fromToolUse(src transcript.Source, raw *transcript.Raw, blk *transcript.Block, seq int, ts time.Time) []*Event {
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
			ev.Bytes = len(cmd)
			return []*Event{&ev}
		}
		out := make([]*Event, 0, len(segs))
		for _, seg := range segs {
			ev := base
			ev.Kind = KindShell
			ev.Action = seg.Cmd
			ev.Detail = trunc(seg.Raw, detailMax)
			ev.Bytes = len(seg.Raw)
			ev.Compound = len(segs) > 1
			out = append(out, &ev)
		}
		return out
	}

	ev := base
	ev.Kind = KindTool
	ev.Action = blk.Name
	ev.Target = target(input)
	ev.Bytes = len(blk.Input)
	ev.Query = getNugQuery(blk.Name, input)
	return []*Event{&ev}
}

// toolGetNug is the trixi search tool whose query-mode calls are retrieval
// episodes (ferret-sq.d0). By-id fetches of the same tool carry no query arg.
const toolGetNug = "mcp__trixi__get_nug"

// getNugQuery returns the full search string of a query-mode get_nug call, or ""
// for any other tool or a by-id fetch. A non-empty return marks the event as a
// retrieval episode whose result ids/scores resolve() captures into Results.
func getNugQuery(name string, input map[string]any) string {
	if name != toolGetNug {
		return ""
	}
	q, _ := input["query"].(string)
	return strings.TrimSpace(q)
}

// parseNugHits extracts the returned nug hits from a get_nug tool_result, in rank
// (result) order. The result body is a JSON array of hit objects, each with an
// "id" and a per-query "score"; an error/timeout result (a bare string) or any
// non-array body yields no hits. Score is captured as-is — zero when a hit omits
// it — so all rank-keyed metrics work and the score-distribution metric is unblocked.
func parseNugHits(content json.RawMessage) []NugHit {
	text, ok := resultText(content)
	if !ok {
		return nil
	}
	if text = strings.TrimSpace(text); text == "" || text[0] != '[' {
		return nil
	}
	var raw []struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	}
	if json.Unmarshal([]byte(text), &raw) != nil {
		return nil
	}
	hits := make([]NugHit, 0, len(raw))
	for _, r := range raw {
		if r.ID == "" {
			continue
		}
		hits = append(hits, NugHit{ID: r.ID, Score: r.Score})
	}
	if len(hits) == 0 {
		return nil
	}
	return hits
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
	// Walk back to a rune boundary so we never split a multibyte rune.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// snipeApproxMarker is the single trailing token snipe appends to a tool_result
// body when it served via fuzzy/semantic fallback rather than an exact match.
const snipeApproxMarker = "~approx"

// isSnipe reports whether ev is a snipe shell invocation. shellnorm keeps the
// subcommand for snipe (snipe_callers, snipe_pack…); a bare or flag-first call
// normalizes to "snipe".
func isSnipe(ev *Event) bool {
	return ev.Kind == KindShell && (ev.Action == "snipe" || strings.HasPrefix(ev.Action, "snipe_"))
}

// snipeApprox reports whether the tool_result body ends with snipe's fallback
// marker as a standalone trailing token. The body is the raw JSON tool_result
// content — either a JSON string or an array of content blocks.
func snipeApprox(content json.RawMessage) bool {
	text, ok := resultText(content)
	if !ok {
		return false
	}
	text = strings.TrimRight(text, " \t\r\n")
	if !strings.HasSuffix(text, snipeApproxMarker) {
		return false
	}
	// Require a whitespace boundary (or start of body) before the marker so a
	// word that merely ends in "~approx" is not mistaken for the token.
	rest := text[:len(text)-len(snipeApproxMarker)]
	if rest == "" {
		return true
	}
	switch rest[len(rest)-1] {
	case ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

// resultText decodes a tool_result content payload into its text. CC emits it
// as either a JSON string or an array of blocks ({"type":"text","text":...});
// for the array form the block texts are concatenated.
func resultText(content json.RawMessage) (string, bool) {
	trimmed := strings.TrimLeft(string(content), " \t\r\n")
	if trimmed == "" {
		return "", false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if json.Unmarshal(content, &s) != nil {
			return "", false
		}
		return s, true
	case '[':
		var blocks []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &blocks) != nil {
			return "", false
		}
		var sb strings.Builder
		for i := range blocks {
			sb.WriteString(blocks[i].Text)
		}
		return sb.String(), true
	}
	return "", false
}
