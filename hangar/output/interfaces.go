package output

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// Tx is the caller-owned database transaction Hangar composes with.
//
// Hangar never begins, commits or rolls one back. A consumer's binding write
// and the Hangar operation beside it either commit together or roll back
// together, so neither a dangling visible binding nor an indefinitely leaked
// claim is a valid crash outcome -- and that is only true if the transaction
// belongs to the caller.
//
// The method set is the intersection that both *sql.Tx and the repository's own
// atc/db.Tx satisfy. QueryRowContext is deliberately absent: atc/db.Tx returns a
// squirrel.RowScanner from it, and Go method sets match exactly, so including it
// would make this interface unsatisfiable by the very transaction it exists to
// accept. Phase 1's atc/db package asserts the satisfaction; this package cannot,
// because importing atc/db would stop it being a leaf.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// *sql.Tx satisfies it, which is the half this package can prove by itself.
var _ Tx = (*sql.Tx)(nil)

// ResolvedReservation is a reservation bound to its server-derived scope and
// digest, after canonicalization and before the first object create.
//
// It exists so that every possibly-created object has a pre-existing generic
// reservation that recovery and inventory can correlate, without either of them
// trusting a key some task supplied. Note what a caller cannot do with it:
// Scope and Digest are fields the control plane fills in, never parameters an
// API accepts.
type ResolvedReservation struct {
	ReservationID   ReservationID
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	HandoffID       HandoffID
	CaptureFence    CaptureFence
	Scope           hangar.Scope
	Digest          hangar.Digest
	Marker          ObjectMarker
}

func (reservation ResolvedReservation) Validate() error {
	if err := reservation.ReservationID.Validate(); err != nil {
		return err
	}
	if err := reservation.Execution.Validate(); err != nil {
		return err
	}
	if reservation.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := reservation.HandoffID.Validate(); err != nil {
		return err
	}
	if reservation.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero", ErrIncomplete)
	}
	if err := reservation.Scope.Validate(); err != nil {
		return err
	}
	if err := reservation.Digest.Validate(); err != nil {
		return err
	}
	if err := reservation.Marker.Validate(); err != nil {
		return err
	}
	if reservation.Marker.Scope != reservation.Scope || reservation.Marker.Digest != reservation.Digest {
		return fmt.Errorf("%w: the marker describes a different logical tree than the reservation",
			ErrInvalidIdentity)
	}
	if reservation.Marker.ReservationID != reservation.ReservationID {
		return fmt.Errorf("%w: the marker names a different reservation", ErrInvalidIdentity)
	}

	return nil
}

// PublishedObject is what a store reports about one exact generation.
//
// Deduplicated says the bytes were already there, in the same server-derived
// scope, with the expected digest, exact strict attributes and an accepted
// marker. It never means "something plausible was at the key": an unmarked
// object, a wrong marker version, corrupt metadata or different immutable
// content is a typed collision, and is never overwritten, relabelled or
// silently adopted.
type PublishedObject struct {
	Attributes     hangar.TreeAttributes
	Metageneration int64
	Marker         ObjectMarker
	Deduplicated   bool
}

func (object PublishedObject) Validate() error {
	if err := object.Attributes.Ref.Validate(); err != nil {
		return err
	}
	if object.Metageneration <= 0 {
		return fmt.Errorf("%w: object metageneration is not positive", ErrIncomplete)
	}
	if err := object.Marker.Validate(); err != nil {
		return err
	}
	if !object.Marker.Matches(object.Attributes.Ref) {
		return fmt.Errorf("%w: the object's marker describes a different logical tree", ErrConflict)
	}

	return nil
}

// Publisher creates and reads output objects. It cannot list and it cannot
// delete, and its cloud principal holds neither permission.
//
// The role split is not decoration. A Kubernetes service account is Pod-wide,
// so adding an output role to an existing daemon would give that daemon's
// cache and strict-input identity the same role. Separate interfaces, separate
// binaries and separate accounts are how the privilege boundary survives
// somebody adding one convenient method.
type Publisher interface {
	// EnsureObject creates the canonical tree if absent and returns the exact
	// generation either way. It takes a resolved reservation rather than a key
	// or a bucket: the location is derived from authenticated deployment
	// context, never chosen by a caller.
	EnsureObject(ctx context.Context, reservation ResolvedReservation, canonical io.Reader, size int64) (PublishedObject, error)

	// StatExactObject reads metadata for one exact generation.
	StatExactObject(ctx context.Context, ref hangar.TreeRef) (PublishedObject, error)

	// OpenExactObject reads the bytes, under an active read lease. The lease is
	// a parameter rather than an ambient fact so the daemon cannot open an
	// object it has not proved it may still read.
	OpenExactObject(ctx context.Context, ref hangar.TreeRef, lease ReadLease) (io.ReadCloser, PublishedObject, error)
}

