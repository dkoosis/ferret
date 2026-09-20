package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/transcript"
)

// ferret-z35, ferret-0qo. A capture (one Claude Code session through
// cmd/capture-proxy: transcript.jsonl + requests/, never committed) lives under
// ~/.ferret/calibration/. Regenerate the committed table from all of them, the
// directories joined like $PATH:
//
//	FERRET_CALIB_DIR=~/.ferret/calibration/z35-20260913:~/.ferret/calibration/0qo-20260919 go test ./internal/event -run TestRegenAttachVisibility
const attachVisibilityPath = "attach-visibility.json"

// Within the ferret-z35 tolerances, per subkey of the capture: a visible one
// prices at ≥50% of the text the capture saw it add, an invisible one at ≤5%
// of the text it carried. priced picks the pricing under test. Both sides must
// be present, or the check proves nothing.
func checkPricing(rows []AttachVisibilityRow, priced func(*AttachVisibilityRow) int) []string {
	var fails []string
	visible, invisible := 0, 0
	for i := range rows {
		r := &rows[i]
		if r.mixed() || r.unmeasured() {
			continue // no single bound applies
		}
		got, text := float64(priced(r)), float64(r.TextBytes)
		name := AttachSubkey(r.Class, r.HookEvent)
		if r.Visible {
			visible++
			if got < 0.5*text {
				fails = append(fails, fmt.Sprintf("%s visible: priced %.0fB < 50%% of %.0fB", name, got, text))
			}
			continue
		}
		invisible++
		if got > 0.05*text {
			fails = append(fails, fmt.Sprintf("%s invisible: priced %.0fB > 5%% of %.0fB", name, got, text))
		}
	}
	if visible == 0 || invisible == 0 {
		fails = append(fails, fmt.Sprintf("%d visible and %d invisible subkeys checked; each side needs ≥1", visible, invisible))
	}
	return fails
}

func committedVisibility(t *testing.T) *AttachVisibility {
	t.Helper()
	var av AttachVisibility
	if err := json.Unmarshal(attachVisibilityJSON, &av); err != nil {
		t.Fatal(err)
	}
	return &av
}

func TestAttachVisibility_PricingMatchesCapture(t *testing.T) {
	av := committedVisibility(t)
	for _, f := range checkPricing(av.Subkeys, func(r *AttachVisibilityRow) int { return r.PricedBytes }) {
		t.Error(f)
	}
	if len(checkPricing(av.Subkeys, func(r *AttachVisibilityRow) int { return r.RecordBytes })) == 0 {
		t.Error("record-byte pricing passes the capture check: it cannot tell a measurement from the old ranking")
	}
}

// The docs say a PreToolUse hook exiting 0 never shows its output to the model.
func TestAttachVisibility_PreToolUseHookIsHidden(t *testing.T) {
	for _, r := range committedVisibility(t).Subkeys {
		if r.Class == "hook_success" && r.HookEvent == "PreToolUse" {
			if r.Visible {
				t.Error("PreToolUse exit-0 stderr reached a request: the docs finding is wrong — record it on ferret-z35")
			}
			return
		}
	}
	t.Error("table has no hook_success/PreToolUse row")
}

func TestAttachVisibility_CarriesNoAuth(t *testing.T) {
	low := bytes.ToLower(attachVisibilityJSON)
	for _, bad := range []string{"x-api-key", "authorization"} {
		if bytes.Contains(low, []byte(bad)) {
			t.Errorf("table contains %q", bad)
		}
	}
}

