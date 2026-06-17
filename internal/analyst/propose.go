package analyst

import (
	"context"
	"encoding/json"
	"strings"
)

// propose is the second analyst mode (ferret-567 step 5, the loop's payoff).
//
// adjudicate judges calls already made (precision over the spine). propose looks
// FORWARD: it reads the deterministic cost-leak ranking ferret emits ('ferret
// candidates') plus the spine those tasks ran in, and returns ONE concrete fix
// per task — automate the recurring routine, or de-context the output-heavy IO.
// ferret measures the leak; the analyst proposes the patch; dk validates it
// (AgentRewardBench: keep the human as the arbiter). The candidate metrics give
// the model WHERE the cost is; the spine gives it the actual calls so the fix
// can name real tools, not generalities.

// ProposeKind is the lever a proposal pulls to cut a task's cost.
type ProposeKind string

const (
	// ProposeAutomate — the task is a recurring, low-variation routine; script
	// it (shell script, Makefile target, or a Claude Code skill/slash-command)
	// so it runs in one step instead of many model turns.
	ProposeAutomate ProposeKind = "automate"
	// ProposeDeContext — the task's cost is dominated by tool OUTPUT pulled into
	// context (high out-weight); shrink what enters the model (summarize/filter,
	// read a slice, use a navigation tool that returns a focused answer).
	ProposeDeContext ProposeKind = "de-context"
	// ProposeNone — no clear fix (genuinely novel work, output that must enter
	// context, a one-off). The conservative default — a bad proposal wastes the
	// validation budget propose exists to spend well.
	ProposeNone ProposeKind = "none"
)

// Proposal is one analyst fix for a ranked cost-leak task. The shape is also the
// JSON contract the model fills — see proposeSchemaInstruction.
type Proposal struct {
	Task       int         `json:"task"`       // the candidate task index this proposal addresses
	Kind       ProposeKind `json:"kind"`       // automate | de-context | none
	Proposal   string      `json:"proposal"`   // the concrete fix (or why none)
	Why        string      `json:"why"`        // one-line rationale, grounded in metrics + calls
	Confidence string      `json:"confidence"` // low | medium | high
}

// ProposeResult is the proposal set for one session.
type ProposeResult struct {
	Session   string     `json:"session"`
	Model     string     `json:"model"`
	Proposals []Proposal `json:"proposals"`
}

// Actionable returns only the proposals that name a fix (kind != none) — the
// subset dk acts on and validates.
func (r ProposeResult) Actionable() []Proposal {
	var out []Proposal
	for _, p := range r.Proposals {
		if p.Kind != ProposeNone {
			out = append(out, p)
		}
	}
	return out
}

// proposeSystemPrompt frames the optimization job and bakes in the two levers
// plus the conservatism guard. The "actionable, names a real tool/command"
// constraint is the discriminator between a useful proposal and the generic
// "be more efficient" advice that would waste dk's validation pass.
const proposeSystemPrompt = `You are ferret's optimization analyst. You are given a session's most expensive TASKS — a deterministic cost-leak ranking — plus the session SPINE: the prompts, stated reasoning, and tool calls those tasks ran. Each candidate carries metrics: cost (bytes in+out), outBytes and out-weight (the share of cost that is tool OUTPUT pulled into context), pivots (mid-task goal shifts), and calls. The candidate's "intent" is its opening prompt — match it to the [user] line in the spine to find that task's calls.

For each candidate task, propose ONE concrete fix that would cut its cost, or decline. Two levers:

- automate: the task is a recurring, low-variation routine — a fixed sequence of calls run the same way each time (low pivots). Propose scripting it: a shell script, a Makefile target, or a Claude Code skill/slash-command, so it runs in one step instead of many model turns.

- de-context: the task's cost is dominated by tool OUTPUT entering context (high out-weight, ow→1). Propose how to shrink what the model ingests — summarize/filter the output, read a slice instead of a whole file, use a code-navigation tool (e.g. snipe) that returns a focused answer instead of a raw dump, or pipe through a formatter.

Ground every proposal in BOTH the candidate's metrics and the actual calls in the spine. Name the specific tools and commands. A proposal must be actionable: "be more efficient" is not a proposal; "replace the three full-file Reads of internal/*.go with 'snipe pack'" is.

Be conservative. If a candidate has no clear fix — genuinely novel work, output that must enter context, a true one-off — set kind "none" and say why. A bad proposal wastes the human validation budget this exists to spend well.

Output ONLY a JSON object, no prose, no markdown fences.`

// proposeSchemaInstruction pins the exact ProposeResult.Proposals shape.
const proposeSchemaInstruction = `Return a JSON object of this exact shape:
{"proposals":[{"task":<candidate task index>,"kind":"automate|de-context|none","proposal":"<the concrete fix, or why none applies>","why":"<one line grounded in the metrics and the calls>","confidence":"low|medium|high"}]}
Emit one proposal per candidate task you were given. No text outside the JSON.`

// BuildProposePrompt assembles the (system, user) pair from the candidate bundle
// (the 'ferret candidates' JSON) and the session spine. Pure and deterministic
// so prompt assembly is unit-testable without a network call.
func BuildProposePrompt(bundle, spine string) (system, user string) {
	var b strings.Builder
	b.WriteString("CANDIDATES (ranked cost-leak tasks):\n")
	b.WriteString(bundle)
	b.WriteString("\n\nSPINE:\n")
	b.WriteString(spine)
	b.WriteString("\n\n")
	b.WriteString(proposeSchemaInstruction)
	return proposeSystemPrompt, b.String()
}

// ParseProposals extracts the Proposals array from a model response, tolerating
// ```json fences and surrounding prose (a guardrail if the model ignores "JSON
// only"). Mirrors ParseFindings. Exported so parsing is unit-testable off the
// network.
func ParseProposals(resp string) ([]Proposal, error) {
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
		Proposals []Proposal `json:"proposals"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &env); err != nil {
		return nil, err
	}
	return env.Proposals, nil
}

// RunPropose feeds the candidate bundle + spine to Claude and parses the fix
// proposals. Same transport as Run (adaptive thinking); differs only in prompt
// assembly and parsing.
func RunPropose(ctx context.Context, cfg Config, session, bundle, spine string) (ProposeResult, error) {
	system, user := BuildProposePrompt(bundle, spine)
	model, text, err := complete(ctx, cfg, system, user)
	if err != nil {
		return ProposeResult{}, err
	}
	proposals, err := ParseProposals(text)
	if err != nil {
		return ProposeResult{}, err
	}
	return ProposeResult{Session: session, Model: model, Proposals: proposals}, nil
}
