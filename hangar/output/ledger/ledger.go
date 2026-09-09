// Package ledger reads the output daemon's source ledger, and only reads it.
//
// The existing artifact daemon has no output-bucket credential and no business
// changing a capture's state, but its sweep, cleanup, delete, replacement,
// remap and reuse paths all destroy things -- and any of them can be pointed at
// a source some capture is about to seal. So it needs one question answered
// before it destroys anything: is this path held?
//
// This package answers it, from the same durable records the output daemon
// writes, WITHOUT a way to change them. There is no writer here, no exported
// mutator and no field an existing caller could use to become one; the
// architecture guard in hangar/output asserts that stays true. A read-only
// classifier is what lets one authority own the state and another respect it.
//
// It FAILS CLOSED, and that is the whole design. Every answer that is not "this
// path is unmanaged" refuses the destructive operation:
//
//	Unmanaged   nothing here holds it; the ordinary path is unchanged
//	Held        a capture holds this source and it must survive
//	Sealed      a capture is reading these exact bytes right now
//	Unavailable the ledger could not be read
//
// Unavailable is deliberately not "probably fine". A daemon that could not read
// the ledger does not know whether it is about to delete a capture's source,
// and deleting on a guess is how a build's declared output disappears with no
// record that it ever existed.
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ControlDirName must match the output daemon's. It is stated here rather than
// imported because this package is the READER: a reader that depended on the
// writer's package would be one import away from being able to write.
const ControlDirName = ".hangar-output-control"

const (
	sourceRecordPrefix = "source-"
	recordVersion      = "hangar-output-control-record-v1"
)

// Class is the closed set of answers.
type Class string

const (
	Unmanaged   Class = "unmanaged"
	Held        Class = "held"
	Sealed      Class = "sealed"
	Unavailable Class = "unavailable"
)

// Destructive reports whether a destructive operation may proceed. Only one
// answer says yes.
func (class Class) Destructive() bool { return class == Unmanaged }

// ErrRefused is what a caller returns when this classifier says no.
var ErrRefused = errors.New("hangar/output/ledger: the source is held by a durable output capture")

// record is the subset of the daemon's source record this reader needs.
//
// It is deliberately a SUBSET and decoded leniently: the writer may add fields,
// and a reader that refused an unknown one would turn every daemon upgrade into
// an outage of the ordinary path. What it must never do is silently read a
// record whose FORMAT it does not know, which is why the version is checked.
type record struct {
	State       string `json:"state"`
	Incarnation struct {
		ExecutionID      string `json:"execution_id"`
		HandleGeneration uint64 `json:"handle_generation"`
		Output           string `json:"output"`
	} `json:"incarnation"`
}

type envelope struct {
	RecordVersion string          `json:"record_version"`
	Body          json.RawMessage `json:"body"`
}

// Classifier answers one question about one path.
type Classifier struct {
	dir string

	// The answers are cached, because the destructive paths ask on every
	// operation and a sweep asks thousands of times in a row.
	//
	// The cache is keyed on the control directory's OWN modification time, not
	// on a timer. A timer would mean a hold established in the last interval is
	// invisible, and "the delete arrived a moment after the hold" is exactly
	// the race this classifier exists for. Creating, replacing or removing a
	// record changes the directory's mtime, so a new hold invalidates the cache
	// the instant it lands. The short TTL underneath it only bounds how often
	// the directory itself is stat-ed.
	mu       sync.Mutex
	held     map[string]Class
	loadedAt time.Time
	loadedAs time.Time
	loadErr  error
	ttl      time.Duration
	clock    func() time.Time
}

// New opens a classifier over the managed storage root.
//
// It does not fail when the control directory is absent: a deployment with no
// output plane has no captures, every path is unmanaged, and the ordinary path
// is unchanged. What it must not do is treat an UNREADABLE directory the same
// way, and Classify is where that distinction lives.
func New(storageRoot string) *Classifier {
	return &Classifier{
		dir:   path.Join(storageRoot, ControlDirName),
		ttl:   time.Second,
		clock: func() time.Time { return time.Now().UTC() },
	}
}

