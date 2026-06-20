// Package gates is ferret's GATE-MINING half: it extracts review-gate checks
// from the canonical event stream and measures, deterministically, how much two
// gates overlap in what they reject. A "gate" is a review tool the agent runs to
// decide whether work may proceed — code-review, plan-review, precommit, QA.
// Each gate invocation carries a verdict (APPROVED / NEEDS_REVISION / ESCALATE),
// classified by regex over the tool_result text. A gate REJECTION (NEEDS_REVISION
// or ESCALATE) is ground-truth friction: the agent itself stated the work was not
// good enough, so no inference is needed — this is higher-precision than the
// surprise-inferred friction the mining half produces.
//
// The headline statistic is the OVERLAP RATIO ω: the Jaccard similarity of two
// gates' rejected-session sets. ω = |reject(A) ∩ reject(B)| / |reject(A) ∪
// reject(B)|. Low ω means the gates are COMPLEMENTARY (they catch different
// problems — both earn their cost); high ω means they are REDUNDANT (they reject
// the same sessions — one is a burn to cut). ω is the cost-leak framing applied
// to the review pipeline itself.
//
// Like the conformance half (internal/conform), this is deliberately thin and
// LLM-free: pure functions over canonical events, same inputs → same Report. The
// LLM only enters for UNKNOWN gate tools (a verdict the regex cannot classify),
// and that hybrid call is analyst-side, not in this spike.
//
// Algorithm: deterministic gate-check extraction + decision classification adapt
// the gate_analyzer.py recipe (source: github.com/mrothroc/claude-code-log-
// analyzer), and the overlap ratio ω is the Jaccard-over-rejections measure
// (source: michael.roth.rocks/research/overlap-ratio). The Jaccard index itself
// is Jaccard, "Étude comparative de la distribution florale", 1901.
package gates

import (
	"regexp"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// Decision is the verdict a gate check resolved to.
type Decision string

const (
	// Approved: the gate passed the work — proceed.
	Approved Decision = "APPROVED"
	// NeedsRevision: the gate rejected the work for rework — a friction signal.
	NeedsRevision Decision = "NEEDS_REVISION"
	// Escalate: the gate could not decide and kicked the call up to a human — a
	// stronger friction signal than NeedsRevision.
	Escalate Decision = "ESCALATE"
	// Unknown: the gate tool is recognized but its verdict text matched no
	// pattern. The LLM fallback (analyst-side, not in this spike) owns these.
	Unknown Decision = "UNKNOWN"
)

// rejected reports whether a decision counts as a gate rejection — the
// ground-truth friction signal that feeds the rejection sets and ω.
func (d Decision) rejected() bool { return d == NeedsRevision || d == Escalate }

// The gate-tool list: the review tools whose invocations are gate checks. A
// tool name matches a gate when the canonical gate token is a substring of the
// event Action (case-insensitive) — so "mcp__reviewer__code-review" and
// "code-review" both map to the code-review gate.
const (
	GateCodeReview = "code-review"
	GatePlanReview = "plan-review"
	GatePrecommit  = "precommit"
	GateQA         = "qa"
)

// gateTokens is the canonical gate list, longest-first so a more specific token
// is preferred when several would match (none currently nest, but the order
// keeps the match deterministic and stable if the list grows).
var gateTokens = []string{GateCodeReview, GatePlanReview, GatePrecommit, GateQA}

// Decision-classifier patterns, evaluated in severity order: a verdict that
// trips ESCALATE never falls through to a weaker class. The patterns run over
// the combined tool_result Detail + Target text.
var (
	reEscalate = regexp.MustCompile(`(?i)\b(escalat|blocker|cannot proceed|needs (a )?human|human (review|decision)|hand off to)\b`)
	reReject   = regexp.MustCompile(`(?i)\b(needs[ _-]?revision|changes? requested|not approved|rejected?|must fix|please (fix|revise)|revise|failed?)\b`)
	reApprove  = regexp.MustCompile(`(?i)\b(approved?|lgtm|looks good|ship it|ready to merge|no (issues|blockers)|passed?)\b`)
)

// Gate maps an event Action (tool name) to its canonical gate token, reporting
// whether the action is a gate at all. Matching is case-insensitive substring
// containment so MCP-prefixed tool names still resolve to their gate.
func Gate(action string) (string, bool) {
	a := strings.ToLower(action)
	for _, g := range gateTokens {
		if strings.Contains(a, g) {
			return g, true
		}
	}
	return "", false
}

// Classify resolves a gate check's verdict from its tool_result text. It is a
// pure function of the text: severity order is ESCALATE > NEEDS_REVISION >
// APPROVED, and an unmatched verdict is UNKNOWN (the LLM-fallback boundary).
func Classify(text string) Decision {
	switch {
	case reEscalate.MatchString(text):
		return Escalate
	case reReject.MatchString(text):
		return NeedsRevision
	case reApprove.MatchString(text):
		return Approved
	default:
		return Unknown
	}
}

// Check is one classified gate invocation: which gate ran, in which session, at
// which event Seq, and how it resolved.
type Check struct {
	Seq      int      `json:"seq"`
	Session  string   `json:"session"`
	Gate     string   `json:"gate"`
	Decision Decision `json:"decision"`
}

// GateStat aggregates one gate across the corpus. Rejections is the sorted,
// de-duplicated set of sessions in which this gate rejected at least once — the
// set ω is computed over.
type GateStat struct {
	Gate       string   `json:"gate"`
	Checks     int      `json:"checks"`
	Approved   int      `json:"approved"`
	Revisions  int      `json:"revisions"`
	Escalated  int      `json:"escalated"`
	Unknown    int      `json:"unknown"`
	Rejections []string `json:"rejections"` // distinct sessions, sorted
}

// Overlap is the pairwise overlap ratio ω between two gates' rejected-session
// sets. High ω = redundant gates (cut one); low ω = complementary gates.
type Overlap struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Shared int     `json:"shared"` // |reject(A) ∩ reject(B)|
	Union  int     `json:"union"`  // |reject(A) ∪ reject(B)|
	Omega  float64 `json:"omega"`  // Jaccard = Shared / Union
}

