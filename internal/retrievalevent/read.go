package retrievalevent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// errSchemaMismatch is the static sentinel a row's schema_version mismatch
// wraps (contract Trap 1): a shape change that isn't version-bumped must
// break the join loudly, not compute silently-wrong numbers.
var errSchemaMismatch = errors.New("retrievalevent: schema_version mismatch")

// ReadEvents streams a vendored retrieval-event JSONL file (contract
// producer stream) into ferret's leaf Event mirror. Every row's
// schema_version is asserted against SchemaVersion before the row is
// trusted; a mismatch returns an error immediately (contract Trap 1) instead
// of silently joining on a shape ferret was not built against. Blank lines
// are skipped.
func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("retrievalevent: open %s: %w", path, err)
	}
	defer f.Close()

	var events []Event
	r := bufio.NewReaderSize(f, 1<<20)
	lineNo := 0
	for {
		line, rerr := r.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		lineNo++
		if len(line) > 0 {
			e, derr := decodeLine(path, lineNo, line)
			if derr != nil {
				return nil, derr
			}
			events = append(events, e)
		}
		if rerr == io.EOF {
			return events, nil
		}
		if rerr != nil {
			return nil, fmt.Errorf("retrievalevent: %s: read: %w", path, rerr)
		}
	}
}

// decodeLine decodes one JSONL line into an Event, asserting schema_version
// before trusting the rest of the row (contract Trap 1). Split out of
// ReadEvents to keep the streaming loop's cognitive complexity low. Event
// carries schema_version itself, so one unmarshal both validates the version
// and populates the row.
func decodeLine(path string, lineNo int, line []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, fmt.Errorf("retrievalevent: %s:%d: decode: %w", path, lineNo, err)
	}
	if e.SchemaVersion != SchemaVersion {
		return Event{}, fmt.Errorf("%w: %s:%d: got %d, reader expects %d (shape may have changed)",
			errSchemaMismatch, path, lineNo, e.SchemaVersion, SchemaVersion)
	}
	return e, nil
}

// LinkReads resolves every read row's search_ref back to its owning search
// event, keyed by the search row's EventID (the collision-proof primary key
// — session_id/agent_id/ts alone admit a same-clock-tick collision). One
// search may surface more than one later read, so the value is a slice.
func LinkReads(events []Event) map[string][]Event {
	links := make(map[string][]Event)
	for i := range events {
		if events[i].Kind != KindRead || events[i].SearchRef == nil {
			continue
		}
		links[events[i].SearchRef.EventID] = append(links[events[i].SearchRef.EventID], events[i])
	}
	return links
}
