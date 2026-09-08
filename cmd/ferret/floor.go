package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dkoosis/ferret/internal/floor"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
)

// floorDefaultLimit is the compact default for the ranked session list — the
// bead asks for "a sampled set of sessions", not a full corpus dump; --limit
// -1 is the escape hatch (applyDefaultLimit's convention, reused by hand here
// since FloorCmd carries no CommonFlags/*common — see the doc below).
const floorDefaultLimit = 10

// FloorCmd is the kong-ready flag struct for `ferret floor` (ferret-khh): the
// fixed cost every session pays before dk's first real turn, as a share of
// total session bytes — a different question from the scoreboard's per-
// command burn ranking (internal/mine/scoreboard.go, ferret-4vx).
//
// No CommonFlags: this reads raw transcripts directly (transcript.Walk, the
// same convention `ferret reach` uses), not the ingested events.jsonl corpus
// — --data/ensureData would imply a dependency this command doesn't have.
type FloorCmd struct {
	Root     string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
	Format   string `help:"Output format: text|json." default:"text" name:"format"`
	Limit    int    `help:"Max session rows to print (0 = compact default; N = exactly N; negative = unlimited)." default:"0" name:"limit"`
	MaxBytes int    `help:"Max output bytes, text only (0 = unlimited)." default:"0" name:"max-bytes"`
	// RulesGlobal/RulesProject default to the SAME two tiers the bead's
	// Givens name (~/.claude/rules/**, .claude/rules/**) — read straight off
	// disk, since neither ever appears as its own line in any transcript
	// (see the internal/floor package doc). RulesProject resolves against
	// the CURRENT directory, not per-session: a corpus spans many projects,
	// and this command reports ONE rule-file cost applied uniformly, the
	// same simplification the bead's own "8 files / 4 files" framing makes.
	RulesGlobal  string `help:"Global rule-file glob, measured from disk (default: ~/.claude/rules/*.md)." name:"rules-global"`
	RulesProject string `help:"Project rule-file glob, measured from disk (default: ./.claude/rules/*.md, this invocation's CWD)." default:".claude/rules/*.md" name:"rules-project"`
}

// cmdFloor wires the kong flags to runFloor + the two renderers.
func cmdFloor() error {
	cmd := &CLI.Floor
	if cmd.Format != fmtText && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	root, err := resolveRoot(cmd.Root)
	if err != nil {
		return err
	}
	rulesGlobal := cmd.RulesGlobal
	if rulesGlobal == "" {
		home, herr := userHomeDir()
		if herr != nil {
			return fmt.Errorf("%w (needed for --rules-global's default)", errNoHomeDir)
		}
		rulesGlobal = filepath.Join(home, ".claude", "rules", "*.md")
	}

	rep, err := runFloor(root, rulesGlobal, cmd.RulesProject)
	if err != nil {
		return err
	}
	if cmd.Format == fmtJSON {
		return writeFloorJSON(os.Stdout, rep)
	}
	return writeFloorText(os.Stdout, rep, cmd.Limit, cmd.MaxBytes)
}

// runFloor walks the transcript root, scans every main-session transcript
// (subagent streams carry no SessionStart hook and are skipped, same
// convention runReach uses), and measures the two rule-file tiers from disk.
// A single unreadable transcript is skipped and counted, not fatal — the
// corpus is large and one bad file must not sink the whole sample.
func runFloor(root, rulesGlobal, rulesProject string) (floor.Report, error) {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return floor.Report{}, err
	}
	var rep floor.Report
	for _, src := range srcs {
		if src.Agent != "" {
			continue
		}
		sess, serr := floor.ScanSource(src)
		if serr != nil {
			rep.DecodeErrs++
			continue
		}
		rep.Sessions = append(rep.Sessions, sess)
	}
	gf, gb, err := floor.RuleFiles(rulesGlobal)
	if err != nil {
		return floor.Report{}, err
	}
	pf, pb, err := floor.RuleFiles(rulesProject)
	if err != nil {
		return floor.Report{}, err
	}
	rep.RuleFilesGlobal, rep.RuleBytesGlobal = gf, gb
	rep.RuleFilesProject, rep.RuleBytesProject = pf, pb
	return rep, nil
}

