package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/score"
)

// errTestJudge is a static sentinel for the faked per-episode judge failure
// (err113: no dynamic errors).
var errTestJudge = errors.New("boom")

// TestHop1EmitPromptAssemblesPerEpisode: the --emit-prompt path renders an
// escalated episode's assembled judge prompt and a floored episode's one-line
// note distinctly, touching no network type.
func TestHop1EmitPromptAssemblesPerEpisode(t *testing.T) {
	eps := []score.Episode{
		{Session: "s", RootSeq: 1, Prompt: "find the loto rules", Query: "loto territory"}, // clean → escalate
		{Session: "s", RootSeq: 4, Prompt: "find x", Query: "x", SelfRequery: true},        // floored
	}
	got := hop1EmitPrompts(eps)

	if !strings.Contains(got, "=== SYSTEM ===") || !strings.Contains(got, "=== USER ===") {
		t.Errorf("escalated episode should assemble a SYSTEM/USER prompt:\n%s", got)
	}
	if !strings.Contains(got, "find the loto rules") || !strings.Contains(got, "loto territory") {
		t.Errorf("escalated prompt should carry the episode's prompt+query:\n%s", got)
	}
	if !strings.Contains(got, "floor: low (self-requery)") {
		t.Errorf("floored episode should render a floor note, not a prompt:\n%s", got)
	}
	// The floored episode's SYSTEM prompt must NOT be assembled for it.
	if strings.Count(got, "=== SYSTEM ===") != 1 {
		t.Errorf("only the escalated episode should assemble a prompt; got %d SYSTEM blocks:\n%s",
			strings.Count(got, "=== SYSTEM ==="), got)
	}
}

// TestWriteRetrievalHop1TextRendersGradeAndBurn: the text scorecard shows each
// episode's grade and the compactBurn-rendered token total.
func TestWriteRetrievalHop1TextRendersGradeAndBurn(t *testing.T) {
	rows := []hop1Row{
		{Result: analyst.Hop1Result{Episode: "s#1", Grade: analyst.Hop1High, Why: "targets the right concept", LLMCalled: true, InputTokens: 1200, OutputTokens: 340}},
		{Result: analyst.Hop1Result{Episode: "s#4", Grade: analyst.Hop1Low, LLMCalled: false}}, // floored
	}
	var buf bytes.Buffer
	sink := out.NewSink(&buf, 0, 0)
	writeRetrievalHop1Text(sink, "abc", rows)
	sink.Close()
	got := buf.String()

	if !strings.Contains(got, "high") || !strings.Contains(got, "low") {
		t.Errorf("scorecard should show per-episode grades:\n%s", got)
	}
	// compactBurn(1200+340=1540) → "1k"
	if !strings.Contains(got, "1k") {
		t.Errorf("scorecard should show the compactBurn token total (1k):\n%s", got)
	}
}

