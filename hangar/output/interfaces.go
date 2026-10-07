package output

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
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

// PrincipalRole names the cloud identity a runtime denial was recorded
// against. The node daemon publishes; the web reclaims.
type PrincipalRole string

const (
	PrincipalPublisher PrincipalRole = "publisher"
	PrincipalReclaimer PrincipalRole = "reclaimer"
)

// Validate refuses a role outside the closed set: a denial recorded against an
// invented role name is a finding an operator cannot map to a service account.
func (role PrincipalRole) Validate() error {
	if slices.Contains(PrincipalRoles(), role) {
		return nil
	}

	return fmt.Errorf("%w: %q is not one of this plane's principals %v",
		ErrUnknownMember, role, PrincipalRoles())
}

func PrincipalRoles() []PrincipalRole {
	return []PrincipalRole{PrincipalPublisher, PrincipalReclaimer}
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

// MaxStatProofAge is how stale the stat admitting a read lease may be: how
// long an observation of the object store may stand in for the object store.
const MaxStatProofAge = 5 * time.Minute
