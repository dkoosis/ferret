// Package analyst is ferret's LLM precision layer. ferret's deterministic
// engine has high recall and bounded precision: it can flag every rg-over-Go
// query in the corpus, but a hand-check showed ~20% of those are legitimate
// (counts, conflict-marker scans, dependency-cache searches) — not the
// tool-for-intent mismatch they pattern-match to. Closing that gap needs
// judgment over the WRITTEN intent, not more regex.
//
// The split this package implements:
//
//	deterministic ferret  →  the session spine (recall: every prompt+reasoning+call)
//	analyst (this pkg)     →  reads the spine, judges each call against the task it
//	                          served, flags applicability mismatches (precision)
//	dk                     →  validates a sample of the verdicts (the loop's anchor)
//
// The model READS intent — it is written down in the [user]/[asst] spine lines
// (prompt + stated reasoning beside each call) — rather than inferring it from
// thin signal. That is the whole reason this is tractable; see memory
// ferret-direction-priorart-2026-06-16.
//
// Borrowed framing (per kuv epic convention — cite the method + source in code):
//   - "invoked != served its purpose": a tool call is judged by whether it
//     advanced the stated task, not by whether it ran. From AgentRewardBench
//     (LLM judges over-credit success → keep dk as the human validator) and the
//     process-mining flower-model warning. Mirror of internal/conform's seam.
//   - applicability mismatch unit = the task (segmented by shared goal), not the
//     call; tool-vs-tool substitution (rg where snipe fits) is the behavioral
//     signal IR abandoned (Jones & Klinkner). nug cb1c87f041dc.
package analyst

import (
	"encoding/json"
	"errors"
	"strings"
)

// Fit is the verdict on whether a tool call served the task it was issued for.
type Fit string

const (
	// FitServed — the tool was the right instrument for the stated intent.
	FitServed Fit = "served"
	// FitMismatch — a different available tool fit the intent better; the chosen
	// tool was invoked but did not serve the task as well as it could have.
	FitMismatch Fit = "mismatch"
)

// Finding is one adjudicated tool call: the task it served (read from the
// spine's stated intent), the call as it appeared, and the verdict + reasoning.
// The shape is also the JSON contract the model fills — see schemaInstruction.
type Finding struct {
	Task       string `json:"task"`             // the stated intent the call served (read, not inferred)
	Call       string `json:"call"`             // the tool call as it appeared in the spine
	ToolUsed   string `json:"toolUsed"`         // the tool actually chosen, e.g. "rg"
	Fit        Fit    `json:"fit"`              // served | mismatch
	Better     string `json:"better,omitempty"` // the tool that would have fit, when fit=mismatch (e.g. "snipe")
	Why        string `json:"why"`              // one-line reasoning, grounded in the stated intent
	Confidence string `json:"confidence"`       // low | medium | high
}

// Result is the adjudication of one session.
type Result struct {
	Session  string    `json:"session"`
	Model    string    `json:"model"`
	Findings []Finding `json:"findings"`
}

// Mismatches returns only the findings the model judged a tool-for-intent
// mismatch — the actionable subset dk validates.
func (r Result) Mismatches() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Fit == FitMismatch {
			out = append(out, f)
		}
	}
	return out
}

// systemPrompt frames the analyst's job. It bakes in the discriminator a
// hand-run validated: the four false-positive classes that look like mismatches
// but are legitimate (counts, conflict-marker scans, content/log search,
// dependency-cache search outside a code-index's scope). Without these, a naive
// "rg-in-a-code-repo = mismatch" judge over-flags by ~20% (and the deterministic
// layer over-flags ~8×). Conservatism is the point — dk is the validator, and an
// over-eager judge wastes the validation budget it exists to conserve.
const systemPrompt = `You audit a single Claude Code session for tool-for-intent applicability mismatches.

You are given a SPINE: the session's user prompts ([user]), the assistant's stated reasoning ([asst]/[think]), each tool call ([call]) and each result ([rslt] status+size). The agent's INTENT is written down in the prompt and reasoning lines beside each call. READ that intent — do not infer or guess it.

For each tool call that matters, decide whether the tool SERVED the task it was issued for, or whether a different tool that was plausibly available would have fit the stated intent better. "Invoked" is not "served its purpose": judge the call by whether it advanced the task, not by whether it ran.

The canonical mismatch: a code-INTELLIGENCE query — find a symbol's definition, its callers/callees, a type's implementations, the call graph — answered with a raw text search (rg/grep) when a code-navigation tool (e.g. snipe) was available and fits that intent better.

Be conservative. Flag a call as a mismatch ONLY when the stated intent clearly calls for a different tool. The following are LEGITIMATE uses of rg/grep — NOT mismatches — even inside a code repo:
  - counting matches (rg -c) — a count, not navigation
  - scanning for merge-conflict markers, TODO/FIXME, or other literal text patterns
  - searching file CONTENT, logs, or config — not code structure
  - searching a dependency/module cache or vendored code OUTSIDE the local project's index scope
  - quick presence checks (does this import string appear?) where a one-shot grep is reasonable

When unsure whether a call is a mismatch, mark fit "served" — a missed mismatch costs less than a false alarm.

Output ONLY a JSON object, no prose, no markdown fences.`

// schemaInstruction is appended to the user turn so the model returns the exact
// Result.Findings shape. We constrain via prompt + parse (the documented Go SDK
// path) rather than output_config.format, whose Go binding we won't guess.
const schemaInstruction = `Return a JSON object of this exact shape:
{"findings":[{"task":"<the stated intent this call served>","call":"<the call as it appeared>","toolUsed":"<tool name, e.g. rg>","fit":"served|mismatch","better":"<tool that would fit, only when fit=mismatch>","why":"<one line, grounded in the stated intent>","confidence":"low|medium|high"}]}
Include only calls worth judging (tool invocations tied to a discernible task). If there are no mismatches, still return any "served" judgments you made, or an empty findings array. No text outside the JSON.`

// BuildPrompt assembles the (system, user) pair for a session spine. Pure and
// deterministic so prompt assembly is unit-testable without a network call.
func BuildPrompt(spine string) (system, user string) {
	var b strings.Builder
	b.WriteString("SPINE:\n")
	b.WriteString(spine)
	b.WriteString("\n\n")
	b.WriteString(schemaInstruction)
	return systemPrompt, b.String()
}

var errNoJSON = errors.New("analyst: model response contained no JSON object")

// ParseFindings extracts the Result.Findings array from a model response,
// tolerating ```json fences and leading/trailing prose (a guardrail in case the
// model ignores "JSON only"). It locates the first '{' and last '}' and decodes
// the span between them. Exported so response parsing is unit-testable
// independent of the network.
func ParseFindings(resp string) ([]Finding, error) {
	s := strings.TrimSpace(resp)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, errNoJSON
	}
	var env struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &env); err != nil {
		return nil, err
	}
	return env.Findings, nil
}
