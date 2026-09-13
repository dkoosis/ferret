package event

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// attachVisibilityJSON is the ferret-z35 calibration: for each attachment
// class and hook event one captured Claude Code session produced, whether the
// record's text entered an API request, and from which record fields.
//
// Measured, not read off the record. A record's bytes and its own "content"
// field both mislead: in the capture a PostToolUse hook's record held 4,220
// bytes of content and none of it reached a request, while a UserPromptSubmit
// hook's record of the same shape reached every one. The docs agree for exit-0
// PreToolUse/PostToolUse hooks; the capture is what checks them.
//
// Regenerated from a capture by TestRegenAttachVisibility (attachvis_test.go).
// A class the capture never produced is absent here and stays uncalibrated.
//
//go:embed attach-visibility.json
var attachVisibilityJSON []byte

// AttachVisibility is the calibration table as committed.
type AttachVisibility struct {
	Note       string                `json:"note"`
	Capture    string                `json:"capture"`    // capture directory name under ~/.ferret/calibration/
	ClaudeCode string                `json:"claudeCode"` // harness version the capture ran; re-capture when it moves far
	Subkeys    []AttachVisibilityRow `json:"subkeys"`
}

// AttachVisibilityRow is one class + hook event's measurement.
//
// Visible means most of its records ADDED text to a request: the first request
// after the record carries more copies of that text than the last one before
// it. A record that snapshots text already sent (prompt_snapshot's system
// prompt) is not visible.
type AttachVisibilityRow struct {
	Class          string   `json:"class"`
	HookEvent      string   `json:"hookEvent,omitempty"`
	Records        int      `json:"records"`
	VisibleRecords int      `json:"visibleRecords"`
	Visible        bool     `json:"visible"`
	SourceFields   []string `json:"sourceFields,omitempty"` // "files[].content": dot = key, [] = each element
	TextBytes      int      `json:"textBytes"`              // visible: text found added; invisible: all text the records carried
	PricedBytes    int      `json:"pricedBytes"`            // what ingest's pricing gives the same records
	RecordBytes    int      `json:"recordBytes"`
}

// visibilityTable indexes the rows by AttachSubkey.
type visibilityTable map[string]*AttachVisibilityRow

var attachVisibility = mustVisibility(attachVisibilityJSON)

func mustVisibility(b []byte) visibilityTable {
	t, err := parseVisibility(b)
	if err != nil {
		panic("event: attach-visibility.json: " + err.Error())
	}
	return t
}

func parseVisibility(b []byte) (visibilityTable, error) {
	var av AttachVisibility
	if err := json.Unmarshal(b, &av); err != nil {
		return nil, err
	}
	return tableOf(av.Subkeys), nil
}

func tableOf(rows []AttachVisibilityRow) visibilityTable {
	t := make(visibilityTable, len(rows))
	for i := range rows {
		t[AttachSubkey(rows[i].Class, rows[i].HookEvent)] = &rows[i]
	}
	return t
}

// AttachSubkey names an attachment's calibration key: its class, plus the hook
// event for a hook record. Visibility turns on the hook event, not the class.
func AttachSubkey(class, hookEvent string) string {
	if hookEvent == "" {
		return class
	}
	return class + "/" + hookEvent
}

// price returns how many bytes of a record's text the model sees, and whether
// the capture covered its subkey at all. A visible subkey counts the strings
// at its source fields, each distinct string once (a hook record repeats its
// output under both content and stdout).
func (t visibilityTable) price(class, hookEvent string, payload []byte) (visible int, calibrated bool) {
	row, ok := t[AttachSubkey(class, hookEvent)]
	if !ok {
		return 0, false
	}
	if !row.Visible {
		return 0, true
	}
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return 0, true
	}
	seen := map[string]struct{}{}
	for _, f := range row.SourceFields {
		walkPath(v, strings.Split(f, "."), func(text string) {
			if _, dup := seen[text]; !dup {
				seen[text] = struct{}{}
				visible += len(text)
			}
		})
	}
	return visible, true
}

// walkPath calls fn on every string at a source-field path. A segment's
// trailing "[]" pairs each descend one array level.
func walkPath(v any, segs []string, fn func(string)) {
	if len(segs) == 0 {
		if s, ok := v.(string); ok {
			fn(s)
		}
		return
	}
	key := strings.TrimSuffix(segs[0], "[]")
	depth := 0
	for rest := segs[0]; strings.HasSuffix(rest, "[]"); rest = strings.TrimSuffix(rest, "[]") {
		depth++
		key = strings.TrimSuffix(rest, "[]")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	eachElement(m[key], depth, func(child any) { walkPath(child, segs[1:], fn) })
}

func eachElement(v any, depth int, fn func(any)) {
	if depth == 0 {
		fn(v)
		return
	}
	arr, ok := v.([]any)
	if !ok {
		return
	}
	for _, child := range arr {
		eachElement(child, depth-1, fn)
	}
}
