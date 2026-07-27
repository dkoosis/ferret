package main

import (
	"fmt"
	"os"

	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/out"
)

// cmdFixesList prints the recorded fixes, newest concerns last (append order).
func cmdFixesList() error {
	cmd := &CLI.Fixes.List
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	if cmd.Format != "text" && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	entries, err := fixes.Load(fixes.Path(data))
	if err != nil {
		return err
	}
	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			"fixes": entries, keyTotal: len(entries),
		})
	}
	sink := out.NewSink(os.Stdout, 0, 0)
	defer sink.Close()
	sink.Head("fixes recorded=%d (ledger %s)", len(entries), fixes.Path(data))
	for _, e := range entries {
		note := ""
		if e.Note != "" {
			note = "  — " + e.Note
		}
		sink.Row("%s  %-8s burn@fix=%-8d  %s → %s%s",
			e.AddedAt.Format("2006-01-02"), e.Disp(), e.BaselineBurn, e.Motif, e.Fix, note)
	}
	return nil
}
