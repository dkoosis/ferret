// Package out enforces the AX contract: dense text, json mode,
// and hard output budgets (--limit rows, --max-bytes total).
package out

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Sink writes capped output. Over-budget rows are counted, and Close
// reports them in a single tail line so truncation is never silent.
type Sink struct {
	w         *bufio.Writer
	limit     int // max rows; 0 = unlimited
	maxBytes  int // max bytes; 0 = unlimited
	rows      int
	bytes     int
	truncated int
}

func NewSink(w io.Writer, limit, maxBytes int) *Sink {
	return &Sink{w: bufio.NewWriter(w), limit: limit, maxBytes: maxBytes}
}

// Head writes an uncapped header line (doesn't count against the row limit).
func (s *Sink) Head(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	s.bytes += len(line) + 1
	fmt.Fprintln(s.w, line)
}

// Row writes one budgeted line. Returns false once the budget is spent.
func (s *Sink) Row(format string, args ...any) bool {
	line := fmt.Sprintf(format, args...)
	if (s.limit > 0 && s.rows >= s.limit) || (s.maxBytes > 0 && s.bytes+len(line)+1 > s.maxBytes) {
		s.truncated++
		return false
	}
	s.rows++
	s.bytes += len(line) + 1
	fmt.Fprintln(s.w, line)
	return true
}

func (s *Sink) Close() error {
	if s.truncated > 0 {
		fmt.Fprintf(s.w, "… +%d more (raise -limit / -max-bytes)\n", s.truncated)
	}
	return s.w.Flush()
}

// JSON emits v as a single JSON document, ignoring row limits
// (the caller pre-caps the slice it hands over).
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(v)
}
