package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// syncDir fsyncs a directory so a just-published rename within it survives a
// crash/power-loss. os.Rename only mutates the directory entry; the dir inode
// must itself be fsync'd or the rename can be lost in the dirty-inode window.
// It is a package var so tests can observe that the publish path invokes it.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		return errors.Join(err, d.Close())
	}
	return d.Close()
}

// removeFile is the os.Remove seam so a test can force a temp-cleanup failure
// and assert it is surfaced rather than silently swallowed.
var removeFile = os.Remove

// stderr is the diagnostic-log seam. The codec's failure paths (rolled-back
// temp cleanup, salvaged truncated read) log to it instead of os.Stderr
// directly so a test can capture the line with a plain buffer rather than
// hijacking the process-global os.Stderr through a pipe.
var stderr io.Writer = os.Stderr

// removeTmp deletes a rolled-back temp artifact, surfacing a removal failure to
// stderr instead of swallowing it. A silently-dropped failure leaves an orphan
// events.jsonl.tmp that looks like an in-progress ingest and is undetectable by
// callers; the log lets an operator tell a stale rollback artifact from a live
// run. An already-absent tmp is the success case (nothing orphaned), so
// os.ErrNotExist is not reported. Content integrity is unaffected either way —
// the next ingest's os.Create truncates any lingering tmp.
func removeTmp(tmp string) {
	if tmp == "" {
		return
	}
	if err := removeFile(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "ferret: %s: temp cleanup failed: %v; orphan .tmp may linger (re-ingest to repair)\n", tmp, err)
	}
}

// Manifest records what an events.jsonl was built from.
type Manifest struct {
	CreatedAt time.Time `json:"createdAt"`
	Root      string    `json:"root"`
	Stats     *Stats    `json:"stats"`
}

// Writer streams events to an ndjson artifact. Writes go to path+".tmp" and
// are atomically renamed onto path only on a fully successful flush+sync+close,
// so an interrupted run never truncates or corrupts a prior artifact in place.
// After the rename the parent directory is fsync'd so the publish survives a
// crash/power-loss — os.Rename alone only dirties the dir inode.
type Writer struct {
	f      *os.File
	buf    *bufio.Writer
	enc    *json.Encoder
	closed bool
	tmp    string // temp file actually written; "" when constructed directly
	path   string // final destination for the atomic rename
}

func NewWriter(path string) (*Writer, error) {
	// Per-process UNIQUE temp (random infix), not a fixed path+".tmp": two
	// ingests racing on the same data dir would both os.Create the same fixed
	// tmp and interleave NDJSON into one shredded file, and the last rename would
	// publish the mix (ferret-0vz). With a unique tmp each run writes its own and
	// the atomic rename publishes exactly one run's events. Matches gen-corpus.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(f, 1<<20)
	return &Writer{f: f, buf: buf, enc: json.NewEncoder(buf), tmp: f.Name(), path: path}, nil
}

func (w *Writer) Write(ev *Event) error { return w.enc.Encode(ev) }

func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.finish(); err != nil {
		// Failed run: drop the partial temp file, leave any prior path intact.
		removeTmp(w.tmp)
		return err
	}
	if w.tmp != "" {
		if err := os.Rename(w.tmp, w.path); err != nil {
			return err
		}
		// fsync the parent dir so the rename itself is crash-durable.
		return syncDir(filepath.Dir(w.path))
	}
	return nil
}

// Abort closes the underlying file and removes the temp file without renaming
// it onto the final path. Use when the ingest run must be rolled back and the
// artifact must not be published — prevents orphan files.
func (w *Writer) Abort() {
	if w.closed {
		return
	}
	w.closed = true
	_ = w.f.Close()
	removeTmp(w.tmp)
}

// finish flushes buffered bytes, fsyncs durable, and closes the fd. The fd is
// always closed (even on flush/sync error), and errors are joined so callers
// see every failure on the path.
func (w *Writer) finish() error {
	if err := w.buf.Flush(); err != nil {
		return errors.Join(err, w.f.Close())
	}
	if err := w.f.Sync(); err != nil {
		return errors.Join(err, w.f.Close())
	}
	return w.f.Close()
}