// PageBudget bounds one inventory pass. Every field is a stop condition.
type PageBudget struct {
	MaxObjects       int
	MaxMetadataBytes int64
	MaxDuration      time.Duration
}

// DefaultPageBudget is the frozen bound from Req 44.
func DefaultPageBudget() PageBudget {
	return PageBudget{
		MaxObjects:       MaxInventoryPageObjects,
		MaxMetadataBytes: MaxInventoryPageMetadataBytes,
		MaxDuration:      MaxInventoryPassDuration,
	}
}

func (budget PageBudget) Validate() error {
	if budget.MaxObjects <= 0 || budget.MaxObjects > MaxInventoryPageObjects {
		return fmt.Errorf("%w: page object budget %d is outside 1..%d",
			ErrIncomplete, budget.MaxObjects, MaxInventoryPageObjects)
	}
	if budget.MaxMetadataBytes <= 0 || budget.MaxMetadataBytes > MaxInventoryPageMetadataBytes {
		return fmt.Errorf("%w: page metadata budget %d is outside 1..%d",
			ErrIncomplete, budget.MaxMetadataBytes, MaxInventoryPageMetadataBytes)
	}
	if budget.MaxDuration <= 0 || budget.MaxDuration > MaxInventoryPassDuration {
		return fmt.Errorf("%w: page duration budget %s is outside 0..%s",
			ErrIncomplete, budget.MaxDuration, MaxInventoryPassDuration)
	}

	return nil
}

// InventoryObject is one object as inventory sees it.
//
// Managed distinguishes an object this system created from one it merely found.
// An unmanaged object is recorded and left completely alone: pre-capability,
// unmarked and malformed objects are never automatically relabelled or deleted,
// however tidy that would be.
type InventoryObject struct {
	ObjectKey      string
	Generation     int64
	Metageneration int64
	Size           int64
	CreatedAt      Timestamp
	Marker         ObjectMarker
	Managed        bool
}

// InventoryPage is one reserved page and the cursor position after it.
//
// Complete is false when the pass stopped on a budget or a failure. The cursor
// advances only when every object in the page has a committed disposition, so
// an incomplete page is replayed rather than skipped.
type InventoryPage struct {
	Objects  []InventoryObject
	Next     InventoryCursor
	Complete bool
}

// Inventory lists and stats. It cannot create and it cannot delete.
//
// Note what the list method does *not* take: a prefix. Bucket-wide list
// authority is a fact about GCS IAM that this code cannot narrow, so the
// boundary is the dedicated bucket plus a server-derived prefix this
// implementation applies itself -- and the honest way to say that is to give
// the caller no way to ask for a different one.
type Inventory interface {
	ListPage(ctx context.Context, cursor InventoryCursor, budget PageBudget) (InventoryPage, error)
	StatExactObject(ctx context.Context, ref hangar.TreeRef) (PublishedObject, error)
}

// Reclaimer is the only interface in this system that can delete a published
// object, and architecture_test.go fails the build if a second one appears.
//
// GCS IAM cannot require a caller to send a generation precondition once delete
// permission exists. So the requirement lives in the signature: there is one
// method, it takes an exact registered ref and an explicit precondition, and
// there is no key-only or unconditional route to fall back to. Only the
// isolated reclaimer workload links an implementation.
type Reclaimer interface {
	DeleteExactGeneration(ctx context.Context, ref hangar.TreeRef, precondition DeletePrecondition) (DeleteOutcome, error)
}

// PrincipalRole is the closed set of cloud identities in the output plane.
type PrincipalRole string

const (
	PrincipalPublisher      PrincipalRole = "publisher"
	PrincipalInventory      PrincipalRole = "inventory"
	PrincipalReclaimer      PrincipalRole = "reclaimer"
	PrincipalPolicyAttestor PrincipalRole = "policy_attestor"
)

func PrincipalRoles() []PrincipalRole {
	return []PrincipalRole{
		PrincipalPublisher,
		PrincipalInventory,
		PrincipalReclaimer,
		PrincipalPolicyAttestor,
	}
}

