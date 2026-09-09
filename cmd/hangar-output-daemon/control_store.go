package main

// The output daemon's private control directory.
//
// Both ledgers -- the base execution ledger and the capture source ledger --
// keep versioned records here. It is a directory inside the shared managed
// hostPath, and the whole point of the file below is that it is *not* an
// ordinary part of that hostPath: Registry does not index it, alias persistence
// does not walk it, steps traversal does not descend into it, and the Sweeper
// does not reclaim it. A ledger the Sweeper could delete is a ledger that fails
// open.
//
// Every operation is descriptor-relative through an os.Root handle rather than
// a path join, so a symlink swapped under the control directory cannot redirect
// a record write, and a record name that climbs is refused by the runtime
// rather than by a string check.
//
// Records are replaced atomically: write a temp file, fsync it, rename it over
// the old name, fsync the directory. A reader that observes the rename observes
// the whole record or the whole previous one, and a crash at any of the four
// points leaves one of those two. That is what makes "the acknowledgement is
// durable before the caller sees it" a fact rather than an intention.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar/output"
)

// ControlDirName is the private directory inside the managed hostPath. The
// leading dot is not the protection -- the exclusions in the artifact daemon
// are -- but it keeps it out of an operator's `ls` by default.
const ControlDirName = ".hangar-output-control"

// controlRecordVersion is the on-disk format. A record written by a newer
// daemon is refused, not absorbed: a field this binary cannot read is a fact it
// would silently drop, and a dropped gate is a destructive operation admitted.
const controlRecordVersion = "hangar-output-control-record-v1"

// quarantineDirName holds records that did not survive validation at startup.
//
// They are moved rather than deleted, and their presence keeps the daemon
// unready on every subsequent start -- otherwise a corrupt ledger would be
// cleared by the act of restarting, which is the failure mode the quarantine
// exists to make loud.
const quarantineDirName = "quarantine"

// controlRecord is the envelope every record is wrapped in.
//
// The checksum covers the body bytes exactly as they were written. It is what
// catches a torn record: a crash during a non-atomic write elsewhere in the
// system, a truncated file restored from a backup, a bit flipped on a disk.
type controlRecord struct {
	RecordVersion string          `json:"record_version"`
	Checksum      string          `json:"checksum"`
	Body          json.RawMessage `json:"body"`
}

// faultStage names a point in the durable write, so a test can crash there.
//
// Fault injection is deterministic and explicit rather than timing-based: the
// crash halves this store has to survive are "after the temp file is written
// and before it is fsynced", "after the fsync and before the rename" and "after
// the rename and before the directory fsync", and no amount of racing a real
// process reproduces those three on demand.
type faultStage string

const (
	faultAfterTempWrite  faultStage = "after-temp-write"
	faultAfterTempFsync  faultStage = "after-temp-fsync"
	faultAfterRename     faultStage = "after-rename"
	faultBeforeTempWrite faultStage = "before-temp-write"
)

// errInjectedCrash is what a fault hook returns. It is a distinct value so a
// test can tell an injected crash from a real failure.
var errInjectedCrash = errors.New("hangar-output-daemon: injected crash")

type controlStore struct {
	root *os.Root
	path string

	// fault is nil in production. Build refuses a configuration that set it,
	// and the architecture test asserts no production file assigns it.
	fault func(faultStage) error
}

// openControlStore opens (creating if needed) the private control directory and
// validates everything already in it.
//
// Validation at open is deliberate. A daemon that discovered a corrupt record
// on the request that needed it would answer that one request wrong; a daemon
// that refuses to become ready answers none.
func openControlStore(parent string) (*controlStore, error) {
	dir := path.Join(parent, ControlDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: creating the output control directory: %v",
			output.ErrInfrastructure, err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: opening the output control directory: %v",
			output.ErrInfrastructure, err)
	}

	return &controlStore{root: root, path: dir}, nil
}

func (store *controlStore) Close() error { return store.root.Close() }

// Path is the directory, for the exclusions that have to name it.
func (store *controlStore) Path() string { return store.path }

// validate walks every record and quarantines the ones that cannot be read.
//
// It returns the quarantined names. A caller that gets a non-empty list must
// refuse readiness: this daemon is the sole authority for the executions those
// records described, and a lost record is not the same as an execution that
// never happened.
func (store *controlStore) validate() ([]string, error) {
	quarantined, err := store.listIn(quarantineDirName)
	if err != nil {
		return nil, err
	}

	names, err := store.names()
	if err != nil {
		return nil, err
	}

	for _, name := range names {
		var body json.RawMessage
		if _, err := store.get(name, &body); err != nil {
			if errors.Is(err, output.ErrInfrastructure) {
				return nil, err
			}
			if err := store.quarantineRecord(name); err != nil {
				return nil, err
			}
			quarantined = append(quarantined, name)
		}
	}
	sort.Strings(quarantined)

	return quarantined, nil
}

