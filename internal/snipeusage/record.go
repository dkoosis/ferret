// Package snipeusage mirrors snipe's own usage-telemetry row shape
// (~/Projects/snipe/internal/telemetry/telemetry.go's Record, lines 45-57)
// and joins it to ferret's canonical Event stream by session+action+time
// (sn-r1do.2). This is ferret's LEAF, stdlib-only copy of that shape —
// ferret does not import the snipe module; it reads snipe's own
// .snipe/usage.jsonl files directly and decodes them with its own struct,
// so the two modules stay independently buildable (same rationale as
// internal/retrievalevent's mirror of trixi's contract, event.go:1-7).
package snipeusage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Record is one usage.jsonl row, field-for-field with snipe's
// internal/telemetry.Record (telemetry.go:45-57).
type Record struct {
	TS             string   `json:"ts"`
	Command        string   `json:"cmd"`
	Outcome        string   `json:"outcome"`
	Ms             int64    `json:"ms"`
	Caller         string   `json:"caller,omitempty"`
	Arg            string   `json:"arg,omitempty"`
	Rung           string   `json:"rung,omitempty"`
	CandidateCount int      `json:"candidate_count,omitempty"`
	IndexState     string   `json:"index_state,omitempty"`
	SessionKey     string   `json:"session_key,omitempty"`
	TriedRungs     []string `json:"tried_rungs,omitempty"`
}

// ReadRecords loads every record from a single usage.jsonl file, skipping
// malformed lines and rows with an empty Command — mirrors snipe's own
// ReadAll (telemetry.go:194-213), generalized from a fixed
// <root>/.snipe/usage.jsonl path to an arbitrary path, since ferret reads
// these files from inside OTHER repos' trees, not its own.
func ReadRecords(path string) ([]Record, error) {
	f, err := os.Open(path) // #nosec G304 -- path supplied by the ferret operator via --snipe-usage
	if err != nil {
		return nil, fmt.Errorf("snipeusage: open %s: %w", path, err)
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil || r.Command == "" {
			continue // malformed or empty-Command row — tolerated, not fatal
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("snipeusage: read %s: %w", path, err)
	}
	return out, nil
}

// ReadGlob expands pattern (filepath.Glob syntax) and concatenates
// ReadRecords over every match — the "accept a path/glob" shape the bead
// calls for (e.g. ~/Projects/*/.snipe/usage.jsonl matching many repos' own
// usage files).
func ReadGlob(pattern string) ([]Record, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("snipeusage: glob %s: %w", pattern, err)
	}
	var out []Record
	for _, m := range matches {
		recs, err := ReadRecords(m)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}
