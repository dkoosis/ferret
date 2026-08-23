// Package apiusage is the harness's own token ledger, read back.
//
// Every assistant turn in a transcript carries the usage the API reported for
// that call: input, cache write, cache read, output, thinking. That is measured
// spend, not a proxy for it — and it is the only number in ferret that an
// outside source (`/usage`) can contradict. Everything else ferret reports is
// bytes it counted itself, which cannot be checked and therefore cannot be
// caught being wrong.
//
// Named apiusage, not usage, to stay distinct from internal/snipeusage (snipe's
// own telemetry) and from the lens "tokens" of internal/lens.
package apiusage

import "time"

// Row is one API call's ledger line. Short JSON keys: an artifact holds one row
// per assistant turn across the whole corpus.
//
// Tokens are counted in four disjoint buckets and MUST NOT be summed into a
// single "total" without weighting — they are priced differently by roughly an
// order of magnitude end to end (see Weights). Thinking is a SUBSET of Output,
// reported separately, and double-counts if added to it.
type Row struct {
	Session string    `json:"s"`
	Agent   string    `json:"a,omitempty"` // subagent id; "" = main thread
	Time    time.Time `json:"t,omitzero"`
	Model   string    `json:"m,omitempty"`

	Input      int `json:"in,omitempty"`  // uncached input tokens
	CacheWrite int `json:"cw,omitempty"`  // cache_creation_input_tokens — written at a premium
	CacheRead  int `json:"cr,omitempty"`  // cache_read_input_tokens — read at a discount
	Output     int `json:"out,omitempty"` // output_tokens, thinking included
	Thinking   int `json:"th,omitempty"`  // subset of Output

	// Write1h/Write5m split cache writes by TTL. A 5-minute write that expires
	// before its next read is a full-price write that bought nothing, so the
	// split is the difference between a cache that pays for itself and one that
	// does not.
	Write1h int `json:"w1h,omitempty"`
	Write5m int `json:"w5m,omitempty"`
}

// Weights are the published per-token price ratios, relative to one uncached
// input token, used to turn four incomparable token counts into one comparable
// number.
//
// These are NOT fitted constants: they are the API's own posted multipliers,
// and they are the reason a token count alone misleads. Cache write is 4.6% of
// dk's tokens and roughly a third of the bill; output is ~0.4% of tokens and
// over a tenth of it. VERIFY against current pricing before trusting a
// weighted figure — a ratio change here reorders the ranking, which is exactly
// why the raw buckets are reported alongside it and never replaced by it.
const (
	WeightInput      = 1.0
	WeightCacheWrite = 1.25
	WeightCacheRead  = 0.1
	WeightOutput     = 5.0
)

// Weighted is the row's cost in input-token-equivalents. Thinking is excluded
// because it is already inside Output.
func (r *Row) Weighted() float64 {
	return WeightInput*float64(r.Input) +
		WeightCacheWrite*float64(r.CacheWrite) +
		WeightCacheRead*float64(r.CacheRead) +
		WeightOutput*float64(r.Output)
}

// Totals accumulates rows into one bucket set.
type Totals struct {
	Calls      int   `json:"calls"`
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cacheWrite"`
	CacheRead  int64 `json:"cacheRead"`
	Output     int64 `json:"output"`
	Thinking   int64 `json:"thinking"`
	Write1h    int64 `json:"write1h"`
	Write5m    int64 `json:"write5m"`
}

// Add folds one row in.
func (t *Totals) Add(r *Row) {
	t.Calls++
	t.Input += int64(r.Input)
	t.CacheWrite += int64(r.CacheWrite)
	t.CacheRead += int64(r.CacheRead)
	t.Output += int64(r.Output)
	t.Thinking += int64(r.Thinking)
	t.Write1h += int64(r.Write1h)
	t.Write5m += int64(r.Write5m)
}

// Tokens is the raw token count — the number a token budget is denominated in,
// and the wrong number to rank cost by.
func (t *Totals) Tokens() int64 { return t.Input + t.CacheWrite + t.CacheRead + t.Output }

// Weighted is the price-weighted cost in input-token-equivalents.
func (t *Totals) Weighted() float64 {
	return WeightInput*float64(t.Input) +
		WeightCacheWrite*float64(t.CacheWrite) +
		WeightCacheRead*float64(t.CacheRead) +
		WeightOutput*float64(t.Output)
}

// ReadPerWrite is how many cached tokens each written token was read back for —
// the cache-efficiency number. Below ~1.25 the cache is losing money outright;
// dk's corpus ran ~20 at the 2026-08-13 diagnostic, which is why chasing hit
// rate was the wrong move and cutting rebuilds was the right one.
func (t *Totals) ReadPerWrite() float64 {
	if t.CacheWrite == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(t.CacheWrite)
}

// ThinkingShare is thinking tokens as a share of output — output is the
// priciest bucket per token, so this says how much of the priciest bucket never
// reached the user.
func (t *Totals) ThinkingShare() float64 {
	if t.Output == 0 {
		return 0
	}
	return 100 * float64(t.Thinking) / float64(t.Output)
}