// Read streams events back from the artifact.
//
// The writer emits one JSON object per line, so Read decodes line-by-line
// rather than running a single json.Decoder over the whole stream. A decoder
// spanning multiple records can't tell a genuinely truncated tail from a
// mid-stream object with unbalanced braces: a dangling key with no value
// (e.g. `{"i":2,"p":`) makes the decoder swallow every following well-formed
// object as that key's nested value until it runs out of input, then reports
// the entire mess as one misleadingly-small "truncated trailing record"
// salvage — silently dropping real records. Decoding line-by-line bounds a
// malformed line to itself.
//
// A single truncated trailing line — the signature of an ingest interrupted
// mid-write (SIGINT, disk-full, power loss) — is tolerated: every fully
// decoded event ahead of it is delivered, and the dangling fragment is logged
// to stderr and dropped rather than failing the whole read. Only a final line
// that the writer never finished — no trailing newline, the structural mark
// of a write cut off mid-record — may be salvaged this way; a complete,
// newline-terminated final line that fails to parse is genuine corruption
// (garbage, not a cutoff) and is reported as an error like any other
// malformed line. A malformed line with more lines still following is
// likewise genuine mid-stream corruption — it is logged distinctly and
// skipped, records after it are still delivered, and Read reports the
// corruption as an error once the file is exhausted.
//
// Lines are read via a growing bufio.Reader rather than bufio.Scanner:
// Scanner enforces a fixed max token size, but event.Prompt stores the full,
// untruncated user-turn text (event.go), which can legitimately exceed any
// fixed cap.
func Read(path string, fn func(*Event) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)

	var corrupt error
	lineNo := 0
	line, ok, delimited, err := readLine(r)
	if err != nil {
		return err
	}
	for ok {
		lineNo++
		// Read ahead now: whether another line follows determines if a
		// decode failure on the current line is a final-line salvage or
		// mid-stream corruption.
		next, nextOK, nextDelimited, err := readLine(r)
		if err != nil {
			return err
		}

		stop, stopErr := processLine(path, lineNo, line, nextOK, delimited, &corrupt, fn)
		if stop {
			return stopErr
		}
		line, ok, delimited = next, nextOK, nextDelimited
	}
	return corrupt
}

// processLine handles one already-read line on Read's behalf: skips blanks,
// applies decodeLine's salvage rule, and invokes fn on a successful decode.
// stop=true tells Read to return immediately with stopErr — either a clean
// truncated-salvage stop (stopErr nil) or a propagated callback error;
// *corrupt accumulates the first mid-stream corruption error seen so far.
func processLine(path string, lineNo int, line []byte, hasMore, delimited bool, corrupt *error, fn func(*Event) error) (stop bool, stopErr error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false, nil
	}
	ev, truncated, lineErr := decodeLine(path, lineNo, trimmed, hasMore, delimited)
	if truncated {
		return true, nil
	}
	if lineErr != nil {
		if *corrupt == nil {
			*corrupt = lineErr
		}
		return false, nil
	}
	if err := fn(&ev); err != nil {
		return true, err
	}
	return false, nil
}

// readLine reads one '\n'-delimited line from r, trailing delimiter
// stripped. ok=false with err=nil signals clean EOF (no more data).
// delimited reports whether a trailing '\n' actually terminated the line —
// false means the reader hit EOF mid-line, the structural signature of a
// write interrupted before it finished. bufio.Reader has no fixed max-line
// ceiling (unlike bufio.Scanner): it grows its buffer as needed, bounded
// only by available memory.
func readLine(r *bufio.Reader) (line []byte, ok, delimited bool, err error) {
	b, readErr := r.ReadBytes('\n')
	if len(b) == 0 && errors.Is(readErr, io.EOF) {
		return nil, false, false, nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, false, readErr
	}
	if bytes.HasSuffix(b, []byte("\n")) {
		return b[:len(b)-1], true, true, nil
	}
	// readErr == io.EOF with no trailing '\n': the file ended mid-line.
	return b, true, false, nil
}

// decodeLine applies Read's salvage rule to one line: a malformed line with
// more input still pending (hasMore) is mid-stream corruption — logged and
// returned as an error so the caller can remember it and continue. A
// malformed FINAL line is only the tolerated truncated-trailing-record case
// when the write was structurally interrupted (!delimited, no trailing
// newline); a complete-but-garbled final line (delimited) is corruption like
// any other malformed line, not a salvage.
func decodeLine(path string, lineNo int, trimmed []byte, hasMore, delimited bool) (ev Event, truncated bool, corrupt error) {
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		if !hasMore && !delimited {
			fmt.Fprintf(stderr, "ferret: %s: truncated trailing record dropped (1 fragment); re-ingest to repair\n", path)
			return Event{}, true, nil
		}
		fmt.Fprintf(stderr, "ferret: %s: line %d: malformed record skipped: %v\n", path, lineNo, err)
		return Event{}, false, fmt.Errorf("%s: line %d: malformed record: %w", path, lineNo, err)
	}
	return ev, false, nil
}

// WriteManifest publishes the completeness sentinel atomically and durably.
// A bare os.WriteFile truncates-then-writes in place: a crash mid-write would
// leave a present-but-0-byte/partial manifest that ensureData's existence gate
// would mistake for a complete corpus. Instead we write path+".tmp", fsync it
// durable, then rename onto path — the rename is atomic, so a reader sees
// either the old manifest or the fully-written new one, never a fragment.
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// fsync the parent dir so the manifest rename is crash-durable and cannot
	// diverge from the events.jsonl rename across a power-loss window.
	return syncDir(filepath.Dir(path))
}