func TestPrice_CountsSourceFieldsOnce(t *testing.T) {
	table := tableOf([]AttachVisibilityRow{
		{Class: "hook_success", HookEvent: "SessionStart", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"content", "stdout"}},
		{Class: "hook_success", HookEvent: "PreToolUse", Records: 1, MeasuredRecords: 1},
		{Class: "hook_additional_context", HookEvent: "SessionStart", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"content"}},
		{Class: "instructions", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"files[].content"}},
		{Class: "prompt_snapshot", Records: 2, MeasuredRecords: 2, VisibleRecords: 1, SourceFields: []string{"systemPrompt[]"}},
		{Class: "date", Records: 1},
	})
	cases := []struct {
		class, hook, payload string
		visible              int
		calibrated           bool
	}{
		{"hook_success", "SessionStart", `{"content":"abcdef","stdout":"abcdef","command":"x"}`, 6, true},
		// A hook that answers with the JSON control protocol is priced at 0:
		// its hook_additional_context record carries the text instead.
		{"hook_success", "SessionStart", `{"content":"","stdout":"{\"hookSpecificOutput\":{\"additionalContext\":\"abcdef\"}}"}`, 0, true},
		// Only stdout carries the control object. A decoded
		// hook_additional_context record whose own content is JSON-shaped is
		// text the model sees, so it is priced.
		{"hook_additional_context", "SessionStart", `{"content":"{\"a\":1}"}`, 7, true},
		{"hook_success", "PreToolUse", `{"stderr":"abcdef"}`, 0, true},
		{"instructions", "", `{"files":[{"path":"p","content":"abc"},{"content":"de"}]}`, 5, true},
		{"instructions", "", `{"files":[{"content":"abc"},{"content":"abc"}]}`, 6, true},
		// A JSON-looking string outside a hook class is just text.
		{"instructions", "", `{"files":[{"content":"{\"a\":1}"}]}`, 7, true},
		{"prompt_snapshot", "", `{"systemPrompt":["abcdef"]}`, 0, false},
		{"date", "", `{"date":"2026-09-19"}`, 0, false},
		{"nested_memory", "", `{"content":"abc"}`, 0, false},
	}
	for _, c := range cases {
		v, cal := table.price(c.class, c.hook, []byte(c.payload))
		if v != c.visible || cal != c.calibrated {
			t.Errorf("%s/%s: price = %d,%v want %d,%v", c.class, c.hook, v, cal, c.visible, c.calibrated)
		}
	}
}

func TestAttachmentEvent_CarriesHookEventAndPrice(t *testing.T) {
	prev := attachVisibility
	attachVisibility = tableOf([]AttachVisibilityRow{
		{Class: "hook_success", HookEvent: "UserPromptSubmit", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"stdout"}},
		{Class: "hook_success", HookEvent: "PreToolUse", Records: 1, MeasuredRecords: 1},
	})
	t.Cleanup(func() { attachVisibility = prev })

	src := writeTranscript(t,
		attachLine("a1", "hook_success", `,"hookEvent":"PreToolUse","stderr":"hidden text"`),
		attachLine("a2", "hook_success", `,"hookEvent":"UserPromptSubmit","stdout":"shown"`),
		attachLine("a3", "nested_memory", `,"content":"never captured"`),
	)
	var evs []*Event
	if err := NewBuilder().File(src, func(ev *Event) { evs = append(evs, ev) }); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		target     string
		visible    int
		calibrated bool
	}{{"PreToolUse", 0, true}, {"UserPromptSubmit", 5, true}, {"", 0, false}}
	if len(evs) != len(want) {
		t.Fatalf("events = %d, want %d", len(evs), len(want))
	}
	for i, w := range want {
		if evs[i].Target != w.target || evs[i].VisibleBytes != w.visible || evs[i].Calibrated != w.calibrated {
			t.Errorf("event %d: target=%q visible=%d calibrated=%v, want %q %d %v",
				i, evs[i].Target, evs[i].VisibleBytes, evs[i].Calibrated, w.target, w.visible, w.calibrated)
		}
	}
}

// A later capture adds the subkeys it alone saw; a subkey both measured keeps
// the earlier capture's row, numbers and all. An earlier row that measured
// nothing is a gap, so a later measured row takes its place.
func TestMergeRows_LaterCaptureOnlyFillsGaps(t *testing.T) {
	first := []AttachVisibilityRow{
		{Class: "instructions", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"files[].content"}, TextBytes: 10, RecordBytes: 40},
		{Class: "hook_success", HookEvent: "SessionStart", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"content"}, TextBytes: 5, RecordBytes: 20},
		{Class: "environment", Records: 1, RecordBytes: 25},
	}
	second := []AttachVisibilityRow{
		{Class: "hook_success", HookEvent: "SessionStart", Records: 2, MeasuredRecords: 2, SourceFields: []string{"stdout"}, TextBytes: 7, RecordBytes: 30},
		{Class: "environment", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"snapshot.workingDirectory"}, TextBytes: 110, RecordBytes: 300},
		{Class: "nested_memory", Records: 1, MeasuredRecords: 1, VisibleRecords: 1, Visible: true, SourceFields: []string{"content.content"}, TextBytes: 9, RecordBytes: 50},
	}
	got := mergeRows(first, second)
	want := []AttachVisibilityRow{second[1], first[1], first[0], second[2]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeRows =\n%+v\nwant\n%+v", got, want)
	}
}

