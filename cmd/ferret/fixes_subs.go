package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/out"
)

// cmdFixesSubs lists the recorded substitution rules, most-cited first.
func cmdFixesSubs() error {
	cmd := &CLI.Fixes.Subs
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	if cmd.Format != "text" && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	subs, err := fixes.LoadSubs(fixes.SubPath(data))
	if err != nil {
		return err
	}
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].Occurrences > subs[j].Occurrences })
	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			"substitutions": subs, keyTotal: len(subs),
		})
	}
	sink := out.NewSink(os.Stdout, 0, 0)
	defer sink.Close()
	sink.Head("substitutions recorded=%d (ledger %s)", len(subs), fixes.SubPath(data))
	for i := range subs {
		s := &subs[i]
		sink.Row("×%-4d %-18s %s → %s", s.Occurrences, s.IntentClass, s.WrongTool, s.Better)
	}
	return nil
}
