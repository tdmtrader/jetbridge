// Package output is the product-neutral contract for turning one ordinary task
// output into durable, claimable result content.
//
// It is deliberately narrow about what it knows. Hangar owns durable capture
// intent, source retention, structural sealing, canonical publication,
// authoritative receipts, exact-generation inventory, opaque claims, orphan
// recovery and physical reclamation. A consumer owns why an output matters, its
// name, authorization, finality, causation and retention policy. Nothing in
// this package can tell you which is which, and that is the point: a consumer
// composes with Hangar through a caller-owned database transaction and an
// opaque identity, and Hangar never learns what it composed with.
//
// Durable capture is an *extension* of the base exact-execution protocol in
// hangar/executioncontrol. It references that package's Identity and
// ActivationEpoch rather than declaring its own, so an execution has one truth
// however many optional gates hang off it. The architecture guard in
// architecture_test.go fails the test suite if a type here redeclares either.
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

	// MarkerVersion is the versioned ownership evidence written into an
	// object's immutable-at-creation metadata. An object without it is
	// unmanaged and is never relabelled, adopted or deleted; an object with a
	// different version is a typed collision.
	MarkerVersion = "hangar-output-v1"

	// SourceLedgerVersion is the on-node record format for source holds and
	// writer tickets. It is reported by the handshake beside the base ledger
	// version, because a node can gain one without the other.
	SourceLedgerVersion = "hangar-output-source-ledger-v1"

	// ReadyLabel attests that the capture extension is usable on a node. A
	// capture Pod requires it *and* executioncontrol.ReadyLabel; neither is
	// authority, because the authenticated ExtensionHandshake is.
	//
	// It is deliberately not concourse.dev/hangar-v1, which attests strict
	// inputs only. Reusing that label would let a strict-input daemon schedule
	// a capture it cannot perform.
	ReadyLabel = "concourse.dev/hangar-output-v1"

	// ReceiptDomain is the Ed25519 signing domain for per-capture receipts. Its
	// private key is mounted only in the output daemon.
	ReceiptDomain = "hangar-output-receipt-v1"

	// MaterializeDomain is the HMAC domain for managed-output read grants. It
	// is a separate key from ReceiptDomain and from the foundation's
	// strict-input materialization key: a read grant is not a publication
	// authority and must not be signable by anything that can mint one.
	MaterializeDomain = "hangar-output-materialize-v1"
)

// The typed outcomes. Absence, signature failure, replay, collision,
// corruption, containment failure, authorization failure, limit rejection,
// cancellation, ambiguity and infrastructure failure all stay distinct, and
// none of them is ever a cache miss (Req 27).
//
// The first six are the foundation's own sentinels rather than new ones, so a
// caller's errors.Is keeps working across the boundary between strict input and
// durable output. A parallel set would have meant every caller checking twice
// and eventually checking once.
var (
	ErrNotFound       = hangar.ErrNotFound
	ErrConflict       = hangar.ErrConflict
	ErrCorrupt        = hangar.ErrCorrupt
	ErrUnauthorized   = hangar.ErrUnauthorized
	ErrLimitExceeded  = hangar.ErrLimitExceeded
	ErrInfrastructure = hangar.ErrInfrastructure

	// ErrCancelled is a capture terminated before its irreversible publish
	// point. After that point cancellation settles a receipt or an orphan; it
	// never unmakes an object.
	ErrCancelled = errors.New("hangar/output: cancelled")

	// ErrTimeout is a deadline reached on the database clock, never on a
	// daemon's wall clock.
	ErrTimeout = errors.New("hangar/output: deadline exceeded")

	// ErrSourceLost is a node that will not come back. It is terminal, it is a
	// failure, and it may never be settled into a synthetic receipt.
	ErrSourceLost = errors.New("hangar/output: source lost")

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

	// ErrSealUnconfirmed is a writer drain or container boundary that could not
	// be proved before the seal deadline. It publishes no receipt, and it never
	// re-executes the producer.
	ErrSealUnconfirmed = errors.New("hangar/output: seal unconfirmed")

	// ErrGenerationConflict is a conditional operation refused because the
	// exact generation is not the one at the key. It becomes debt; it never
	// broadens into an unconditional delete.
	ErrGenerationConflict = errors.New("hangar/output: generation conflict")

	// ErrAtRisk is the fail-closed state entered when the bucket's lifetime
	// policy cannot currently be proved safe. It blocks new captures, claims,
	// grants, adoption and reclaim admission while leaving releases and
	// diagnosis possible.
	ErrAtRisk = errors.New("hangar/output: policy trust is at risk")

	// ErrUnknownMember is a closed vocabulary asked to accept a member it does
	// not have.
	ErrUnknownMember = errors.New("hangar/output: unknown closed-vocabulary member")

	// ErrInvalidIdentity is a malformed or absent opaque identity.
	ErrInvalidIdentity = errors.New("hangar/output: invalid identity")

	// ErrIncomplete is a structurally valid value whose parts contradict each
	// other.
	ErrIncomplete = errors.New("hangar/output: incomplete value")

	// ErrUnsupportedProtocol is a message from outside this cohort.
	ErrUnsupportedProtocol = errors.New("hangar/output: unsupported protocol version")

	// ErrCaptureDisabled is a durable-output-capture operation asked of a
	// component that does not have the facet.
	//
	// Req 58 makes this a TYPED result with no cache-tier fallback, and the
	// distinction is the whole of the requirement: "this daemon has no output
	// bucket" and "this capture failed" must not be the same answer, because a
	// caller that cannot tell them apart writes the retry, and the retry it
	// writes is the cache tier. Nothing in this plane degrades into a cache
	// miss.
	ErrCaptureDisabled = errors.New("hangar/output: durable output capture is not enabled here")
)