func TestRegenAttachVisibility(t *testing.T) {
	dirs := filepath.SplitList(os.Getenv("FERRET_CALIB_DIR"))
	if len(dirs) == 0 {
		t.Skip("FERRET_CALIB_DIR unset: regenerating needs the captures, which are never committed")
	}
	av, err := deriveVisibility(dirs...)
	if err != nil {
		t.Fatal(err)
	}
	if fails := checkPricing(av.Subkeys, func(r *AttachVisibilityRow) int { return r.PricedBytes }); len(fails) > 0 {
		t.Fatalf("derived table fails its own capture: %v", fails)
	}
	b, err := json.MarshalIndent(av, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachVisibilityPath, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// deriveVisibility builds the table from one or more captures, in order. A
// later capture adds the subkeys no earlier one measured and changes nothing
// else (mergeRows).
func deriveVisibility(dirs ...string) (*AttachVisibility, error) {
	var (
		rows    []AttachVisibilityRow
		names   []string
		version string
	)
	for _, dir := range dirs {
		captured, v, err := deriveCapture(dir)
		if err != nil {
			return nil, err
		}
		rows = mergeRows(rows, captured)
		names = append(names, filepath.Base(dir))
		version = v // the last capture's: the newest harness the table has seen
	}
	return &AttachVisibility{
		Note: "ferret-z35 attachment visibility, derived from proxy captures: class names, field paths and byte counts only. " +
			"Regenerate with TestRegenAttachVisibility; the captures themselves are never committed.",
		Capture: strings.Join(names, "+"), ClaudeCode: version, Subkeys: rows,
	}, nil
}

// deriveCapture measures one capture's records against its own requests and
// prices them off the rows that measurement gives.
func deriveCapture(dir string) (rows []AttachVisibilityRow, version string, err error) {
	reqs, err := loadRequests(filepath.Join(dir, "requests"))
	if err != nil {
		return nil, "", err
	}
	recs, version, err := loadAttachRecords(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return nil, "", err
	}
	rows = classifyRecords(recs, reqs)
	table := tableOf(rows)
	for i := range recs {
		r := &recs[i]
		v, _ := table.price(r.class, r.hookEvent, r.payload)
		row, ok := table[AttachSubkey(r.class, r.hookEvent)]
		if !ok {
			// classifyRecords built one row per subkey seen in recs, so this
			// can't happen; guard rather than assume for nilaway's sake.
			continue
		}
		row.PricedBytes += v
	}
	return rows, version, nil
}

// mergeRows adds to earlier the subkeys only later measured. A subkey both
// measured keeps earlier's row untouched: a second capture exists to fill
// gaps, and where the two disagree the disagreement is a finding to look at
// (ferret-0qo's trail), not a number to average into the pricing. An earlier
// row that measured nothing is a gap too, so a later row that did measure
// something replaces it.
func mergeRows(earlier, later []AttachVisibilityRow) []AttachVisibilityRow {
	merged := make([]AttachVisibilityRow, len(earlier), len(earlier)+len(later))
	copy(merged, earlier)
	have := tableOf(merged) // points into merged, which the cap above keeps put
	for i := range later {
		switch prev, ok := have[AttachSubkey(later[i].Class, later[i].HookEvent)]; {
		case !ok:
			merged = append(merged, later[i])
		case prev.unmeasured() && !later[i].unmeasured():
			*prev = later[i]
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return AttachSubkey(merged[i].Class, merged[i].HookEvent) < AttachSubkey(merged[j].Class, merged[j].HookEvent)
	})
	return merged
}

// capturedRequest is one main-thread request body and its UTC time of day,
// read from the proxy's file name (NNNN-HHMMSS.mmm_path.json).
type capturedRequest struct {
	tod  time.Duration
	body []byte
}

// minMainRequest drops side calls (session titles and the like, a few KB):
// every main-thread request carries the system prompt and tool list.
const minMainRequest = 20000

func loadRequests(dir string) ([]capturedRequest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var reqs []capturedRequest
	for _, e := range entries {
		name := e.Name()
		if len(name) < 15 {
			continue
		}
		clock, err := time.Parse("150405.000", name[5:15])
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(body) >= minMainRequest {
			reqs = append(reqs, capturedRequest{tod: timeOfDay(clock), body: body})
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].tod < reqs[j].tod })
	return reqs, nil
}

func timeOfDay(t time.Time) time.Duration {
	t = t.UTC()
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())
}

type attachRecord struct {
	tod       time.Duration
	class     string
	hookEvent string
	payload   json.RawMessage
}

func loadAttachRecords(path string) (recs []attachRecord, version string, err error) {
	err = transcript.ReadLines(path, func(line []byte) error {
		if rec, v, ok := parseAttachRecord(line); ok {
			recs = append(recs, rec)
			if v != "" {
				version = v
			}
		}
		return nil
	})
	return recs, version, err
}

