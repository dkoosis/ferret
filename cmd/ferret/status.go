package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// StatusCmd backs both `ferret status` and bare `ferret` (ferret-c91).
//
// Bare invocation used to print a forty-line synopsis and exit 1. AXI #8
// (content first) says a no-args run should show live data, not a wall of
// help; dk's DK-AXI profile narrows that to "compact status by default, the
// full listing stays an explicit verb" — which is this: corpus health, the
// three heaviest waste rows, and the legal next moves.
//
// Two deliberate departures from the other subcommands:
//
//   - No ensureData. That helper silently runs a full ingest when the corpus
//     is missing — a minutes-long transcript walk is a surprising side effect
//     for a status read, and a bare command name is the likeliest thing an
//     unfamiliar caller types. A missing corpus reports itself and exits 0.
//   - No motif leg. Sequence mining dominates the runtime of the merged view;
//     status wants a fast read, so it prices only the two cheap detectors.
type StatusCmd struct {
	CommonFlags
}

// statusTop is how many waste rows the compact view shows. Three: enough to
// see whether the head of the ranking is repeats, failures, or one runaway
// command, few enough that the whole output stays scannable.
const statusTop = 3

// cmdStatus renders the compact status view.
func cmdStatus(cmd *StatusCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	st := readStatus(c)
	if c.format == fmtJSON {
		return writeStatusJSON(os.Stdout, st)
	}
	return writeStatusText(os.Stdout, st, c.maxBytes)
}

// status is the whole compact view's data. A corpus that is missing or
// unreadable yields Ready=false with Note explaining which, never an error:
// "you have no corpus yet" is a state to report, not a failure to exit on.
type status struct {
	Data     string          `json:"data"`
	Ready    bool            `json:"ready"`
	Note     string          `json:"note,omitempty"`
	BuiltAt  time.Time       `json:"builtAt,omitzero"`
	Stale    bool            `json:"stale"`
	Newest   time.Time       `json:"newest,omitzero"`
	Events   int             `json:"events,omitempty"`
	Sessions int             `json:"sessions,omitempty"`
	Waste    int             `json:"wasteBytes,omitempty"`
	Gross    int             `json:"grossBytes,omitempty"`
	Top      []mine.WasteRow `json:"top,omitempty"`
}

// readStatus gathers the view without ever building or refreshing the corpus.
func readStatus(c *common) status {
	st := status{Data: c.data}
	manifestPath := filepath.Join(c.data, "manifest.json")
	if !manifestComplete(manifestPath) {
		st.Note = "no corpus"
		return st
	}
	st.Ready = true
	st.Stale, st.BuiltAt, st.Newest = corpusStale(manifestPath)

	rep, err := statusWaste(c)
	if err != nil {
		// A readable manifest with an unreadable events file is a real corpus
		// problem, but it is still a state this view reports rather than an
		// exit code: the ingest hint below is the fix either way.
		st.Ready = false // a JSON consumer must not see ready:true with no metrics
		st.Note = "corpus unreadable: " + err.Error()
		return st
	}
	st.Events, st.Sessions = rep.Events, rep.Sessions
	st.Waste, st.Gross = rep.TotalWasted, rep.TotalBytes
	st.Top = rep.Rows
	if len(st.Top) > statusTop {
		st.Top = st.Top[:statusTop]
	}
	return st
}

// statusWaste runs the two cheap detectors (polling, misfires) priced by burn
// — the merged view minus its expensive motif leg.
func statusWaste(c *common) (mine.WasteReport, error) {
	burn, err := mine.Burn(c.eventsPath())
	if err != nil {
		return mine.WasteReport{}, err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return mine.WasteReport{}, err
	}
	return mine.MergeWaste(burn, mine.MineMisfires(events), mine.MinePolling(events), nil, nil), nil
}

// writeStatusText renders the compact view. Every branch ends in a `next:`
// block, because the whole point of a no-args run is that the caller does not
// yet know what to type.
func writeStatusText(w io.Writer, st status, maxBytes int) error {
	sink := out.NewSink(w, 0, maxBytes)
	defer sink.Close()

	if !st.Ready || st.Note != "" {
		sink.Head("ferret %s — %s", st.Data, st.Note)
		sink.NextHead("ferret ingest", "ferret --help")
		return nil
	}
	sink.Head("ferret %s · %d events · %d sessions · built %s%s",
		st.Data, st.Events, st.Sessions, st.BuiltAt.Format(time.RFC3339), staleMark(st))
	// waste≤: the detectors overlap, so the sum is an upper bound, never a
	// share of the corpus total (internal/mine/friction.go's WasteReport).
	sink.Head("waste≤%s of %s context bytes — top %d:",
		humanBytes(st.Waste), humanBytes(st.Gross), len(st.Top))
	// Rows go through Row, not Head, so --max-bytes actually caps this command
	// (contract: a hard output budget that some lines ignore is not a budget).
	for i := range st.Top {
		r := &st.Top[i]
		sink.Row("  %10s  %-8s %5dx  %s", humanBytes(r.WastedBytes), r.Source, r.Occurrences, r.Key)
	}
	// Legal moves, not a plan (DK-AXI rule 11). The refresh hint appears only
	// when the corpus is actually stale — a hint that is always on is noise a
	// reader learns to skip.
	sink.NextHead("ferret friction", staleHint(st), "ferret --help")
	return nil
}

// staleMark appends the staleness tell to the header line.
func staleMark(st status) string {
	if !st.Stale {
		return ""
	}
	return fmt.Sprintf(" · STALE (transcripts newer: %s)", st.Newest.Format(time.RFC3339))
}

// staleHint returns the re-ingest hint, or "" when the corpus is current —
// out.Next drops empty entries, so this composes inline.
func staleHint(st status) string {
	if !st.Stale {
		return ""
	}
	return "ferret ingest"
}

// writeStatusJSON emits the same view as one document. No row cap: the view is
// already bounded at statusTop.
func writeStatusJSON(w io.Writer, st status) error {
	return out.JSON(w, st)
}
