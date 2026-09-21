package output

import (
	"context"
	"database/sql"
	"fmt"
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
// accept. The atc/db package asserts the satisfaction through CaptureRepository;
// this package cannot, because importing atc/db would stop it being a leaf.
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
//
// Debt is the other half of "every object has a committed disposition". An
// object that cannot be classified -- poisoned metadata, a marker some other
// cohort wrote, metadata larger than a whole pass budget -- is a disposition
// too, and Req 44 requires it to be committed BEFORE the cursor moves past it.
// Carrying it beside the objects is what lets one transaction write both, which
// is the only arrangement in which a poisoned object can neither be lost nor
// replayed forever.
type InventoryPage struct {
	Objects  []InventoryObject
	Debt     []InventoryDebt
	Next     InventoryCursor
	Complete bool
}

// PrincipalRole is the closed set of cloud identities in the output plane.
type PrincipalRole string

const (
	PrincipalPublisher      PrincipalRole = "publisher"
	PrincipalInventory      PrincipalRole = "inventory"
	PrincipalReclaimer      PrincipalRole = "reclaimer"
	PrincipalPolicyAttestor PrincipalRole = "policy_attestor"
)

// Validate refuses a role outside the closed set.
//
// It matters here for the same reason it matters everywhere else in this
// vocabulary: a runtime denial recorded against an invented role name is a
// violation row an operator cannot map to a service account.
func (role PrincipalRole) Validate() error {
	for _, member := range PrincipalRoles() {
		if role == member {
			return nil
		}
	}

	return fmt.Errorf("%w: %q is not one of this plane's four principals %v",
		ErrUnknownMember, role, PrincipalRoles())
}

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

	// UnrecognisedRoles is every IAM role name the translation could not
	// expand into permissions, per principal that holds it.
	//
	// It is a separate field and not an entry in Permissions, and the
	// difference is the whole of R1-F5. An unknown role folded in as a
	// pseudo-permission matches nothing the matrix forbids, so a publisher
	// bound roles/storage.objectCreator PLUS a custom role carrying
	// storage.objects.delete satisfied its required set, tripped no excess
	// finding, and attested SAFE. The matrix cannot know what a custom role
	// contains -- only the project that defined it does -- so the honest
	// answer is not "harmless", it is "unknown, therefore unsafe", and that is
	// a finding rather than an omission.
	UnrecognisedRoles map[PrincipalRole][]string
}

// WriterAdmission is one writer's ticket over a source incarnation.
//
// Every operation that can obtain or exercise write capability holds one:
// main, sidecar, init, ephemeral and hijack processes, and the daemon's own
// cleanup, delete, replacement, remap and reuse paths. A ticket cannot be
// transferred to a new process, Pod UID, handle generation or fence.
type WriterAdmission struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
	WriterTicketID  WriterTicketID                   `json:"writer_ticket_id"`
	WriterFence     WriterFence                      `json:"writer_fence"`
	PodUID          executioncontrol.PodUID          `json:"pod_uid"`
}

func (admission WriterAdmission) Validate() error {
	if err := validateProtocol(admission.ProtocolVersion); err != nil {
		return err
	}
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
	// A ticket that names no pod is bound to nothing. `sameWriter` compares two
	// empty strings and calls them the same process, so an admission with no
	// Pod UID would replay for any later writer that also omitted it -- the
	// ticket's whole binding, vacuous, for a caller that just left the field
	// out. Req 13: a ticket cannot be transferred to a new process or Pod UID,
	// which requires it to name one.
	if admission.PodUID == "" {
		return fmt.Errorf("%w: writer admission names no pod", ErrIncomplete)
	}

	return nil
}

// SealRequest asks for the incarnation to stop accepting writers.
type SealRequest struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
	CaptureFence    CaptureFence                     `json:"capture_fence"`
	DeadlineAt      Timestamp                        `json:"deadline_at"`
}

