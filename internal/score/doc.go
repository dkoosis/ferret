// Package score is the home for ferret's reference-free / self-reference,
// per-task deterministic scorers — the third category beside internal/conform
// (replay against an analyst-supplied reference plan) and internal/mine
// (cross-corpus frequency discovery). Per the kuv design doc Decision 2, scorers
// here are neither analyst-replay nor corpus-frequency: they read a task in
// isolation, or against the agent's OWN stated plan/landmarks, and emit a score.
//
// The shared per-task unit every scorer rides is Result (one session's
// segmentation) and its Segment (one deterministic boundary candidate, with the
// tool calls it owns, its byte cost, and its tool-shape token sequence). The
// deterministic segmenter that produces these — formerly in cmd/ferret — lives
// here so cmd/ stays a thin dispatch+render shell and leaf scorers (gates ω,
// terminal-action, landmark, quality axes, repair/acceptance) reuse one substrate
// instead of each racing it into existence.
//
// Decision — observed-call type. Scorers here do NOT reuse conform.ObsCall. That
// type is bound to conformance replay: it carries an analyst-assigned plan Step
// and a Noise flag, and conform is frozen for this epic (no ω/landmark/quality
// extensions). score is reference-free, so it has no plan step to attach. The
// per-task input is the Segment — which already carries the owned call-index span
// (FirstCall/LastCall) and the ordered tool-shape tokens (Shape). A finer-grained
// per-call type is deferred until a leaf scorer actually needs one; it would be a
// local score type, never an import of conform.
//
// Everything here is a pure function of the transcript bytes: no time, no
// map-order output, no randomness — repeated runs on the same input are
// byte-stable, the acceptance contract leaf scorers inherit.
package score