// Classify answers for one path relative to the managed steps directory --
// "<execution>.<generation>/<output>", or anything beneath it.
//
// A path beneath a held incarnation is held. Deleting one file out of a source
// a capture is about to seal is not less destructive than deleting the
// directory; it is the same damage, harder to notice.
func (classifier *Classifier) Classify(stepsRelative string) Class {
	classifier.mu.Lock()
	defer classifier.mu.Unlock()

	if err := classifier.refresh(); err != nil {
		return Unavailable
	}

	cleaned := strings.Trim(path.Clean("/"+stepsRelative), "/")
	for held, class := range classifier.held {
		if cleaned == held || strings.HasPrefix(cleaned, held+"/") {
			return class
		}
	}

	return Unmanaged
}

// Reason explains a refusal, for the log line and the response body. A refusal
// nobody can read is a stuck operation nobody can diagnose.
func (classifier *Classifier) Reason(stepsRelative string, class Class) error {
	switch class {
	case Held:
		return fmt.Errorf("%w: %s is a source a durable output capture holds; it must survive "+
			"until the capture releases it", ErrRefused, stepsRelative)
	case Sealed:
		return fmt.Errorf("%w: %s is sealed and its exact bytes are being read now", ErrRefused,
			stepsRelative)
	case Unavailable:
		return fmt.Errorf("%w: the output source ledger under %s could not be read, so whether "+
			"%s is held is unknown. Destroying it on that basis is how a build's declared output "+
			"disappears with no record it existed: %v", ErrRefused, classifier.dir, stepsRelative,
			classifier.loadErr)
	}

	return nil
}

func (classifier *Classifier) refresh() error {
	now := classifier.clock()
	stamp, stamped := classifier.stamp()
	fresh := classifier.held != nil &&
		now.Sub(classifier.loadedAt) < classifier.ttl &&
		stamped && stamp.Equal(classifier.loadedAs)
	if fresh {
		return classifier.loadErr
	}
	classifier.loadedAt, classifier.loadedAs = now, stamp
	classifier.held, classifier.loadErr = classifier.load()

	return classifier.loadErr
}

// stamp is the control directory's modification time, or nothing when there is
// no directory to stat. "Nothing" is never treated as fresh, so a node that
// gains an output plane is picked up on the next call rather than at the next
// restart.
func (classifier *Classifier) stamp() (time.Time, bool) {
	info, err := os.Stat(classifier.dir)
	if err != nil {
		return time.Time{}, false
	}

	return info.ModTime(), true
}

func (classifier *Classifier) load() (map[string]Class, error) {
	entries, err := os.ReadDir(classifier.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No output plane on this node. Every path is unmanaged, and the
			// ordinary path is unchanged.
			return map[string]Class{}, nil
		}

		return nil, err
	}

	held := map[string]Class{}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), sourceRecordPrefix) ||
			!strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(path.Join(classifier.dir, name))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		var wrapper envelope
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", name, err)
		}
		if wrapper.RecordVersion != recordVersion {
			return nil, fmt.Errorf("%s is record version %q and this reader knows %q; a record "+
				"it cannot read is a hold it would miss", name, wrapper.RecordVersion, recordVersion)
		}
		var body record
		if err := json.Unmarshal(wrapper.Body, &body); err != nil {
			return nil, fmt.Errorf("decoding the body of %s: %w", name, err)
		}

		class := classFor(body.State)
		if class == Unmanaged {
			continue
		}
		held[fmt.Sprintf("%s.%d/%s",
			body.Incarnation.ExecutionID, body.Incarnation.HandleGeneration,
			body.Incarnation.Output)] = class
	}

	return held, nil
}

// classFor maps the writer's state vocabulary onto this reader's answers.
//
// An unknown state is HELD, not unmanaged. The writer may learn a state this
// reader does not, and the safe reading of "I do not know what this means" over
// a record that exists at all is that something holds it.
func classFor(state string) Class {
	switch state {
	case "released":
		return Unmanaged
	case "sealing", "sealed":
		return Sealed
	case "":
		return Unmanaged
	default:
		return Held
	}
}