func (store *controlStore) quarantineRecord(name string) error {
	if err := store.root.MkdirAll(quarantineDirName, 0o700); err != nil {
		return fmt.Errorf("%w: creating the control quarantine: %v", output.ErrInfrastructure, err)
	}
	if err := store.root.Rename(name, path.Join(quarantineDirName, name)); err != nil {
		return fmt.Errorf("%w: quarantining the unreadable control record %q: %v",
			output.ErrInfrastructure, name, err)
	}

	return nil
}

// names lists the record names in the control directory, in sorted order.
func (store *controlStore) names() ([]string, error) { return store.listIn(".") }

func (store *controlStore) listIn(dir string) ([]string, error) {
	entries, err := fs.ReadDir(store.root.FS(), dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("%w: listing the output control directory: %v",
			output.ErrInfrastructure, err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	return names, nil
}

// put replaces a record durably.
func (store *controlStore) put(name string, body any) error {
	if err := validRecordName(name); err != nil {
		return err
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encoding the control record %q: %v", output.ErrCorrupt, name, err)
	}
	sum := sha256.Sum256(raw)
	encoded, err := json.Marshal(controlRecord{
		RecordVersion: controlRecordVersion,
		Checksum:      hex.EncodeToString(sum[:]),
		Body:          raw,
	})
	if err != nil {
		return fmt.Errorf("%w: encoding the control record %q: %v", output.ErrCorrupt, name, err)
	}

	if err := store.crash(faultBeforeTempWrite); err != nil {
		return err
	}

	temp := name + ".tmp"
	file, err := store.root.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: opening the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()

		return fmt.Errorf("%w: writing the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if err := store.crash(faultAfterTempWrite); err != nil {
		_ = file.Close()

		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()

		return fmt.Errorf("%w: fsyncing the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: closing the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if err := store.crash(faultAfterTempFsync); err != nil {
		return err
	}
	if err := store.root.Rename(temp, name); err != nil {
		return fmt.Errorf("%w: renaming the control record over %q: %v",
			output.ErrInfrastructure, name, err)
	}
	if err := store.crash(faultAfterRename); err != nil {
		return err
	}

	return store.syncDir()
}

// syncDir makes the rename itself durable. Without it the record's bytes
// survive a power loss and the name that reaches them may not.
func (store *controlStore) syncDir() error {
	dir, err := store.root.Open(".")
	if err != nil {
		return fmt.Errorf("%w: opening the control directory to fsync it: %v",
			output.ErrInfrastructure, err)
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("%w: fsyncing the control directory: %v", output.ErrInfrastructure, err)
	}

	return nil
}

// get reads a record. It reports whether the record exists; a missing record is
// not an error, and every other problem is.
func (store *controlStore) get(name string, into any) (bool, error) {
	if err := validRecordName(name); err != nil {
		return false, err
	}

	raw, err := store.root.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("%w: reading the control record %q: %v",
			output.ErrInfrastructure, name, err)
	}

	var record controlRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, fmt.Errorf("%w: the control record %q is not readable: %v",
			output.ErrCorrupt, name, err)
	}
	if record.RecordVersion != controlRecordVersion {
		return false, fmt.Errorf("%w: the control record %q is version %q and this daemon reads "+
			"%q; a record it cannot read is a fact it must not drop",
			output.ErrUnsupportedProtocol, name, record.RecordVersion, controlRecordVersion)
	}
	sum := sha256.Sum256(record.Body)
	if hex.EncodeToString(sum[:]) != record.Checksum {
		return false, fmt.Errorf("%w: the control record %q does not match its checksum",
			output.ErrCorrupt, name)
	}
	if err := json.Unmarshal(record.Body, into); err != nil {
		return false, fmt.Errorf("%w: the control record %q does not decode: %v",
			output.ErrCorrupt, name, err)
	}

	return true, nil
}

func (store *controlStore) crash(stage faultStage) error {
	if store.fault == nil {
		return nil
	}

	return store.fault(stage)
}

// validRecordName refuses anything that is not a single flat file name.
//
// os.Root would refuse a climb anyway; this refuses it with a message that says
// what was wrong, and it refuses a nested name too, so the directory stays a
// flat set of records that validate() can enumerate.
func validRecordName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".json") {
		return fmt.Errorf("%w: %q is not a control record name", output.ErrUnauthorized, name)
	}

	return nil
}
