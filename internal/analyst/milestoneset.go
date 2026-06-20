package analyst

import (
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/score"
)

// Milestone-set library — analyst-side goal→milestones mapping for landmark
// progress scoring (ferret-vy7, follow-on to ferret-kuv.10's Q4-new resolution).
//
// Algorithm — landmark-based goal recognition: Pereira, Oren & Meneguzzi,
// "Landmark-based approaches for goal recognition as planning", Artificial
// Intelligence vol. 279 (AIJ 2020). A landmark is a fact that MUST hold on any
// path to the goal; the engine (internal/score) counts achieved landmarks weighted
// by uniqueness. That scorer needs a SET of necessary milestones per goal — this
// library is where those sets live, on the ANALYST side of the engine/analyst seam
// (mirroring conform consuming analyst-supplied reference steps): naming which
// milestones a goal of a given kind requires, and which Shape tokens satisfy each,
// is semantic work, not corpus arithmetic.
//
// The split, end to end:
//
//	analyst (this file)  →  goal text → []score.Milestone (ID + satisfying Shape
//	                        tokens). The semantics: which milestones a goal needs.
//	engine (score)       →  fills each milestone's uniqueness Weight from corpus
//	                        frequency (WeighByCorpus); ScoreLandmarks rolls hits
//	                        into a weighted progress ratio. Deterministic, pure.
//	ferret-afm           →  the --session orchestration on top: segment a session,
//	                        map each Segment's stated goal to its milestone set
//	                        here, score Segment.Shape against it.
//
// v1 is a SMALL canned catalog keyed on goal-text cues, not an LLM. Recognizing
// the goal KIND from a one-line stated intent is a coarse classification a keyword
// scan handles for the common Claude-Code task shapes (ship / fix / investigate /
// refactor); a richer LLM-driven recognizer is a deliberate follow-on, the same way
// the segmenter ships a deterministic half before the analyst merge. Keeping v1
// deterministic also keeps the whole landmark --session path byte-stable for a
// fixed corpus, which the scorer contract wants.

// defaultWeight is the uniqueness weight used when no corpus is available to
// measure one. ScoreLandmarks divides by total weight, so an all-zero set would
// score every shape 0; a uniform positive default keeps the ratio meaningful
// (every milestone counts equally) until a corpus refines it. The value matches
// the +1 floor WeighByCorpus's smoothed-idf adds, so the fallback sits at the same
// scale as a measured weight for a ubiquitous tool.
const defaultWeight = 1.0

// MilestoneSet is one named goal pattern: the milestones a goal of this KIND must
// touch, plus the lowercased text cues that recognize the goal from its stated
// intent. The analyst owns this semantics; the engine fills the per-milestone
// uniqueness Weight from the corpus (score.WeighByCorpus).
type MilestoneSet struct {
	ID         string            // the goal-pattern id, e.g. "ship-feature"
	Cues       []string          // lowercase intent substrings that recognize this goal
	Milestones []score.Milestone // necessary milestones; Tools = satisfying Shape tokens
}

