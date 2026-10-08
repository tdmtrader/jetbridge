package outputplane

// The output plane's private control directory.
//
// Both ledgers -- the base execution ledger and the capture source ledger --
// keep their records here, one JSON file per record. It is a directory inside
// the shared managed hostPath, and the whole point of the file below is that it
// is *not* an ordinary part of that hostPath: Registry does not index it, alias
// persistence does not walk it, steps traversal does not descend into it, and
// the Sweeper does not reclaim it. A ledger the Sweeper could delete is a
// ledger that fails open.
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

type controlStore struct {
	root *os.Root
	path string
}

// openControlStore opens (creating if needed) the private control directory.
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

// names lists the record names in the control directory, in sorted order.
func (store *controlStore) names() ([]string, error) {
	entries, err := fs.ReadDir(store.root.FS(), ".")
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

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encoding the control record %q: %v", output.ErrCorrupt, name, err)
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
	if err := file.Sync(); err != nil {
		_ = file.Close()

		return fmt.Errorf("%w: fsyncing the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: closing the control record %q: %v", output.ErrInfrastructure, temp, err)
	}
	if err := store.root.Rename(temp, name); err != nil {
		return fmt.Errorf("%w: renaming the control record over %q: %v",
			output.ErrInfrastructure, name, err)
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
// not an error, and every other problem -- including a record that does not
// decode -- is, so the caller refuses rather than reading it as absent.
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

	if err := json.Unmarshal(raw, into); err != nil {
		return false, fmt.Errorf("%w: the control record %q does not decode: %v",
			output.ErrCorrupt, name, err)
	}

	return true, nil
}

// remove deletes a record durably. A record already gone is not an error.
func (store *controlStore) remove(name string) error {
	if err := validRecordName(name); err != nil {
		return err
	}
	if err := store.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: removing the control record %q: %v", output.ErrInfrastructure, name, err)
	}

	return store.syncDir()
}

// validRecordName refuses anything that is not a single flat file name.
//
// os.Root would refuse a climb anyway; this refuses it with a message that says
// what was wrong, and it refuses a nested name too, so the directory stays a
// flat set of records that names() can enumerate.
func validRecordName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".json") {
		return fmt.Errorf("%w: %q is not a control record name", output.ErrUnauthorized, name)
	}

	return nil
}