// TestWriteRetrievalJSONIncludesPerEpisodeHop1: with hop1 rows, each episode's
// JSON object carries a hop1 key (grade/why/llmCalled/tokens/error); with nil
// rows, no hop1 key appears on the common path.
func TestWriteRetrievalJSONIncludesPerEpisodeHop1(t *testing.T) {
	eps := []score.Episode{
		{Session: "s", RootSeq: 1, Query: "q1"},
		{Session: "s", RootSeq: 4, Query: "q2"},
	}
	rows := []hop1Row{
		{Result: analyst.Hop1Result{Episode: "s#1", Grade: analyst.Hop1High, Why: "ok", LLMCalled: true, InputTokens: 100, OutputTokens: 20}},
		{Result: analyst.Hop1Result{Episode: "s#4", LLMCalled: true}, Err: "rate limited"},
	}

	var withHop1 bytes.Buffer
	if err := writeRetrievalJSON(&withHop1, "s", score.Rollup{}, eps, 0, rows); err != nil {
		t.Fatalf("writeRetrievalJSON: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(withHop1.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, withHop1.String())
	}
	episodes, ok := env["episodes"].([]any)
	if !ok || len(episodes) != 2 {
		t.Fatalf("episodes = %v, want 2 entries", env["episodes"])
	}
	ep0, ok := episodes[0].(map[string]any)
	if !ok {
		t.Fatalf("episode[0] not an object: %v", episodes[0])
	}
	h0, ok := ep0["hop1"].(map[string]any)
	if !ok {
		t.Fatalf("episode[0] missing hop1 key: %v", ep0)
	}
	if h0["grade"] != "high" {
		t.Errorf("episode[0].hop1.grade = %v, want high", h0["grade"])
	}
	ep1, ok := episodes[1].(map[string]any)
	if !ok {
		t.Fatalf("episode[1] not an object: %v", episodes[1])
	}
	h1, ok := ep1["hop1"].(map[string]any)
	if !ok {
		t.Fatalf("episode[1] missing hop1 key: %v", ep1)
	}
	if h1["error"] != "rate limited" {
		t.Errorf("episode[1].hop1.error = %v, want \"rate limited\"", h1["error"])
	}
	if _, ok := env["hop1Cost"]; !ok {
		t.Errorf("session-level hop1Cost key missing: %v", env)
	}

	// nil rows → the common path carries no hop1 noise.
	var noHop1 bytes.Buffer
	if err := writeRetrievalJSON(&noHop1, "s", score.Rollup{}, eps, 0, nil); err != nil {
		t.Fatalf("writeRetrievalJSON(nil): %v", err)
	}
	if strings.Contains(noHop1.String(), "hop1") {
		t.Errorf("nil rows should emit no hop1 key:\n%s", noHop1.String())
	}
}

// TestCmdRetrievalHop1RequiresSession: --hop1 without --session fails fast, before
// any network work.
func TestCmdRetrievalHop1RequiresSession(t *testing.T) {
	if err := validateHop1(true, ""); !errors.Is(err, errHop1RequiresSession) {
		t.Errorf("validateHop1(true, \"\") = %v, want errHop1RequiresSession", err)
	}
	if err := validateHop1(true, "abc"); err != nil {
		t.Errorf("validateHop1(true, \"abc\") = %v, want nil", err)
	}
	if err := validateHop1(false, ""); err != nil {
		t.Errorf("validateHop1(false, \"\") = %v, want nil (no --hop1, no guard)", err)
	}
}

// TestHop1LoopContinuesPastPerEpisodeError: a per-episode judge failure never
// aborts the run — every episode is reported, the failed one carries its error
// and an empty grade, and the loop signals a non-zero-exit-worthy result.
func TestHop1LoopContinuesPastPerEpisodeError(t *testing.T) {
	eps := []score.Episode{
		{Session: "s", RootSeq: 1, Prompt: "a", Query: "qa"},
		{Session: "s", RootSeq: 2, Prompt: "b", Query: "qb"},
		{Session: "s", RootSeq: 3, Prompt: "c", Query: "qc"},
	}
	// Fake judge: succeed on every episode except the 2nd.
	judge := func(_ context.Context, _ analyst.Config, id string, ep score.Episode) (analyst.Hop1Result, error) {
		if ep.RootSeq == 2 {
			return analyst.Hop1Result{Episode: id, LLMCalled: true}, errTestJudge
		}
		return analyst.Hop1Result{Episode: id, Grade: analyst.Hop1High, LLMCalled: true}, nil
	}

	rows, anyErr := runHop1Episodes(context.Background(), analyst.Config{}, eps, judge)
	if len(rows) != 3 {
		t.Fatalf("want all 3 episodes reported, got %d", len(rows))
	}
	if !anyErr {
		t.Error("anyErr = false, want true (a per-episode failure must mark the run non-zero)")
	}
	if rows[1].Err == "" || rows[1].Result.Grade != "" {
		t.Errorf("failed episode row = %+v, want empty grade + error string", rows[1])
	}
	if rows[0].Result.Grade != analyst.Hop1High || rows[2].Result.Grade != analyst.Hop1High {
		t.Errorf("surrounding episodes should be judged normally: %+v %+v", rows[0], rows[2])
	}
}

// TestHop1FanOutPreservesOrderAndBoundsConcurrency: the ferret-fk8 fan-out judges
// episodes concurrently (two judges rendezvous — a serial loop would deadlock the
// barrier and time the test out), never exceeds the 8-wide semaphore, and emits
// rows in episode order regardless of completion order.
func TestHop1FanOutPreservesOrderAndBoundsConcurrency(t *testing.T) {
	const n = 20
	eps := make([]score.Episode, n)
	for i := range eps {
		eps[i] = score.Episode{Session: "s", RootSeq: i + 1, Prompt: "p", Query: "q"}
	}

	var inFlight, maxInFlight atomic.Int32
	barrier := make(chan struct{})
	var barrierOnce sync.Once
	judge := func(_ context.Context, _ analyst.Config, id string, _ score.Episode) (analyst.Hop1Result, error) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		if cur >= 2 {
			barrierOnce.Do(func() { close(barrier) })
		}
		// Wait for proof of overlap — deadlocks (test timeout) if calls run serially.
		<-barrier
		return analyst.Hop1Result{Episode: id, Grade: analyst.Hop1High, LLMCalled: true}, nil
	}

	rows, anyErr := runHop1Episodes(context.Background(), analyst.Config{}, eps, judge)
	if anyErr {
		t.Fatal("anyErr = true, want false")
	}
	if len(rows) != n {
		t.Fatalf("want %d rows, got %d", n, len(rows))
	}
	for i, r := range rows {
		if want := hop1EpisodeID(eps[i]); r.Result.Episode != want {
			t.Fatalf("rows[%d].Episode = %q, want %q (order must be preserved)", i, r.Result.Episode, want)
		}
	}
	if got := maxInFlight.Load(); got > 8 {
		t.Errorf("max in-flight judges = %d, want <= 8", got)
	}
}
