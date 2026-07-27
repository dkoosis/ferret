package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
)

// cmdFixesSub records a confirmed tool-for-intent mismatch (a `ferret adjudicate`
// fit=mismatch finding dk has validated) as a substitution rule, deduping by
// (intent, better): a repeat bumps the rule's occurrence count instead of adding
// a row (ferret-kuv.15).
func cmdFixesSub() error {
	cmd := &CLI.Fixes.Sub
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	sub := fixes.Substitution{
		IntentClass: strings.TrimSpace(cmd.Intent),
		WrongTool:   strings.TrimSpace(cmd.Wrong),
		Better:      strings.TrimSpace(cmd.Better),
		Example:     cmd.Example,
		Session:     cmd.Session,
	}
	rule, created, err := fixes.RecordSub(fixes.SubPath(data), sub, time.Now())
	if err != nil {
		return err
	}
	verb := "bumped"
	if created {
		verb = "recorded"
	}
	fmt.Printf("%s substitution: %s → %s (×%d)\n", verb, rule.IntentClass, rule.Better, rule.Occurrences)
	return nil
}
