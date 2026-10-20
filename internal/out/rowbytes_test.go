package out

import (
	"bytes"
	"strings"
	"testing"
)

// TestRowBytesIgnoresRowLimitButHonoursMaxBytes: a RowBytes line is not stopped
// by an exhausted row limit, yet it is stopped (and counted) by the byte budget.
func TestRowBytesIgnoresRowLimitButHonoursMaxBytes(t *testing.T) {
	var buf bytes.Buffer
	s := NewSink(&buf, 1, 30)
	if !s.Row("main row") { // spends the whole row limit
		t.Fatal("first Row refused")
	}
	if s.Row("second") {
		t.Fatal("second Row must hit the row limit")
	}
	if !s.RowBytes("extra %d", 1) {
		t.Error("RowBytes must ignore the spent row limit")
	}
	if s.RowBytes("%s", strings.Repeat("x", 40)) {
		t.Error("RowBytes must refuse a line past --max-bytes")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "extra 1") || strings.Contains(got, "xxxx") || !strings.Contains(got, "+2 more") {
		t.Errorf("unexpected output:\n%s", got)
	}
}
