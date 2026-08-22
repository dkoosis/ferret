package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

// wasteReport builds a minimal merged report for the render tests — the merge
// itself (pricing, ranking, exclusions) is pinned in
// internal/mine/friction_test.go; these pin the CLI render layer.
func wasteReport() mine.WasteReport {
	return mine.WasteReport{
		Events: 40, Sessions: 6, TotalWasted: 1900, TotalBytes: 9000,
		BySource: map[mine.WasteSource]int{mine.WasteRepeat: 1200, mine.WasteFail: 500, mine.WasteMotif: 200},
		Rows: []mine.WasteRow{
			{Key: "sh:git_status", Source: mine.WasteRepeat, WastedBytes: 1200, Occurrences: 9,
				Sessions: 3, GrossBytes: 4000, BytesPerCall: 133, Detail: "git status --short"},
			{Key: "Edit", Source: mine.WasteFail, WastedBytes: 500, Occurrences: 5,
				Sessions: 2, GrossBytes: 5000, BytesPerCall: 100},
			{Key: "Read ⇝ Edit", Source: mine.WasteMotif, WastedBytes: 200, Occurrences: 4, Sessions: 2},
		},
	}
}

// frictionRowsOut scopes an assertion past the about() preamble, which names
// several command keys in prose and would otherwise satisfy a bare Contains.
func frictionRowsOut(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "friction rows=")
	if i < 0 {
		t.Fatalf("missing friction rows= header\n---\n%s", out)
	}
	return out[i:]
}

func TestWriteFrictionText_RanksWasteFirstInTheEyePath_When_RowsAreMixedSources(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionText(&buf, wasteReport(), 0, 0); err != nil {
		t.Fatalf("writeFrictionText: %v", err)
	}
	rows := frictionRowsOut(t, buf.String())

	// Miner order must survive the render verbatim.
	poll, fail, motif := strings.Index(rows, "sh:git_status"), strings.Index(rows, " Edit\n"), strings.Index(rows, "Read ⇝ Edit")
	if poll >= fail || fail >= motif {
		t.Errorf("row order poll=%d misfire=%d motif=%d, want miner order preserved\n---\n%s", poll, fail, motif, rows)
	}
	for _, want := range []string{"1.2KB waste", "poll", "misfire", "motif", `"git status --short"`} {
		if !strings.Contains(rows, want) {
			t.Errorf("output missing %q\n---\n%s", want, rows)
		}
	}
}

// The header carries the corpus size for scale — a waste figure with nothing
// beside it can't tell a reader whether it's worth a fix. It is context, not a
// denominator: see the upper-bound test below.
func TestWriteFrictionText_ReportsGrossTotalForScale_When_CorpusIsScored(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionText(&buf, wasteReport(), 0, 0); err != nil {
		t.Fatalf("writeFrictionText: %v", err)
	}
	hdr := frictionRowsOut(t, buf.String())
	if !strings.Contains(hdr, "gross=8.8KB") {
		t.Errorf("header missing the corpus gross total\n---\n%s", hdr)
	}
}

// DK-AXI: a definitive empty state. Silence reads to a model as a failed run,
// and it retries.
func TestWriteFrictionText_SaysZeroRows_When_CorpusHasNoWaste(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionText(&buf, mine.WasteReport{Events: 3, Sessions: 1}, 0, 0); err != nil {
		t.Fatalf("writeFrictionText: %v", err)
	}
	if !strings.Contains(buf.String(), "0 rows") {
		t.Errorf("empty report must say so explicitly\n---\n%s", buf.String())
	}
}

// Sink counts every refused row, so a renderer that breaks at the first
// refusal reports "+1 more" no matter how many it dropped.
func TestWriteFrictionText_ReportsEveryDroppedRow_When_LimitTruncates(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionText(&buf, wasteReport(), 1, 0); err != nil {
		t.Fatalf("writeFrictionText: %v", err)
	}
	if !strings.Contains(buf.String(), "+2 more") {
		t.Errorf("truncation tail must count all 2 dropped rows\n---\n%s", buf.String())
	}
}

func TestWriteFrictionJSON_CapsRowsButNotTotals_When_LimitTruncates(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionJSON(&buf, wasteReport(), 2); err != nil {
		t.Fatalf("writeFrictionJSON: %v", err)
	}
	var got struct {
		Rows        []mine.WasteRow `json:"rows"`
		TotalWasted int             `json:"totalWasted"`
		Total       int             `json:"total"`
		Truncated   bool            `json:"truncated"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n---\n%s", err, buf.String())
	}
	if len(got.Rows) != 2 || got.Total != 3 || !got.Truncated {
		t.Errorf("rows=%d total=%d truncated=%v, want 2/3/true", len(got.Rows), got.Total, got.Truncated)
	}
	if got.TotalWasted != 1900 {
		t.Errorf("totalWasted = %d, want the uncapped corpus figure 1900", got.TotalWasted)
	}
}

// DK-AXI rule 3: an unknown flag value is fatal, never accepted and dropped.
func TestValidateWasteSource_Rejects_When_SourceIsUnknown(t *testing.T) {
	for _, ok := range []string{"", "poll", "misfire", "motif"} {
		if err := validateWasteSource(ok); err != nil {
			t.Errorf("validateWasteSource(%q) = %v, want nil", ok, err)
		}
	}
	if err := validateWasteSource("burn"); !errors.Is(err, errBadSource) {
		t.Errorf("validateWasteSource(%q) = %v, want errBadSource", "burn", err)
	}
}

func TestFilterWasteReport_KeepsOnlyTheNamedDetector_When_SourceIsSet(t *testing.T) {
	got := filterWasteReport(wasteReport(), mine.WasteRepeat)
	if len(got.Rows) != 1 || got.Rows[0].Source != mine.WasteRepeat {
		t.Errorf("filtered rows = %+v, want the single poll row", got.Rows)
	}
}

// Filtering rows without re-summing prints a header that counts detectors the
// table no longer shows — `--source poll` reporting misfire waste.
func TestFilterWasteReport_ReDerivesTotals_When_SourceIsSet(t *testing.T) {
	got := filterWasteReport(wasteReport(), mine.WasteRepeat)
	if got.TotalWasted != 1200 {
		t.Errorf("totalWasted = %d, want 1200 (the poll row alone, not the 1900 all-source sum)", got.TotalWasted)
	}
	if got.BySource[mine.WasteFail] != 0 || got.BySource[mine.WasteRepeat] != 1200 {
		t.Errorf("bySource = %v, want only the poll subtotal", got.BySource)
	}
	if got.TotalBytes != 9000 {
		t.Errorf("totalBytes = %d, want the corpus denominator untouched", got.TotalBytes)
	}
}

// The detectors overlap, so the sum is an upper bound. The header must not
// present it as a share of the corpus total, and must show the split that IS
// sound.
func TestWriteFrictionText_LabelsWasteAsUpperBoundWithSplit_When_SourcesOverlap(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrictionText(&buf, wasteReport(), 0, 0); err != nil {
		t.Fatalf("writeFrictionText: %v", err)
	}
	hdr := frictionRowsOut(t, buf.String())
	if !strings.Contains(hdr, "waste≤") {
		t.Errorf("total must be marked an upper bound\n---\n%s", hdr)
	}
	if strings.Contains(hdr, "waste=1.9KB of") {
		t.Errorf("total must not read as a fraction of the corpus total\n---\n%s", hdr)
	}
	for _, want := range []string{"poll 1.2KB", "misfire 500B", "motif 200B"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header missing per-source subtotal %q\n---\n%s", want, hdr)
		}
	}
}
