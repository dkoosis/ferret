package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/apiusage"
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
	"github.com/dkoosis/ferret/internal/snipeusage"
	"github.com/dkoosis/ferret/internal/transcript"
)

// buildRevision returns the VCS commit this binary was built from, so a corpus
// records which ferret measured it. `go build` stamps this into the build info
// automatically inside a git checkout; a build from a source archive has no
// revision to stamp, which reads as "unknown" rather than a fabricated value.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "unknown"
}

// ---- ingest ----

func cmdIngest() error {
	cmd := &CLI.Ingest
	data := cmd.Data
	if data == "~/.ferret" {
		d, err := defaultData()
		if err != nil {
			return err
		}
		data = d
	}
	root := cmd.Root
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	return ingest(data, root, cmd.Project, cmd.SnipeUsage, cmd.DryRun)
}

// eventSink is the persistence seam for ingest: the real implementation is
// *event.Writer, but tests inject a writer that fails after K records to prove
// a mid-ingest write error aborts the run and suppresses the manifest.
// Abort discards the in-progress temp file without sealing the artifact.
type eventSink interface {
	Write(ev *event.Event) error
	Close() error
	Abort()
}

// newEventWriter is indirected through a var so a test can substitute a failing
// writer without touching the event package.
var newEventWriter = func(path string) (eventSink, error) { return event.NewWriter(path) }

// errWriteAbort wraps the first per-record write error so ingest can abort the
// loop and refuse to seal a manifest over a partially-written artifact.
var errWriteAbort = errors.New("ingest aborted: record write failed")

func ingest(dataDir, root, project, snipeUsageGlob string, dryRun bool) error {
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	sources, err := transcript.Walk(root)
	if err != nil {
		return err
	}
	if project != "" {
		var keep []transcript.Source
		for _, s := range sources {
			if strings.Contains(s.Project, project) {
				keep = append(keep, s)
			}
		}
		sources = keep
	}

	b := event.NewBuilder()
	// Opt-in snipe usage.jsonl join (sn-r1do.2): strictly additive — no glob,
	// no join, behavior-identical to before this bead. UsageSources/
	// UsageRecords are set here; Index.Matched/Attempted (surfaced via
	// b.Stats.UsageJoined) populate as the ingest loop below runs resolve().
	if snipeUsageGlob != "" {
		matches, gerr := filepath.Glob(snipeUsageGlob)
		if gerr != nil {
			return fmt.Errorf("snipe-usage glob %s: %w", snipeUsageGlob, gerr)
		}
		records, uerr := snipeusage.ReadGlob(snipeUsageGlob)
		if uerr != nil {
			return uerr
		}
		b.SetUsage(snipeusage.NewIndex(records))
		b.Stats.UsageSources = len(matches)
		b.Stats.UsageRecords = len(records)
	}
	// Builder.File takes a non-fallible emit; capture the first write error in a
	// closure-scoped var instead. Once set, the outer loop stops and the run is
	// treated as partial — no manifest gets sealed over a truncated artifact.
	var emitErr error
	emit := func(*event.Event) {}
	// The API token ledger accumulates in memory and publishes once, after the
	// events artifact seals. Ordering matters: usage.jsonl must never be newer
	// than the events.jsonl it describes, or a crash between the two leaves a
	// ledger that looks current beside a corpus that is not.
	var ledger []apiusage.Row
	if !dryRun {
		b.SetLedger(func(r *apiusage.Row) { ledger = append(ledger, *r) })
	}
	var w eventSink
	if !dryRun {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		// Serialize concurrent ingests on the same data dir (ferret-0vz): without
		// this, two ferret processes both triggered by ensureData would write the
		// artifact at once.
		release, lerr := lockData(dataDir)
		if lerr != nil {
			return lerr
		}
		defer release()
		w, err = newEventWriter(filepath.Join(dataDir, "events.jsonl"))
		if err != nil {
			return err
		}
		emit = func(ev *event.Event) {
			if emitErr != nil {
				return // already failed; drain remaining emits cheaply
			}
			if werr := w.Write(ev); werr != nil {
				emitErr = fmt.Errorf("%w: %w", errWriteAbort, werr)
			}
		}
	}
	start := time.Now()
	for _, src := range sources {
		if err := b.File(src, emit); err != nil {
			fmt.Fprintf(os.Stderr, "ferret: %s: %v (skipped)\n", src.Path, err)
		}
		if emitErr != nil {
			break
		}
	}
	if w != nil {
		if emitErr != nil {
			// Mid-write failure: ABORT (close-without-rename) rather than Close.
			// Close would flush the bytes written so far, fsync, and rename the
			// TRUNCATED tmp onto events.jsonl — publishing a partial corpus whose
			// only safety net is the suppressed manifest (ferret-0m7). Abort drops
			// the tmp so no partial artifact ever lands.
			w.Abort()
			return emitErr
		}
		// Invalidate the previous generation BEFORE publishing any replacement.
		// The manifest is the completeness sentinel for the whole data dir, not
		// for events.jsonl alone: without this, a failure after events.jsonl is
		// renamed but before usage.jsonl and the manifest are rewritten leaves a
		// new events file beside an old ledger under a manifest that still reads
		// complete — and every later command silently mixes two ingest runs.
		// Removing it first means any mid-publish failure reads as "no corpus",
		// which ensureData repairs by re-ingesting.
		manifestPath := filepath.Join(dataDir, "manifest.json")
		if rerr := os.Remove(manifestPath); rerr != nil && !os.IsNotExist(rerr) {
			w.Abort()
			return rerr
		}
		if cerr := w.Close(); cerr != nil {
			// Close failed: the atomic Writer never sealed events.jsonl, so no
			// later mine runs on silently-truncated data.
			return cerr
		}
		m := &event.Manifest{
			SchemaVersion: event.SchemaVersion,
			CreatedAt:     time.Now(),
			Root:          root,
			Provenance: event.Provenance{
				Ferret:     buildRevision(),
				Normalizer: shellnorm.Version,
				Flags:      event.Flags{Project: project, SnipeUsage: snipeUsageGlob},
			},
			Stats: b.Stats,
		}
		if err := apiusage.Write(filepath.Join(dataDir, apiusage.Artifact), ledger); err != nil {
			return err
		}
		if err := event.WriteManifest(manifestPath, m); err != nil {
			return err
		}
	}

	st := b.Stats
	fmt.Printf("ingest files=%d lines=%d events=%d prompts=%d in %s\n",
		st.Files, st.Lines, st.Events, st.Prompts, time.Since(start).Round(time.Millisecond))
	fmt.Printf("health unpaired=%.1f%% shell-fallback=%d deduped=%d decode-errs=%d\n",
		pct(st.Unpaired, st.Events), st.Fallback, st.Deduped, st.DecodeErrs)
	if st.APICalls > 0 {
		fmt.Printf("api-ledger calls=%d duplicate-lines-collapsed=%d\n", st.APICalls, st.APIDupes)
	}
	if st.UsageSources > 0 {
		fmt.Printf("usage sources=%d records=%d joined=%d\n", st.UsageSources, st.UsageRecords, st.UsageJoined)
	}
	types := make([]string, 0, len(st.ByType))
	for t := range st.ByType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return st.ByType[types[i]] > st.ByType[types[j]] })
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%s:%d", t, st.ByType[t]))
	}
	fmt.Println("types", strings.Join(parts, " "))
	return nil
}
