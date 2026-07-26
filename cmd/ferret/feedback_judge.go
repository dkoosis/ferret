package main

// cmdFeedbackJudge is the RunRelevance wiring: re-derive the deterministic
// helped verdict for one search event (mirroring cmd/ferret/helped.go's own
// segment+join+lattice pipeline, per the Design section "Matching the single
// event"), run the content-relevance judge over the search's returned nugs
// (fetched by the caller — the Stop-hook bash script — and handed in on
// stdin), and bank an AskCandidate when feedback.Select's disagreement fires.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/feedback"
	"github.com/dkoosis/ferret/internal/retrievalevent"
	"github.com/dkoosis/ferret/internal/score"
)

// errFeedbackSearchEventRequired guards `feedback judge` — without a target
// event id there is nothing to re-derive a verdict for.
var errFeedbackSearchEventRequired = errors.New("feedback judge: --search-event ID required")

// nugCandidateInput is one stdin candidate: a nug id + its fetched body text
// (the bash Stop-hook script assembles this by looping `trixi get` over
// `feedback prep`'s nug_ids, skipping any 404s).
type nugCandidateInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// relevanceJudge is the seam runFeedbackJudge calls — analyst.RunRelevance in
// production, a fake in tests (mirrors cmd/ferret/retrieval_hop1.go's
// hop1Judge injection pattern) so the disagreement/agreement/unclassifiable
// paths are exercisable without a live API key.
type relevanceJudge func(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error)

// judgeResult is what runFeedbackJudge decided: whether an AskCandidate should
// be banked, and the candidate to bank if so.
type judgeResult struct {
	Fired bool
	Ask   feedback.AskCandidate
}

// judgeSessionInputs bundles cmdFeedbackJudge's resolved session-side inputs
// (everything runFeedbackJudge needs except the candidates and the seams) —
// split out so cmdFeedbackJudge itself stays a short flag→call wire-up.
type judgeSessionInputs struct {
	res    score.Result
	events []retrievalevent.Event
	turns  []userTurn
}

// resolveFeedbackJudgeInputs segments the session transcript and loads the
// retrieval-event feed — the same segment+events resolution cmdHelped does,
// scoped to one judge invocation. data resolves this session's already-
// recorded labels (ferret-j33 §6, P0-2), which de-contaminate BOTH read paths
// exactly as cmdHelped does offline: `adjust` folds an earlier probe answer
// out of segmentation (SegmentSourceExcluding — else its spurious segment
// could become the owning segment whose Prompt this judge hands the live
// relevance API for a LATER search), and `exclude` skips it in sessionUserTurns
// (else its raw text injects a false repair-adjacency signal into the same
// search's live Disagree computation).
func resolveFeedbackJudgeInputs(root, session, eventsFile, data string) (judgeSessionInputs, error) {
	src, _, err := resolveSpineSource(root, session)
	if err != nil {
		return judgeSessionInputs{}, err
	}
	adjust, exclude, err := sessionProbeAdjustments(src, data)
	if err != nil {
		return judgeSessionInputs{}, err
	}
	res, err := score.SegmentSourceExcluding(src, adjust)
	if err != nil {
		return judgeSessionInputs{}, err
	}
	turns, err := sessionUserTurns(src.Path, exclude)
	if err != nil {
		return judgeSessionInputs{}, err
	}
	events, err := readEventsTolerant(eventsFile)
	if err != nil {
		return judgeSessionInputs{}, err
	}
	return judgeSessionInputs{res: res, events: events, turns: turns}, nil
}

// readEventsTolerant reads path as a JSONL retrieval-event feed end to end
// (from offset 0), by REUSING scanNewLines' exact tolerance contract — decode
// errors, schema_version mismatches, AND its torn-final-line handling — so
// judge's read degrades identically to prep's live tail, by construction,
// rather than risking silent semantic drift between two independent
// implementations of "skip a bad row" (a self-review finding: an earlier
// version of this function reimplemented the scan with bufio.Scanner, which
// treats a final line with no trailing newline as a complete token — unlike
// scanNewLines, which correctly withholds it as still-mid-write. Since the
// retrieval-live file is shared and actively appended across every one of
// dk's concurrent sessions, catching it mid-write is a realistic, not
// hypothetical, race — and the Scanner version would have misreported that
// ordinary race as "skipping unparseable/mismatched-schema line", a false
// alarm scanNewLines' version doesn't produce).
//
// judge has no cursor to persist (it re-reads the whole file fresh on every
// invocation, unlike prep's offset-tracked tail), so only the decoded events
// are kept; the byte offsets scanNewLines pairs them with (which prep's
// settled-tail bookkeeping needs) are discarded here.
func readEventsTolerant(path string) ([]retrievalevent.Event, error) {
	found, _, err := scanNewLines(path, 0)
	if err != nil {
		return nil, err
	}
	events := make([]retrievalevent.Event, len(found))
	for i := range found {
		events[i] = found[i].event
	}
	return events, nil
}

// readStdinCandidates decodes the [{"id":"...","text":"..."}] candidate
// bodies the Stop-hook bash script assembled (via `trixi get`) and piped on
// stdin.
func readStdinCandidates() ([]analyst.NugCandidate, error) {
	var raw []nugCandidateInput
	if err := json.NewDecoder(os.Stdin).Decode(&raw); err != nil {
		return nil, fmt.Errorf("feedback judge: decoding stdin candidates: %w", err)
	}
	cands := make([]analyst.NugCandidate, len(raw))
	for i, c := range raw {
		cands[i] = analyst.NugCandidate{ID: c.ID, Text: c.Text}
	}
	return cands, nil
}