// writeFloorJSON emits every sampled session plus the corpus-wide rollup and
// the two rule-file tiers, so a JSON consumer never has to recompute Rollup
// itself to get the headline share.
func writeFloorJSON(w io.Writer, rep floor.Report) error {
	return out.JSON(w, map[string]any{
		keySessions: rep.Sessions,
		"totals":    floor.Rollup(rep.Sessions, rep.RuleBytes()),
		"ruleFiles": map[string]any{
			"global":  map[string]any{"files": rep.RuleFilesGlobal, "bytes": rep.RuleBytesGlobal},
			"project": map[string]any{"files": rep.RuleFilesProject, "bytes": rep.RuleBytesProject},
		},
		"decodeErrs": rep.DecodeErrs,
	})
}

// writeFloorText renders the headline totals (fixed floor vs. per-turn burn,
// the floor's share of total session bytes) followed by the sampled sessions
// ranked worst-share-first — the bead's own point: a short session shows the
// worst ratio, and averaging the corpus hides exactly that session.
func writeFloorText(w io.Writer, rep floor.Report, limit, maxBytes int) error {
	sink := out.NewSink(w, applyFloorLimit(limit), maxBytes)
	defer sink.Close()

	about(sink,
		"≡ floor: the fixed cost every session pays before dk's first real turn — the",
		"≡ SessionStart hook render (⭐ banner + itzy context) and the deferred-tool/",
		"≡ agent/mcp/skill roster the harness injects once at open. Rule files",
		"≡ (~/.claude/rules/**, .claude/rules/**) never appear as their own line in any",
		"≡ transcript — CC folds them into the system prompt — so they're read straight",
		"≡ off disk instead and folded into every session's floor + the share denominator.",
		"≡ share = floor / (transcript bytes + rule bytes); turn = everything else.")

	if len(rep.Sessions) == 0 {
		emptyNote(sink, 0, "sessions")
		return nil
	}

	ruleBytes := rep.RuleBytes()
	tot := floor.Rollup(rep.Sessions, ruleBytes)

	sink.Head("rules: global %d files/%s + project %d files/%s = %s fixed, paid at every session open",
		rep.RuleFilesGlobal, humanBytes(rep.RuleBytesGlobal), rep.RuleFilesProject, humanBytes(rep.RuleBytesProject), humanBytes(ruleBytes))
	sink.Head("floor %s vs. turn %s (of %s total) · corpus share=%.1f%% median=%.1f%% worst=%.1f%% (%s) · %d sessions",
		humanBytes(int(tot.FloorBytes)), humanBytes(int(tot.TurnBytes)), humanBytes(int(tot.TranscriptBytes+int64(ruleBytes)*int64(tot.Sessions))),
		tot.SharePct, tot.MedianSharePct, tot.WorstSharePct, shortSession(tot.WorstSession), tot.Sessions)
	if rep.DecodeErrs > 0 {
		sink.Head("%d transcripts skipped (unreadable)", rep.DecodeErrs)
	}

	sessions := append([]floor.Session(nil), rep.Sessions...)
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Share(ruleBytes) > sessions[j].Share(ruleBytes) })

	sink.Head("sessions ranked by floor share (worst first):")
	for _, s := range sessions {
		if !sink.Row("  %5.1f%%  floor=%-8s turn=%-8s total=%-8s  hook=%-7s itzy=%-6s roster=%-7s  %s",
			s.Share(ruleBytes)*100, humanBytes(s.FloorBytes(ruleBytes)), humanBytes(s.TurnBytes()), humanBytes(s.TranscriptBytes+ruleBytes),
			humanBytes(s.Cat.HookBytes), humanBytes(s.Cat.ItzyBytes), humanBytes(s.Cat.RosterBytes), shortSession(s.Session)) {
			break
		}
	}
	sink.NextHead("ferret usage", "ferret home")
	return nil
}

// applyFloorLimit resolves --limit for a command with no *common receiver
// (FloorCmd carries no CommonFlags — see its doc): unset (0) → the compact
// default, negative → unlimited, positive → exactly that many rows. Mirrors
// applyDefaultLimit's three-state contract (main.go) without requiring the
// shared *common type.
func applyFloorLimit(limit int) int {
	switch {
	case limit == 0:
		return floorDefaultLimit
	case limit < 0:
		return 0
	default:
		return limit
	}
}