// PrincipalBindings is what the attestor observed about who may do what.
//
// Permissions is deliberately the raw observed grant list rather than a set of
// booleans this code computed. Requirement 41 is explicit that IAM does not
// provide a metadata-only object permission or a prefix-scoped list, and a
// struct with a `CanReadMetadataOnly` field would quietly assert the opposite.
type PrincipalBindings struct {
	BucketFingerprint string
	Permissions       map[PrincipalRole][]string
}

// PolicyReader reads bucket lifetime policy and IAM. It has no object method
// at all: the attestor's cloud principal cannot read, create or delete a single
// object, which is why it can be trusted to say what the policy is.
type PolicyReader interface {
	ReadLifetimePolicy(ctx context.Context) (PolicySnapshot, error)
	ReadPrincipalBindings(ctx context.Context) (PrincipalBindings, error)
}

// WriterAdmission is one writer's ticket over a source incarnation.
//
// Every operation that can obtain or exercise write capability holds one:
// main, sidecar, init, ephemeral and hijack processes, and the daemon's own
// cleanup, delete, replacement, remap and reuse paths. A ticket cannot be
// transferred to a new process, Pod UID, handle generation or fence.
type WriterAdmission struct {
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	HandoffID       HandoffID
	Incarnation     SourceIncarnation
	WriterTicketID  WriterTicketID
	WriterFence     WriterFence
	PodUID          executioncontrol.PodUID
}

func (admission WriterAdmission) Validate() error {
	if err := admission.Execution.Validate(); err != nil {
		return err
	}
	if admission.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := admission.HandoffID.Validate(); err != nil {
		return err
	}
	if err := admission.Incarnation.Validate(); err != nil {
		return err
	}
	if err := admission.WriterTicketID.Validate(); err != nil {
		return err
	}
	if admission.WriterFence == 0 {
		return fmt.Errorf("%w: writer fence is zero", ErrIncomplete)
	}

	return nil
}

// SealRequest asks for the incarnation to stop accepting writers.
type SealRequest struct {
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	HandoffID       HandoffID
	Incarnation     SourceIncarnation
	CaptureFence    CaptureFence
	DeadlineAt      Timestamp
}

// SealStarted is the daemon half: admission is fenced, and this is the exact
// set of writers the other half must account for.
//
// DrainSet is captured at the moment admission is fenced, and it is the set
// sealing waits for. A later query returning zero current tickets is not the
// same thing and is not accepted as proof: the point of capturing the set is
// that a writer admitted and closed during the wait is still accounted for.
//
// It is returned rather than waited on because the ATC cannot know *which* pod
// writers to terminate until it has this list, and the seal cannot be confirmed
// until it has terminated them. A single blocking call would make each half
// wait for the other.
type SealStarted struct {
	Acknowledgement CaptureAcknowledgement
	DrainSet        []WriterTicketID
}

func (started SealStarted) Validate() error {
	if err := started.Acknowledgement.ValidateAs(CaptureSealStarted); err != nil {
		return err
	}

	seen := map[WriterTicketID]bool{}
	for _, ticket := range started.DrainSet {
		if err := ticket.Validate(); err != nil {
			return err
		}
		if seen[ticket] {
			return fmt.Errorf("%w: writer ticket %s appears twice in the captured drain set",
				ErrIncomplete, ticket)
		}
		seen[ticket] = true
	}

	return nil
}

// DrainedWriter is one ticket from the captured drain set, and the proof that
// it is closed.
//
// PodUID is empty for a writer that was never a pod process -- the daemon's own
// cleanup, delete, replacement, remap and reuse paths hold tickets too. When it
// is set, Req 14's Kubernetes half applies: the final status of every regular,
// init, sidecar and ephemeral container must have been observed `terminated`
// for that exact UID. NotFound, Gone and a force deletion are not that
// observation, which is why the field records what was seen rather than what
// was attempted.
type DrainedWriter struct {
	WriterTicketID       WriterTicketID
	Closed               CaptureAcknowledgement
	PodUID               executioncontrol.PodUID
	ContainersTerminated bool
}

func (drained DrainedWriter) Validate() error {
	if err := drained.WriterTicketID.Validate(); err != nil {
		return err
	}
	if err := drained.Closed.ValidateAs(CaptureWriterTicketClosed); err != nil {
		return err
	}
	if drained.Closed.WriterTicketID != drained.WriterTicketID {
		return fmt.Errorf("%w: the close statement names writer ticket %s, and this evidence is "+
			"offered for %s", ErrInvalidIdentity, drained.Closed.WriterTicketID, drained.WriterTicketID)
	}
	if drained.PodUID != "" && !drained.ContainersTerminated {
		return fmt.Errorf("%w: writer ticket %s was held by pod %s and no terminated status was "+
			"observed for its containers", ErrSealUnconfirmed, drained.WriterTicketID, drained.PodUID)
	}

	return nil
}

