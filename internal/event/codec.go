package event

import (
	"bufio"
	"encoding/json"
	"errors"
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
