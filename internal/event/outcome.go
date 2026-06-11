package event

import (
	"bufio"
	"encoding/json"
	"os"
)

// Outcome is a stream-level ground-truth label, kept in a separate artifact
// (outcomes.jsonl) rather than on Event: it is per-stream, not per-action.
// Sourced from corpora that carry labels (e.g. SWE-agent target/exit_status);
// CC ingest writes none.
type Outcome struct {
	Stream     string `json:"stream"` // "project/session@agent" — matches Corpus.StreamKeys
	Target     bool   `json:"target"` // issue resolved
	ExitStatus string `json:"exitStatus,omitempty"`
}

// OutcomeWriter streams outcomes to an ndjson artifact.
type OutcomeWriter struct {
	f   *os.File
	buf *bufio.Writer
	enc *json.Encoder
}

func NewOutcomeWriter(path string) (*OutcomeWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(f, 1<<16)
	return &OutcomeWriter{f: f, buf: buf, enc: json.NewEncoder(buf)}, nil
}

func (w *OutcomeWriter) Write(o *Outcome) error { return w.enc.Encode(o) }

func (w *OutcomeWriter) Close() error {
	if err := w.buf.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// ReadOutcomes loads the outcomes sidecar into a stream-keyed map. A missing
// file is not an error — it just means the corpus carries no labels.
func ReadOutcomes(path string) (map[string]Outcome, error) {
	out := map[string]Outcome{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil // no labels for this corpus — not an error
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<16))
	for dec.More() {
		var o Outcome
		if err := dec.Decode(&o); err != nil {
			return nil, err
		}
		out[o.Stream] = o
	}
	return out, nil
}
