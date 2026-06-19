package analyst

// Relevance judging is the analyst leg of the search-quality spec
// (docs/design/ferret-search-quality-metrics.md, Phase 2). The deterministic
// engine supplies behavioral signals (consumed, use-rank, QPP); this judge
// supplies the graded relevance labels those signals can't: R4 context
// precision, R5 recall, R6 nDCG, and the Q3 intent-coverage check.
//
// Same stance as the applicability auditor in analyst.go: READ the intent — it
// is written in the human's prompt — do not infer it; emit JSON only; dk
// validates a sample. Two design choices specific to relevance:
//
//   - Graded, not binary. Grades 0–3 are what nDCG consumes (Järvelin &
//     Kekäläinen 2002, "Cumulated gain-based evaluation"). A binary judge can't
//     distinguish an exact-answer nug from a merely on-topic one, which is the
//     whole point of a ranking metric.
//   - The judge is BLIND to retrieval rank and to whether a nug was returned.
//     It grades a shuffled candidate pool (returned ∪ sampled-unreturned) so
//     recall (R5) is computable and the judge can't anchor on "the engine
//     returned it, so it must be relevant" — the presentation-bias trap. Pooling
//     to estimate recall over un-returned items: Spärck Jones & van Rijsbergen
//     1975 (the TREC pooling method).
//   - Conservative tie-break. When between two grades, pick the LOWER. Mirrors
//     analyst.go's "when unsure, served": an over-generous judge inflates every
//     downstream metric and burns dk's validation budget (AgentRewardBench: LLM
//     judges over-credit success).

import (
	"context"
	"strings"
)

// RelGrade is a graded relevance label for one nug against one intent.
type RelGrade int

const (
	GradeIrrelevant RelGrade = 0 // off-topic; does not bear on the intent
	GradeMarginal   RelGrade = 1 // touches the topic but does not materially help
	GradeRelevant   RelGrade = 2 // materially helps answer the intent
	GradeExact      RelGrade = 3 // directly answers the intent on its own
)

// NugJudgment is the judge's verdict on one candidate nug. Why is kept for
// traceability — every grade points at its reason so dk can audit cheaply, the
// same contract as analyst.Finding.Why.
type NugJudgment struct {
	NugID string   `json:"nugId"`
	Grade RelGrade `json:"grade"` // 0..3
	Why   string   `json:"why"`   // one line, grounded in the stated intent
}

// RelevanceResult is the adjudication of one retrieval episode's candidate pool.
// Returned-vs-pooled membership and rank are NOT given to the judge; the caller
// rejoins these judgments to the retrieval order to compute precision@k, recall,
// and nDCG.
type RelevanceResult struct {
	Episode   string        `json:"episode"`
	Judgments []NugJudgment `json:"judgments"`
}

// relevanceSystemPrompt frames the graded-relevance job. It is deliberately
// narrow: grade each nug against the human's intent, nothing else. It does not
// see the retrieval scores or rank — that blinding is what makes recall and
// ranking metrics built on these grades trustworthy.
const relevanceSystemPrompt = `You grade how well each stored "nug" (a saved note/fact/snippet) answers a human's intent in a Claude Code session.

You are given the human's PROMPT (their request, in their own words), the QUERY Claude sent to the get_nug search, and a CANDIDATES list of nugs. The candidates are in arbitrary order. You are NOT told which nugs the search returned, their rank, or their retrieval scores — judge each nug only on whether its CONTENT answers the human's intent. The intent is written in the PROMPT: read it, do not infer a different one.

Grade every candidate on this scale:
  3 — exact: on its own, this nug directly answers the intent.
  2 — relevant: this nug materially helps answer the intent (a key fact, the right file/symbol, a decision that settles it).
  1 — marginal: touches the same topic but does not materially help (adjacent, partial, stale-but-related).
  0 — irrelevant: does not bear on the intent.

Rules:
  - Judge CONTENT against INTENT, not surface word-overlap. A nug that shares keywords but answers a different question is 0 or 1, not 2.
  - When you are between two grades, choose the LOWER one. A missed-high grade costs less than an inflated one — a human validates a sample of your grades and an over-generous judge wastes that budget.
  - Grade each candidate independently. Do not grade on a curve or assume some candidate must be relevant; an episode may have zero relevant nugs.
  - Ground each "why" in the stated intent in one line.

Output ONLY a JSON object, no prose, no markdown fences.`

// relevanceSchema pins the exact RelevanceResult.Judgments shape. Constrain via
// prompt + parse (the documented Go SDK path), reusing decodeFirstObject from
// analyst.go for the fenced/prose guardrail.
const relevanceSchema = `Return a JSON object of this exact shape:
{"judgments":[{"nugId":"<id from CANDIDATES>","grade":0,"why":"<one line, grounded in the intent>"}]}
Include one entry for EVERY candidate, using its exact nugId. grade is an integer 0, 1, 2, or 3. No text outside the JSON.`

// NugCandidate is one pooled nug presented for judging. The caller shuffles the
// slice and strips rank/score before calling BuildRelevancePrompt — the judge
// must not see retrieval order.
type NugCandidate struct {
	ID   string
	Text string
}

