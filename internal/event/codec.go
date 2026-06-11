package event

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Manifest records what an events.jsonl was built from.
type Manifest struct {
	CreatedAt time.Time `json:"createdAt"`
	Root      string    `json:"root"`
	Stats     *Stats    `json:"stats"`
}

// Writer streams events to an ndjson artifact. Writes go to path+".tmp" and
// are atomically renamed onto path only on a fully successful flush+sync+close,
// so an interrupted run never truncates or corrupts a prior artifact in place.
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
		if w.tmp != "" {
			_ = os.Remove(w.tmp)
		}
		return err
	}
	if w.tmp != "" {
		return os.Rename(w.tmp, w.path)
	}
	return nil
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

func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
