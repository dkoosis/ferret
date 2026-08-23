package apiusage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/dkoosis/ferret/internal/durable"
)

// Artifact is the ledger's filename beside events.jsonl in the data dir.
const Artifact = "usage.jsonl"

// Write publishes the whole ledger atomically — temp file, rename, dir fsync —
// by reusing internal/durable rather than restating event.Writer's streaming
// path. The ledger is one row per API call, an order of magnitude fewer than
// events, so buffering it costs a few MB and buys a single audited write path
// instead of a second one to keep in sync.
func Write(path string, rows []Row) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range rows {
		if err := enc.Encode(&rows[i]); err != nil {
			return err
		}
	}
	return durable.WriteTempRename(path, buf.Bytes())
}

// Read streams the ledger back. A missing artifact is reported as such: a
// corpus ingested before the ledger existed has no usage.jsonl, and that is a
// state to name, not a crash.
func Read(path string, fn func(*Row) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			continue // one malformed line must not void a whole corpus read
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