// Loop is a confirmed friction loop: a run of one gate's rejections within a
// single session, split at APPROVED boundaries. Revisions is how many rejecting
// checks the run held before it resolved; Resolved is whether the run ended in
// an APPROVED (false = the session ended still rejecting). Because the boundary
// is the gate's own APPROVED verdict, this is confirmed friction — no inference.
type Loop struct {
	Session   string `json:"session"`
	Gate      string `json:"gate"`
	Revisions int    `json:"revisions"`
	Resolved  bool   `json:"resolved"`
}

// Report is the full deterministic gate-mining result for a corpus.
type Report struct {
	Checks   []Check    `json:"checks"`
	Gates    []GateStat `json:"gates"`
	Overlaps []Overlap  `json:"overlaps"`
	Loops    []Loop     `json:"loops"`
}

// Extract mines gate checks from canonical events and computes the full Report:
// per-gate stats, pairwise ω over rejection sets, and confirmed friction loops.
// It is a pure function of its input — same events → same Report, always — and
// deterministic: every emitted slice is sorted by stable keys, so no map
// iteration leaks into the output.
//
// Each event whose Action matches the gate-tool list is one gate check; its
// verdict is classified by regex over Detail+Target. A rejection (NEEDS_REVISION
// or ESCALATE) is ground-truth friction. Events are read in Seq order within a
// session so the revision-loop split sees verdicts in the order they occurred.
func Extract(events []event.Event) Report {
	rep := Report{}
	rep.Checks = extractChecks(events)
	rep.Gates = aggregate(rep.Checks)
	rep.Overlaps = overlaps(rep.Gates)
	rep.Loops = loops(rep.Checks)
	return rep
}

// extractChecks turns gate-matching events into classified checks, in a stable
// (session, seq) order so the downstream loop split and the JSON output are
// deterministic regardless of input ordering.
func extractChecks(events []event.Event) []Check {
	var checks []Check
	for i := range events {
		ev := &events[i]
		g, ok := Gate(ev.Action)
		if !ok {
			continue
		}
		text := ev.Detail
		if ev.Target != "" {
			text = ev.Target + " " + text
		}
		checks = append(checks, Check{
			Seq:      ev.Seq,
			Session:  ev.Session,
			Gate:     g,
			Decision: Classify(text),
		})
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].Session != checks[j].Session {
			return checks[i].Session < checks[j].Session
		}
		if checks[i].Seq != checks[j].Seq {
			return checks[i].Seq < checks[j].Seq
		}
		return checks[i].Gate < checks[j].Gate
	})
	return checks
}