// BucketFingerprintScheme is the one prefix a bucket fingerprint carries.
//
// A fingerprint rather than the bare name, because it is compared between
// components that were configured separately -- the attestor's expectation, the
// daemon's handshake, the inventory cursor's key -- and two spellings of one
// bucket is how a cursor ends up scoped to a bucket nobody is publishing into.
//
// It is a CONSTANT and not a function taking a bucket name, deliberately. A
// function here would be an exported API in this package that accepts a
// caller-chosen storage location, which is the shape checkNoAPIAcceptsAStorageLocation
// exists to reject; the guard found it when it was written that way, and the
// right answer was to stop writing it that way rather than to rename the
// parameter past the rule. The server-derived side reads
// OutputNamespace.BucketFingerprint, which takes nothing; the attestor, whose
// bucket is its own authenticated flag, composes the same two parts.
const BucketFingerprintScheme = "gs://"

func validateProtocol(version string) error {
	if version != ProtocolVersion {
		return fmt.Errorf("%w: %q, this cohort speaks %q", ErrUnsupportedProtocol, version, ProtocolVersion)
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
// where a handoff id belongs does not compile.

// HandoffID is the idempotency key for one capture handoff. It is predeclared
// before the producing process may start, and repeating it with the same
// immutable facts returns the same durable outcome; reuse for different facts
// conflicts. A new build gets a new one.
type HandoffID string

func (id HandoffID) Validate() error { return validateUUID("handoff id", string(id)) }

// SourceLeaseID is the provisional, non-authorizing hold on the source
// incarnation. It prevents cleanup, replacement, remap, reuse and loss; it
// authorizes no sealing, termination, deletion, publication or binding.
type SourceLeaseID string

func (id SourceLeaseID) Validate() error { return validateUUID("source lease id", string(id)) }

// ReleaseIntentID names one exact fenced release of a source hold. The two
// halves of a release -- the caller's recorded intent and the daemon's
// acknowledgement -- carry the same one, which is what makes the pair
// crash-recoverable rather than a claimed atomic commit.
type ReleaseIntentID string

func (id ReleaseIntentID) Validate() error { return validateUUID("release intent id", string(id)) }

// ReservationID names the reservation created with the producer-completion
// checkpoint. It exists before the first object create precisely so recovery
// and inventory can correlate a possibly-created object without trusting a
// task-supplied key.
type ReservationID string

func (id ReservationID) Validate() error { return validateUUID("reservation id", string(id)) }

// WriterTicketID names one admitted write capability over a source
// incarnation. Every operation that can obtain or exercise write capability
// holds one; sealing captures the exact set outstanding and waits for it.
type WriterTicketID string

func (id WriterTicketID) Validate() error { return validateUUID("writer ticket id", string(id)) }

// ClaimID is a caller-generated UUID with no domain meaning. Acquiring the same
// id for the same exact ref is idempotent; reusing it for another ref is a
// typed conflict. A consumer that moves a binding from hidden to published
// keeps the same id -- Hangar neither replaces nor reacquires it.
type ClaimID string

func (id ClaimID) Validate() error { return validateUUID("claim id", string(id)) }

// ReadLeaseID names one active read of an exact generation. Reclaim admission
// is refused while one is active, even after the last claim is released.
type ReadLeaseID string

func (id ReadLeaseID) Validate() error { return validateUUID("read lease id", string(id)) }

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

// HandleGeneration is the daemon-assigned generation of a source handle. A
// handle string alone is never an identity: handles are reused, and a reused
// handle with a stale generation is exactly the confusion the source
// incarnation exists to make impossible.
type HandleGeneration uint64

// CaptureFence is the monotonic fencing epoch of capture *ownership*. It is a
// different fence from the execution's: takeover of a capture lease advances
// this one and leaves the execution's alone.
type CaptureFence uint64

// WriterFence is the monotonic fencing epoch of *writer admission* over one
// source incarnation. Ticket issuance and the open-to-sealing transition
// serialize on it.
type WriterFence uint64

// FirstWriterFence is the writer-admission epoch of a source incarnation no
// writer has ever been fenced out of.
//
// It is the floor rather than a default: a source with no ticket still HAS a
// writer-admission epoch -- nobody has been superseded -- and a receipt has to
// be able to claim it, because ReceiptClaims.Validate refuses a zero fence. The
// ATC spells the same value for its first admission; this is the one the node's
// own ledger answers with when it is asked what it admitted.
const FirstWriterFence = WriterFence(1)

// LeaseFence is the monotonic fencing epoch of a read or reclaim lease.
type LeaseFence uint64

// CursorFence is the monotonic fencing epoch of the single inventory cursor
// owner for one output bucket and activation epoch.
type CursorFence uint64

// Timestamp is re-exported from the base protocol so that a value written here
// and a value written there have the same single spelling on the wire.
type Timestamp = executioncontrol.Timestamp

// NewTimestamp is likewise re-exported.
func NewTimestamp(at time.Time) Timestamp { return executioncontrol.NewTimestamp(at) }

// Clock is the seam every deadline in this package is measured against.
//
// It exists so that no production path can accidentally measure a lease against
// a daemon's wall clock. Requirements 10, 11, 36, 39 and 48 all say "database
// clock", and they mean it: a node whose clock drifts must not be able to
// expire its own hold, and expiry alone is never proof or release authority.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time { return fn() }
