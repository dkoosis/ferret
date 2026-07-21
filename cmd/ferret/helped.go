package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/retrievalevent"
	"github.com/dkoosis/ferret/internal/score"
)

// cmdHelped wires the kong CLI to the live retrieval-outcome adjudicator: it
// segments a session, joins each retrieval-event search row to its owning task
// segment by timestamp (score.JoinLegs, mechanism A), and runs the deterministic
// helped lattice (score.AdjudicateEvents) over the result. The fixture-only
// AdjudicateEvents (bbp.14) becomes end-to-end here (bbp.16).
func cmdHelped() error {
	cmd := &CLI.Helped
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}

	res, err := segmentSession(root, cmd.Session)
	if err != nil {
		return err
	}

	events, err := readRetrievalEvents(cmd.Events)
	if err != nil {
		return err
	}

	legs := score.JoinLegs(res.Segments, events, segEvidence(res.Segments), segmentID(res))
	records, filtered := score.AdjudicateEvents(events, legs)

	return writeHelped(os.Stdout, res, records, filtered, cmd.Format)
}

// readRetrievalEvents loads the trixi sidecar JSONL from path, or from stdin when
// path is empty or "-".
func readRetrievalEvents(path string) ([]retrievalevent.Event, error) {
	if path == "" || path == "-" {
		return retrievalevent.ReadEventsFrom(os.Stdin, "<stdin>")
	}
	return retrievalevent.ReadEvents(path)
}

// segEvidence computes the per-segment episode-side signal the join hands to the
// lattice: the segment's rolled-up dialogue outcome (Tell), aligned by index.
// RepairAdjacent stays false in v1 — see score.SegEvidence — so the live join
// never emits the correlational `misled` verdict until read-adjacency lands.
func segEvidence(segs []score.Segment) []score.SegEvidence {
	ev := make([]score.SegEvidence, len(segs))
	for i := range segs {
		ev[i] = score.SegEvidence{
			Tell: classifyTurnsCross(segmentUserTurns(&segs[i]), nextOpensNewTask(segs, i)),
		}
	}
	return ev
}

// segmentID returns the stable identifier for a segment in this session's
// records (contract AC4). Session + 1-based boundary index disambiguates a
// segment within the interrupted-time-series; the record also carries
// session_id/agent_id/ts, so the index is the piece those don't supply.
func segmentID(res score.Result) func(score.Segment) string {
	return func(seg score.Segment) string {
		return fmt.Sprintf("%s#%d", res.Session, seg.Index)
	}
}

// writeHelped emits the adjudicated records. JSON is the machine feed for the
// interrupted-time-series reader (search-loop.4); text is the audit view.
func writeHelped(w io.Writer, res score.Result, records []score.HelpedRecord, filtered int, format string) error {
	if format == fmtJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Session  string               `json:"session"`
			Filtered int                  `json:"filtered"`
			Records  []score.HelpedRecord `json:"records"`
		}{res.Session, filtered, records})
	}

	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "helped session=%s records=%d", res.Session, len(records))
	if filtered > 0 {
		fmt.Fprintf(bw, " filtered=%d (agent_id degraded to session_id — contract Trap 2)", filtered)
	}
	fmt.Fprintln(bw)
	for i := range records {
		r := &records[i]
		fmt.Fprintf(bw, "[%s] search=%s segment=%s ts=%s", r.Verdict, r.SearchRef, r.SegmentID, r.TS)
		if r.Correlational {
			fmt.Fprint(bw, " (correlational)")
		}
		fmt.Fprintln(bw)
	}
	return bw.Flush()
}