// BuildRelevancePrompt assembles the (system, user) pair for grading one
// episode's candidate pool. Pure and deterministic so prompt assembly is
// unit-testable without a network call (same contract as BuildPrompt). The
// caller is responsible for having shuffled candidates and for capping each
// nug's Text.
func BuildRelevancePrompt(prompt, query string, candidates []NugCandidate) (system, user string) {
	var b strings.Builder
	b.WriteString("PROMPT:\n")
	b.WriteString(prompt)
	b.WriteString("\n\nQUERY:\n")
	b.WriteString(query)
	b.WriteString("\n\nCANDIDATES:\n")
	for _, c := range candidates {
		b.WriteString("- nugId: ")
		b.WriteString(c.ID)
		b.WriteString("\n  text: ")
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(relevanceSchema)
	return relevanceSystemPrompt, b.String()
}

// ParseRelevance extracts the per-nug judgments from a model response. Mirrors
// ParseFindings: shared decodeFirstObject (one-object decode tolerant of fences
// and trailing prose with a stray '}', ferret-001). Exported so parsing is
// unit-testable off the network.
func ParseRelevance(resp string) ([]NugJudgment, error) {
	var env struct {
		Judgments []NugJudgment `json:"judgments"`
	}
	if err := decodeFirstObject(resp, &env); err != nil {
		return nil, err
	}
	return env.Judgments, nil
}

// RunRelevance grades one episode's shuffled, rank-blind candidate pool. Same
// transport as Run (adaptive thinking); differs only in prompt assembly and
// parsing. The caller is responsible for having built the pool (BuildPool):
// candidates must already be the returned ∪ sampled-unreturned union, shuffled,
// with rank/score stripped, so recall is computable and the judge can't anchor
// on retrieval order.
func RunRelevance(ctx context.Context, cfg Config, episode, prompt, query string, candidates []NugCandidate) (RelevanceResult, error) {
	system, user := BuildRelevancePrompt(prompt, query, candidates)
	_, text, err := complete(ctx, cfg, system, user)
	if err != nil {
		return RelevanceResult{}, err
	}
	judgments, err := ParseRelevance(text)
	if err != nil {
		return RelevanceResult{}, err
	}
	return RelevanceResult{Episode: episode, Judgments: judgments}, nil
}

// --- Q3: intent coverage (a separate, cheaper judge) ---------------------------
//
// Intent coverage asks the HopInterp question the relevance judge does not:
// independent of what came back, did Claude's QUERY faithfully encode the
// human's intent? It needs no results, so it can run on every episode, not just
// pooled ones. Scale is 0–2 (coarser than relevance — there is less to grade).

// CoverageGrade scores how well a query encodes the prompt's retrievable intent.
type CoverageGrade int

const (
	CoverageMiss    CoverageGrade = 0 // query asks for something other than what the human wanted
	CoveragePartial CoverageGrade = 1 // captures some salient concepts, drops or distorts others
	CoverageFull    CoverageGrade = 2 // faithfully encodes the retrievable intent
)

// CoverageResult is the judge's verdict on one prompt→query translation.
type CoverageResult struct {
	Episode string        `json:"episode"`
	Grade   CoverageGrade `json:"grade"`
	Why     string        `json:"why"`
}

const coverageSystemPrompt = `You judge whether a search QUERY faithfully encodes a human's intent.

You are given the human's PROMPT and the QUERY Claude sent to the get_nug search. You do NOT see any results — judge only whether the query asks for what the human wanted, the retrievable parts of it.

Grade:
  2 — full: the query faithfully encodes the salient retrievable intent of the prompt.
  1 — partial: the query captures some salient concepts but drops or distorts others.
  0 — miss: the query asks for something materially different from what the human wanted.

Judge the query's INTENT-FIT, not its keyword overlap or whether you think it would succeed. A terse query that targets the right concept is full coverage; a verbose query aimed at the wrong concept is a miss. When between two grades, choose the LOWER. Ground your one-line reason in the prompt.

Output ONLY a JSON object, no prose, no markdown fences.`

const coverageSchema = `Return a JSON object of this exact shape:
{"grade":0,"why":"<one line, grounded in the prompt's intent>"}
grade is an integer 0, 1, or 2. No text outside the JSON.`

// BuildCoveragePrompt assembles the (system, user) pair for the intent-coverage
// judge. Pure and deterministic, same contract as BuildRelevancePrompt.
func BuildCoveragePrompt(prompt, query string) (system, user string) {
	var b strings.Builder
	b.WriteString("PROMPT:\n")
	b.WriteString(prompt)
	b.WriteString("\n\nQUERY:\n")
	b.WriteString(query)
	b.WriteString("\n\n")
	b.WriteString(coverageSchema)
	return coverageSystemPrompt, b.String()
}

// ParseCoverage extracts the single coverage verdict from a model response.
// Shares decodeFirstObject with the other analyst parsers (fence/prose tolerant).
func ParseCoverage(resp string) (CoverageResult, error) {
	var env struct {
		Grade CoverageGrade `json:"grade"`
		Why   string        `json:"why"`
	}
	if err := decodeFirstObject(resp, &env); err != nil {
		return CoverageResult{}, err
	}
	return CoverageResult{Grade: env.Grade, Why: env.Why}, nil
}

// RunCoverage judges one prompt→query translation (Q3). It needs no results, so
// it runs on every episode — not just pooled ones. Same transport as Run.
func RunCoverage(ctx context.Context, cfg Config, episode, prompt, query string) (CoverageResult, error) {
	system, user := BuildCoveragePrompt(prompt, query)
	_, text, err := complete(ctx, cfg, system, user)
	if err != nil {
		return CoverageResult{}, err
	}
	res, err := ParseCoverage(text)
	if err != nil {
		return CoverageResult{}, err
	}
	res.Episode = episode
	return res, nil
}