// milestoneSets is the v1 catalog. Each entry's Milestones name the Shape tokens
// (Segment.Shape vocabulary: built-in tool names, "sh:<verb>" for shell verbs,
// "mcp:server.tool" for MCP) that satisfy the milestone — any one of them counts as
// a hit (score.milestoneHit's any-of policy). Cues are matched case-insensitively
// against the stated goal; entries are ordered most-specific first so MatchGoal is
// deterministic when a goal trips several patterns (the canonical "fix the bug AND
// ship the feature" overlap).
//
// Kept deliberately small and shape-token-grounded: a milestone whose Tools never
// appear in the corpus vocab would score a constant max-rarity weight and never
// hit, so the catalog cites only tokens the lens actually emits.
var milestoneSets = []MilestoneSet{
	{
		// Ship a feature: locate the code, change it, verify, and land it. The
		// commit/PR milestone is the load-bearing one (a feature isn't shipped
		// until it lands) and is the rarest token, so WeighByCorpus weights it up.
		ID:   "ship-feature",
		Cues: []string{"ship", "implement", "add the", "add a", "build the", "build a", "feature"},
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}},
			{ID: "edit", Tools: []string{"Edit", "Write", "MultiEdit"}},
			{ID: "test", Tools: []string{"sh:go_test", "sh:go", "sh:make", "sh:npm", "sh:pytest"}},
			{ID: "commit", Tools: []string{"sh:git_commit", "sh:git"}},
		},
	},
	{
		// Fix a bug: read the failing code, change it, re-run to confirm the fix.
		ID:   "fix-bug",
		Cues: []string{"fix", "bug", "panic", "broken", "failing", "regression", "repair"},
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}},
			{ID: "edit", Tools: []string{"Edit", "Write", "MultiEdit"}},
			{ID: "verify", Tools: []string{"sh:go_test", "sh:go", "sh:make", "sh:npm", "sh:pytest"}},
		},
	},
	{
		// Refactor: read the existing structure, then restructure it. No
		// commit/test milestone — "refactor" often names the structural change
		// alone; the ship-feature pattern covers the land-it goal.
		ID:   "refactor",
		Cues: []string{"refactor", "restructure", "clean up", "cleanup", "extract", "rename", "split"},
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}},
			{ID: "edit", Tools: []string{"Edit", "Write", "MultiEdit"}},
		},
	},
	{
		// Investigate / explore / explain: read code and/or search it; no change.
		// Last because its cues ("why", "look at") are the most generic — a more
		// specific pattern above should win an overlap.
		ID:   "investigate",
		Cues: []string{"investigate", "explore", "explain", "understand", "look at", "find out", "why", "audit", "review", "search for"},
		Milestones: []score.Milestone{
			{ID: "read", Tools: []string{"Read"}},
			{ID: "search", Tools: []string{"Grep", "Glob", "sh:rg", "sh:grep", "sh:find", "sh:fd"}},
		},
	},
}

// MilestoneSets returns the v1 catalog of canned goal patterns. Returned by value
// (a copy of the slice header over immutable entries) so callers can range it
// without reaching into package state.
func MilestoneSets() []MilestoneSet {
	return milestoneSets
}

// MatchGoal recognizes the goal KIND from a stated intent and returns its
// milestone set. The first catalog entry (most-specific first) with a cue that
// appears in the lowercased goal wins — deterministic, order-stable. ok is false
// when no pattern matches (an unrecognized goal: the --session mode skips it rather
// than score it against the wrong landmarks). The returned set's Milestones carry
// NO weights yet — use MilestonesForGoal to get a weighted, caller-owned copy.
func MatchGoal(goal string) (MilestoneSet, bool) {
	low := strings.ToLower(goal)
	if strings.TrimSpace(low) == "" {
		return MilestoneSet{}, false
	}
	for _, set := range milestoneSets {
		for _, cue := range set.Cues {
			if strings.Contains(low, cue) {
				return set, true
			}
		}
	}
	return MilestoneSet{}, false
}

// MilestonesForGoal maps a stated goal to the []score.Milestone the landmark
// scorer consumes, with uniqueness weights filled from the corpus. This is the
// library's payoff — the exact input ferret-afm's --session mode scores a segmented
// task's Shape against.
//
// It returns a FRESH copy of the matched set's milestones (the catalog entries stay
// weightless and shared), then calls score.WeighByCorpus on the copy to fill each
// Weight from inverse corpus frequency in place. A nil corpus (WeighByCorpus is a
// no-op then) leaves the copy's weights at the uniform defaultWeight floor so
// ScoreLandmarks still yields a meaningful ratio. ok mirrors MatchGoal.
func MilestonesForGoal(goal string, corpus *mine.Corpus) ([]score.Milestone, bool) {
	set, ok := MatchGoal(goal)
	if !ok {
		return nil, false
	}
	ms := make([]score.Milestone, len(set.Milestones))
	for i, m := range set.Milestones {
		tools := make([]string, len(m.Tools))
		copy(tools, m.Tools)
		ms[i] = score.Milestone{ID: m.ID, Tools: tools, Weight: defaultWeight}
	}
	if corpus != nil {
		// Clear the default floor so WeighByCorpus measures every milestone (it
		// skips any with a non-zero Weight, treating it as a caller override).
		for i := range ms {
			ms[i].Weight = 0
		}
		score.WeighByCorpus(ms, corpus)
	}
	return ms, true
}
