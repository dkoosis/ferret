package fixes

import (
	"regexp"
	"strings"
)

// Proposal is a candidate substitution mined from an assistant's own
// self-audit text — a confessed tool-call waste named in its own words (e.g.
// "I did a redundant second ask after a get miss"). Shaped so a confirmed
// proposal drops straight into RecordSub's Substitution parameter: dk reviews
// the proposal, then runs the EXISTING `ferret fixes sub` command to record
// it. Detection here never calls RecordSub itself (ferret-kuv.16 v1 has no
// auto-record leg — a proposal is a suggestion, not a ledger write).
type Proposal struct {
	// IntentClass mirrors Substitution.IntentClass: the normalized waste
	// archetype this confession matches (e.g. "ask-after-miss").
	IntentClass string `json:"intentClass"`
	// Better mirrors Substitution.Better: the fix template the confession
	// itself names (e.g. "search alone").
	Better string `json:"better"`
	// Example is the confessed sentence, verbatim (trimmed), for traceability
	// — dk can see exactly what triggered the proposal before confirming it.
	Example string `json:"example"`
	// Session is the source session prefix the confession was mined from.
	Session string `json:"session,omitempty"`
	// Tell names which detector fired, so a false positive can be traced back
	// to the pattern that needs tightening.
	Tell string `json:"tell"`
}

// ToSubstitution renders a proposal in the exact shape RecordSub expects.
// WrongTool is left empty: regex extraction over prose doesn't reliably
// isolate the tool actually reached for, so dk supplies --wrong by hand when
// confirming via `ferret fixes sub` rather than have this guess.
func (p Proposal) ToSubstitution() Substitution {
	return Substitution{
		IntentClass: p.IntentClass,
		Better:      p.Better,
		Example:     p.Example,
		Session:     p.Session,
	}
}

// selfAuditTell patterns: the GATE that must fire somewhere in the text before
// any specific-archetype detector runs. Bias precision over recall (this
// feeds a human-confirm loop, ferret-kuv.16) — a session whose assistant text
// never confesses waste in one of these shapes yields zero proposals, full
// stop, regardless of what a narrower pattern might otherwise coincidentally
// match.
var (
	// callCountRe: an explicit call-budget confession ("floor was 4; I spent 6").
	callCountRe = regexp.MustCompile(`(?i)\bfloor\s+was\s+\d+\b.{0,60}?\bspent\s+\d+\b`)
	// wasteWordsRe: the vocabulary of self-audit admission.
	wasteWordsRe = regexp.MustCompile(`(?i)\b(redundant|wasted?|unnecessary|should(?:n't| not)\s+have)\b`)
	// numberedWasteRe: a numbered list line naming waste explicitly.
	numberedWasteRe = regexp.MustCompile(`(?im)^\s*\d+[.)]\s+.*\b(wasted?|redundant|unnecessary)\b`)

	sentenceSplitRe = regexp.MustCompile(`[.\n;]+`)
)

// SelfAuditTell reports whether text contains a self-audit confession signal:
// a call-count comparison, a waste word, or a numbered waste-list line.
func SelfAuditTell(text string) bool {
	return callCountRe.MatchString(text) ||
		wasteWordsRe.MatchString(text) ||
		numberedWasteRe.MatchString(text)
}

// containsAllFold reports whether s contains every sub, case-insensitively.
func containsAllFold(s string, subs ...string) bool {
	low := strings.ToLower(s)
	for _, sub := range subs {
		if !strings.Contains(low, strings.ToLower(sub)) {
			return false
		}
	}
	return true
}

// detectAskAfterMiss matches the archetype: a redundant second ask fired
// after a lookup miss, where a search alone would have covered it. Requires
// "redundant"/"unnecessary" + "ask" + "miss" + "search" all in the same
// sentence — narrow on purpose, precision over recall.
func detectAskAfterMiss(sentence string) (Proposal, bool) {
	if !containsAllFold(sentence, "ask", "miss", "search") {
		return Proposal{}, false
	}
	if !containsAllFold(sentence, "redundant") && !containsAllFold(sentence, "unnecessary") {
		return Proposal{}, false
	}
	return Proposal{
		IntentClass: "ask-after-miss",
		Better:      "search alone",
		Example:     strings.TrimSpace(sentence),
		Tell:        "redundant-ask-after-miss",
	}, true
}

// detectSplitRead matches the archetype: a read split into partial fetches
// (e.g. head then sed) where one full read/get was already known to be
// needed. Requires "split" + ("read"|"get") + "full" in the same sentence.
func detectSplitRead(sentence string) (Proposal, bool) {
	if !containsAllFold(sentence, "split", "full") {
		return Proposal{}, false
	}
	if !containsAllFold(sentence, "read") && !containsAllFold(sentence, "get") {
		return Proposal{}, false
	}
	return Proposal{
		IntentClass: "split-read",
		Better:      "one full get",
		Example:     strings.TrimSpace(sentence),
		Tell:        "split-read-known-needed",
	}, true
}

// detectors is the ordered list of specific-archetype extractors DetectProposals
// runs per sentence once the SelfAuditTell gate has cleared. Add new confessed-
// waste shapes here.
var detectors = []func(string) (Proposal, bool){
	detectAskAfterMiss,
	detectSplitRead,
}

// DetectProposals mines one assistant turn's text for confessed call-waste,
// returning zero or more candidate proposals for dk to review. Returns nil
// (not just an empty slice) whenever the text carries no SelfAuditTell signal
// at all — the precision gate that keeps an ordinary turn's assistant text,
// however it happens to be phrased, from producing noise for the confirm
// loop. session is stamped onto every proposal for traceability.
func DetectProposals(session, text string) []Proposal {
	if !SelfAuditTell(text) {
		return nil
	}
	var out []Proposal
	for _, sentence := range sentenceSplitRe.Split(text, -1) {
		if strings.TrimSpace(sentence) == "" {
			continue
		}
		for _, detect := range detectors {
			p, ok := detect(sentence)
			if !ok {
				continue
			}
			p.Session = session
			out = append(out, p)
			break
		}
	}
	return out
}