// cmdFeedbackJudge wires the kong CLI flags to runFeedbackJudge: segments the
// session, reads the retrieval-event feed and the stdin candidate bodies, and
// — on a disagreement — writes the pending bank file.
func cmdFeedbackJudge() error {
	cmd := &CLI.Feedback.Judge
	if strings.TrimSpace(cmd.Session) == "" {
		return errFeedbackSessionRequired
	}
	if strings.TrimSpace(cmd.SearchEvent) == "" {
		return errFeedbackSearchEventRequired
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	eventsFile, err := resolveFeedbackEventsPath(cmd.Events)
	if err != nil {
		return err
	}
	in, err := resolveFeedbackJudgeInputs(root, cmd.Session, eventsFile, data)
	if err != nil {
		return err
	}
	cands, err := readStdinCandidates()
	if err != nil {
		return err
	}

	cfg := analyst.Config{Model: cmd.Model, Timeout: cmd.Timeout}
	ctx, stop := analystContext()
	defer stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // shuffle blinding, not security

	result, err := runFeedbackJudge(ctx, cfg, analystRunRelevance, rng, cmd.SearchEvent, in.res, in.events, in.turns, cands)
	if err != nil {
		return err
	}
	if !result.Fired {
		return nil
	}
	return feedback.SavePending(pendingBankPath(data, cmd.Session), result.Ask)
}

// analystRunRelevance is the production relevanceJudge — a thin adapter so the
// seam's signature doesn't have to track analyst.RunRelevance's forever.
func analystRunRelevance(ctx context.Context, cfg analyst.Config, episode, prompt, query string, candidates []analyst.NugCandidate) (analyst.RelevanceResult, error) {
	return analyst.RunRelevance(ctx, cfg, episode, prompt, query, candidates)
}

// runFeedbackJudge is feedback judge's pure(ish) core — the only side effects
// are the injected judge call and rng draw, both seams. It:
//
//  1. Re-runs the FULL-SESSION segment→join→lattice pipeline (exactly
//     cmdHelped's call: score.JoinLegs + score.AdjudicateEvents over every
//     kind:search row), then filters to the one HelpedRecord whose SearchRef
//     matches searchEventID (Design: "Matching the single event" — no record
//     is a silent no-op, not an error).
//  2. Locates that event's own row (for Query/Returned/TS) and its owning
//     segment (for Prompt), via score.OwningSegment — the same join JoinLegs
//     used internally.
//  3. Caps each candidate's Text (truncateRunes, nugTextCap) and shuffles the
//     slice with rng (golden.go's BuildPool seam pattern) — rank-blind, per
//     analyst.NugCandidate's contract.
//  4. Calls judge, folds []analyst.NugJudgment{NugID,Grade} into the
//     map[string]int feedback.SearchFit wants (the one place RelGrade meets
//     feedback's plain int), and runs SearchFit over the event's actually-
//     Returned ids (not just whatever candidates survived the bash fetch).
//  5. feedback.Select on the (verdict, fit) pair decides fired/not.
func runFeedbackJudge(ctx context.Context, cfg analyst.Config, judge relevanceJudge, rng *rand.Rand,
	searchEventID string, res score.Result, events []retrievalevent.Event, turns []userTurn,
	rawCandidates []analyst.NugCandidate,
) (judgeResult, error) {
	repairAdj := repairAdjacency(events, turns)
	legs := score.JoinLegs(res.Segments, events, segEvidence(res.Segments), repairAdj, segmentID(res))
	records, _ := score.AdjudicateEvents(events, legs)

	var rec *score.HelpedRecord
	for i := range records {
		if records[i].SearchRef == searchEventID {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		return judgeResult{}, nil // no matching record — silent no-op, not an error
	}

	var ev *retrievalevent.Event
	for i := range events {
		if events[i].Kind == retrievalevent.KindSearch && events[i].EventID == searchEventID {
			ev = &events[i]
			break
		}
	}
	if ev == nil {
		return judgeResult{}, nil // defensive: rec matched but the row vanished
	}

	ownerIdx := score.OwningSegment(res.Segments, ev.TS)
	var prompt string
	if ownerIdx >= 0 {
		prompt = res.Segments[ownerIdx].Prompt
	}

	cands := make([]analyst.NugCandidate, len(rawCandidates))
	for i, c := range rawCandidates {
		cands[i] = analyst.NugCandidate{ID: c.ID, Text: truncateRunes(c.Text, nugTextCap)}
	}
	rng.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })

	result, err := judge(ctx, cfg, searchEventID, prompt, ev.Query, cands)
	if err != nil {
		return judgeResult{}, fmt.Errorf("feedback judge: relevance: %w", err)
	}

	// The one place analyst.RelGrade meets feedback's plain int grade map.
	grades := make(map[string]int, len(result.Judgments))
	for _, j := range result.Judgments {
		grades[j.NugID] = int(j.Grade)
	}

	returnedIDs := make([]string, len(ev.Returned))
	for i, r := range ev.Returned {
		returnedIDs[i] = r.NugID
	}

	fit, ok := feedback.SearchFit(returnedIDs, grades)
	if !ok {
		return judgeResult{}, nil // unclassifiable — never manufacture a disagreement from missing data
	}

	moment := feedback.Moment{Tool: "get_nug", Query: ev.Query}
	cand, fires := feedback.Select(*rec, fit, moment)
	return judgeResult{Fired: fires, Ask: cand}, nil
}
