package out

import (
	"bytes"
	"strings"
	"testing"
)

func TestNext_RendersInline_When_OneCommand(t *testing.T) {
	var buf bytes.Buffer
	Next(&buf, "ferret burn")
	if got := buf.String(); got != "next: ferret burn\n" {
		t.Errorf("got %q, want a single inline next line", got)
	}
}

func TestNext_RendersIndentedMenu_When_SeveralCommands(t *testing.T) {
	var buf bytes.Buffer
	Next(&buf, "ferret polling", "ferret misfires")
	want := "next:\n  ferret polling\n  ferret misfires\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The empty case is the rule the hand-rolled versions each had to restate:
// pointing a reader at a drill-down that returns nothing is the loop these
// hints exist to prevent.
func TestNext_WritesNothing_When_NoCommands(t *testing.T) {
	var buf bytes.Buffer
	Next(&buf)
	Next(&buf, "", "")
	if buf.Len() != 0 {
		t.Errorf("got %q, want no output", buf.String())
	}
}

func TestNext_DropsEmptyCommands_When_HintIsConditional(t *testing.T) {
	var buf bytes.Buffer
	Next(&buf, "", "ferret burn", "")
	if got := buf.String(); got != "next: ferret burn\n" {
		t.Errorf("got %q, want the one non-empty command inline", got)
	}
}

// NextHead goes through the uncapped header path, so a --limit that truncates
// every row still leaves the hint visible — the truncated case is exactly when
// a reader needs to know where to go next.
func TestSinkNextHead_SurvivesRowTruncation_When_LimitIsSpent(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, 1, 0)
	s.Row("row one")
	s.Row("row two")
	s.NextHead("ferret friction")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "next: ferret friction") {
		t.Errorf("hint dropped by the row budget:\n%s", got)
	}
	if !strings.Contains(got, "+1 more") {
		t.Errorf("truncation tail missing:\n%s", got)
	}
}
