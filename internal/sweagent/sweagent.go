// Package sweagent adapts nebius/SWE-agent-trajectories rows onto ferret's
// canonical event.Event stream. Each dataset row is one stream; the
// trajectory's role-tagged messages become events. Outcome labels (target,
// exit_status) are stream-level, so they go to a separate sidecar — not
// onto Event, which is per-action.
//
// The HF dataset card is vague about exact field names, so the row decoder
// is deliberately tolerant: it accepts several spellings for the trajectory
// list, message role, and action text (see Row.UnmarshalJSON and the format
// notes in the package README / PR).
package sweagent

import (
	"strings"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// Corpus marker: every SWE-agent stream lands under this Project so the
// existing commands (summary -by project, tokens -session) keep working and
// the corpus is distinguishable from CC transcripts.
const Project = "swe-agent"

// builtins are SWE-agent's first-class commands (the ACI verbs). Everything
// else in an ai action is treated as a shell command and routed through
// shellnorm so its tokens line up with the CC corpus vocabulary.
var builtins = map[string]bool{
	"open": true, "goto": true, "scroll_down": true, "scroll_up": true,
	"create": true, "edit": true, "submit": true,
	"search_dir": true, "search_file": true, "find_file": true,
}

// observation failure markers — unambiguous signals that the previous action
// errored. Kept modest on purpose (spec: "everything else ok"). No cfail
// analog: these are single commands, not compound chains.
var failMarkers = []string{
	"Traceback (most recent call last)",
	"command not found",
	"No such file or directory",
	"SyntaxError",
	"Your command ran successfully, but produced no output", // not a failure — excluded below
}

// Events converts one decoded row into its event stream. Position in the
// trajectory is the authoritative Seq. An ai message supplies the action;
// the immediately following user message is its observation, used only for
// the modest fail heuristic.
func Events(r *Row) []*event.Event {
	msgs := r.Trajectory
	var evs []*event.Event
	seq := 0
	for i := range msgs {
		m := msgs[i]
		if !m.isAI() {
			continue
		}
		action := strings.TrimSpace(m.action())
		if action == "" {
			continue
		}
		obs := ""
		if i+1 < len(msgs) && msgs[i+1].isUser() {
			obs = msgs[i+1].content()
		}
		evs = append(evs, eventFromAction(r.InstanceID, seq, action, obs))
		seq++
	}
	return evs
}

// eventFromAction parses one action string into a single canonical event.
func eventFromAction(session string, seq int, action, obs string) *event.Event {
	ev := &event.Event{
		Seq:     seq,
		Project: Project,
		Session: session,
		Status:  statusFor(obs),
	}
	head, rest := splitHead(action)
	if builtins[head] {
		ev.Kind = event.KindTool
		ev.Action = head
		ev.Target = firstArg(rest)
		ev.Detail = trunc(action)
		return ev
	}
	// Treat as a shell command: reuse shellnorm so bash tokens match CC
	// (sh:python, sh:git_diff, …). shellnorm splits compounds; an agent
	// action is one command, so the first segment is authoritative.
	segs, _ := shellnorm.Split(action)
	ev.Kind = event.KindShell
	if len(segs) == 0 {
		ev.Action = "sh"
		ev.Detail = trunc(action)
		return ev
	}
	ev.Action = segs[0].Cmd
	ev.Target = firstArg(rest)
	ev.Detail = trunc(segs[0].Raw)
	ev.Compound = len(segs) > 1
	return ev
}

// statusFor applies the modest observation heuristic.
func statusFor(obs string) string {
	if obs == "" {
		return event.StatusOK
	}
	for _, m := range failMarkers {
		if !strings.Contains(obs, m) {
			continue
		}
		// The "no output" banner is a success, not a failure; it shares the
		// list only so the contains-scan is in one place.
		if m == "Your command ran successfully, but produced no output" {
			continue
		}
		return event.StatusFail
	}
	// SWE-agent error banners. Either marker alone signals failure; many tools
	// emit only "Error: <msg>" without a separate all-caps ERROR token.
	if strings.Contains(obs, "Error:") || strings.Contains(obs, "ERROR") {
		return event.StatusFail
	}
	return event.StatusOK
}

// splitHead returns the command head (first whitespace-delimited word) and
// the remainder of the action string.
func splitHead(action string) (head, rest string) {
	action = strings.TrimSpace(action)
	if i := strings.IndexAny(action, " \t\n"); i >= 0 {
		return action[:i], strings.TrimSpace(action[i+1:])
	}
	return action, ""
}

// firstArg returns the first whitespace-delimited argument that is not a
// flag — the most identifying value (usually a file path).
//
// TODO(tokenizer): naive whitespace split leaves quoting artifacts visible
// only in the exact lens — search_dir "def parse" yields Target=`"def`, and a
// bare verb like `git diff` yields Target=`diff`. The tool/coarse lenses drop
// Target, so this is cosmetic for now; strip surrounding quotes when a real
// arg tokenizer lands.
func firstArg(rest string) string {
	for f := range strings.FieldsSeq(rest) {
		if strings.HasPrefix(f, "-") {
			continue
		}
		return trunc(f)
	}
	return ""
}

const detailMax = 160

func trunc(s string) string {
	if len(s) <= detailMax {
		return s
	}
	// Walk back to a rune boundary so we never split a multibyte rune.
	n := detailMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