// SealConfirmation is the ATC half: the drain and container-termination
// evidence for the set BeginSeal captured.
//
// It carries the whole SealStarted rather than a ticket count, because the
// captured set is the only admissible proof. Validate requires the evidence to
// cover exactly that set, so "no tickets are outstanding right now" has nowhere
// to be expressed.
type SealConfirmation struct {
	Started      SealStarted
	Drained      []DrainedWriter
	CaptureFence CaptureFence
	ObservedAt   Timestamp
}

func (confirmation SealConfirmation) Validate() error {
	if err := confirmation.Started.Validate(); err != nil {
		return err
	}
	if confirmation.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero; a stale owner may not confirm a seal",
			ErrIncomplete)
	}

	fenced := confirmation.Started.Acknowledgement
	accounted := map[WriterTicketID]bool{}
	for _, drained := range confirmation.Drained {
		if err := drained.Validate(); err != nil {
			return err
		}
		if accounted[drained.WriterTicketID] {
			return fmt.Errorf("%w: writer ticket %s is accounted for twice",
				ErrIncomplete, drained.WriterTicketID)
		}
		if drained.Closed.Execution != fenced.Execution {
			return fmt.Errorf("%w: the close statement for writer ticket %s belongs to a "+
				"different exact execution than the seal", ErrInvalidIdentity, drained.WriterTicketID)
		}
		if drained.Closed.ActivationEpoch != fenced.ActivationEpoch {
			return fmt.Errorf("%w: the close statement for writer ticket %s was made under epoch "+
				"%d and the seal under %d", ErrInvalidIdentity, drained.WriterTicketID,
				drained.Closed.ActivationEpoch, fenced.ActivationEpoch)
		}
		accounted[drained.WriterTicketID] = true
	}

	captured := map[WriterTicketID]bool{}
	for _, ticket := range confirmation.Started.DrainSet {
		captured[ticket] = true
		if !accounted[ticket] {
			return fmt.Errorf("%w: writer ticket %s was in the captured drain set and no close "+
				"statement accounts for it", ErrSealUnconfirmed, ticket)
		}
	}
	for _, drained := range confirmation.Drained {
		if !captured[drained.WriterTicketID] {
			return fmt.Errorf("%w: writer ticket %s was never in the captured drain set; a seal "+
				"is confirmed against the set captured when admission was fenced, not against "+
				"whatever is outstanding now", ErrIncomplete, drained.WriterTicketID)
		}
	}

	return confirmation.ObservedAt.Validate()
}

// ReleaseIntent is the caller-recorded half of an exact fenced source release.
type ReleaseIntent struct {
	Disposition     Disposition
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	HandoffID       HandoffID
	SourceLeaseID   SourceLeaseID
	ReleaseIntentID ReleaseIntentID
	Incarnation     SourceIncarnation
}

// SourceControl is the authenticated node-local API for the source ledger.
//
// The existing artifact daemon holds no output-bucket credential and
// constructs no output GCS client; for a capture-marked source its sweep,
// cleanup, delete, replacement, remap and reuse paths come here instead, and
// fail closed when this authority is unavailable.
type SourceControl interface {
	// AcknowledgeHold durably records the pre-start, non-authorizing hold. The
	// producer's main process may not start before it returns.
	AcknowledgeHold(ctx context.Context, admission CaptureAdmission, incarnation SourceIncarnation) (CaptureAcknowledgement, error)

	// AdmitWriter issues a ticket, or refuses because sealing won the race.
	// Issuance and the open-to-sealing transition serialize on one durable
	// boundary, so there is no third outcome.
	AdmitWriter(ctx context.Context, admission WriterAdmission) (CaptureAcknowledgement, error)

	// RetireWriter closes a ticket.
	RetireWriter(ctx context.Context, admission WriterAdmission) (CaptureAcknowledgement, error)

	// BeginSeal fences future admission and returns the drain set captured at
	// that instant. It does not wait: the caller cannot terminate the writers
	// it has not been told about, so waiting here would be each half waiting
	// for the other.
	BeginSeal(ctx context.Context, request SealRequest) (SealStarted, error)

	// ConfirmSeal takes the drain and container-termination evidence for that
	// exact captured set and returns the seal_confirmed acknowledgement, or
	// ErrSealUnconfirmed with the reason. Nothing is canonicalized before it
	// returns, no receipt is published if it does not, and the producer is
	// never re-executed.
	ConfirmSeal(ctx context.Context, confirmation SealConfirmation) (CaptureAcknowledgement, error)

	// AcknowledgeRelease is the daemon half of a no-capture or
	// pre-reservation-cancel handoff. It is idempotent for the same intent.
	AcknowledgeRelease(ctx context.Context, intent ReleaseIntent) (ReleaseAcknowledgement, error)
}

