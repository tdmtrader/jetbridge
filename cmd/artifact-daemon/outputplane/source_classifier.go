package outputplane

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

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
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
// Keep the diagnostic prefix stable for existing refusal responses.
var ErrRefused = errors.New("hangar/output/ledger: the source is held by a durable output capture")

// marker is the subset of the daemon's step marker this reader needs.
//
// It is deliberately a SUBSET and decoded leniently: the writer may add fields.
// A record that does not decode at all, or names no step directory, makes the
// whole ledger unavailable; a record that decodes with a state this reader
// does not know is held.
type marker struct {
	State     string `json:"state"`
	Execution string `json:"execution"`
	Output    string `json:"output"`
}

// Classifier answers one question about one path.
type Classifier struct {
	dir string

	// mu serializes the read so two destructive paths cannot both be halfway
	// through the directory while a record is renamed under them. loadErr is
	// kept only so Reason can say what went wrong.
	mu      sync.Mutex
	loadErr error

	// THERE IS NO CACHE, and the reason is a CI failure rather than a
	// preference.
	//
	// The first version cached on the control directory's modification time,
	// which is sound in principle -- the writer replaces records by rename, and
	// a rename bumps the directory's mtime. It is not sound in practice: on
	// Linux the mtime granularity is coarse enough that a hold written in the
	// same tick as the previous read leaves the directory looking unchanged, so
	// the classifier answered "unmanaged" for a hold that already existed and a
	// delete went through. That is the exact race this component exists for,
	// and it passed on a Mac and failed in CI.
	//
	// So every call reads the directory. The cost is one ReadDir plus one
	// ReadFile per LIVE CAPTURE on the node -- the ledger holds a record per
	// capture, not per artifact -- and a wrong answer here deletes a build's
	// declared output. If a sweep ever needs a snapshot it should take one
	// explicitly, for a bounded scope it can name, rather than every caller
	// silently sharing a stale one.
}

// NewClassifier creates a read-only classifier over the managed storage root.
// It requires no output-plane configuration and never creates directories.
//
// It does not fail when the control directory is absent: a deployment with no
// output plane has no captures, every path is unmanaged, and the ordinary path
// is unchanged. What it must not do is treat an UNREADABLE directory the same
// way, and Classify is where that distinction lives.
func NewClassifier(storageRoot string) *Classifier {
	return &Classifier{dir: path.Join(storageRoot, ControlDirName)}
}

// Classify answers for one path relative to the managed steps directory --
// "<execution>.capture/<output>", or anything beneath it, or anything it is
// beneath.
//
// It answers in BOTH directions, and the second one is the one a first reading
// misses.
//
// Downward is obvious: a path beneath a held incarnation is held, because
// deleting one file out of a source a capture is about to seal is not less
// destructive than deleting the directory -- it is the same damage, harder to
// notice.
//
// Upward is the direction the callers actually use. A hold names
// "<execution>.capture/<output>"; the Reaper deletes
// "steps/<handle>" and the sweeper removes an expired "steps/<handle>"
// directory, both of which are the PARENT of that. A classifier that answered
// only downward told them the directory containing a held source was
// unmanaged, and the source went with it -- on a timer, with nobody watching.
//
// The comparison is by path SEGMENT in both directions. A string prefix would
// make "exec-1.3" hold "exec-1.30", which is a different handle generation and
// a different artifact; the registry has had exactly that bug (see
// Registry.RemoveByPath) and it presented as a permanent cache miss.
func (classifier *Classifier) Classify(stepsRelative string) Class {
	classifier.mu.Lock()
	defer classifier.mu.Unlock()

	held, err := classifier.load()
	classifier.loadErr = err
	if err != nil {
		return Unavailable
	}

	cleaned := strings.Trim(path.Clean("/"+stepsRelative), "/")
	if cleaned == "" {
		// The steps root itself. If anything at all is held, destroying the
		// root destroys it.
		for _, class := range held {
			return class
		}

		return Unmanaged
	}

	for incarnation, class := range held {
		switch {
		case cleaned == incarnation:
			return class
		case strings.HasPrefix(cleaned, incarnation+"/"):
			// Beneath a held incarnation.
			return class
		case strings.HasPrefix(incarnation, cleaned+"/"):
			// An ancestor of one. Destroying it takes the incarnation with it.
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
		return fmt.Errorf("%w: %s is, contains, or is contained by a source a durable output "+
			"capture holds; it must survive until the capture releases it", ErrRefused,
			stepsRelative)
	case Sealed:
		return fmt.Errorf("%w: %s is, contains, or is contained by a sealed source whose exact "+
			"bytes are being read now", ErrRefused, stepsRelative)
	case Unavailable:
		return fmt.Errorf("%w: the output source ledger under %s could not be read, so whether "+
			"%s is held is unknown. Destroying it on that basis is how a build's declared output "+
			"disappears with no record it existed: %v", ErrRefused, classifier.dir, stepsRelative,
			classifier.loadErr)
	}

	return nil
}

// PublicReason is what an UNAUTHENTICATED caller may be told, per class.
//
// It is a fixed sentence per class and names nothing the caller did not already
// name. Reason above is the operator's text, and the Unavailable arm of it
// carries the classifier's own directory and the underlying OS error -- which
// is the node's control-directory path and the raw errno, both of which the
// output plane's redaction rule forbids any route to emit. The route that
// serves this question is deliberately mTLS-exempt, because a pod on the node
// has to be able to ask it, so its audience is every pod on the node including
// a task pod.
//
// The detailed text keeps its readers: the authenticated refuseIfCaptureHeld
// callers and the daemon's log.
func PublicReason(class Class) string {
	switch class {
	case Held:
		return "the source is held by a durable output capture and must survive until the " +
			"capture releases it"
	case Sealed:
		return "the source is sealed and its exact bytes are being read now"
	case Unavailable:
		return "the output source ledger on this node could not be read, so whether this step " +
			"directory is held is unknown; the daemon's log says why"
	}

	return ""
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
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), stepMarkerPrefix) ||
			!strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(path.Join(classifier.dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Released between the listing and the read.
				continue
			}

			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		var body marker
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", name, err)
		}
		if body.Execution == "" || body.Output == "" {
			return nil, fmt.Errorf("%s names no step directory", name)
		}

		if class := classFor(body.State); class != Unmanaged {
			key := output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(body.Execution), Output: output.OutputName(body.Output)}
			held[key.Directory()] = class
		}
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
	case "sealed":
		return Sealed
	case "released":
		// A tombstone: the capture is over and holds nothing.
		return Unmanaged
	}

	return Held
}
