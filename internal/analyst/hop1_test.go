package analyst

import (
	"context"
	"encoding/json"
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

// TestHop1RulesHashPinned pins the fingerprint's rules_hash (ferret-bbp.15 AC2):
// it must change iff the floor rules or the coverage prompt change. A failure
// here means the judge changed — if intentional, update wantHash (the replay
// version tag moves with it, which is the point).
func TestHop1RulesHashPinned(t *testing.T) {
	const wantHash = "393318d15b4cb20aa53b6dffe6d9f0b2"
	if hop1RulesHash != wantHash {
		t.Errorf("hop1RulesHash = %s, want %s (floor rules or coverage prompt changed — if intentional, update wantHash)", hop1RulesHash, wantHash)
	}
	const wantPromptHash = "04abe3774702e4e22367268c8beac1ab"
	if coveragePromptHash != wantPromptHash {
		t.Errorf("coveragePromptHash = %s, want %s (coverage prompt changed — if intentional, update wantPromptHash)", coveragePromptHash, wantPromptHash)
	}
}

// TestHop1FloorFingerprint: a floor-decided episode stamps the hop1 fingerprint
// with a nil LLM leg — deterministic verdict, versioned all the same.
func TestHop1FloorFingerprint(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	got, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{SelfRequery: true, Prompt: "find x", Query: "x"})
	if err != nil {
		t.Fatalf("Hop1: unexpected err %v", err)
	}
	fp := got.JudgeFingerprint
	if fp.Adjudicator != hop1Adjudicator || fp.Scheme != hop1Scheme || fp.RulesHash != hop1RulesHash {
		t.Errorf("fingerprint = %+v, want adjudicator=%s scheme=%s rules_hash=%s", fp, hop1Adjudicator, hop1Scheme, hop1RulesHash)
	}
	if fp.LLM != nil {
		t.Errorf("floor-decided LLM = %+v, want nil", fp.LLM)
	}
}

// TestHop1EscalationFingerprintCarriesLLM: an escalating episode stamps the LLM
// leg (model + prompt hash) even on the error path — the fingerprint records
// which judge was CONSULTED, and the no-key error fires after that decision.
func TestHop1EscalationFingerprintCarriesLLM(t *testing.T) {
	t.Setenv("FERRET_ANTHROPIC_API_KEY", "")
	got, err := Hop1(context.Background(), Config{}, "ep1", score.Episode{Prompt: "find the loto rules", Query: "loto"})
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	fp := got.JudgeFingerprint
	if fp.LLM == nil {
		t.Fatalf("escalating episode LLM leg = nil, want model+prompt_hash")
	}
	if fp.LLM.Model != DefaultModel || fp.LLM.PromptHash != coveragePromptHash {
		t.Errorf("LLM = %+v, want Model=%s PromptHash=%s", fp.LLM, DefaultModel, coveragePromptHash)
	}
}

// TestHop1FingerprintJSON pins the on-wire shape (D4, same contract as helped's
// TestJudgeFingerprintJSON): explicit null llm when the floor decided, and a
// {model, prompt_hash} object when the judge ran.
func TestHop1FingerprintJSON(t *testing.T) {
	b, err := json.Marshal(hop1Fingerprint(nil))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["adjudicator"] != "hop1" || got["scheme"] != "staged-floor-v1" {
		t.Errorf("adjudicator/scheme = %v/%v, want hop1/staged-floor-v1", got["adjudicator"], got["scheme"])
	}
	if v, ok := got["llm"]; !ok {
		t.Error(`"llm" key missing — want explicit null, not omitted`)
	} else if v != nil {
		t.Errorf("llm = %v, want null", v)
	}

	b, err = json.Marshal(hop1Fingerprint(&score.LLMFingerprint{Model: "m1", PromptHash: "abc"}))
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	llm, ok := got["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm = %v, want object", got["llm"])
	}
	if llm["model"] != "m1" || llm["prompt_hash"] != "abc" {
		t.Errorf("llm = %v, want {model:m1, prompt_hash:abc}", llm)
	}
}
