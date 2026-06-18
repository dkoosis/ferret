package dialogue

// Hop attribution answers, for a repair turn, WHICH hop of the retrieval episode
// broke. The end-to-end shape is one episode, two translation hops:
//
//	human turn  →[hop 1: interpret]→  agent's tool query  →[hop 2: execute]→  result  →  human reaction
//
// A repair (the human reaction) indicts one of the hops, and they are separable:
//
//   - HopInterp   — the agent's query did not faithfully encode the human's
//     intent. Tell: the agent SELF-re-queried (the bd_show!→bd_show, Edit!→Read
//     motifs) and fixed it without new human input — interpretation was the gap.
//   - HopRetrieval — the query was faithful but the results were bad anyway
//     (empty/oversized result set; the human supplies or diagnoses a data fix,
//     e.g. "it probably moved the file"). Post-retrieval QPP-style result-set
//     features are the deterministic tell here (Cronen-Townsend 2002 clarity /
//     NQC family — reference-free, fits ferret's no-ground-truth posture).
//   - HopNone — no hop broke (a plain accept, or a repair the model can't place).
//
// This is the leverage the trace gives over flat eval: it doesn't just say "the
// search was bad", it says which hop to fix. ∇ The signals below are NOT yet
// wired — they live in the event/segment stream (self-requery is a retry motif;
// empty/oversized result is a tool_result size). This file fixes the contract so
// the wiring is a fill-in, not a redesign.
type Hop string

const (
	HopInterp    Hop = "interp"
	HopRetrieval Hop = "retrieval"
	HopNone      Hop = "none"
)

// TurnContext is the evidence a repair turn is judged against — the deterministic
// signals the event/segment stream already measures, surfaced here so attribution
// is a pure function of them.
//
// ∇ Populated by the (not-yet-built) wiring in cmd/ferret that walks a repair
// turn back to the tool calls of the segment it closes.
type TurnContext struct {
	// SelfRequery — the agent repeated the same action/target shortly after a
	// failure within this episode (event.Event.Retry), i.e. it tried to fix its
	// own query before the human spoke. Points at HopInterp.
	SelfRequery bool
	// EmptyResult — a tool_result in this episode came back empty or error.
	// Points at HopRetrieval.
	EmptyResult bool
	// OversizedResult — a tool_result dragged back an outlying payload (the QPP
	// "low clarity" analog: the query was too broad). Points at HopRetrieval.
	OversizedResult bool
}

// AttributeHop places a repair onto the hop it indicts, given the episode's
// deterministic signals. ∇ STUB — returns HopNone until TurnContext is wired from
// the event/segment stream. The precedence (once filled) is: a self-requery that
// preceded the human repair is the clearest interpretation-gap tell; an
// empty/oversized result with no self-requery is a retrieval-gap tell.
func AttributeHop(move Move, ctx TurnContext) Hop {
	if move != MoveRepair {
		return HopNone
	}
	switch {
	case ctx.SelfRequery:
		return HopInterp
	case ctx.EmptyResult || ctx.OversizedResult:
		return HopRetrieval
	default:
		return HopNone
	}
}
