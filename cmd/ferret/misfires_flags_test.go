package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
)

// unknownFlagEv is a shell call rejected for an unknown flag at noon on day.
func unknownFlagEv(session, day, action, detail, flag string) event.Event {
	ts, _ := time.ParseInLocation(dateLayout, day, time.Local)
	return event.Event{
		Time: ts.Add(12 * time.Hour), Session: session, Kind: event.KindShell, Action: action,
		Detail: detail, Status: event.StatusFail, Flags: []string{flag},
		Err: "Error: unknown flag: " + flag,
	}
}

// AC4: --since drops a flag whose last chain is older than the window.
func TestWindowMisfireEvents_DropsStaleFlag_When_LastChainPredatesSince(t *testing.T) {
	evs := []event.Event{
		unknownFlagEv("s1", "2026-09-20", "bd_comment", "bd comment x --text y", "--text"),
		unknownFlagEv("s2", "2026-10-15", "bd_list", "bd list --short", "--short"),
	}
	if rows := mine.MineFlagMisfires(evs); len(rows) != 2 {
		t.Fatalf("unwindowed rows = %+v; want both flags", rows)
	}
	kept, err := windowMisfireEvents(evs, "2026-10-06", "2026-10-19")
	if err != nil {
		t.Fatalf("windowMisfireEvents: %v", err)
	}
	rows := mine.MineFlagMisfires(kept)
	if len(rows) != 1 || rows[0].Flag != "--short" || rows[0].LastSeen != "2026-10-15" {
		t.Errorf("windowed rows = %+v; want only bd list --short, last seen 2026-10-15", rows)
	}
}

func TestWindowMisfireEvents_KeepsEverything_When_NoWindow(t *testing.T) {
	evs := []event.Event{{Kind: event.KindShell}} // no timestamp
	got, err := windowMisfireEvents(evs, "", "")
	if err != nil || len(got) != 1 {
		t.Errorf("got %d events, err %v; want the input untouched", len(got), err)
	}
}

func TestWindowMisfireEvents_RefusesBadDate(t *testing.T) {
	if _, err := windowMisfireEvents(nil, "14d", ""); !errors.Is(err, errBadSince) {
		t.Errorf("err = %v; want errBadSince", err)
	}
	if _, err := windowMisfireEvents(nil, "", "yesterday"); !errors.Is(err, errBadUntil) {
		t.Errorf("err = %v; want errBadUntil", err)
	}
	if _, err := windowMisfireEvents(nil, "2026-10-19", "2026-10-01"); !errors.Is(err, errReachWindow) {
		t.Errorf("err = %v; want errReachWindow for --since after --until", err)
	}
}

// The wrong-flag section renders command, flag, chains (measured and
// inferred apart), sessions, calls and the last-seen date.
func TestWriteMisfiresText_RendersWrongFlagSection_When_FlagRowsExist(t *testing.T) {
	evs := []event.Event{unknownFlagEv("s1", "2026-10-12", "bd_list", "bd list --short", "--short")}
	rep := mine.MineMisfires(evs)
	rep.Flags = mine.MineFlagMisfires(evs)
	var buf bytes.Buffer
	if err := writeMisfiresText(&buf, rep, 0, 0); err != nil {
		t.Fatalf("writeMisfiresText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"wrong flags", "bd list", "--short", "chains=1", "measured=1", "inferred=0",
		"sessions=1", "calls=1/1", "last=2026-10-12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteMisfiresJSON_EmitsFlags_When_FlagRowsExist(t *testing.T) {
	evs := []event.Event{unknownFlagEv("s1", "2026-10-12", "bd_list", "bd list --short", "--short")}
	rep := mine.MineMisfires(evs)
	rep.Flags = mine.MineFlagMisfires(evs)
	var buf bytes.Buffer
	if err := writeMisfiresJSON(&buf, rep, 0); err != nil {
		t.Fatalf("writeMisfiresJSON: %v", err)
	}
	var doc struct {
		Flags      []mine.FlagRow `json:"flags"`
		FlagsTotal int            `json:"flagsTotal"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.FlagsTotal != 1 || len(doc.Flags) != 1 || doc.Flags[0].Flag != "--short" {
		t.Errorf("doc = %+v; want one --short flag row", doc)
	}
}
