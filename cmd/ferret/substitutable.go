package main

import (
	"io"
	"os"
	"sort"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// SubstitutableCmd is the kong-ready flag struct for `ferret substitutable`
// (ferret-cax item 3): a corpus-wide ranking of Bash calls a native tool
// (Grep/Glob/Read) could have replaced — deterministic, no judge call. Emitting
// the ranking is all this command does; recording a rule as validated is
// `ferret fixes sub` (internal/fixes) — a separate, dk-approved ledger this
// command reads nothing from and writes nothing to.
//
// Registration is left to the primary (write-set boundary): add
//
//	Substitutable SubstitutableCmd `cmd:"" help:"Rank Bash calls a native tool (Grep/Glob/Read) could replace — deterministic, no judge." name:"substitutable"`
//
// to the CLI struct in cmd/ferret/main.go, and
//
//	case "substitutable":
//		err = cmdSubstitutable(CLI.Substitutable)
//
// to the dispatch switch, mirroring every other subcommand.
type SubstitutableCmd struct {
	CommonFlags
}

// cmdSubstitutable wires the kong flags to mine.MineSubstitutions over the
// canonical events artifact — the same load-then-mine shape as cmdPolling
// (polling.go).
func cmdSubstitutable(cmd SubstitutableCmd) error {
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return err
	}
	rep := mine.MineSubstitutions(events)
	if c.format == fmtJSON {
		return writeSubstJSON(os.Stdout, rep, c.limit)
	}
	return writeSubstText(os.Stdout, rep, c.limit, c.maxBytes)
}

// writeSubstJSON emits the ranked table as a single JSON document, pre-
// capping rows to limit (0 = unlimited) — mirrors writePollingJSON; the
// out.JSON contract ignores row limits itself, so the cap happens here.
func writeSubstJSON(w io.Writer, rep mine.SubstReport, limit int) error {
	total := len(rep.Rows)
	rows := rep.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return out.JSON(w, map[string]any{
		"rows":       rows,
		"sessions":   rep.Sessions,
		"excluded":   rep.Excluded,
		keyTotal:     total,
		keyTruncated: len(rows) < total,
	})
}

// writeSubstText emits the dense human/analyst rendering: the substitution
// table (verb, native tool, calls, session spread, score) followed by the
// excluded-reason tally, so a reader sees both what would substitute and how
// much the corpus's escape hatches held back.
func writeSubstText(w io.Writer, rep mine.SubstReport, limit, maxBytes int) error {
	sink := out.NewSink(w, limit, maxBytes)
	defer sink.Close()
	about(sink,
		"≡ substitutable: Bash calls a native tool (Grep/Glob/Read) could have served",
		"≡ instead — deterministic verb+flag table over shellnorm output, no judge call in",
		"≡ the path. FLOOR semantics: pipe/compound/redirect/expansion/unsupported-flag/",
		"≡ truncated calls are excluded rather than guessed at (excluded tally below).",
		"≡ score = calls × sessions, so a habit spread across sessions outranks one",
		"≡ runaway session. This command only ranks candidates — it does not validate a",
		"≡ rule or write to the ledger; a dk-approved rewrite is recorded separately via",
		"≡ `ferret fixes sub` (internal/fixes), which `ferret fixes subs` lists.")

	sink.Head("substitutable rows=%d sessions=%d", len(rep.Rows), rep.Sessions)
	for _, row := range rep.Rows {
		if !sink.Row("calls=%-5d sessions=%-4d score=%-8.0f %-6s %-20s  %q",
			row.Calls, row.Sessions, row.Score, row.Tool, row.Key, row.Exemplar) {
			break
		}
	}

	if len(rep.Excluded) > 0 {
		sink.Head("excluded (floor — never a guess):")
		for _, reason := range sortedExcludedReasons(rep.Excluded) {
			sink.Row("%s=%d", reason, rep.Excluded[reason])
		}
	}
	return nil
}

// sortedExcludedReasons orders the Excluded tally's keys deterministically —
// map iteration order is not stable, and this is rendered text.
func sortedExcludedReasons(excluded map[string]int) []string {
	reasons := make([]string, 0, len(excluded))
	for reason := range excluded {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}
