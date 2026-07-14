package reach

import (
	"encoding/json"
	"regexp"
	"strings"
)

// RU (retrieval usefulness) is the Phase-2 verdict on a store-reach: once the
// trixi store answered a recall opportunity, did its content actually shape the
// following turns? tx-qw86 success-criterion #1, transcript-based per dk's
// 2026-07-13 ratification (defer the tx-kji6 live-telemetry join until reach
// rises and the transcript verdict proves noisy).
//
//	USED         — the retrieved content demonstrably shapes the following turns:
//	               a nug/bead id or distinctive term from the result reappears in
//	               the agent's own prose (cited, quoted, acted on).
//	UNUSED        — retrieved then ignored: the agent answers with no overlap, or
//	               falls back to another channel as if the store returned empty.
//	INCONCLUSIVE — the transcript can't tell (empty/error result, or no agent
//	               prose after the reach to read use from).
//
// RU = USED / (USED + UNUSED); INCONCLUSIVE is excluded from the denominator but
// reported alongside n. Only store-reaches carry a verdict; RUNone is the
// sentinel for every other channel.
type RU string

const (
	RUNone         RU = ""             // not a store reach — no verdict
	RUUsed         RU = "used"         // content shaped the following turns
	RUUnused       RU = "unused"       // retrieved then ignored
	RUInconclusive RU = "inconclusive" // transcript can't tell
)

// usedTermMin is how many distinctive content terms from the retrieved nug must
// reappear in the agent's following prose to read USED on term-overlap alone. An
// id/ref hit is decisive on its own (idHit below); generic topical overlap is
// weaker — the opportunity's topic seeds both the result and the answer — so the
// bar is deliberately high. Tunable; the metric ships as a gauge at tiny n.
const usedTermMin = 4

// idRe matches the distinctive references that only appear in prose if the agent
// read them off the result: hex nug ids (≥8 hex) and slug-shaped bead/ref ids
// (ferret-3zh, tx-qw86, ccp-y37). A hit is the strongest USED tell.
var idRe = regexp.MustCompile(`\b(?:[0-9a-f]{8,}|[a-z]{2,}-[a-z0-9]{2,})\b`)

// termRe matches candidate content terms (≥5 letters); stopwords are filtered.
var termRe = regexp.MustCompile(`[a-z]{5,}`)

// ruStopwords are common ≥5-letter words that carry no discriminating signal, so
// their overlap between result and prose is noise, not evidence of use.
var ruStopwords = map[string]bool{
	"about": true, "there": true, "these": true, "those": true, "their": true,
	"which": true, "where": true, "would": true, "could": true, "should": true,
	"being": true, "other": true, "after": true, "before": true, "still": true,
	"first": true, "because": true, "between": true, "into": true, "with": true,
	"that": true, "this": true, "have": true, "from": true, "were": true,
}

// adjudicateRU grades one store-reach from the transcript: the retrieved content
// (result), the agent's prose after the reach (text + thinking, up to the next
// user boundary), whether a result was captured at all, and whether the agent
// fell back to another retrieval channel in the same arc.
func adjudicateRU(result, prose string, gotResult, fellBack bool) RU {
	if !gotResult || isEmptyResult(result) {
		return RUInconclusive // store returned nothing to use — can't grade an empty hit
	}
	if prose = strings.TrimSpace(prose); prose != "" {
		proseLower := strings.ToLower(prose)
		if idHit(result, proseLower) {
			return RUUsed // cited a nug/bead id from the result — decisive
		}
		overlap := termOverlap(result, proseLower)
		if overlap >= usedTermMin {
			return RUUsed
		}
		if overlap > 0 && !fellBack {
			return RUInconclusive // touched the topic but not clearly off the nug
		}
	}
	// No USED signal. A fallback to another channel, or an answer with zero
	// content overlap, reads as UNUSED — proceeded as if the store were empty.
	if fellBack || prose != "" {
		return RUUnused
	}
	return RUInconclusive // nothing followed the reach and no fallback to judge
}

// isEmptyResult reports whether a captured store result carries no usable content
// — an empty payload or a known "nothing found" marker. Such a reach is a store
// miss, not the agent ignoring content, so it grades INCONCLUSIVE.
func isEmptyResult(result string) bool {
	s := strings.ToLower(strings.TrimSpace(result))
	switch s {
	case "", "[]", "{}", "null", "none":
		return true
	}
	return strings.Contains(s, "no results") ||
		strings.Contains(s, "no nug") ||
		strings.Contains(s, "not found") ||
		strings.Contains(s, "no matching")
}

// idHit reports whether any distinctive id/ref in the result reappears verbatim
// in the (lowercased) prose.
func idHit(result, proseLower string) bool {
	for _, id := range idRe.FindAllString(strings.ToLower(result), -1) {
		if strings.Contains(proseLower, id) {
			return true
		}
	}
	return false
}

// termOverlap counts distinct content terms from the result that reappear in the
// prose word set — the topical-overlap signal, weaker than an id hit.
func termOverlap(result, proseLower string) int {
	proseWords := wordSet(proseLower)
	seen := map[string]bool{}
	n := 0
	for _, t := range termRe.FindAllString(strings.ToLower(result), -1) {
		if ruStopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		if proseWords[t] {
			n++
		}
	}
	return n
}

// wordSet splits already-lowercased text into a set of ≥5-letter words.
func wordSet(lower string) map[string]bool {
	set := map[string]bool{}
	for _, w := range termRe.FindAllString(lower, -1) {
		set[w] = true
	}
	return set
}

// resultText flattens a tool_result payload (a JSON string, or an array of
// {type,text} content blocks as MCP tools emit) into plain text for grading.
func resultText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case '[':
		var blocks []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return string(raw)
}
