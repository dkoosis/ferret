package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
)

// cmdFixesProposals mines one session's assistant text for confessed
// call-waste and prints candidate substitution proposals for dk to review.
// v1 is regex/heuristic-only (no LLM leg, ferret-kuv.16) and never calls
// fixes.RecordSub itself — dk confirms a proposal by running the EXISTING
// `ferret fixes sub` command separately with the fields this prints.
func cmdFixesProposals() error {
	cmd := &CLI.Fixes.Proposals
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
	if err := validateFormat(cmd.Format); err != nil {
		return err
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	return runFixesProposals(os.Stdout, root, cmd.Session, cmd.Format)
}

// runFixesProposals resolves session (a prefix) to one transcript, walks
// every assistant turn's text through fixes.DetectProposals, and renders the
// accumulated proposals. Mirrors runDialogue's session-resolution +
// transcript.ReadLines shape (cmd/ferret/dialogue.go).
func runFixesProposals(w io.Writer, root, session, format string) error {
	src, distinct, err := resolveSpineSource(root, session)
	if err != nil {
		return err
	}
	warnAmbiguousSession(session, distinct, src.Session, "emitting")

	proposals, err := scanProposals(src)
	if err != nil {
		return err
	}

	if format == fmtJSON {
		return out.JSON(w, map[string]any{
			"session": src.Session, "proposals": proposals, keyTotal: len(proposals),
		})
	}
	return writeProposalsText(w, src.Session, proposals)
}

// scanProposals walks one transcript's assistant turns and runs each turn's
// text through fixes.DetectProposals, accumulating the proposals in transcript
// order. A line that fails to decode is skipped (tolerant, same as
// tagUserTurn) rather than failing the whole scan.
func scanProposals(src transcript.Source) ([]fixes.Proposal, error) {
	var proposals []fixes.Proposal
	err := transcript.ReadLines(src.Path, func(line []byte) error {
		var raw transcript.Raw
		if uerr := json.Unmarshal(line, &raw); uerr != nil {
			return nil //nolint:nilerr // tolerate a broken line; not fatal to the scan
		}
		if raw.IsMeta || raw.Message == nil || raw.Type != "assistant" {
			return nil
		}
		text := turn.PromptText(raw.Message.Content)
		if text == "" {
			return nil
		}
		proposals = append(proposals, fixes.DetectProposals(src.Session, text)...)
		return nil
	})
	return proposals, err
}

// writeProposalsText renders proposals for dk's confirm loop: each row names
// the confessed intent+better fix plus the exact `ferret fixes sub` call that
// records it, so confirming is a copy-paste away (--wrong is the one field
// regex can't reliably isolate from prose, so it stays a placeholder).
func writeProposalsText(w io.Writer, session string, proposals []fixes.Proposal) error {
	sink := out.NewSink(w, 0, 0)
	defer sink.Close()
	sink.Head("fixes proposals session=%s count=%d", session, len(proposals))
	if len(proposals) == 0 {
		sink.Row("no confessed call-waste found (no self-audit text, or none matched a known waste shape)")
		return nil
	}
	for _, p := range proposals {
		sink.Row("%s -> %s  (%s)", p.IntentClass, p.Better, p.Tell)
		sink.Row("  example: %s", p.Example)
		sink.Row("  confirm: ferret fixes sub --intent %s --wrong <tool> --better %s --example %s --session %s",
			shellQuote(p.IntentClass), shellQuote(p.Better), shellQuote(p.Example), shellQuote(session))
	}
	return nil
}

// shellQuote wraps s in POSIX single quotes so it pastes into a shell as one
// literal argument. %q (Go-string quoting) is NOT shell-safe: a transcript-
// derived p.Example can carry $(...), backticks, or a bare " that survives
// %q's escaping and still gets interpreted by the shell dk pastes the printed
// "confirm:" line into. Single quotes suppress every shell metacharacter
// except ' itself, which closes-escapes-reopens the quoted string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