// StatChallenge is a one-use, database-clock-bounded demand for fresh proof
// that an exact generation is really there, with the attributes and marker it
// should have.
//
// It exists because a signature over old facts proves the facts were once true.
// The nonce is consumed by the caller's transaction, which revalidates every
// bound fact before the deadline, so a receipt cannot be replayed for another
// capture, source, output or fence.
type StatChallenge struct {
	Nonce           string
	HandoffID       HandoffID
	ReservationID   ReservationID
	ActivationEpoch executioncontrol.ActivationEpoch
	Ref             hangar.TreeRef
	CaptureFence    CaptureFence
	NotAfter        Timestamp
}

func (challenge StatChallenge) Validate() error {
	if challenge.Nonce == "" {
		return fmt.Errorf("%w: stat challenge carries no nonce", ErrIncomplete)
	}
	if err := challenge.HandoffID.Validate(); err != nil {
		return err
	}
	if err := challenge.ReservationID.Validate(); err != nil {
		return err
	}
	if challenge.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := challenge.Ref.Validate(); err != nil {
		return err
	}
	if challenge.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero", ErrIncomplete)
	}

	return challenge.NotAfter.Validate()
}

// ReceiptVerifier checks a receipt before anything is bound to it.
//
// Syntactic validity is explicitly not enough (Req 26). Verification checks the
// signature against the activation epoch, matches every signed claim to the
// durable checkpoint, reservation and fence, and performs an exact-generation
// metadata stat for the strict attributes and marker.
type ReceiptVerifier interface {
	VerifyReceipt(ctx context.Context, receipt Receipt, challenge StatChallenge) (PublishedObject, error)
}

// ClaimRepository composes Hangar protection with a consumer's own writes.
//
// Both methods take the caller's transaction and return only an error, because
// the outcomes worth distinguishing are already typed sentinels: ErrConflict
// for the same claim id reused for another ref, ErrNotFound for an unregistered
// ref, ErrAtRisk when policy trust is not currently provable. A parallel
// outcome enum would have meant callers checking twice and eventually checking
// once.
type ClaimRepository interface {
	// AcquireClaim is idempotent for the same id and exact ref.
	AcquireClaim(ctx context.Context, tx Tx, acquisition ClaimAcquisition) error

	// ReleaseClaim is idempotent, and tombstones the identity for the lifetime
	// of the exact-ref lifecycle record.
	ReleaseClaim(ctx context.Context, tx Tx, release ClaimRelease) error
}

// ReadLeaseRequest asks for the right to read one exact generation.
//
// MaterializationTimeout is on the request because the lease term is derived
// from it: at least MinLeaseTerm, and at least the timeout plus
// LeaseTermMargin. A caller that under-reports its own timeout gets a lease it
// will outlive, and MayStartWork refuses to begin.
type ReadLeaseRequest struct {
	ReadLeaseID            ReadLeaseID
	ClaimID                ClaimID
	Ref                    hangar.TreeRef
	ActivationEpoch        executioncontrol.ActivationEpoch
	RequestedAt            Timestamp
	MaterializationTimeout time.Duration
}

func (request ReadLeaseRequest) Validate() error {
	if err := request.ReadLeaseID.Validate(); err != nil {
		return err
	}
	if err := request.ClaimID.Validate(); err != nil {
		return err
	}
	if err := request.Ref.Validate(); err != nil {
		return err
	}
	if request.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if request.MaterializationTimeout <= 0 {
		return fmt.Errorf("%w: materialization timeout is not positive; the lease term is derived "+
			"from it", ErrIncomplete)
	}

	return request.RequestedAt.Validate()
}

