package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// errRuntimeFixture stands in for any non-validation failure — the "the
// command ran and could not finish" side of the exit-code split.
var errRuntimeFixture = errors.New("disk on fire")

// The DK-AXI operating contract (kg reference.decision 03447c82d743) applied
// to ferret's CLI. These pin the three rules that are invisible in normal use
// and only bite a caller who is parsing output or reading exit codes.

// Rule 3 + 4: a mistyped command line is distinguishable from a run that
// tried and failed. Every validation sentinel is usage; everything else is a
// failure.
func TestExitCodeFor_SeparatesUsageFromFailure_When_ErrorIsValidation(t *testing.T) {
	usage := []error{errBadFormat, errBadKind, errBadSource, errMaxBytesJSON, errMaxBytesMD, errMinSupport, errMaxGap, errMaxLen, errOrder}
	for _, err := range usage {
		if got := exitCodeFor(err); got != exitUsage {
			t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, exitUsage)
		}
		// Wrapped is the shape callers actually return (`%w: %q`).
		if got := exitCodeFor(fmt.Errorf("%w: %q", err, "x")); got != exitUsage {
			t.Errorf("wrapped exitCodeFor(%v) = %d, want %d", err, got, exitUsage)
		}
	}
	if got := exitCodeFor(errRuntimeFixture); got != exitFailure {
		t.Errorf("exitCodeFor(runtime error) = %d, want %d", got, exitFailure)
	}
}

// Rule: definitive empty states. Silence is indistinguishable from a failed
// run, so a model retries — the exact friction ferret exists to find.
func TestEmptyNote_SaysZeroExplicitly_When_TableIsEmpty(t *testing.T) {
	var buf bytes.Buffer
	sink := out.NewSink(&buf, 0, 0)
	emptyNote(sink, 0, "commands")
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "0 commands") {
		t.Errorf("got %q, want an explicit zero line", got)
	}
}

func TestEmptyNote_StaysSilent_When_TableHasRows(t *testing.T) {
	var buf bytes.Buffer
	sink := out.NewSink(&buf, 0, 0)
	emptyNote(sink, 3, "commands")
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("got %q, want nothing", buf.String())
	}
}

// Rule: truncation reports what it dropped. Sink counts every refused row, so
// a renderer that breaks at the first refusal always reports "+1 more" no
// matter how many it actually dropped. This pins the honest count on a
// representative renderer.
func TestWriteBurnText_ReportsEveryDroppedRow_When_LimitTruncates(t *testing.T) {
	res := &mine.BurnResult{Events: 9, Sessions: 3}
	for i := range 5 {
		res.Rows = append(res.Rows, mine.BurnRow{
			Key: fmt.Sprintf("sh:cmd%d", i), Calls: 1, OutBytes: 10, RenderCost: 100, RenderPerCall: 100,
		})
	}
	var buf bytes.Buffer
	if err := writeBurnText(&buf, res, 2, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	if !strings.Contains(buf.String(), "+3 more") {
		t.Errorf("truncation tail must count all 3 dropped rows\n---\n%s", buf.String())
	}
}

func TestWritePollingText_ReportsEveryDroppedRow_When_LimitTruncates(t *testing.T) {
	rep := mine.PollingReport{Sessions: 3}
	for i := range 5 {
		rep.Rows = append(rep.Rows, mine.PollingRow{
			Command: fmt.Sprintf("cmd %d", i), Key: "sh:cmd", TotalRepeats: 4, Sessions: 1, MaxPerSession: 4, Score: 4,
		})
	}
	var buf bytes.Buffer
	if err := writePollingText(&buf, rep, 2, 0); err != nil {
		t.Fatalf("writePollingText: %v", err)
	}
	if !strings.Contains(buf.String(), "+3 more") {
		t.Errorf("truncation tail must count all 3 dropped rows\n---\n%s", buf.String())
	}
}
