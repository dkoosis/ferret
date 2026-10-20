package main

import (
	"encoding/json"
	"testing"
)

// around already has a "window" JSON key (its --window step count); the
// stream window must not overwrite it.
func TestAroundWindow_JSON_KeepsStepCountBesideStreamWindow(t *testing.T) {
	data := writeWindowCorpus(t)
	saved := CLI.Around
	t.Cleanup(func() { CLI.Around = saved })
	CLI.Around.Data, CLI.Around.Format, CLI.Around.Limit = data, fmtJSON, -1
	CLI.Around.Lens, CLI.Around.Token, CLI.Around.Window = "cmd", "bd_close", 2
	CLI.Around.Since = "2026-10-19"
	got := captureStdout(t, cmdAround)
	var m struct {
		Window       int `json:"window"`
		StreamWindow struct {
			Kept  int `json:"kept"`
			Total int `json:"total"`
		} `json:"streamWindow"`
	}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("around JSON: %v\n%s", err, got)
	}
	if m.Window != 2 {
		t.Errorf("window = %d, want the --window step count 2:\n%s", m.Window, got)
	}
	if m.StreamWindow.Kept != 2 || m.StreamWindow.Total != 4 {
		t.Errorf("streamWindow = %+v, want kept 2 of 4:\n%s", m.StreamWindow, got)
	}
}
