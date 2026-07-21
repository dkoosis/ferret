package analyst

// Hop1 is the interp-fidelity leg of ferret's two-hop retrieval-rating model
// (ferret-bbp.5): per episode, how faithfully did Claude's get_nug QUERY encode
// what the human actually asked for — the prompt→query leg, separate from Hop2's
// deterministic query→result leg (internal/score/qpp.go). Prior art for grading a
// natural-language→query translation by whether it targets the right intent:
// text-to-SQL execution-accuracy (Yu et al., Spider, EMNLP 2018).
//
// It composes an existing judge, it does not build a new one: the escalation is
// the analyst's Q3 intent-coverage judge (RunCoverage), already results-blind and
// conservative-tie-break by construction — its bias guards (AgentRewardBench
// over-credit caveat) are documented at coverageSystemPrompt in relevance.go and
// are not restated here. Hop1 adds one thing on top: a deterministic floor so the
// paid judge is only ever invoked on the case the trace itself can't answer.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/dkoosis/ferret/internal/score"
)

// hop1Adjudicator and hop1Scheme name this adjudicator + scheme in every
// fingerprint it stamps — the staged sibling of helped's lattice-v1
// (ferret-bbp.15): a deterministic floor that escalates to a paid judge,
// so the LLM leg is per-result (null when the floor decided) rather than
// always-null.
const (
	hop1Adjudicator = "hop1"
	hop1Scheme      = "staged-floor-v1"
)

// hop1FloorRules is the canonical serialization of the deterministic floor —
// the staged half of the rules the fingerprint vouches for (the other half is
// coverageSystemPrompt, the judge the clean case escalates to). Restated as
// data, not derived from code, for the same reason helped's lattice hashes
// its own table: the hash must change iff the RULES change, and this string
// is the rules. Keep it in lockstep with Hop1Escalates + Hop1's floor branch.
const hop1FloorRules = `self_requery|retry_motif -> low, no call
no prompt -> no signal, no call
clean first-try -> escalate to results-blind Q3 coverage judge
coverage: full -> high, partial -> mid, miss -> low`

// hop1RulesHash covers floor + escalation prompt together: a revision to
// either is a different judge, and a replay reading only rules_hash must see
// that. Computed once at package init, pinned by test.
var hop1RulesHash = hashRules(hop1FloorRules + "\n" + coverageSystemPrompt)

// coveragePromptHash is the LLM leg's prompt hash — just the paid judge's
// system prompt, so the model+prompt pair identifies the escalation target
// independent of floor-rule churn.
var coveragePromptHash = hashRules(coverageSystemPrompt)

// hashRules is the shared 128-bit rule digest (same shape as helped's
// computeRulesHash: sha256 truncated to 16 bytes, hex).
func hashRules(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// hop1Fingerprint builds the fingerprint one Hop1Result carries. llm is nil
// for a floor-decided (or no-signal) episode and names the consulted
// model+prompt when the coverage judge ran.
func hop1Fingerprint(llm *score.LLMFingerprint) score.JudgeFingerprint {
	return score.JudgeFingerprint{
		Adjudicator: hop1Adjudicator,
		Scheme:      hop1Scheme,
		RulesHash:   hop1RulesHash,
		LLM:         llm,
	}
}

// Hop1Grade mirrors score.QPPGrade's low/mid/high taxonomy so a Hop1 grade and a
// Hop2 grade are two readings of the same episode, joinable without translation.
type Hop1Grade string

const (
	Hop1Low  Hop1Grade = "low"
	Hop1Mid  Hop1Grade = "mid"
	Hop1High Hop1Grade = "high"
)

// Hop1Result is one episode's interp-fidelity verdict. Grade is "" when no
// judgment was made (empty Prompt); Why is "" whenever the deterministic floor or
// the no-signal path decided (only an actual judge verdict carries a reason). The
// token counts are this call's own burn — zero for a floor-graded episode, which
// made no call, reported not omitted so a reader can tell "cost nothing" from
// "unknown".
type Hop1Result struct {
	Episode      string    `json:"episode"`
	Grade        Hop1Grade `json:"grade,omitempty"`
	Why          string    `json:"why,omitempty"`
	LLMCalled    bool      `json:"llmCalled"`
	InputTokens  int64     `json:"inputTokens,omitempty"`
	OutputTokens int64     `json:"outputTokens,omitempty"`
	// JudgeFingerprint versions the verdict (contract amendment rank 3,
	// ferret-bbp.15): same shape helped stamps, hop1's own scheme. LLM is
	// null when the deterministic floor decided.
	JudgeFingerprint score.JudgeFingerprint `json:"judge_fingerprint"`
}

// fromCoverageGrade maps the Q3 coverage scale (0=miss/1=partial/2=full) onto the
// shared Hop1 taxonomy.
func fromCoverageGrade(g CoverageGrade) Hop1Grade {
	switch g {
	case CoverageFull:
		return Hop1High
	case CoveragePartial:
		return Hop1Mid
	default:
		return Hop1Low
	}
}

// Hop1Escalates reports whether Hop1 would pay for the LLM judge on this episode:
// true only for a clean-first-try episode (no self-requery, no retry motif) that
// has an opening prompt to judge. It is the negation of the deterministic floor —
// single-sourced here so the CLI's --emit-prompt path decides "which episodes
// escalate" exactly the way a live run does.
func Hop1Escalates(ep score.Episode) bool {
	return !ep.SelfRequery && !ep.RetryMotif && ep.Prompt != ""
}

// Hop1 grades one episode's interp-fidelity leg. The deterministic floor decides
// first and is never LLM-overridable: a self-requery or a retry-after-failure is
// the trace confessing its own interpretation churn, so it floors to Low with NO
// network call (mirrors dialogue.AttributeHop, which only ever attributes to
// HopInterp when self-requery is set). This also confines the paid judge to the
// exact set where the trace carries no defect signal — the "clean first-try" case
// where a query may have silently misread the ask and happened to return
// something — which is where AgentRewardBench's over-credit risk is highest and
// the borrowed judge's result-blindness earns its keep. An episode with no
// captured opening prompt yields no signal (Grade ""), never a call.
func Hop1(ctx context.Context, cfg Config, episodeID string, ep score.Episode) (Hop1Result, error) {
	if !Hop1Escalates(ep) {
		// Floor: self-requery / retry motif → hard Low; no opening prompt → no signal.
		grade := Hop1Grade("")
		if ep.SelfRequery || ep.RetryMotif {
			grade = Hop1Low
		}
		return Hop1Result{Episode: episodeID, Grade: grade, LLMCalled: false, JudgeFingerprint: hop1Fingerprint(nil)}, nil
	}
	llm := &score.LLMFingerprint{Model: cfg.model(), PromptHash: coveragePromptHash}
	cov, usage, err := RunCoverage(ctx, cfg, episodeID, ep.Prompt, ep.Query)
	if err != nil {
		return Hop1Result{
			Episode:          episodeID,
			LLMCalled:        true,
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			JudgeFingerprint: hop1Fingerprint(llm),
		}, err
	}
	return Hop1Result{
		Episode:          episodeID,
		Grade:            fromCoverageGrade(cov.Grade),
		Why:              cov.Why,
		LLMCalled:        true,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		JudgeFingerprint: hop1Fingerprint(llm),
	}, nil
}
