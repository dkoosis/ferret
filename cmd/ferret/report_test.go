package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
)

// TestSinceFixAnnotation guards the report --since-fixes join: a finding whose
// motif is in the ledger gets a "[fixed DATE burn BASE→NOW ↓]" suffix, the
// arrow reflects the burn direction, an unmatched motif gets nothing, and a nil
// index (flag off) is a safe no-op. The join is keyed on the comma-joined motif
// — the stable sort key — so it survives across ingests.
func TestSinceFixAnnotation(t *testing.T) {
	at := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	idx := fixes.Index([]fixes.Entry{
		{Motif: "Edit!,Read", Fix: "hookify read-before-edit", AddedAt: at, BaselineBurn: 253000},
	})

	// Matched motif, burn fell → ↓ with compacted before→after figures.
	got, ok := sinceFixAnnotation(idx, []string{"Edit!", "Read"}, 11000)
	if !ok {
		t.Fatal("expected a match for a ledgered motif")
	}
	for _, want := range []string{"fixed 2026-06-12", "253k", "11k", "↓"} {
		if !strings.Contains(got, want) {
			t.Errorf("annotation %q missing %q", got, want)
		}
	}

	// Matched motif, burn rose → ↑ (regression).
	if up, _ := sinceFixAnnotation(idx, []string{"Edit!", "Read"}, 300000); !strings.Contains(up, "↑") {
		t.Errorf("rising burn must read ↑, got %q", up)
	}

	// Unmatched motif → no annotation.
	if _, ok := sinceFixAnnotation(idx, []string{"Grep", "Read"}, 9000); ok {
		t.Error("unledgered motif must not annotate")
	}

	// Nil index (flag off) → safe no-op.
	if _, ok := sinceFixAnnotation(nil, []string{"Edit!", "Read"}, 11000); ok {
		t.Error("nil index must not annotate")
	}
}

// TestCompactBurn: inline annotations show a glance-readable magnitude — sub-1k
// verbatim, ≥1k as a k-suffixed integer.
func TestCompactBurn(t *testing.T) {
	for in, want := range map[int]string{
		0:      "0",
		999:    "999",
		1000:   "1k",
		11500:  "11k",
		253000: "253k",
	} {
		if got := compactBurn(in); got != want {
			t.Errorf("compactBurn(%d) = %q, want %q", in, got, want)
		}
	}
}
