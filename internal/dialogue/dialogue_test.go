package dialogue

import (
	"strings"
	"testing"
)

func TestTagMove(t *testing.T) {
	tests := []struct {
		name string
		turn string
		want Move
	}{
		// repair — corrective / rejection
		{"leading no", "no, use snipe instead", MoveRepair},
		{"not that", "not that one, the other file", MoveRepair},
		{"thats wrong", "that's wrong", MoveRepair},
		{"try again", "try again with the flag set", MoveRepair},
		{"i meant", "i meant the other package", MoveRepair},
		{"you missed", "you missed the error case", MoveRepair},
		{"actually no", "actually no, revert that", MoveRepair},

		// accept — success leg
		{"leading yes", "yes exactly that", MoveAccept},
		{"great", "great, that works", MoveAccept},
		{"lgtm", "lgtm ship it", MoveAccept},
		{"save this", "save this to the kg", MoveAccept},
		{"thanks", "thanks, perfect", MoveAccept},

		// neutral — fresh task content, no signal
		{"fresh request", "add a test for the parser", MoveNeutral},
		{"question", "where do the events live?", MoveNeutral},
		{"empty", "", MoveNeutral},

		// dev-jargon trap — workflow vocabulary, NOT repair or affect
		{"kill jargon", "kill that process and restart", MoveNeutral},
		{"dead jargon", "the daemon is dead, boot it", MoveNeutral},

		// repair wins when both signals appear
		{"repair beats accept", "no, but the rename is great", MoveRepair},

		// long-turn gate: a cue buried deep in composed/pasted content is task
		// content, not a correction — only the leading window counts.
		{"buried cue in long turn", "Help me respond to Jeremy about the call next week " +
			strings.Repeat("with a lot of neutral lead-in prose first ", 5) +
			"and then that isn't relevant", MoveNeutral},
		{"leading cue in long turn", "no, that's wrong — " +
			strings.Repeat("here is the long explanation that follows ", 10), MoveRepair},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := TagMove(tt.turn)
			if got != tt.want {
				t.Errorf("TagMove(%q) = %q, want %q", tt.turn, got, tt.want)
			}
		})
	}
}

func TestTagMoveCue(t *testing.T) {
	if _, cue := TagMove("no, try the other one"); cue == "" {
		t.Error("repair turn returned empty cue; want the matched phrase for traceability")
	}
	if _, cue := TagMove("just add a function"); cue != "" {
		t.Errorf("neutral turn returned cue %q; want empty", cue)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		moves []Move
		want  Outcome
	}{
		{"clean success", []Move{MoveNeutral, MoveAccept}, OutcomeSuccess},
		{"one repair then accept", []Move{MoveNeutral, MoveRepair, MoveAccept}, OutcomeSuccess},
		{"repair heavy but lands", []Move{MoveRepair, MoveRepair, MoveRepair, MoveAccept}, OutcomeRepairHeavy},
		{"abandoned on repair", []Move{MoveNeutral, MoveRepair}, OutcomeAbandoned},
		{"no signal", []Move{MoveNeutral, MoveNeutral}, OutcomeUnknown},
		{"empty", nil, OutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.moves); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.moves, got, tt.want)
			}
		})
	}
}

func TestAttributeHop(t *testing.T) {
	tests := []struct {
		name string
		move Move
		ctx  TurnContext
		want Hop
	}{
		{"non-repair is none", MoveAccept, TurnContext{SelfRequery: true}, HopNone},
		{"self-requery is interp", MoveRepair, TurnContext{SelfRequery: true}, HopInterp},
		{"empty result is retrieval", MoveRepair, TurnContext{EmptyResult: true}, HopRetrieval},
		{"oversized is retrieval", MoveRepair, TurnContext{OversizedResult: true}, HopRetrieval},
		{"no signal is none", MoveRepair, TurnContext{}, HopNone},
		{"self-requery beats retrieval", MoveRepair, TurnContext{SelfRequery: true, EmptyResult: true}, HopInterp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AttributeHop(tt.move, tt.ctx); got != tt.want {
				t.Errorf("AttributeHop(%q, %+v) = %q, want %q", tt.move, tt.ctx, got, tt.want)
			}
		})
	}
}
