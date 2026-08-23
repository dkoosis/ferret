package main

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/dkoosis/ferret/internal/analyst"
)

// Command surfaces (ferret-wvo).
//
// ferret has 41 commands and top-level help used to interleave them with no
// distinction, so nothing told a reader which four send transcript-derived
// material to Anthropic. Every command therefore carries a surface label, and
// the label is printed in help:
//
//	local — reads the corpus or transcripts on this machine. Nothing leaves it.
//	model — sends transcript-derived material to the Anthropic API. Costs money.
//	hook  — designed to run from a Claude Code hook, not typed by hand.
//
// The map is exhaustive by test (surface_test.go): a new command with no entry
// fails the build's test step rather than defaulting to "local", because the
// dangerous default is the silent one.
type surface string

const (
	surfaceLocal surface = "local"
	surfaceModel surface = "model"
	surfaceHook  surface = "hook"
)

// cmdSurface classifies one command. escalates names the flag that turns an
// otherwise-local command into a model call — `retrieval` is local until
// --hop1, and labelling it a flat "model" would be as misleading as "local".
type cmdSurface struct {
	kind      surface
	escalates string
}

// label renders the help prefix.
func (s cmdSurface) label() string {
	if s.escalates != "" {
		return fmt.Sprintf("[%s, model with %s]", s.kind, s.escalates)
	}
	return "[" + string(s.kind) + "]"
}

// callsModel reports whether this command, as invoked, will reach the API.
// escalating commands consult their flag.
func (s cmdSurface) callsModel(escalated bool) bool {
	if s.kind == surfaceModel {
		return true
	}
	return s.escalates != "" && escalated
}

// commandSurfaces is the classification, keyed by the kong command path with
// argument placeholders stripped (see normalizeCommand).
var commandSurfaces = map[string]cmdSurface{
	// The four that call Anthropic.
	"adjudicate":      {kind: surfaceModel},
	"over-initiative": {kind: surfaceModel},
	"feedback judge":  {kind: surfaceModel},
	// retrieval is a deterministic scorer; --hop1 adds the LLM judge leg.
	"retrieval": {kind: surfaceLocal, escalates: "--hop1"},

	// Hook surfaces: invoked by a Claude Code hook, not typed by hand.
	"feedback prep":   {kind: surfaceHook},
	"feedback check":  {kind: surfaceHook},
	"feedback answer": {kind: surfaceHook},
	"emit":            {kind: surfaceHook},
	"recurrence":      {kind: surfaceHook},

	// Everything else reads local artifacts and transcripts.
	"ingest":          {kind: surfaceLocal},
	"summary":         {kind: surfaceLocal},
	"ngrams":          {kind: surfaceLocal},
	"seqs":            {kind: surfaceLocal},
	"rank":            {kind: surfaceLocal},
	"report":          {kind: surfaceLocal},
	"surprise":        {kind: surfaceLocal},
	"graph":           {kind: surfaceLocal},
	"tokens":          {kind: surfaceLocal},
	"spine":           {kind: surfaceLocal},
	"segments":        {kind: surfaceLocal},
	"dialogue":        {kind: surfaceLocal},
	"search":          {kind: surfaceLocal},
	"candidates":      {kind: surfaceLocal},
	"conformance":     {kind: surfaceLocal},
	"landmark":        {kind: surfaceLocal},
	"gates":           {kind: surfaceLocal},
	"status":          {kind: surfaceLocal},
	"friction":        {kind: surfaceLocal},
	"burn":            {kind: surfaceLocal},
	"misfires":        {kind: surfaceLocal},
	"polling":         {kind: surfaceLocal},
	"usage":           {kind: surfaceLocal},
	"substitutable":   {kind: surfaceLocal},
	"quality":         {kind: surfaceLocal},
	"helped":          {kind: surfaceLocal},
	"fixes add":       {kind: surfaceLocal},
	"fixes list":      {kind: surfaceLocal},
	"fixes sub":       {kind: surfaceLocal},
	"fixes subs":      {kind: surfaceLocal},
	"fixes proposals": {kind: surfaceLocal},
	"reach":           {kind: surfaceLocal},
}

// normalizeCommand strips kong's argument placeholders so "search <query>"
// looks up as "search". kong reports the selected command with positional
// arguments spelled out; the surface of a command does not depend on them.
func normalizeCommand(cmd string) string {
	fields := strings.Fields(cmd)
	kept := fields[:0]
	for _, f := range fields {
		if strings.HasPrefix(f, "<") || strings.HasPrefix(f, "[<") {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// labelCommandHelp prefixes every command's help text with its surface label,
// in the parsed grammar, before help is rendered. Doing it here rather than in
// 41 struct tags keeps the classification in one readable table and out of the
// grammar, where it would drift silently.
func labelCommandHelp(model *kong.Application) {
	var walk func(n *kong.Node, prefix string)
	walk = func(n *kong.Node, prefix string) {
		for _, c := range n.Children {
			path := strings.TrimSpace(prefix + " " + c.Name)
			if len(c.Children) > 0 {
				walk(c, path)
				continue
			}
			s, ok := commandSurfaces[path]
			if !ok {
				// Unclassified: say so rather than implying "local". The test
				// makes this unreachable in a green build.
				c.Help = "[unclassified] " + c.Help
				continue
			}
			c.Help = s.label() + " " + c.Help
		}
	}
	walk(model.Node, "")
}

// escalatedToModel reports whether the invoked command's model-escalating flag
// is set. Only `retrieval --hop1` escalates today; the switch keeps the check
// beside the map instead of scattered through dispatch.
func escalatedToModel(cmd string) bool {
	if cmd == "retrieval" {
		return CLI.Retrieval.Hop1
	}
	return false
}

// guardOffline refuses a model-backed command before it does any work.
//
// This is a USAGE error, not a run failure: the command was well-formed and the
// operator forbade the call, which is the same class as an unknown flag and
// carries the same exit code (DK-AXI rule 3 — "you asked for the wrong thing"
// must be distinguishable from "the run broke").
func guardOffline(cmd string) error {
	s, ok := commandSurfaces[cmd]
	if !ok {
		return nil
	}
	if !s.callsModel(escalatedToModel(cmd)) {
		return nil
	}
	if !CLI.Offline && !analyst.EnvOfflineSet() {
		return nil
	}
	return usage(fmt.Sprintf("`ferret %s` calls the Anthropic API — %s", cmd, analyst.ErrOffline))
}
