package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/score"
)

// This file is the bbp.6 "dialogue pass": it de-islands the dialogue + retrieval
// (Hop2 QPP) scorers — until now reachable only via the standalone `dialogue` /
// `retrieval` commands — into the MAIN findings pipeline. It reads the SAME events
// artifact the report corpus is built from, reduces each session to a deterministic
// dialogue rollup, and hands mine.AttachDialogue a per-stream index keyed exactly
// like Corpus.StreamKeys, so report/rank/out surface the human-side outcome beside
// burn. The hop1 LLM interp-fidelity leg (ferret-bbp.5) is not built; mine.Finding
// reserves a zero-value field for it, and this pass never sets it.

// streamDialogueIndex reduces every (project,session,agent) stream in the events
// artifact to a mine.StreamDialogue, keyed for mine.AttachDialogue. Subagent streams
// stay separate (the agent segment of the key), mirroring the corpus and the
// retrieval command — interleaving them would fabricate episodes that never ran. It
// is a pure read of the events: same input → same index.
func streamDialogueIndex(eventsPath string) (map[string]mine.StreamDialogue, error) {
	streams := map[string][]event.Event{}
	var order []string
	err := event.Read(eventsPath, func(ev *event.Event) error {
		key := ev.Project + "/" + ev.Session + "@" + ev.Agent
		if _, ok := streams[key]; !ok {
			order = append(order, key)
		}
		streams[key] = append(streams[key], *ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx := make(map[string]mine.StreamDialogue, len(order))
	for _, key := range order {
		evs := streams[key]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
		idx[key] = reduceStreamDialogue(evs)
	}
	return idx, nil
}

// reduceStreamDialogue folds one session's ordered events into its dialogue rollup:
// the user-turn moves roll up into a PARADISE Outcome plus repair/accept tallies, and
// the retrieval episodes contribute the worst Hop2 QPP grade. Both legs are pure
// functions of the event stream (the dialogue mover + score.BuildEpisodes), so the
// rollup is byte-stable. Move tagging reads ev.Prompt directly — the same genuine
// user-turn text the retrieval episode builder tags its closing move from.
func reduceStreamDialogue(evs []event.Event) mine.StreamDialogue {
	var (
		moves            []dialogue.Move
		repairs, accepts int
	)
	for i := range evs {
		if evs[i].Kind != event.KindPrompt {
			continue
		}
		m, _ := dialogue.TagMove(evs[i].Prompt)
		moves = append(moves, m)
		switch {
		case dialogue.IsRepairMove(m):
			// v2 split: both MoveRepair (Redo-Differently) and MoveReject
			// (Disagree-Correct) are repairs — counting only MoveRepair here would
			// silently drop every rejecting turn from the r/a tally.
			repairs++
		case m == dialogue.MoveAccept:
			accepts++
		default:
			// clarify/meta/constrain/new-task + catalog moves carry no r/a tally;
			// the friction ones still reach Outcome via dialogue.Classify below.
		}
	}
	sd := mine.StreamDialogue{Repairs: repairs, Accepts: accepts}
	if out := dialogue.Classify(moves); out != dialogue.OutcomeUnknown {
		sd.Outcome = string(out)
	}
	sd.Hop2 = worstHop2(score.BuildEpisodes(evs))
	sd.Friction = frictionToMine(score.ComputeFriction(evs))
	return sd
}

// frictionToMine converts the score-package friction rollup into the mine-local
// value the finding carries, keeping package mine decoupled from package score
// (the same seam as the dialogue.Outcome / QPP grade → string conversions above).
func frictionToMine(f score.Friction) mine.Friction {
	return mine.Friction{
		Prompts:            f.Prompts,
		Actions:            f.Actions,
		TurnsToGoal:        f.TurnsToGoal,
		GoalReached:        f.GoalReached,
		FailedActions:      f.FailedActions,
		LatePreconditions:  f.LatePreconditions,
		IgnoredConstraints: f.IgnoredConstraints,
		ConfirmationWaste:  f.ConfirmationWaste,
		PrematureStops:     f.PrematureStops,
	}
}

// worstHop2 returns the lowest (worst) Hop2 QPP grade across a session's retrieval
// episodes — the clearest retrieval defect the session produced — or "" when the
// session ran no retrieval episode. low ≺ mid ≺ high.
func worstHop2(eps []score.Episode) string {
	worst := ""
	for i := range eps {
		g := string(eps[i].QPP().Grade)
		if hop2Rank(g) > hop2Rank(worst) {
			worst = g
		}
	}
	return worst
}

// reportDialogueNote renders a finding's de-islanded dialogue + Hop2 signals as a
// trailing note on the dense text report row — "  [outcome:abandoned hop2:low r3/a0]"
// — emitting only when a signal is present, so signal-less findings read unchanged.
func reportDialogueNote(f *mine.Finding) string {
	var parts []string
	if f.Outcome != "" {
		parts = append(parts, "outcome:"+f.Outcome)
	}
	if f.Hop2 != "" {
		parts = append(parts, "hop2:"+f.Hop2)
	}
	if f.Repairs > 0 || f.Accepts > 0 {
		parts = append(parts, fmt.Sprintf("r%d/a%d", f.Repairs, f.Accepts))
	}
	parts = append(parts, frictionParts(f.Friction)...)
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, " ") + "]"
}

// frictionParts renders the deterministic friction rollup (bbp.10) as compact
// note tokens, emitting only the metrics that fired so a friction-free finding
// reads unchanged: fail=N (failed actions), late=N (late preconditions), ic=N
// (ignored constraints), cw=N (confirmation waste), stop=N (premature stops), and
// ttg=N when a goal was reached (turns-to-goal).
func frictionParts(fr mine.Friction) []string {
	var parts []string
	if fr.FailedActions > 0 {
		parts = append(parts, fmt.Sprintf("fail=%d", fr.FailedActions))
	}
	if fr.LatePreconditions > 0 {
		parts = append(parts, fmt.Sprintf("late=%d", fr.LatePreconditions))
	}
	if fr.IgnoredConstraints > 0 {
		parts = append(parts, fmt.Sprintf("ic=%d", fr.IgnoredConstraints))
	}
	if fr.ConfirmationWaste > 0 {
		parts = append(parts, fmt.Sprintf("cw=%d", fr.ConfirmationWaste))
	}
	if fr.PrematureStops > 0 {
		parts = append(parts, fmt.Sprintf("stop=%d", fr.PrematureStops))
	}
	if fr.GoalReached {
		parts = append(parts, fmt.Sprintf("ttg=%d", fr.TurnsToGoal))
	}
	return parts
}

// hop2Rank ranks QPP grade strings so the worst (low) wins the per-session reduction.
func hop2Rank(g string) int {
	switch g {
	case string(score.QPPLow):
		return 3
	case string(score.QPPMid):
		return 2
	case string(score.QPPHigh):
		return 1
	default:
		return 0
	}
}
