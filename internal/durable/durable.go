// Package durable centralizes ferret's crash-durability primitives — the
// fsync/rename/flock discipline that was copy-pasted across every package with a
// persistent file (event codec, fixes + label ledgers, feedback budget/bank,
// the ingest cursor, friction signatures). One tested home for the write
// contract means a durability fix lands once, not five times, and the packages
// keep only their file-specific rationale.
package durable

import (
	"errors"
	"os"
	"path/filepath"
)

// SyncDir fsyncs a directory so a just-published rename or first-create within
// it survives a crash/power-loss. os.Rename (and a new file's directory entry)
// only mutates the directory; the dir inode itself must be fsync'd or the change
// can be lost in the dirty-inode window.
//
// Packages assign this to a `var syncDir` seam so a test can observe or fail the
// publish path with a plain override; keeping it a plain func here (not a var)
// lets those seams point at one body.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		return errors.Join(err, d.Close())
	}
	return d.Close()
}

// WriteTempRename publishes data to path via a per-process unique temp file
// (random infix, so two writers racing on the same path never share a temp),
// fsync'd durable, then atomically renamed onto path. A reader therefore sees
// either the old file or the fully-written new one, never a torn write; a failed
// write drops its temp and leaves any prior path intact. The parent dir is NOT
// fsync'd here — the caller does that through its own SyncDir seam, so a test can
// still observe the dir-fsync leg of the publish. Callers that need the rename
// itself to be crash-durable must follow a successful WriteTempRename with
// SyncDir(filepath.Dir(path)).
func WriteTempRename(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		return errors.Join(werr, tmp.Close(), os.Remove(tmpName))
	}
	if serr := tmp.Sync(); serr != nil {
		return errors.Join(serr, tmp.Close(), os.Remove(tmpName))
	}
	if cerr := tmp.Close(); cerr != nil {
		return errors.Join(cerr, os.Remove(tmpName))
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		return errors.Join(rerr, os.Remove(tmpName))
	}
	return nil
}
