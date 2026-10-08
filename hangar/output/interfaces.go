package output

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/concourse/concourse/hangar"
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
