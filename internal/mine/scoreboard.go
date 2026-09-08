package mine

import (
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/fixes"
)

// Scoreboard (ferret-4vx) — the data bare `ferret` renders: the compact
// measurement view over the SAME two pipelines `ferret report --lens cmd` and
// `ferret friction` already compute. No new detector, no new scorer — this
// package only caps and ledger-filters what those two already produce.
//
// The fix ledger (internal/fixes) is the third input: a motif or waste key
// that has been adjudicated (fix|wontfix|watch) leaves both rankings — a
// scan should not keep re-suggesting a call that was already decided — and a
// `fix` verdict additionally earns a row on the Delta line, joined the same
// way `report --since-fixes` joins it: by fixes.MotifKey, never a second
// matcher.

// ScoreboardRoutine is one routine row: a motif from the report pipeline,
// resolved to its display key and measured cost. ExStream/ExSeq are the raw
// corpus coordinates of one occurrence — the CLI layer resolves them to a
// human exemplar ("session@seq"); this package stays pure (no corpus-key
// truncation/formatting policy).
//
// Cost is in BYTES, converted here from Finding's token counts. Every other
// number in the Scoreboard payload — WasteRow.WastedBytes, ScoreboardDelta's
// Before/AfterBytes — is already bytes, so emitting tokens on this one struct
// would hand a `--format json` consumer two units under sibling keys with
// nothing marking the difference. Ferret is a byte-measurement tool; the
// payload speaks one unit.
type ScoreboardRoutine struct {
	Key           string `json:"key"` // token chain, e.g. "Read ⇝ Read ⇝ Edit"
	BurnBytes     int    `json:"burnBytes"`
	SideBurnBytes int    `json:"sideBurnBytes"`
	Count         int    `json:"count"`
	Sessions      int    `json:"sessions"`
	ExStream      int    `json:"exStream"`
	ExSeq         int    `json:"exSeq"`
}

// ScoreboardDelta is one ledger `fix` entry's burn delta — before (the
// baseline captured by `fixes add`) and after (the motif's burn this ingest,
// 0 when the motif no longer recurs at all). Both bytes, converted from the
// ledger's/Finding's token counts by the same BytesPerToken ratio Findings
// uses throughout.
type ScoreboardDelta struct {
	Key         string `json:"key"`
	Fix         string `json:"fix"`
	FixedAt     string `json:"fixedAt"`
	BeforeBytes int    `json:"beforeBytes"`
	AfterBytes  int    `json:"afterBytes"`
}

// Scoreboard is the whole compact view's data: bare `ferret`'s --format json
// payload, one-to-one.
type Scoreboard struct {
	Routines []ScoreboardRoutine `json:"routines"`
	Waste    []WasteRow          `json:"waste"`
	Delta    []ScoreboardDelta   `json:"delta"`
	// BelowCut is routines-past-cap plus waste-past-cap plus delta-past-cap
	// combined — one figure for the tail line ("… N more"), not a per-category
	// breakdown; a reader who wants the split already has `ferret report`,
	// `ferret friction` and `ferret report --since-fixes`.
	BelowCut int `json:"belowCut"`
}

// BuildScoreboard caps and ledger-filters the report/friction pipelines'
// output into the scoreboard view. findings is the report's default (noise
// already dropped) ranking, lens=cmd, sorted by burn — same slice cmdReport
// would render. waste is the friction merge with the motif leg skipped (poll
// ∪ misfire only — home.go arranges that by calling mergeFriction with
// NoMotifs, so no second union is computed here). ledger is every recorded
// fix/wontfix/watch entry; rowCap bounds each of Routines, Waste and Delta (3,
// per the bead) — the ledger only grows, so an uncapped Δ section is the one
// part of the scoreboard that would outrun the 40-line budget over time.
func BuildScoreboard(corpus *Corpus, findings []*Finding, waste WasteReport, ledger []fixes.Entry, rowCap int) Scoreboard {
	idx := fixes.Index(ledger)

	var sb Scoreboard
	for _, f := range findings {
		key := fixes.MotifKey(corpus.Tokens(f.IDs))
		if _, ok := idx[key]; ok {
			continue // adjudicated (fix/wontfix/watch) — leaves the ranking
		}
		if len(sb.Routines) >= rowCap {
			sb.BelowCut++
			continue
		}
		sb.Routines = append(sb.Routines, ScoreboardRoutine{
			Key:           strings.Join(corpus.Tokens(f.IDs), " ⇝ "),
			BurnBytes:     f.Burn * BytesPerToken,
			SideBurnBytes: f.SideBurn * BytesPerToken,
			Count:         f.Count,
			Sessions:      f.Sessions,
			ExStream:      f.ExStream,
			ExSeq:         f.ExSeq,
		})
	}

	for _, r := range waste.Rows {
		key := fixes.MotifKey([]string{r.Key})
		if _, ok := idx[key]; ok {
			continue
		}
		if len(sb.Waste) >= rowCap {
			sb.BelowCut++
			continue
		}
		sb.Waste = append(sb.Waste, r)
	}

	delta := buildDelta(corpus, findings, idx)
	if len(delta) > rowCap {
		sb.BelowCut += len(delta) - rowCap
		delta = delta[:rowCap]
	}
	sb.Delta = delta
	return sb
}

// buildDelta renders one row per ledger `fix` entry (wontfix/watch verdicts
// have no baseline burn to delta — they only ever leave the ranking above).
// It returns every row; BuildScoreboard applies the cap, so the count folded
// into BelowCut is computed from the full set.
// "after" is the matching finding's current burn, 0 when the motif no longer
// recurs — the fix worked all the way to elimination, not merely a
// reduction.
//
// Ordered newest fix first (ferret-yid). The section answers "did the fix I
// just landed work?", and BuildScoreboard keeps only the first rowCap rows —
// so an alphabetical order, which is what this sorted by until the cap
// existed, hid today's fix behind whichever motif keys happened to sort
// early. AddedAt descending puts the fix under evaluation at the top; the key
// breaks same-day ties, so repeated runs stay byte-stable (map iteration
// order is not).
func buildDelta(corpus *Corpus, findings []*Finding, idx map[string]fixes.Entry) []ScoreboardDelta {
	keys := make([]string, 0, len(idx))
	for key, e := range idx {
		if e.Disp() == fixes.DispositionFix {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		ti, tj := idx[keys[i]].AddedAt, idx[keys[j]].AddedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return keys[i] < keys[j]
	})

	delta := make([]ScoreboardDelta, 0, len(keys))
	for _, key := range keys {
		e := idx[key]
		displayKey, after := key, 0
		for _, f := range findings {
			if fixes.MotifKey(corpus.Tokens(f.IDs)) == key {
				displayKey = strings.Join(corpus.Tokens(f.IDs), " ⇝ ")
				after = f.Burn
				break
			}
		}
		delta = append(delta, ScoreboardDelta{
			Key: displayKey, Fix: e.Fix, FixedAt: e.AddedAt.Format("2006-01-02"),
			BeforeBytes: e.BaselineBurn * BytesPerToken,
			AfterBytes:  after * BytesPerToken,
		})
	}
	return delta
}
