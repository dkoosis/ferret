package analyst

import (
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/score"
)

// Landmark --session orchestration (ferret-afm, the deliberate follow-on to
// ferret-kuv.10's Q4-new resolution and ferret-vy7's milestone-set library).
//
// Algorithm — landmark-based goal recognition: Pereira, Oren & Meneguzzi,
// "Landmark-based approaches for goal recognition as planning", Artificial
// Intelligence vol. 279 (AIJ 2020). The pure scorer (score.ScoreLandmarks) and
// the goal→milestones library (MilestonesForGoal) already encode the adaptation;
// this layer only wires them to a segmented session.
//
// The seam, end to end (mirrors the milestoneset.go header):
//
//	segment (score)  →  SegmentSource turns a session transcript into per-task
//	                    Segments, each carrying a stated goal (Prompt) and the
//	                    ordered Shape tokens of the calls it owns.
//	library (analyst) → MilestonesForGoal maps each task's stated goal to its
//	                    necessary milestones, uniqueness-weighted from the corpus.
//	scorer (score)   →  ScoreLandmarks rolls each task's Shape against its
//	                    milestone set into a weighted progress ratio.
//	this file (afm)  →  the loop: per real task, map goal → score Shape → collect.
//
// Determinism: the whole path is byte-stable for a fixed corpus — the segmenter
// is pure, MatchGoal is a deterministic cue scan, and ScoreLandmarks is corpus-
// free once weights are filled. No LLM in v1 (the library's goal recognizer is a
// keyword scan); a richer recognizer is a deliberate follow-on, the same staging
// as the segmenter's deterministic-then-analyst split.

// TaskProgress is one segmented task's landmark verdict: the task's index and
// stated goal, whether a milestone set was recognized for it, and (when it was)
// the weighted progress over that set's milestones. Recognized=false carries no
// Progress — an unrecognized goal is skipped, not scored against the wrong
// landmarks (MatchGoal's ok=false case), and is surfaced so the count is honest.
type TaskProgress struct {
	Index      int            `json:"index"`
	Goal       string         `json:"goal"`
	GoalKind   string         `json:"goalKind,omitempty"` // the matched MilestoneSet.ID, empty when unrecognized
	Recognized bool           `json:"recognized"`         // a milestone set matched the stated goal
	Progress   score.Progress `json:"progress,omitzero"`  // weighted landmark coverage; zero value when unrecognized
}

// SessionProgress is the landmark verdict for a whole session: the per-task rows
// (real tasks only — the synthetic preamble owns no stated goal) plus the
// recognized/total rollup a renderer reports.
type SessionProgress struct {
	Session    string         `json:"session"`
	Project    string         `json:"project"`
	Agent      string         `json:"agent,omitempty"`
	Tasks      []TaskProgress `json:"tasks"`
	Total      int            `json:"total"`      // real tasks considered (excludes the preamble)
	Recognized int            `json:"recognized"` // tasks whose goal matched a milestone set
}

// ScoreSessionLandmarks scores every real task in a segmented session against its
// stated goal's milestone set. For each task it recognizes the goal KIND from the
// Prompt (MatchGoal, via MilestonesForGoal), fills the milestones' uniqueness
// weights from corpus (a nil corpus leaves the uniform default floor so the ratio
// stays meaningful), and scores the task's Shape with score.ScoreLandmarks.
//
// The synthetic preamble (Index 0 — calls before any prompt, no stated goal) is
// skipped: it has no goal to map. An unrecognized goal yields a Recognized=false
// row with no Progress rather than a forced score against the wrong landmarks —
// surfaced, not silently dropped, so the rollup is honest. Pure given (res,
// corpus): no I/O, byte-stable for a fixed corpus (the scorer contract).
func ScoreSessionLandmarks(res score.Result, corpus *mine.Corpus) SessionProgress {
	tasks := make([]TaskProgress, 0, len(res.Segments))
	var recognized int
	for i := range res.Segments {
		seg := &res.Segments[i]
		if seg.Index == 0 {
			continue // synthetic preamble — no stated goal to map
		}
		tp := TaskProgress{Index: seg.Index, Goal: seg.Prompt}
		if set, ok := MatchGoal(seg.Prompt); ok {
			ms, _ := MilestonesForGoal(seg.Prompt, corpus)
			tp.Recognized = true
			tp.GoalKind = set.ID
			tp.Progress = score.ScoreLandmarks(ms, seg.Shape)
			recognized++
		}
		tasks = append(tasks, tp)
	}
	return SessionProgress{
		Session:    res.Session,
		Project:    res.Project,
		Agent:      res.Agent,
		Tasks:      tasks,
		Total:      len(tasks),
		Recognized: recognized,
	}
}