// parseAttachRecord decodes one transcript line; ok is false for any line that
// is not a decodable attachment.
func parseAttachRecord(line []byte) (rec attachRecord, version string, ok bool) {
	var raw struct {
		Type       string          `json:"type"`
		Timestamp  time.Time       `json:"timestamp"`
		Version    string          `json:"version"`
		Attachment json.RawMessage `json:"attachment"`
	}
	if json.Unmarshal(line, &raw) != nil || raw.Type != "attachment" {
		return rec, "", false
	}
	var head struct {
		Type      string `json:"type"`
		HookEvent string `json:"hookEvent"`
	}
	if json.Unmarshal(raw.Attachment, &head) != nil {
		return rec, "", false
	}
	return attachRecord{tod: timeOfDay(raw.Timestamp), class: head.Type,
		hookEvent: head.HookEvent, payload: raw.Attachment}, raw.Version, true
}

// minLeafText skips ids, names and flags: a string this short matches request
// bodies by accident.
const minLeafText = 32

func classifyRecords(recs []attachRecord, reqs []capturedRequest) []AttachVisibilityRow {
	bySubkey := map[string]*AttachVisibilityRow{}
	fields := map[string]map[string]struct{}{}
	for i := range recs {
		r := &recs[i]
		k := AttachSubkey(r.class, r.hookEvent)
		row, ok := bySubkey[k]
		if !ok {
			row = &AttachVisibilityRow{Class: r.class, HookEvent: r.hookEvent}
			bySubkey[k] = row
			fields[k] = map[string]struct{}{}
		}
		row.Records++
		row.RecordBytes += len(r.payload)
		added, all := addedText(r, reqs, fields[k])
		if all == 0 {
			continue // carried nothing long enough to look for: no evidence
		}
		row.MeasuredRecords++
		if added > 0 {
			row.VisibleRecords++
			row.TextBytes += added
		} else {
			row.TextBytes += all
		}
	}
	rows := make([]AttachVisibilityRow, 0, len(bySubkey))
	for k, row := range bySubkey {
		row.Visible = row.VisibleRecords*2 > row.MeasuredRecords
		for f := range fields[k] {
			row.SourceFields = append(row.SourceFields, f)
		}
		sort.Strings(row.SourceFields)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return AttachSubkey(rows[i].Class, rows[i].HookEvent) < AttachSubkey(rows[j].Class, rows[j].HookEvent)
	})
	return rows
}

// addedText measures one record against the requests around it: the bytes of
// its text the next request carries more copies of than the previous one, and
// the bytes of all its text. Counts, not presence: a hook that prints the same
// text every turn finds its earlier copies in the history. Fields whose text
// was added go into fields.
func addedText(r *attachRecord, reqs []capturedRequest, fields map[string]struct{}) (added, all int) {
	var before, after []byte
	for i := range reqs {
		if reqs[i].tod <= r.tod {
			before = reqs[i].body
			continue
		}
		after = reqs[i].body
		break
	}
	var v any
	if json.Unmarshal(r.payload, &v) != nil {
		return 0, 0
	}
	firstPath := map[string]string{} // text → the path that counted it; same rule as price
	walkLeaves(v, "", func(path, text string) {
		if len(text) < minLeafText || hookRouting(r.class, path) || hookControlJSON(r.class, path, text) {
			return
		}
		if at, dup := firstPath[text]; dup && at != path {
			return
		}
		firstPath[text] = path
		all += len(text)
		probe := jsonProbe(text)
		if after != nil && bytes.Count(after, probe) > bytes.Count(before, probe) {
			added += len(text)
			fields[path] = struct{}{}
		}
	})
	return added, all
}

// walkLeaves visits every string with its source-field path, in the syntax
// walkPath reads back.
func walkLeaves(v any, path string, fn func(path, text string)) {
	switch x := v.(type) {
	case string:
		fn(path, x)
	case map[string]any:
		for k, child := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkLeaves(child, p, fn)
		}
	case []any:
		for _, child := range x {
			walkLeaves(child, path+"[]", fn)
		}
	}
}

// jsonProbe is a 48-byte window from the middle of text as it appears inside a
// JSON string in a request body. HTML escaping is off: the harness does not
// escape < and >, and the probe must match its bytes.
func jsonProbe(text string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(text); err != nil {
		return []byte(text) // a string always encodes; never reached
	}
	s := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	s = s[1 : len(s)-1]
	const window = 48
	if len(s) <= window {
		return s
	}
	mid := (len(s) - window) / 2
	return s[mid : mid+window]
}
