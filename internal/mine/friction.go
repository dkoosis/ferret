package mine

import (
	"sort"
	"strings"
)

// Waste merge (ferret-q6y) — the combined friction op behind `ferret friction`.
//
// ferret's four friction detectors each answer a different question in a
// different unit: burn ranks GROSS context bytes per normalized command
// (burn.go), misfires rank failed calls (misfire.go), polling ranks
// exact-duplicate commands inside one session (polling.go), and the motif
// findings rank recurring sequences (finding.go). Reading them meant running
// four commands and doing the cross-join by eye.
//
// The merge needs one currency, and gross cost is the wrong one — an expensive
// command that had to run is not waste. The currency here is the cost of the
// calls that SHOULDN'T HAVE HAPPENED:
//
//	poll    (repeats beyond the first per session) × the key's bytes-per-call
//	misfire (calls that failed)                    × the key's bytes-per-call
//	motif   (occurrences beyond the first per session) — its share of the
//	        motif's measured burn
//
// One theory across all three: the first occurrence bought information; the
// rest is the toll for not having fixed it. burn's own rows contribute no
// waste of their own — a key's GrossBytes and BytesPerCall ride along on
// every row as CONTEXT (and supply the per-call price the other two multiply
// by), which is the join burn.go's Key column was already designed for.
//
// Deliberately excluded: SwallowRow. A swallow is a FLOOR on hidden failures,
// not a count of them (misfire.go) — multiplying a floor by a per-call price
// would manufacture a number the corpus cannot support.
//
// Plain aggregation and a unit conversion over fields the four detectors
// already compute — no algorithm to cite.

// WasteSource tags which detector produced a row, so a reader can drill from
// the merged table back to the single-detector command that owns the detail.
type WasteSource string

const (
	// WasteRepeat is a command re-run inside one session after its answer was
	// already on screen (`ferret polling`).
	WasteRepeat WasteSource = "poll"
	// WasteFail is a call that returned nothing usable (`ferret misfires`).
	WasteFail WasteSource = "misfire"
	// WasteMotif is a recurring sequence classified friction or loop
	// (`ferret report --kind friction`).
	WasteMotif WasteSource = "motif"
)

// WasteRow is one estimated-waste finding, whatever detector found it.
type WasteRow struct {
	Key    string      `json:"key"`    // normalized command key ("sh:git_status", "Read") or the motif's token chain
	Source WasteSource `json:"source"` // poll | misfire | motif

	// WastedBytes is the estimate this table ranks on: context bytes spent on
	// occurrences that bought nothing. An estimate, not a measurement — the
	// per-call price is measured (burn.go), but which occurrences "bought
	// nothing" is the detectors' judgment.
	WastedBytes int `json:"wastedBytes"`
	// Occurrences is the count WastedBytes was charged for: redundant repeats,
	// failed calls, or redundant motif occurrences.
	Occurrences int `json:"occurrences"`
	Sessions    int `json:"sessions"` // distinct sessions the waste spreads across

	// GrossBytes / BytesPerCall are the key's GROSS cost, joined from the burn
	// table — context, never the rank key. Zero when the key has no burn row
	// (a motif chain, which is not a single command key).
	GrossBytes   int     `json:"grossBytes,omitempty"`
	BytesPerCall float64 `json:"bytesPerCall,omitempty"`

	// Detail is the raw command text for a poll row, empty otherwise — a poll's
	// unit is the exact text, not the normalized key (polling.go).
	Detail string `json:"detail,omitempty"`
}

// WasteReport is the merged ranking plus the figures a reader needs to size
// it.
type WasteReport struct {
	Rows []WasteRow `json:"rows"`

	// TotalWasted sums WastedBytes over ALL rows (before any caller-side cap).
	//
	// It is an UPPER BOUND, not a corpus measurement, because the detectors
	// are not disjoint: two identical failing shell calls in one session are
	// charged once by polling and twice by misfires, and a motif can charge
	// the same calls a third time. Deduplicating is not available at this
	// grain — the detectors hand over aggregates, not events — so the honest
	// move is to label the sum and publish the per-source split beside it.
	// Never render this as a percentage of TotalBytes: overlapping charges
	// can exceed the corpus total they would be a fraction of.
	TotalWasted int `json:"totalWasted"`
	// BySource splits TotalWasted per detector. Within one source the rows ARE
	// disjoint (one row per key, or per command text for polling), so each
	// subtotal is a sound figure on its own — which is why the breakdown, not
	// the sum, is what the renderers lead with.
	BySource map[WasteSource]int `json:"bySource,omitempty"`

	TotalBytes int `json:"totalBytes"` // summed GrossBytes over the whole corpus — context, not a denominator for TotalWasted
	Events     int `json:"events"`
	Sessions   int `json:"sessions"`
}

