package analyst

import (
	"context"
	"errors"
	"testing"

	"github.com/dkoosis/ferret/internal/score"
)

// TestHop1FloorsOnSelfRequery / TestHop1FloorsOnRetryMotif prove the
// deterministic floor short-circuits to Low with no network call: with the API
// key cleared, a floored episode still returns cleanly (err == nil, LLMCalled
// false) — if the floor regressed and let the call through, complete() would
// return ErrNoAPIKey and the assertion would fail.
func TestHop1FloorsOnSelfRequery(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	got, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{SelfRequery: true, Prompt: "find x", Query: "x"})
	if err != nil {
		t.Fatalf("Hop1: unexpected err %v", err)
	}
	if got.Grade != Hop1Low || got.LLMCalled {
		t.Errorf("got %+v, want Grade=low LLMCalled=false", got)
	}
}

func TestHop1FloorsOnRetryMotif(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	got, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{RetryMotif: true, Prompt: "find x", Query: "x"})
	if err != nil {
		t.Fatalf("Hop1: unexpected err %v", err)
	}
	if got.Grade != Hop1Low || got.LLMCalled {
		t.Errorf("got %+v, want Grade=low LLMCalled=false", got)
	}
}

// TestHop1NoSignalWhenPromptEmpty: a clean episode with no captured opening
// prompt has nothing to judge — no grade, no call, no error.
func TestHop1NoSignalWhenPromptEmpty(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	got, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{Prompt: "", Query: "x"})
	if err != nil {
		t.Fatalf("Hop1: unexpected err %v", err)
	}
	if got.Grade != "" || got.LLMCalled {
		t.Errorf("got %+v, want Grade=\"\" LLMCalled=false", got)
	}
}

// TestHop1WithoutAPIKeyReturnsErrNoAPIKey: a clean-first-try episode with a real
// prompt is the escalation case — it must reach the judge, so with no key it
// surfaces ErrNoAPIKey unchanged (the --emit-prompt escape hatch is the CLI's).
func TestHop1WithoutAPIKeyReturnsErrNoAPIKey(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	_, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{Prompt: "find the loto rules", Query: "loto"})
	if !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("err = %v, want ErrNoAPIKey", err)
	}
}

// TestFromCoverageGradeMapsToHop1Taxonomy pins the Q3-coverage → Hop1 grade map:
// the two judges share the low/mid/high string taxonomy so a downstream reader
// joins them without a translation table.
func TestFromCoverageGradeMapsToHop1Taxonomy(t *testing.T) {
	cases := []struct {
		in   CoverageGrade
		want Hop1Grade
	}{
		{CoverageFull, Hop1High},
		{CoveragePartial, Hop1Mid},
		{CoverageMiss, Hop1Low},
	}
	for _, c := range cases {
		if got := fromCoverageGrade(c.in); got != c.want {
			t.Errorf("fromCoverageGrade(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
