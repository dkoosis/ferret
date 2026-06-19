package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/score"
	"github.com/dkoosis/ferret/internal/transcript"
)

// dialogueCap bounds the per-turn label rendered/stored.
const dialogueCap = 120

// dlgResult is the whole user-turn tagging for one session: the per-turn move
// signals plus the rolled-up PARADISE outcome. This is the v1 emission ferret-bbp
// feeds into the Finding model (so a finding can say "routine SUCCEEDS" vs "loop
// ends in abandonment" — sharper than burn alone).
type dlgResult struct {
	Session    string            `json:"session"`
	Project    string            `json:"project"`
	Agent      string            `json:"agent,omitempty"`
	Signals    []dialogue.Signal `json:"signals"`
	Outcome    dialogue.Outcome  `json:"outcome"`
	Repairs    int               `json:"repairs"`
	Accepts    int               `json:"accepts"`
	DecodeErrs int               `json:"decodeErrs,omitempty"`
}

// cmdDialogue wires the kong flags to dialogue tagging, resolving the transcript
// root the same way spine/segments do.
func cmdDialogue() error {
	cmd := &CLI.Dialogue
	if strings.TrimSpace(cmd.Session) == "" {
		return errSpineSessionRequired
	}
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	root := cmd.Root
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	return runDialogue(os.Stdout, root, cmd.Session, cmd.Format)
}

// runDialogue resolves session (a prefix) to one transcript, tags every genuine
// user turn with a v1 dialogue Move, and emits the per-turn signals plus the
// session outcome. Carriers, control built-ins, and affirmations that the
// segmenter folds in are skipped here too — they are plumbing, not intent.
//
// ∇ This is the SESSION-level rollup (one outcome for the whole session). The
// per-EPISODE rollup (one outcome per segment, via dialogue.Classify over each
// segment's turns) is the follow-on once this is wired through the segmenter.
func runDialogue(w io.Writer, root, session, format string) error {
	src, distinct, err := resolveSpineSource(root, session)
	if err != nil {
		return err
	}
	if distinct > 1 {
		fmt.Fprintf(os.Stderr,
			"ferret: --session %q matched %d sessions; emitting %q (use a longer prefix to disambiguate)\n",
			session, distinct, src.Session)
	}

	var res dlgResult
	res.Session, res.Project, res.Agent = src.Session, src.Project, src.Agent
	var moves []dialogue.Move
	turn := 0
	if err := transcript.ReadLines(src.Path, func(line []byte) error {
		tagUserTurn(line, &res, &moves, &turn)
		return nil
	}); err != nil {
		return err
	}
	res.Outcome = dialogue.Classify(moves)

	if format == fmtJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	return writeDialogueText(w, res)
}

// tagUserTurn decodes one transcript line and, if it carries a genuine user
// prompt that isn't a fold-in (carrier/control/affirmation), tags it with a Move
// and appends the signal. Mirrors the segmenter's tolerance: a bad line bumps the
// decode counter and is dropped.
func tagUserTurn(line []byte, res *dlgResult, moves *[]dialogue.Move, turn *int) {
	var raw transcript.Raw
	if err := json.Unmarshal(line, &raw); err != nil {
		res.DecodeErrs++
		return
	}
	if raw.IsMeta || raw.Message == nil || raw.Type != "user" {
		return
	}
	prompt := score.PromptText(raw.Message.Content)
	if prompt == "" {
		return
	}
	if skip, _, _ := score.ClassifyBoundary(prompt); skip {
		return
	}
	move, cue := dialogue.TagMove(prompt)
	res.Signals = append(res.Signals, dialogue.Signal{
		Turn: *turn, Move: move, Cue: cue,
		Text: truncateRunes(prompt, dialogueCap),
	})
	*moves = append(*moves, move)
	switch move {
	case dialogue.MoveRepair:
		res.Repairs++
	case dialogue.MoveAccept:
		res.Accepts++
	default:
		// neutral and the reserved v2 moves carry no v1 tally
	}
	*turn++
}

// writeDialogueText emits the human-readable rendering.
func writeDialogueText(w io.Writer, res dlgResult) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "dialogue session=%s project=%s", res.Session, res.Project)
	if res.Agent != "" {
		fmt.Fprintf(bw, " agent=%s", res.Agent)
	}
	fmt.Fprintln(bw)
	fmt.Fprintln(bw,
		"≡ v1 user-turn moves (regex-first): repair = human-side friction marker, "+
			"accept = outcome success leg. Outcome = PARADISE task-success rollup.")
	for _, s := range res.Signals {
		cue := ""
		if s.Cue != "" {
			cue = fmt.Sprintf(" cue=%q", s.Cue)
		}
		fmt.Fprintf(bw, "[turn %d] %-8s%s  %s\n", s.Turn, s.Move, cue, s.Text)
	}
	fmt.Fprintf(bw, "--- turns=%d repairs=%d accepts=%d outcome=%s",
		len(res.Signals), res.Repairs, res.Accepts, res.Outcome)
	if res.DecodeErrs > 0 {
		fmt.Fprintf(bw, " decode-errs=%d", res.DecodeErrs)
	}
	fmt.Fprintln(bw)
	return bw.Flush()
}
