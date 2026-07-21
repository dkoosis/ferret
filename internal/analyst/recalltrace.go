package analyst

// Recall-use judging is the memory leg of the analyst (trixi-bot gg-8rq.16 /
// ferret-izo). trixi-bot's flat-file memory auto-recalls fragments at run start;
// this judge reads, PER RUN, whether each recalled fragment SHAPED the final
// answer (served) or was noise the run didn't use (mismatch), and — when a
// fragment missed — what recall SHOULD have surfaced (better). It reuses
// analyst.Finding as its output contract so trixi-bot's eval/adjudicate mapper
// consumes it unchanged: fit=served → the fragment was USED (use-rate), fit=
// mismatch → a recall MISS carrying `better` as the miss signal (miss-rate).
//
// Same stance as the applicability auditor in analyst.go and the relevance judge
// in relevance.go: judge memory-USE (did the content shape the answer?), NOT
// tool-for-intent; emit JSON only; be conservative — dk validates a sample. Two
// design choices specific to recall:
//
//   - The recall trace carries NO explicit user prompt — only the recalled
//     fragments, the final answer, and the run outcome. The run's intent is read
//     from the FINAL ANSWER: judge whether each fragment's content is reflected
//     in, or plausibly informed, that answer.
//   - The tie-break leans toward `mismatch`, the OPPOSITE of analyst.go's "when
//     unsure, served". Here `served` credits recall as useful; over-crediting it
//     inflates the health metric and hides the recall gap the measurement exists
//     to expose (AgentRewardBench: LLM judges over-credit success). So a fragment
//     is `served` only when its specific content clearly surfaces in the answer.

import (
	"context"
	"strings"
)

// RecalledItem is one recalled memory fragment presented for judging: its
// provenance (Source) and content (Text). Mirrors trixi-bot's RecalledFragment
// minus the timestamp the judge doesn't need.
type RecalledItem struct {
	Source string
	Text   string
}

// recallSystemPrompt frames the memory-use job. It deliberately inverts
// analyst.go's conservatism: there the unsure verdict is "served" (avoid a false
// alarm); here it is "mismatch" (avoid over-crediting recall). The measurement is
// a health/gap signal for phase-4 reinforcement, so an honest-low use-rate beats
// an inflated one.
const recallSystemPrompt = `You audit ONE agent run for whether its auto-recalled MEMORY shaped the answer.

At the start of the run the agent's flat-file memory automatically surfaced a set of FRAGMENTS (saved notes/facts/decisions from earlier sessions). The run then produced a FINAL ANSWER. You do NOT see the user's original question — read the run's intent from the FINAL ANSWER itself.

For each recalled FRAGMENT, decide whether it SERVED the run — its specific content is reflected in, or plausibly informed, the final answer — or whether it was a MISMATCH: recalled but not used, off-target for what this run actually needed. "Recalled" is not "used": judge the fragment by whether its content shaped the answer, not by whether memory surfaced it.

When a fragment is a MISMATCH, name in "better" what recall SHOULD have surfaced to actually help this run — the topic, fact, decision, or source the answer needed and memory did not provide. If you cannot name a better target, leave "better" empty.

Be conservative toward NOT crediting use. Shared vocabulary or same-topic-ness is NOT use — mark a fragment "served" ONLY when its specific content clearly surfaces in the final answer. When you are unsure whether a fragment shaped the answer, mark it "mismatch": for this measurement an over-counted "used" inflates the recall-health number and hides the gap this audit exists to expose.

Output ONLY a JSON object, no prose, no markdown fences.`

// recallSchema pins the exact Finding shape trixi-bot's mapper reads. It reuses
// the adjudicate findings contract (task/call/toolUsed/fit/better/why/confidence)
// so the downstream projection (fit=served→used, fit=mismatch→miss with better)
// works unchanged; only the semantics of each field shift to memory-use.
const recallSchema = `Return a JSON object of this exact shape:
{"findings":[{"task":"<the run's need, read from the final answer>","call":"<the recalled fragment's source, echoed verbatim>","toolUsed":"recall","fit":"served|mismatch","better":"<what recall should have surfaced, only when fit=mismatch; may be empty>","why":"<one line: did this fragment's content shape the answer?>","confidence":"low|medium|high"}]}
Return one entry for EVERY recalled fragment, using its exact source in "call". fit is "served" or "mismatch". No text outside the JSON.`

// BuildRecallPrompt assembles the (system, user) pair for judging one run's
// recalled fragments against its final answer. Pure and deterministic so prompt
// assembly is unit-testable without a network call (same contract as BuildPrompt
// / BuildRelevancePrompt). The caller is responsible for capping fragment sizes.
func BuildRecallPrompt(finalText, outcome string, recalled []RecalledItem) (system, user string) {
	var b strings.Builder
	b.WriteString("FINAL ANSWER:\n")
	b.WriteString(finalText)
	b.WriteString("\n\nRUN OUTCOME: ")
	b.WriteString(outcome)
	b.WriteString("\n\nRECALLED FRAGMENTS:\n")
	for _, f := range recalled {
		b.WriteString("- source: ")
		b.WriteString(f.Source)
		b.WriteString("\n  text: ")
		b.WriteString(f.Text)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(recallSchema)
	return recallSystemPrompt, b.String()
}

// RunRecallTrace judges one run's recalled fragments against its final answer and
// returns the per-fragment findings plus the responding model id. Same transport
// as Run (adaptive thinking); differs only in prompt assembly. Reuses
// ParseFindings — the output IS the adjudicate findings shape, so the trixi-bot
// mapper reads it with no change. The caller aggregates findings across a batch
// of runs into one Result.
func RunRecallTrace(ctx context.Context, cfg Config, finalText, outcome string, recalled []RecalledItem) ([]Finding, string, error) {
	system, user := BuildRecallPrompt(finalText, outcome, recalled)
	model, text, _, err := complete(ctx, cfg, system, user)
	if err != nil {
		return nil, "", err
	}
	findings, err := ParseFindings(text)
	if err != nil {
		return nil, "", err
	}
	return findings, model, nil
}
