// Package output is the contract for turning one ordinary task output into
// durable, claimable result content.
//
// Hangar owns the capture row's vocabulary, the step marker, structural
// sealing, canonical publication, claims and physical reclamation. Its
// one consumer, the pipeline Run, owns why an output matters, its name,
// authorization and retention policy, and composes with Hangar through a
// caller-owned database transaction and an opaque identity.
//
// Durable capture is an *extension* of the base exact-execution protocol in
// hangar/executioncontrol. It references that package's Identity rather than
// declaring its own, so an execution has one truth however many optional gates
// hang off it. The architecture guard in architecture_test.go fails the test
// suite if a type here redeclares it.
//
// The wire contract is frozen, language-neutrally, in testdata/protocol-v1.
// Every enum is closed: an unknown or newly added member is refused at decode.
//
// This package imports hangar and hangar/executioncontrol and nothing else
// first-party. It contains no database, Kubernetes or cloud dependency at all;
// the PostgreSQL implementations live in atc/db so they can accept the existing
// atc/db.Tx without an import cycle, and the GCS roles get their own packages
// beneath this one so no umbrella client hands every caller the strongest
// credential.
package output

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

const (
	// ProtocolVersion is the only version of the capture extension that exists.
	ProtocolVersion = "hangar-output-v1"

	// SourceLedgerVersion is the on-node record format for step markers. It is reported by the handshake beside the base ledger
	// version, because a node can gain one without the other.
	SourceLedgerVersion = "hangar-output-source-ledger-v1"

	// ReadyLabel says the capture extension is served on a node. A capture
	// Pod requires it *and* executioncontrol.ReadyLabel; neither is authority,
	// because the warrant the daemon verifies is.
	//
	// It is deliberately not concourse.dev/hangar-v1, which advertises strict
	// inputs only. Reusing that label would let a strict-input daemon schedule
	// a capture it cannot perform.
	ReadyLabel = "concourse.dev/hangar-output-v1"
)

// The typed outcomes. Absence, signature failure, replay, collision,
// corruption, containment failure, authorization failure, limit rejection,
// cancellation, ambiguity and infrastructure failure all stay distinct, and
// none of them is ever a cache miss.
//
// The first six are the foundation's own sentinels rather than new ones, so a
// caller's errors.Is keeps working across the boundary between strict input and
// durable output. A parallel set would have meant every caller checking twice
// and eventually checking once.
//
// # The two lifecycle states, and what a consumer sees
//
// A registered generation is a lifecycle row with no reclaimed_at; a reclaimed
// one has it stamped. A consumer asking for a reclaimed, unregistered or absent
// generation gets ErrNotFound; one asking while an integrity finding is open
// gets ErrAtRisk. A consumer must treat ErrNotFound as "not available, recapture
// under a new generation" and must not infer anything finer from it.
var (
	ErrNotFound       = hangar.ErrNotFound
	ErrConflict       = hangar.ErrConflict
	ErrCorrupt        = hangar.ErrCorrupt
	ErrUnauthorized   = hangar.ErrUnauthorized
	ErrLimitExceeded  = hangar.ErrLimitExceeded
	ErrInfrastructure = hangar.ErrInfrastructure

	// ErrTimeout is a deadline reached on the database clock, never on a
	// daemon's wall clock.
	ErrTimeout = errors.New("hangar/output: deadline exceeded")

	// ErrUnresolved is the honest answer when the exact outcome cannot yet be
	// proved. It authorizes waiting and nothing else -- not capture, not
	// release, not cleanup.
	ErrUnresolved = errors.New("hangar/output: unresolved")

	// ErrSealed is a writer admission refused because sealing has begun.
	//
	// It is a distinct value from ErrSealUnconfirmed and the distinction is the
	// point: this one says the source stopped accepting writers, which is the
	// system working; that one says a seal could not be PROVED, which is the
	// system failing closed. A caller that conflated them would retry the first
	// and give up on the second, both backwards.
	ErrSealed = errors.New("hangar/output: the source is sealed")

	// ErrSealUnconfirmed is a container boundary that could not be proved
	// before the capture deadline. It publishes nothing, and it never
	// re-executes the producer.
	ErrSealUnconfirmed = errors.New("hangar/output: seal unconfirmed")

	// ErrInProgress is a node operation that has begun in the background and
	// not finished. It authorizes asking again later and nothing else.
	ErrInProgress = errors.New("hangar/output: in progress")

	// ErrSealInProgress is a seal waiting for the producing Pod's containers
	// to stop, or canonicalizing.
	ErrSealInProgress = fmt.Errorf("%w: seal", ErrInProgress)

	// ErrPublishInProgress is a publish uploading the sealed tree.
	ErrPublishInProgress = fmt.Errorf("%w: publish", ErrInProgress)

	// ErrGenerationConflict is a conditional operation refused because the
	// exact generation is not the one at the key. It is counted; it never
	// broadens into an unconditional delete.
	ErrGenerationConflict = errors.New("hangar/output: generation conflict")

	// ErrAtRisk indicates an unresolved failure observed during storage operations.
	// It blocks new captures, claims, warrants and reclaim admission
	// while leaving releases and
	// diagnosis possible.
	ErrAtRisk = errors.New("hangar/output: storage integrity is at risk")

	// ErrUnknownMember is a closed vocabulary asked to accept a member it does
	// not have.
	ErrUnknownMember = errors.New("hangar/output: unknown closed-vocabulary member")

	// ErrInvalidIdentity is a malformed or absent opaque identity.
	ErrInvalidIdentity = errors.New("hangar/output: invalid identity")

	// ErrIncomplete is a structurally valid value whose parts contradict each
	// other.
	ErrIncomplete = errors.New("hangar/output: incomplete value")

	// ErrUnsupportedProtocol is a message in a protocol version this daemon does not speak.
	ErrUnsupportedProtocol = errors.New("hangar/output: unsupported protocol version")

	// ErrCaptureDisabled is a durable-output-capture operation asked of a
	// component that does not have the facet.
	//
	// It is a TYPED result with no cache-tier fallback, and the distinction is
	// the whole point: "this daemon has no output bucket" and "this capture
	// failed" must not be the same answer, because a
	// caller that cannot tell them apart writes the retry, and the retry it
	// writes is the cache tier. Nothing in this plane degrades into a cache
	// miss.
	ErrCaptureDisabled = errors.New("hangar/output: durable output capture is not enabled here")
)

