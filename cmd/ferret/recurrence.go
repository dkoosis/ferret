package main

import (
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/friction"
	"github.com/dkoosis/ferret/internal/out"
)

// cmdRecurrence wires the CLI to the friction-recurrence detector over the
// canonical events artifact. It loads the known-signature file (optional — an
// absent file makes the detector learn signatures from the corpus itself), runs
// the detector, and emits one match record per 2nd+ occurrence of a known
// friction signature. The match records are the deliverable a downstream /wrap
// trap-graduation prompt consumes to escalate a recorded trap into a hook.
func cmdRecurrence() error {
	c, err := fromCommonFlags(CLI.Recurrence.CommonFlags)
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	sigPath := CLI.Recurrence.Signatures
	if sigPath == "" {
		sigPath = friction.SigPath(c.data)
	}
	sigs, err := friction.LoadSignatures(sigPath)
	if err != nil {
		return err
	}
	events, err := loadEvents(c.eventsPath())
	if err != nil {
		return err
	}
	det := friction.NewDetector(sigs)
	matches := det.ScanInto(events)

	// Persist signatures learned this run so a fingerprint first seen now is a
	// KNOWN signature next run — this is what makes cross-run recurrence
	// (occurrence 2 in a later process) reachable rather than lost at exit.
	learned, err := friction.PersistLearned(sigPath, det.Signatures())
	if err != nil {
		return err
	}

	if c.format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			"matches":    matches,
			"signatures": len(sigs),
			"learned":    learned,
			keyTotal:     len(matches),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	sink.Head("friction-recurrence matches=%d (known signatures=%d, learned=%d, file %s)", len(matches), len(sigs), learned, sigPath)
	for i := range matches {
		m := &matches[i]
		label := m.Label
		if label == "" {
			label = "(unlabeled)"
		}
		if !sink.Row("occ#%d  %s#%d  %s  [%s]  %s",
			m.Occurrence, m.Session, m.Seq, label, m.Fingerprint, truncRaw(m.Raw)) {
			break // row cap reached; stop formatting further rows
		}
	}
	return nil
}

// truncRaw caps the exemplar so one match row stays a single line under the AX
// output caps; the full raw is available in --format json. Newlines collapse to
// spaces (single-line invariant) and truncation is by rune, never mid-codepoint,
// so a multibyte exemplar never emits a malformed UTF-8 tail.
func truncRaw(s string) string {
	const limit = 80
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
