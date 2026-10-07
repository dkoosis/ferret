package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dkoosis/ferret/internal/friction"
	"github.com/dkoosis/ferret/internal/out"
)

// seedResult is what one `signatures seed` run did: nugs read, signatures
// derived from them, how many were new to the file, and the nugs it could not
// fingerprint (no tool: clause), named so dk can fix the nug rather than lose
// the trap silently.
type seedResult struct {
	Nugs       int      `json:"nugs"`
	Signatures int      `json:"signatures"`
	Added      int      `json:"added"`
	Skipped    []string `json:"skipped,omitempty"`
	File       string   `json:"file"`
}

// cmdSignaturesSeed fills the known-signatures file from the friction nugs in a
// mnemd nugbase, so a trap a past session recorded is a KNOWN signature to
// `ferret recurrence` before the corpus it scans ever repeats it. PR 68
// (ferret-5jb) built the matcher and deferred this populator; it is the step
// that turns "recorded once" into "flagged on the next sighting".
func cmdSignaturesSeed() error {
	c, err := fromCommonFlags(CommonFlags{Data: CLI.Signatures.Seed.Data, Format: CLI.Signatures.Seed.Format})
	if err != nil {
		return err
	}
	if err := c.validate(fmtText, fmtJSON); err != nil {
		return err
	}
	if err := os.MkdirAll(c.data, 0o755); err != nil {
		return err
	}
	sigPath := CLI.Signatures.Seed.Signatures
	if sigPath == "" {
		sigPath = friction.SigPath(c.data)
	}
	res, err := runSignaturesSeed(CLI.Signatures.Seed.Nugs, sigPath)
	if err != nil {
		return err
	}
	if c.format == fmtJSON {
		return out.JSON(os.Stdout, res)
	}
	fmt.Printf("friction-signatures seeded: nugs=%d signatures=%d added=%d (file %s)\n",
		res.Nugs, res.Signatures, res.Added, res.File)
	for _, id := range res.Skipped {
		fmt.Printf("skipped nug %s: no tool: clause to fingerprint\n", id)
	}
	return nil
}

// runSignaturesSeed reads every friction nug under nugsDir, derives one
// signature per nug, and merges them into the signatures file at sigPath via
// friction.PersistLearned (deduped by fingerprint, atomic rewrite, existing
// labels kept). A nug with no tool: clause is reported in Skipped, not fatal:
// one malformed nug must not block the rest from seeding.
func runSignaturesSeed(nugsDir, sigPath string) (seedResult, error) {
	nugs, err := friction.ReadFrictionNugs(nugsDir)
	if err != nil {
		return seedResult{}, err
	}
	res := seedResult{Nugs: len(nugs), File: sigPath}
	sigs := make([]friction.Signature, 0, len(nugs))
	for i := range nugs {
		sig, err := friction.SignatureFromNug(nugs[i])
		if errors.Is(err, friction.ErrNoTool) {
			res.Skipped = append(res.Skipped, nugs[i].ID)
			continue
		}
		if err != nil {
			return seedResult{}, err
		}
		sigs = append(sigs, sig)
	}
	res.Signatures = len(sigs)
	if len(sigs) == 0 {
		return res, nil
	}
	added, err := friction.PersistLearned(sigPath, sigs)
	if err != nil {
		return seedResult{}, err
	}
	res.Added = added
	return res, nil
}
