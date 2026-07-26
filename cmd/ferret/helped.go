package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/retrievalevent"
	"github.com/dkoosis/ferret/internal/score"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
)

// cmdHelped wires the kong CLI to the live retrieval-outcome adjudicator: it
// segments a session, joins each retrieval-event search row to its owning task
// segment by timestamp (score.JoinLegs, mechanism A), computes per-search
// read-adjacency (bbp.17), and runs the deterministic helped lattice
// (score.AdjudicateEvents) over the result. The fixture-only AdjudicateEvents
// (bbp.14) becomes end-to-end here (bbp.16).
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

	src, distinct, err := resolveSpineSource(root, cmd.Session)
	if err != nil {
		return err
	}
	if distinct > 1 {
		fmt.Fprintf(os.Stderr,
			"ferret: --session %q matched %d sessions; emitting %q (use a longer prefix to disambiguate)\n",
			cmd.Session, distinct, src.Session)
	}

	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	adjust, exclude, err := sessionProbeAdjustments(src, data)
	if err != nil {
		return err
	}

	res, err := score.SegmentSourceExcluding(src, adjust)
	if err != nil {
		return err
	}
	turns, err := sessionUserTurns(src.Path, exclude)
	if err != nil {
		return err
	}

	events, err := readRetrievalEvents(cmd.Events)
	if err != nil {
		return err
	}

	repairAdj := repairAdjacency(events, turns)
	legs := score.JoinLegs(res.Segments, events, segEvidence(res.Segments), repairAdj, segmentID(res))
	records, filtered := score.AdjudicateEvents(events, legs)

	return writeHelped(os.Stdout, res, records, filtered, cmd.Format)
}

// userTurn is one genuine user turn's dialogue move at a wall-clock instant —
// the timeline read-adjacency walks to find the turn right after a read.
type userTurn struct {
	ts   time.Time
	move dialogue.Move
}

// sessionUserTurns walks the transcript once and returns every genuine user
// turn (non-empty prompt, not a carrier/control fold-in) as {ts, move}, in
// transcript order. TagMove is the plain (context-free) tagger: read-adjacency
// only asks "is this a repair/reject", which is cue-based and needs no
// prior-question context. A line with an unparseable timestamp is skipped — it
// can't be placed after a read, so it can't carry adjacency.
//
// exclude (ferret-j33 §6, de-contamination) skips a line OUTRIGHT — before
// TagMove ever sees it — when the line's own timestamp is a key: a confirmed
// feedback-tap probe answer (buildProbeAdjustments) must never register as a
// repair/accept move, not even a blanked one, since repairAdjacency reads
// this list to attribute misled verdicts. nil (every caller but cmdHelped and
// runFeedbackJudge) preserves the exact prior behavior.
func sessionUserTurns(path string, exclude map[string]bool) ([]userTurn, error) {
	var turns []userTurn
	err := transcript.ReadLines(path, func(line []byte) error {
		raw, ok := decodeRaw(line)
		if !ok || raw.IsMeta || raw.Message == nil {
			return nil // tolerant: a bad line is skipped, not fatal (mirrors segmenter)
		}
		if raw.Type != roleUser {
			return nil
		}
		if exclude[raw.Timestamp] {
			return nil // a confirmed probe-answer turn — never a genuine dialogue move
		}
		prompt := turn.PromptText(raw.Message.Content)
		if prompt == "" {
			return nil
		}
		if skip, kind, _ := turn.ClassifyBoundary(prompt); skip && kind != "affirmation" {
			return nil // control/carrier envelopes are not user intent
		}
		ts, ok := parseNano(raw.Timestamp)
		if !ok {
			return nil // an unplaceable turn can't carry adjacency
		}
		move, _ := dialogue.TagMove(prompt)
		turns = append(turns, userTurn{ts: ts, move: move})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].ts.Before(turns[j].ts) })
	return turns, nil
}

// repairAdjacency computes, per search event, whether a repair/reject move sits
// immediately after any read that search surfaced (bbp.17 — the misled tell).
// For each search's linked reads, it finds the FIRST user turn after the read's
// ts; the search is repair-adjacent iff that turn is a repair move. Only the
// immediately-following turn counts — a repair elsewhere in the segment is
// unrelated churn, which is what keeps misled from over-firing. Keyed by search
// EventID, the same key JoinLegs plugs into the Leg.
func repairAdjacency(events []retrievalevent.Event, turns []userTurn) map[string]bool {
	links := retrievalevent.LinkReads(events)
	adj := make(map[string]bool)
	for searchID, reads := range links {
		for i := range reads {
			rt, ok := parseNano(reads[i].TS)
			if !ok {
				continue
			}
			if m, ok := firstTurnAfter(turns, rt); ok && dialogue.IsRepairMove(m) {
				adj[searchID] = true
				break
			}
		}
	}
	return adj
}

// decodeRaw unmarshals one transcript line, returning ok=false on a decode
// error — the tolerant skip the segmenter also applies (a torn line drops, the
// walk continues).
func decodeRaw(line []byte) (transcript.Raw, bool) {
	var raw transcript.Raw
	return raw, json.Unmarshal(line, &raw) == nil
}

// parseNano parses an RFC3339Nano timestamp, returning ok=false on any parse
// error (the two clocks — CC transcript, trixi sidecar — differ in fractional
// precision, so parse-don't-lexical-compare is the rule; an unparseable stamp
// is simply unplaceable, never fatal).
func parseNano(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// firstTurnAfter returns the move of the earliest user turn strictly after t
// (turns are ts-sorted), or ok=false when none follows.
func firstTurnAfter(turns []userTurn, t time.Time) (dialogue.Move, bool) {
	for i := range turns {
		if turns[i].ts.After(t) {
			return turns[i].move, true
		}
	}
	return "", false
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
// Read-adjacency (the misled tell) is per-event, computed separately in
// repairAdjacency, not here.
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
