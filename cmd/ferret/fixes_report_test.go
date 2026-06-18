package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/mine"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written. warnLensDivergence logs the operator warning there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestWarnLensDivergence guards ferret-s3z path (b): a fix recorded under lens A
// changes the join key when the report runs under lens B (mark-fail appends '!',
// etc.), so the entry silently matches nothing. report must WARN on the
// unmatched, differing-lens entry rather than miss it quietly — and must stay
// silent for entries that matched or share the report's lens.
func TestWarnLensDivergence(t *testing.T) {
	corpus := &mine.Corpus{Vocab: []string{"Edit!", "Read", "Grep"}}
	// A finding whose key is "Edit!,Read" — present this run under lens=tool.
	f := &mine.Finding{IDs: []uint32{0, 1}}
	at := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)

	idx := map[string]fixes.Entry{
		// Matched this run, same lens → no warning.
		"Edit!,Read": {Motif: "Edit!,Read", Fix: "hookify", AddedAt: at, Lens: "tool"},
		// Recorded under a different lens, matched nothing → the divergence to warn.
		"Grep,Read": {Motif: "Grep,Read", Fix: "skill", AddedAt: at, Lens: "fail"},
	}

	out := captureStderr(t, func() {
		warnLensDivergence(idx, corpus, []*mine.Finding{f}, nil, "tool")
	})
	if !strings.Contains(out, "different lens") {
		t.Errorf("expected a lens-divergence warning, got %q", out)
	}

	// All entries share the report lens → silence.
	idxSameLens := map[string]fixes.Entry{
		"Grep,Read": {Motif: "Grep,Read", Fix: "skill", AddedAt: at, Lens: "tool"},
	}
	if out := captureStderr(t, func() {
		warnLensDivergence(idxSameLens, corpus, []*mine.Finding{f}, nil, "tool")
	}); strings.Contains(out, "different lens") {
		t.Errorf("no warning expected when lenses match, got %q", out)
	}
}
