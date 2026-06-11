package event

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Manifest records what an events.jsonl was built from.
type Manifest struct {
	CreatedAt time.Time `json:"createdAt"`
	Root      string    `json:"root"`
	Stats     *Stats    `json:"stats"`
}

// Writer streams events to an ndjson artifact.
type Writer struct {
	f   *os.File
	buf *bufio.Writer
	enc *json.Encoder
}

func NewWriter(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(f, 1<<20)
	return &Writer{f: f, buf: buf, enc: json.NewEncoder(buf)}, nil
}

func (w *Writer) Write(ev *Event) error { return w.enc.Encode(ev) }

func (w *Writer) Close() error {
	if err := w.buf.Flush(); err != nil {
		w.f.Close()
		return err
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