func (request SealRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}
	if request.ActivationEpoch == 0 {
		return fmt.Errorf("%w: a seal names no activation epoch", ErrIncomplete)
	}
	if err := request.HandoffID.Validate(); err != nil {
		return err
	}
	if err := request.Incarnation.Validate(); err != nil {
		return err
	}
	if request.CaptureFence == 0 {
		return fmt.Errorf("%w: a seal names no capture fence", ErrIncomplete)
	}

	return request.DeadlineAt.Validate()
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
	Acknowledgement CaptureAcknowledgement `json:"acknowledgement"`
	DrainSet        []WriterTicketID       `json:"drain_set"`

	// Confirmed reports whether the drain has already been confirmed for this
	// exact seal.
	//
	// It is here rather than derived from the drain set because an empty drain
	// set is a real and different answer -- nobody was writing when admission
	// was fenced -- and a coordinator that read "no outstanding tickets" as
	// "confirmed" would skip the container boundary entirely. It is the node's
	// state, reported: `sealing` and `sealed` are two states and only the
	// second one may be read from.
	Confirmed bool `json:"confirmed"`
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
	WriterTicketID       WriterTicketID          `json:"writer_ticket_id"`
	Closed               CaptureAcknowledgement  `json:"closed"`
	PodUID               executioncontrol.PodUID `json:"pod_uid"`
	ContainersTerminated bool                    `json:"containers_terminated"`
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
//
// All three dispositions can record one. The capture branch's is the narrow
// case settled by the branch review's F7: a capture that terminally cancels or
// fails before the irreversible publish point has released nothing, and the
// source stays held until the daemon acknowledges this intent.
type ReleaseIntent struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Disposition     Disposition                      `json:"disposition"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	SourceHoldID    SourceHoldID                     `json:"source_hold_id"`
	ReleaseIntentID ReleaseIntentID                  `json:"release_intent_id"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
}