// aggregate rolls checks up per gate: verdict tallies plus the distinct set of
// sessions in which the gate rejected. Gates are returned sorted by name and
// each rejection set is sorted, so the Report is byte-stable.
func aggregate(checks []Check) []GateStat {
	byGate := map[string]*GateStat{}
	rejSet := map[string]map[string]bool{}
	for _, c := range checks {
		gs := byGate[c.Gate]
		if gs == nil {
			gs = &GateStat{Gate: c.Gate}
			byGate[c.Gate] = gs
		}
		gs.Checks++
		switch c.Decision {
		case Approved:
			gs.Approved++
		case NeedsRevision:
			gs.Revisions++
		case Escalate:
			gs.Escalated++
		case Unknown:
			gs.Unknown++
		}
		if c.Decision.rejected() {
			rs := rejSet[c.Gate]
			if rs == nil {
				rs = map[string]bool{}
				rejSet[c.Gate] = rs
			}
			rs[c.Session] = true
		}
	}
	stats := make([]GateStat, 0, len(byGate))
	for g, gs := range byGate {
		gs.Rejections = sortedKeys(rejSet[g])
		stats = append(stats, *gs)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Gate < stats[j].Gate })
	return stats
}

// overlaps computes ω for every unordered pair of gates that EACH rejected at
// least one session. A gate that never rejected has no rejection behavior to
// compare, so any pair touching it is omitted (its ω would be a meaningless 0
// over a half-empty union). Output is ordered (A<B, then by pair) so it is
// deterministic.
func overlaps(stats []GateStat) []Overlap {
	var out []Overlap
	for i := range stats {
		if len(stats[i].Rejections) == 0 {
			continue
		}
		for j := i + 1; j < len(stats); j++ {
			if len(stats[j].Rejections) == 0 {
				continue
			}
			shared, union, omega := jaccard(stats[i].Rejections, stats[j].Rejections)
			if union == 0 {
				continue
			}
			out = append(out, Overlap{
				A: stats[i].Gate, B: stats[j].Gate,
				Shared: shared, Union: union, Omega: omega,
			})
		}
	}
	return out
}

// jaccard computes the overlap ratio ω = |A∩B| / |A∪B| over two sorted,
// de-duplicated session sets. Returns (0, 0, 0) when both sets are empty (ω
// undefined → the caller drops the pair).
func jaccard(a, b []string) (shared, union int, omega float64) {
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if set[s] {
			shared++
		}
	}
	union = len(a) + len(b) - shared
	if union == 0 {
		return 0, 0, 0
	}
	return shared, union, float64(shared) / float64(union)
}

// loops splits each (session, gate) check sequence at APPROVED boundaries into
// confirmed friction loops. A run of ≥1 rejecting checks terminated by an
// APPROVED is a resolved loop; rejecting checks left pending when the sequence
// ends form an unresolved loop. APPROVED verdicts with no preceding rejections
// emit nothing (a clean pass is not a loop), and UNKNOWN verdicts are skipped —
// they neither extend nor close a run until the analyst classifies them.
//
// checks arrive in (session, seq) order from extractChecks, so iterating in
// place sees each gate's verdicts chronologically within its session.
func loops(checks []Check) []Loop {
	var out []Loop
	// pending[session+"\x00"+gate] = rejections accumulated since the last reset.
	pending := map[string]int{}
	order := []string{}
	flush := func(key string, resolved bool) {
		n := pending[key]
		if n == 0 {
			return
		}
		sess, gate := splitKey(key)
		out = append(out, Loop{Session: sess, Gate: gate, Revisions: n, Resolved: resolved})
		pending[key] = 0
	}
	for _, c := range checks {
		key := c.Session + "\x00" + c.Gate
		if _, seen := pending[key]; !seen {
			order = append(order, key)
		}
		switch {
		case c.Decision.rejected():
			pending[key]++
		case c.Decision == Approved:
			flush(key, true)
		}
	}
	// Drain runs still pending at end-of-corpus as unresolved loops, in first-
	// seen order so the output stays deterministic.
	for _, key := range order {
		flush(key, false)
	}
	return out
}

// splitKey reverses the session\x00gate composite key used by loops.
func splitKey(key string) (session, gate string) {
	session, gate, _ = strings.Cut(key, "\x00")
	return session, gate
}

// sortedKeys returns a set's keys as a sorted slice (nil for an empty set, so an
// empty rejection set marshals as JSON null/[] rather than carrying map order).
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
