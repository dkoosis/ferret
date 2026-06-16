package event

import (
	"bufio"
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
		fmt.Fprintf(os.Stderr, "ferret: %s: temp cleanup failed: %v; orphan .tmp may linger (re-ingest to repair)\n", tmp, err)
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
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(f, 1<<20)
	return &Writer{f: f, buf: buf, enc: json.NewEncoder(buf), tmp: tmp, path: path}, nil
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
// A single truncated trailing record — the signature of an ingest interrupted
// mid-write (SIGINT, disk-full, power loss) — is tolerated: every fully decoded
// event ahead of it is delivered, and the dangling fragment is logged to stderr
// and dropped rather than failing the whole read. Only the FINAL record may be
// salvaged this way; a decode error with more input still pending is a genuinely
// corrupt mid-stream record and is returned as a hard error.
func Read(path string, fn func(*Event) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	for dec.More() {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			// A truncated trailing object surfaces as an unexpected EOF (or a
			// bare EOF mid-token). dec.More() returned true, so bytes were
			// present, but the object never completed and no input follows.
			// Salvage the events already streamed instead of poisoning the run.
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr, "ferret: %s: truncated trailing record dropped (1 fragment); re-ingest to repair\n", path)
				return nil
			}
			return err
		}
		if err := fn(&ev); err != nil {
			return err
		}
	}
	return nil
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