// BucketFingerprintScheme identifies GCS buckets in stored namespace identities.
// The daemon handshake, the object marker and the orphan sweep must agree on
// that identity. Disk namespaces use a separately pinned storage identity.
const BucketFingerprintScheme = "gs://"

func validateProtocol(version string) error {
	if version != ProtocolVersion {
		return fmt.Errorf("%w: %q, this daemon speaks %q", ErrUnsupportedProtocol, version, ProtocolVersion)
	}

	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validateUUID(what, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidIdentity, what)
	}
	if !uuidPattern.MatchString(value) {
		return fmt.Errorf("%w: %s %q is not a lowercase RFC 4122 UUID", ErrInvalidIdentity, what, value)
	}

	return nil
}

// The opaque identities. Every one of them is caller- or server-generated, has
// no readable structure, and is a distinct Go type so that passing a claim id
// where a capture id belongs does not compile.

// ReservationID is the correlation handle an object marker carries: an input
// stage's id, or the id a capture derives from its key (CaptureKey.MarkerID).
// It exists before the first object create so a possibly-created object can be
// correlated without trusting a task-supplied key.
type ReservationID string

func (id ReservationID) Validate() error { return validateUUID("reservation id", string(id)) }

// ClaimID is a caller-generated UUID with no domain meaning. Acquiring the same
// id for the same tree ref is idempotent; reusing it for another ref is a
// typed conflict. A consumer that moves a binding from hidden to published
// keeps the same id -- Hangar neither replaces nor reacquires it.
type ClaimID string

func (id ClaimID) Validate() error { return validateUUID("claim id", string(id)) }

// OpaqueID is an identifier Hangar stores, compares and hands back, and never
// interprets. Producer checkpoints and consumer bindings are opaque: the moment
// Hangar could read one, it would know what a Run is.
//
// It is deliberately not a UUID. A consumer may key its own records however it
// likes; the only rules are that the value is bounded and non-empty.
type OpaqueID string

// MaxOpaqueIDBytes bounds an opaque identifier so a consumer cannot use one as
// a side channel for a payload.
const MaxOpaqueIDBytes = 256

func (id OpaqueID) Validate() error {
	if id == "" {
		return fmt.Errorf("%w: opaque identifier is empty", ErrInvalidIdentity)
	}
	if len(id) > MaxOpaqueIDBytes {
		return fmt.Errorf("%w: opaque identifier is %d bytes, the bound is %d",
			ErrLimitExceeded, len(id), MaxOpaqueIDBytes)
	}

	return nil
}

// OutputName is the declared ordinary task output selected for capture. It is a
// name the task already declares, never a path: no API here accepts an absolute
// path, a hostPath, an object key or a caller-chosen root.
type OutputName string

// MaxOutputNameBytes matches the ordinary task-output name bound.
const MaxOutputNameBytes = 255

var outputNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (name OutputName) Validate() error {
	if name == "" {
		return fmt.Errorf("%w: output name is empty", ErrInvalidIdentity)
	}
	if len(name) > MaxOutputNameBytes {
		return fmt.Errorf("%w: output name is %d bytes, the bound is %d",
			ErrLimitExceeded, len(name), MaxOutputNameBytes)
	}
	if !outputNamePattern.MatchString(string(name)) {
		return fmt.Errorf("%w: output name %q is not a declared output name; it must not be a "+
			"path, a key or a root", ErrInvalidIdentity, name)
	}

	return nil
}

// Timestamp is re-exported from the base protocol so that a value written here
// and a value written there have the same single spelling on the wire.
type Timestamp = executioncontrol.Timestamp

// NewTimestamp is likewise re-exported.
func NewTimestamp(at time.Time) Timestamp { return executioncontrol.NewTimestamp(at) }

// Clock is the seam every deadline in this package is measured against.
//
// It exists so that no production path can accidentally measure a lease against
// a daemon's wall clock. Every deadline is on the database clock: a node whose
// clock drifts must not be able to expire its own hold, and expiry alone is
// never proof or release authority.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time { return fn() }
