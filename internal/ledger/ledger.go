// Package ledger consolidates the append-only JSONL ledger mechanics that used
// to be hand-rolled, nearly byte-for-byte identical, in internal/label,
// internal/fixes, and internal/spool: an uncapped trailing-line reader
// (ReadLines) and the durable single-record append spine (AppendJSON) — open,
// marshal, write, fsync, close. A durability fix here (ferret-isz was the
// pattern: two concurrent over-threshold appends splicing into one torn line)
// now lands once instead of needing to be ported into three copies by hand.
//
// Locking and first-create parent-dir fsync stay with each caller rather than
// living inside AppendJSON: a lock's scope sometimes needs to cover more than
// the write itself (spool.Writer.Append takes its lock before an on-disk dedup
// check that must run before the write is decided), and firstCreate detection
// stays paired with each package's own syncDir seam so existing tests that hook
// a package's syncDir var keep observing the real call. AppendJSON owns only the
// inner bytes-on-disk spine that was genuinely byte-identical across all three.
//
// Corruption-tolerant parsing (a single salvageable trailing line vs. a hard
// mid-ledger error) also stays bespoke per caller: label and fixes parse into a
// slice, but spool's parseIDs collects into a dedup set, not a slice, so a
// generic Parse[T any] wouldn't fit spool cleanly. Rather than migrate two of
// three callers and leave the third as an odd one out, all three keep their own
// parse function — each now built on ledger.ReadLines instead of a hand-rolled
// copy of it.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadLines returns every non-blank line of f, in order. It uses an uncapped
// bufio.Reader rather than a bufio.Scanner: a single ledger row can carry
// arbitrary user text (a "say-more" label, a --note, a transcript_path) that
// pushes one line past any fixed token cap, and Scanner would surface
// bufio.ErrTooLong before a caller's trailing-corruption-salvage loop ever runs
// — hard-erroring the whole ledger and defeating the salvage guarantee.
// ReadString has no such limit.
func ReadLines(f *os.File) ([]string, error) {
	var lines []string
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			lines = append(lines, s)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return lines, nil // the final unterminated line was captured above
			}
			return nil, rerr
		}
	}
}

// AppendJSON durably appends one JSON-marshaled record as a line to the ledger
// at path, creating the parent dir if it does not yet exist. The line is
// flushed and fsync'd before AppendJSON returns, so a record it reports success
// for is not lost to a crash. AppendJSON does not lock and does not fsync the
// parent dir on first create — see the package doc for why those two steps stay
// with the caller.
func AppendJSON[T any](path string, v T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errors.Join(err, f.Close())
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}
