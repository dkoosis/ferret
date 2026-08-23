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

import (
	"strings"
	"time"
)

// Row is one API call's ledger line. Short JSON keys: an artifact holds one row
// per assistant turn across the whole corpus.
//
// Tokens are counted in four disjoint buckets and MUST NOT be summed into a
// single "total" without pricing — they differ in price by up to 50x within one
// model, and by another 10x across models. Thinking is a SUBSET of Output,
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

	// Write1h/Write5m split CacheWrite by TTL, and the split is not cosmetic:
	// a 1-hour write costs 2x base input where a 5-minute write costs 1.25x.
	// Pricing the pooled bucket at one rate understates a corpus that mostly
	// writes 1-hour entries by ~60% of its write cost.
	Write1h int `json:"w1h,omitempty"`
	Write5m int `json:"w5m,omitempty"`
}

// Multipliers on a model's base input price, published by the API and identical
// across models — only the base price differs, which is what priceFor supplies.
//
// Break-even follows from them: a 5-minute entry pays for itself on the second
// read (1.25 + 0.1 vs 2.0 uncached), a 1-hour entry on the third.
const (
	MulCacheRead = 0.1
	MulWrite5m   = 1.25
	MulWrite1h   = 2.0
)

// PricedAt is when the price table below was last read from Anthropic's posted
// pricing. Printed with every report: a spend figure whose prices are undated
// invites exactly the silent staleness this package exists to prevent.
const PricedAt = "2026-06-24"

// Price is one model's posted rate in USD per million tokens. Cache terms are
// not listed because they are fixed multiples of Input (see the constants
// above); Output is listed because it is a posted price, not a derived one.
type Price struct {
	Input  float64
	Output float64
}

// prices is the posted table. Keys are model-id prefixes: a transcript may
// carry a dated variant (claude-haiku-4-5-20251001), so lookup is by longest
// matching prefix rather than exact equality.
//
// A model absent here is NOT priced at a guess — its calls land in Unpriced and
// are reported, because a fabricated price is indistinguishable from a measured
// one once it reaches a total. "<synthetic>" turns (harness-generated, never
// billed) fall out through the same door.
var prices = map[string]Price{
	"claude-fable-5":    {Input: 10, Output: 50},
	"claude-mythos-5":   {Input: 10, Output: 50},
	"claude-opus-5":     {Input: 5, Output: 25},
	"claude-opus-4-8":   {Input: 5, Output: 25},
	"claude-opus-4-7":   {Input: 5, Output: 25},
	"claude-opus-4-6":   {Input: 5, Output: 25},
	"claude-sonnet-5":   {Input: 3, Output: 15},
	"claude-sonnet-4-6": {Input: 3, Output: 15},
	"claude-haiku-4-5":  {Input: 1, Output: 5},
}

// priceFor resolves a model id to its posted price by longest prefix match.
func priceFor(model string) (Price, bool) {
	best, bestLen, found := Price{}, 0, false
	for k, p := range prices {
		if strings.HasPrefix(model, k) && len(k) > bestLen {
			best, bestLen, found = p, len(k), true
		}
	}
	return best, found
}

// Cost prices one call in USD, or reports that its model is unknown.
//
// Cache writes are split by TTL. A write with no TTL split (an older transcript
// schema) is charged the 5-minute rate: the conservative direction, since the
// alternative inflates a number this package exists to keep honest.
func (r *Row) Cost() (float64, bool) {
	p, ok := priceFor(r.Model)
	if !ok {
		return 0, false
	}
	// max(…, 0): a split wider than the bucket means a schema we misread — the
	// remainder is never negative.
	unsplit := max(r.CacheWrite-r.Write1h-r.Write5m, 0)
	inputUnits := float64(r.Input) +
		MulCacheRead*float64(r.CacheRead) +
		MulWrite1h*float64(r.Write1h) +
		MulWrite5m*float64(r.Write5m+unsplit)
	return (inputUnits*p.Input + float64(r.Output)*p.Output) / 1e6, true
}

// Totals accumulates rows into one bucket set, with cost accumulated per row
// rather than derived at the end: cost depends on the model, and a pooled
// bucket has already lost which model it came from.
type Totals struct {
	Calls      int   `json:"calls"`
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cacheWrite"`
	CacheRead  int64 `json:"cacheRead"`
	Output     int64 `json:"output"`
	Thinking   int64 `json:"thinking"`
	Write1h    int64 `json:"write1h"`
	Write5m    int64 `json:"write5m"`

	USD         float64 `json:"usd"`
	USDInput    float64 `json:"usdInput"`
	USDWrite    float64 `json:"usdCacheWrite"`
	USDRead     float64 `json:"usdCacheRead"`
	USDOutput   float64 `json:"usdOutput"`
	Unpriced    int     `json:"unpriced"` // calls whose model has no posted price
	UnpricedTok int64   `json:"unpricedTokens"`
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

	p, ok := priceFor(r.Model)
	if !ok {
		t.Unpriced++
		t.UnpricedTok += int64(r.Input+r.CacheWrite+r.CacheRead) + int64(r.Output)
		return
	}
	unsplit := max(r.CacheWrite-r.Write1h-r.Write5m, 0)
	t.USDInput += float64(r.Input) * p.Input / 1e6
	t.USDRead += MulCacheRead * float64(r.CacheRead) * p.Input / 1e6
	t.USDWrite += (MulWrite1h*float64(r.Write1h) + MulWrite5m*float64(r.Write5m+unsplit)) * p.Input / 1e6
	t.USDOutput += float64(r.Output) * p.Output / 1e6
	t.USD = t.USDInput + t.USDRead + t.USDWrite + t.USDOutput
}

// Tokens is the raw token count — the number a token budget is denominated in,
// and the wrong number to rank cost by.
func (t *Totals) Tokens() int64 { return t.Input + t.CacheWrite + t.CacheRead + t.Output }

// ReadPerWrite is how many cached tokens each written token was read back for —
// the cache-efficiency number. Below the write multiplier the cache is losing
// money outright.
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
