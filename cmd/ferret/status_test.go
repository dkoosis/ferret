package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/mine"
)

func readyStatus() status {
	return status{
		Data: "/tmp/.ferret", Ready: true,
		BuiltAt:  time.Date(2026, 8, 11, 21, 43, 39, 0, time.UTC),
		Events:   149060,
		Sessions: 1416,
		Waste:    2000, Render: 9000,
		Top: []mine.WasteRow{
			{Key: "SendMessage", Source: mine.WasteFail, WastedBytes: 1200, Occurrences: 684},
			{Key: "sh:git_status", Source: mine.WasteRepeat, WastedBytes: 800, Occurrences: 1348},
		},
	}
}

// AXI #8: a no-args run shows live data. The whole view must also stay short
// enough to read at a glance — the help wall it replaces was 40 lines.
func TestWriteStatusText_ShowsCorpusAndTopWaste_When_CorpusIsReady(t *testing.T) {
	var buf bytes.Buffer
	if err := writeStatusText(&buf, readyStatus(), 0); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"149060 events", "1416 sessions", "waste 2.0KB of 8.8KB", "SendMessage", "sh:git_status", "next:"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n > 15 {
		t.Errorf("status is %d lines, want ≤ 15 (compact is the point)\n---\n%s", n, got)
	}
}

// A stale corpus must say so AND offer the refresh — the failure mode this
// guards is day-1 data mined for weeks (ferret-17q).
func TestWriteStatusText_FlagsStalenessAndOffersIngest_When_TranscriptsAreNewer(t *testing.T) {
	st := readyStatus()
	st.Stale = true
	st.Newest = time.Date(2026, 8, 12, 18, 30, 0, 0, time.UTC)

	var buf bytes.Buffer
	if err := writeStatusText(&buf, st, 0); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "STALE") {
		t.Errorf("stale corpus must say so\n---\n%s", got)
	}
	if !strings.Contains(got, "ferret ingest") {
		t.Errorf("stale corpus must offer the refresh\n---\n%s", got)
	}
}

// The converse: an always-on refresh hint is noise a reader learns to skip.
func TestWriteStatusText_OmitsIngestHint_When_CorpusIsCurrent(t *testing.T) {
	var buf bytes.Buffer
	if err := writeStatusText(&buf, readyStatus(), 0); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	if strings.Contains(buf.String(), "ferret ingest") {
		t.Errorf("current corpus should not suggest a re-ingest\n---\n%s", buf.String())
	}
}

// No corpus is a state to report, not a failure — and never a trigger for a
// minutes-long ingest the caller did not ask for.
func TestWriteStatusText_ReportsMissingCorpusWithHint_When_NotIngested(t *testing.T) {
	var buf bytes.Buffer
	if err := writeStatusText(&buf, status{Data: "/tmp/.ferret", Note: "no corpus"}, 0); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "no corpus") || !strings.Contains(got, "ferret ingest") {
		t.Errorf("missing-corpus view must name the state and the fix\n---\n%s", got)
	}
}

func TestReadStatus_ReportsNoCorpus_When_ManifestIsAbsent(t *testing.T) {
	st := readStatus(&common{data: t.TempDir()})
	if st.Ready {
		t.Errorf("ready = true on an empty data dir; got %+v", st)
	}
	if st.Note != "no corpus" {
		t.Errorf("note = %q, want %q", st.Note, "no corpus")
	}
}

func TestWriteStatusJSON_CarriesTheWholeView_When_CorpusIsReady(t *testing.T) {
	var buf bytes.Buffer
	if err := writeStatusJSON(&buf, readyStatus()); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	var got status
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n---\n%s", err, buf.String())
	}
	if !got.Ready || got.Events != 149060 || len(got.Top) != 2 {
		t.Errorf("round-trip lost data: %+v", got)
	}
}