// ReadLeaseRepository manages the reader's half of protection.
//
// Acquisition happens inside the caller's transaction, together with claim,
// registration, policy and reclaim-exclusion revalidation. Only after that
// transaction commits may a usable grant be minted -- and minting is
// deliberately not atomic with the database, because signing is not a database
// operation and saying otherwise would be the atomic-commit claim this design
// refuses to make anywhere else.
type ReadLeaseRepository interface {
	AcquireReadLease(ctx context.Context, tx Tx, request ReadLeaseRequest) (ReadLease, error)
	RenewReadLease(ctx context.Context, tx Tx, lease ReadLease) (ReadLease, error)
	ReleaseReadLease(ctx context.Context, tx Tx, lease ReadLease) error
}

// HandoffStatus is what a generic caller may learn about a capture.
//
// Disposition is a pointer because before the arbiter is won there is no
// branch, and a "pending" enum member would make the unwon state look like a
// fourth outcome rather than the absence of one.
//
// PastIrreversiblePublishPoint is the field that matters to a canceller:
// before it, cancellation may terminally cancel and fenced-release the source;
// after it, cancellation must settle a registered receipt or a terminal orphan,
// and can never create a consumer binding.
type HandoffStatus struct {
	HandoffID                    HandoffID
	Disposition                  *Disposition
	Settled                      bool
	PastIrreversiblePublishPoint bool
	Receipt                      *Receipt
}

func (status HandoffStatus) Validate() error {
	if err := status.HandoffID.Validate(); err != nil {
		return err
	}
	if status.Disposition != nil {
		if err := status.Disposition.Validate(); err != nil {
			return err
		}
	} else if status.Settled {
		return fmt.Errorf("%w: a settled handoff has no disposition", ErrIncomplete)
	}
	if status.Receipt != nil {
		if err := status.Receipt.Validate(); err != nil {
			return err
		}
		if !status.PastIrreversiblePublishPoint {
			return fmt.Errorf("%w: a receipt exists for a handoff reported as pre-publish",
				ErrIncomplete)
		}
	}

	return nil
}

// CancelSettler is the product-neutral cancel and settle seam.
//
// A later consumer track may call these; Hangar owns their effects. Neither
// operation can create a consumer binding, and neither takes a reason: the
// reason a caller wants to stop is exactly the kind of meaning this package
// exists not to learn.
type CancelSettler interface {
	ClassifyHandoff(ctx context.Context, tx Tx, handoff HandoffID) (HandoffStatus, error)
	CancelOrSettle(ctx context.Context, tx Tx, handoff HandoffID) (HandoffStatus, error)
}

// DurableOutputCapture is the optional extension of one base execution.
//
// It carries no json tags, and neither does ControlledExecution: they are a
// Go-side composition seam, not a wire type. Phases 3 and 4 extend them with
// writer tickets and seal state, so freezing their JSON now would freeze a
// shape that is about to grow -- and a tag would claim it was already frozen.
// The rule they exist for is asserted behaviourally instead, in
// TestTheCaptureExtensionCannotForkTheBaseExecution.
//
// It is a distinct value hanging off a ControlledExecution rather than extra
// fields on the base envelope, because the protocol must keep an execution with
// *no* capture representable -- that is the shape the sibling
// `exact_execution_control` track wires every ordinary job onto, and a base
// envelope with capture fields set to zero would be a different thing that
// merely looks the same.
type DurableOutputCapture struct {
	Admission CaptureAdmission
}

func (capture DurableOutputCapture) Validate() error {
	return capture.Admission.Validate()
}

// ControlledExecution is a base execution and its optional capture extension.
type ControlledExecution struct {
	Envelope executioncontrol.Envelope
	Capture  *DurableOutputCapture
}

// HasDurableOutputCapture reports whether this execution opted in.
func (execution ControlledExecution) HasDurableOutputCapture() bool {
	return execution.Capture != nil
}

// Validate enforces the rule the whole extension design rests on: the capture
// references the base identity and the base activation epoch, and does not get
// its own.
func (execution ControlledExecution) Validate() error {
	if err := execution.Envelope.Validate(); err != nil {
		return err
	}
	if execution.Capture == nil {
		return nil
	}
	if err := execution.Capture.Validate(); err != nil {
		return err
	}
	if execution.Capture.Admission.Execution != execution.Envelope.Identity {
		return fmt.Errorf("%w: the capture extension names a different exact execution than the "+
			"envelope it extends", ErrInvalidIdentity)
	}
	if execution.Capture.Admission.ActivationEpoch != execution.Envelope.ActivationEpoch {
		return fmt.Errorf("%w: the capture extension was admitted under epoch %d and the envelope "+
			"under %d; one epoch attests both facets", ErrInvalidIdentity,
			execution.Capture.Admission.ActivationEpoch, execution.Envelope.ActivationEpoch)
	}

	return nil
}