func (intent ReleaseIntent) Validate() error {
	if err := validateProtocol(intent.ProtocolVersion); err != nil {
		return err
	}
	if err := intent.Disposition.Validate(); err != nil {
		return err
	}
	if err := intent.Execution.Validate(); err != nil {
		return err
	}
	if intent.ActivationEpoch == 0 {
		return fmt.Errorf("%w: a release intent names no activation epoch", ErrIncomplete)
	}
	if err := intent.HandoffID.Validate(); err != nil {
		return err
	}
	if err := intent.SourceHoldID.Validate(); err != nil {
		return err
	}
	if err := intent.ReleaseIntentID.Validate(); err != nil {
		return err
	}
	if err := intent.Incarnation.Validate(); err != nil {
		return err
	}
	if intent.Incarnation.ExecutionID != intent.Execution.ExecutionID {
		return fmt.Errorf("%w: the incarnation to release belongs to a different execution",
			ErrInvalidIdentity)
	}

	return nil
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
	//
	// The pod is the Downward API's `metadata.uid`, presented by the capture
	// control init from inside the Pod. It is bound once and every later
	// operation on this hold presents the same one -- admission and reservation
	// both precede the Pod and therefore bind identity, fence and node only.
	AcknowledgeHold(ctx context.Context, admission CaptureAdmission, incarnation SourceIncarnation,
		pod executioncontrol.PodUID) (CaptureAcknowledgement, error)

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
// The bounds a challenge is issued under, mirroring
// hangar_receipt_stat_challenges' own CHECK constraints. They are restated here
// because the daemon that signs against a challenge never sees the schema, and
// a bound only the database knows is a bound the signer cannot enforce.
const (
	MinChallengeNonceBytes = 16
	MaxChallengeNonceBytes = 128
	MaxChallengeWindow     = 5 * time.Minute
)

type StatChallenge struct {
	Nonce           string
	HandoffID       HandoffID
	ReservationID   ReservationID
	ActivationEpoch executioncontrol.ActivationEpoch
	Ref             hangar.TreeRef
	CaptureFence    CaptureFence
	IssuedAt        Timestamp
	NotAfter        Timestamp
}

func (challenge StatChallenge) Validate() error {
	if challenge.Nonce == "" {
		return fmt.Errorf("%w: stat challenge carries no nonce", ErrIncomplete)
	}
	if len(challenge.Nonce) < MinChallengeNonceBytes || len(challenge.Nonce) > MaxChallengeNonceBytes {
		return fmt.Errorf("%w: the nonce is %d bytes and the schema issues between %d and %d",
			ErrLimitExceeded, len(challenge.Nonce), MinChallengeNonceBytes, MaxChallengeNonceBytes)
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
	if err := challenge.IssuedAt.Validate(); err != nil {
		return err
	}
	if err := challenge.NotAfter.Validate(); err != nil {
		return err
	}
	if !challenge.NotAfter.After(challenge.IssuedAt.Time) {
		return fmt.Errorf("%w: the challenge expires at %s and was issued at %s", ErrIncomplete,
			challenge.NotAfter.UTC(), challenge.IssuedAt.UTC())
	}
	if window := challenge.NotAfter.Sub(challenge.IssuedAt.Time); window > MaxChallengeWindow {
		return fmt.Errorf("%w: the challenge window is %s and the bound is %s. The window is the "+
			"interval in which a stale observation can still be presented as fresh, and it is the "+
			"schema's five minutes rather than the caller's choice", ErrLimitExceeded,
			window, MaxChallengeWindow)
	}

	return nil
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

	// Destination and WarrantNonce are what the warrant for this lease will bind.
	// They are on the REQUEST, and stored with the lease, because the warrant is
	// minted after the transaction commits and may have to be minted again: a
	// nonce chosen at mint time would make two mints of one lease differ.
	Destination  ReadDestination
	WarrantNonce string

	// StatProof is the exact-generation metadata stat, performed OUTSIDE the
	// locks and revalidated inside them. Requirement 35 admits a managed-output
	// warrant only after a stat proves the registered marked generation is
	// present; a lease created without one would be protection for content
	// nobody looked at.
	StatProof PublishedObject

	// StatObservedAt is when that stat was taken. It is separate from
	// RequestedAt because a caller may hold a request open while retrying, and
	// what has to be fresh is the OBSERVATION.
	StatObservedAt Timestamp
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
	if err := ValidateMaterializationTimeout(request.MaterializationTimeout); err != nil {
		return err
	}
	if err := request.Destination.Validate(); err != nil {
		return err
	}
	if err := validateReadWarrantNonce(request.WarrantNonce); err != nil {
		return err
	}
	// The marker is checked BEFORE the generic stat validation, because both
	// would refuse a wrong version and only one of them says what happened: an
	// unmarked or wrong-version object is unmanaged, which is a typed conflict
	// about ownership, not an incomplete request.
	if request.StatProof.Marker.Version != MarkerVersion {
		return fmt.Errorf("%w: the stat carries marker version %q, not %q; an unmarked or "+
			"wrong-version object is unmanaged and never a managed read", ErrConflict,
			request.StatProof.Marker.Version, MarkerVersion)
	}
	if err := request.StatProof.Validate(); err != nil {
		return fmt.Errorf("%w: a read lease is admitted on an exact-generation stat: %v",
			ErrIncomplete, err)
	}
	if request.StatProof.Attributes.Ref != request.Ref {
		return fmt.Errorf("%w: the stat proves %s/%s/%d and the lease is for %s/%s/%d",
			ErrConflict,
			request.StatProof.Attributes.Ref.Scope, request.StatProof.Attributes.Ref.Digest,
			request.StatProof.Attributes.Ref.Generation,
			request.Ref.Scope, request.Ref.Digest, request.Ref.Generation)
	}
	if err := request.StatObservedAt.Validate(); err != nil {
		return err
	}

	return request.RequestedAt.Validate()
}

// MaxStatProofAge is how stale the admitting stat may be.
//
// It is the challenge window, and deliberately the same five minutes: both
// answer the same question -- how long may an observation of the object store
// stand in for the object store -- and two different answers to it would be two
// different opinions about the same risk.
const MaxStatProofAge = MaxChallengeWindow

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