// MergeWaste joins the four detectors' reports into one waste-ranked table.
//
// burn is required — it supplies the per-call price every other estimate is
// denominated in, and its Key space ("sh:"-prefixed shell commands, bare tool
// names — burnKey/misfireKey/pollingKey are the same function three times) is
// the join key. motifs may be nil; corpus is only read when it isn't.
func MergeWaste(burn *BurnResult, mis MisfireReport, poll PollingReport, corpus *Corpus, motifs []*Finding) WasteReport {
	price := priceIndex(burn)
	rep := WasteReport{Events: burn.Events, Sessions: burn.Sessions}
	for i := range burn.Rows {
		rep.TotalBytes += burn.Rows[i].Bytes
	}

	rows := make([]WasteRow, 0, len(poll.Rows)+len(mis.Rows)+len(motifs))
	rows = appendPollWaste(rows, poll, price)
	rows = appendMisfireWaste(rows, mis, price)
	rows = appendMotifWaste(rows, corpus, motifs)

	kept := rows[:0]
	rep.BySource = map[WasteSource]int{}
	for _, r := range rows {
		if r.WastedBytes <= 0 {
			continue // no estimable waste — a row of zeros is noise in a ranking
		}
		rep.TotalWasted += r.WastedBytes
		rep.BySource[r.Source] += r.WastedBytes
		kept = append(kept, r)
	}
	sort.Slice(kept, func(i, j int) bool { return wasteLess(&kept[i], &kept[j]) })
	rep.Rows = kept
	return rep
}

// priceIndex projects the burn table into the per-key gross cost + per-call
// price the other detectors multiply by.
func priceIndex(burn *BurnResult) map[string]*BurnRow {
	idx := make(map[string]*BurnRow, len(burn.Rows))
	for i := range burn.Rows {
		idx[burn.Rows[i].Key] = &burn.Rows[i] // index-range: BurnRow carries a map field
	}
	return idx
}

// appendPollWaste charges each polled command for its repeats beyond the first
// per session. TotalRepeats sums the runs over sessions that polled and
// Sessions counts those sessions, so TotalRepeats−Sessions is exactly the
// redundant-run count (polling.go's aggregatePolling).
func appendPollWaste(rows []WasteRow, poll PollingReport, price map[string]*BurnRow) []WasteRow {
	for _, p := range poll.Rows {
		redundant := p.TotalRepeats - p.Sessions
		if redundant <= 0 {
			continue
		}
		row := WasteRow{
			Key:         p.Key,
			Source:      WasteRepeat,
			Occurrences: redundant,
			Sessions:    p.Sessions,
			Detail:      p.Command,
		}
		applyPrice(&row, price, float64(redundant))
		rows = append(rows, row)
	}
	return rows
}

// appendMisfireWaste charges each key for the calls that failed. cfail rides
// in Fails alongside fail (misfire.go) — a call inside a failed compound chain
// put its bytes in the request either way.
func appendMisfireWaste(rows []WasteRow, mis MisfireReport, price map[string]*BurnRow) []WasteRow {
	for _, m := range mis.Rows {
		if m.Fails <= 0 {
			continue
		}
		row := WasteRow{
			Key:         m.Key,
			Source:      WasteFail,
			Occurrences: m.Fails,
			Sessions:    m.FailSess,
		}
		applyPrice(&row, price, float64(m.Fails))
		rows = append(rows, row)
	}
	return rows
}

// appendMotifWaste charges each friction/loop motif for its occurrences beyond
// the first per session, as a share of the motif's own measured burn. Only
// those two kinds qualify: a routine recurring is the thing to script, not
// waste to eliminate, and noise is by definition not actionable.
//
// Finding.Burn is in TOKENS (finding.go's bytesPerToken divide) while every
// other column in this table is bytes — the multiply back is the whole reason
// this cannot be a one-line join.
//
// A nil corpus means the motif leg was skipped (frictionMotifs returns
// (nil, nil) together) — nothing to render a chain from, so bail before the
// loop rather than trust motifs to be empty too.
func appendMotifWaste(rows []WasteRow, corpus *Corpus, motifs []*Finding) []WasteRow {
	if corpus == nil {
		return rows
	}
	for _, f := range motifs {
		if f.Kind != KindFriction && f.Kind != KindLoop {
			continue
		}
		redundant := f.Count - f.Sessions
		if redundant <= 0 || f.Count <= 0 {
			continue
		}
		share := float64(f.Burn*bytesPerToken) * float64(redundant) / float64(f.Count)
		rows = append(rows, WasteRow{
			Key:         strings.Join(corpus.Tokens(f.IDs), " ⇝ "),
			Source:      WasteMotif,
			WastedBytes: int(share),
			Occurrences: redundant,
			Sessions:    f.Sessions,
		})
	}
	return rows
}

// applyPrice joins a row to its burn entry: the key's gross cost as context,
// and n × the per-call price as the waste estimate. A key absent from the burn
// table leaves the row at zero waste, which MergeWaste then drops — burn walks
// every non-prompt event, so an absent key means the detector and the burn
// table disagree about the corpus, and inventing a price would hide that.
func applyPrice(row *WasteRow, price map[string]*BurnRow, n float64) {
	b, ok := price[row.Key]
	if !ok {
		return
	}
	row.GrossBytes = b.Bytes
	row.BytesPerCall = b.BytesPerCall
	row.WastedBytes = int(n * b.BytesPerCall)
}

// wasteLess is the row ordering: estimated waste descending, then spread
// (sessions) descending — a habit across many sessions is a better fix
// candidate than one runaway session at equal cost — then source and key
// ascending so repeated runs are byte-stable.
func wasteLess(a, b *WasteRow) bool {
	if a.WastedBytes != b.WastedBytes {
		return a.WastedBytes > b.WastedBytes
	}
	if a.Sessions != b.Sessions {
		return a.Sessions > b.Sessions
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	return a.Key < b.Key
}
