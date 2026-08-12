package out

import (
	"fmt"
	"io"
)

// Contextual disclosure (AXI #9 / DK-AXI operating-contract rule 11): after a
// listing, append the exact commands that can follow it, so a reader — human
// or agent — chains to the next stage instead of guessing a command name.
//
// The rule has one hard edge, and it is why this is a mechanism rather than a
// convenience: the hint names LEGAL MOVES, never a workflow. Exposing "you can
// drill here" is discovery; telling the caller which one to run next is plan
// steering, which the contract forbids — the tool does not own the caller's
// plan.
//
// Two more constraints ride along, both learned from the hand-rolled versions
// this replaces (cmd/ferret/candidates.go, cmd/ferret/adjudicate.go):
//
//   - Text mode only. JSON's schema is the contract; a prose hint inside it is
//     noise a parser has to skip.
//   - Never on an empty result. A hint that says "drill into this" when there
//     is nothing to drill into sends a reader on an errand that returns
//     nothing — the exact loop these hints exist to prevent.

// Next writes the trailing `next:` block naming the commands that may follow
// this output. Zero commands writes nothing, which is what makes the
// empty-result rule a no-op at the call site rather than an if at every
// caller.
//
// One command renders inline; several render as an indented list, so a reader
// sees a menu of legal moves rather than a sentence to parse:
//
//	next: ferret adjudicate --session ab12 --propose
//
//	next:
//	  ferret polling
//	  ferret misfires
func Next(w io.Writer, cmds ...string) {
	writeNext(func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
	}, cmds)
}

// NextHead writes the same block through a Sink's uncapped header path, so the
// hint survives row truncation — a `next:` line dropped by --limit is a hint
// the caller never sees, and the truncated case is exactly when a reader most
// needs to know where to go next.
func (s *Sink) NextHead(cmds ...string) {
	writeNext(s.Head, cmds)
}

// writeNext is the shared rendering, parameterized by how one line reaches the
// output — a plain writer or a Sink header. Empty strings are dropped so a
// caller can pass a conditional hint inline (`hintIf(cond)`) without an if.
func writeNext(line func(format string, args ...any), cmds []string) {
	kept := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if c != "" {
			kept = append(kept, c)
		}
	}
	switch len(kept) {
	case 0:
		return
	case 1:
		line("next: %s", kept[0])
	default:
		line("next:")
		for _, c := range kept {
			line("  %s", c)
		}
	}
}
