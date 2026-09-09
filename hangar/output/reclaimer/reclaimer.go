// Package reclaimer is the only place in this system that deletes a published
// object.
//
// GCS IAM cannot require a caller to send a generation precondition once delete
// permission exists, so the requirement lives in the code: one method, taking an
// exact registered ref and an explicit precondition, with no key-only and no
// unconditional route to fall back to. The store interface here has no writer
// and no list, and its handle's Delete is reachable only through a generation
// pin and a precondition.
//
// The outcome vocabulary is the point of the package. `deleted` and
// `already_absent` are different facts, and so are `deleted` and "we asked and
// never heard back": a delete whose response was lost removed the object but
// cannot be reported as confirmed, because confirming it would let reclamation
// claim evidence it does not have. That distinction is Req 48's, and it is why
// this returns a typed outcome beside its error rather than only an error.
package reclaimer

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
)

// Store is the reclaimer's view: stat and conditional delete.
type Store interface {
	Object(bucket, key string) Handle
}

// Handle offers a generation pin, a precondition, a stat and a delete. There is
// no writer and no reader: a reclaimer that could read could exfiltrate, and a
// reclaimer that could write could resurrect.
type Handle interface {
	If(objectstore.Conditions) Handle
	Generation(int64) Handle
	Attrs(ctx context.Context) (objectstore.Attrs, error)
	Delete(ctx context.Context) error
}

// Restrict narrows a full client to the reclaimer's role.
func Restrict(client objectstore.Client) Store { return restricted{client: client} }

type restricted struct{ client objectstore.Client }

func (store restricted) Object(bucket, key string) Handle {
	return restrictedHandle{handle: store.client.Object(bucket, key)}
}

type restrictedHandle struct{ handle objectstore.Handle }

func (handle restrictedHandle) If(conditions objectstore.Conditions) Handle {
	return restrictedHandle{handle: handle.handle.If(conditions)}
}

func (handle restrictedHandle) Generation(generation int64) Handle {
	return restrictedHandle{handle: handle.handle.Generation(generation)}
}

func (handle restrictedHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	return handle.handle.Attrs(ctx)
}

func (handle restrictedHandle) Delete(ctx context.Context) error {
	return handle.handle.Delete(ctx)
}

// Reclaimer deletes exact generations in one namespace.
type Reclaimer struct {
	namespace output.OutputNamespace
	store     Store
}

// New builds a reclaimer for one namespace.
func New(namespace output.OutputNamespace, store Store) (*Reclaimer, error) {
	if namespace.IsZero() {
		return nil, fmt.Errorf("%w: the reclaimer needs a derived output namespace",
			output.ErrIncomplete)
	}
	if store == nil {
		return nil, fmt.Errorf("%w: the reclaimer needs an object store", output.ErrIncomplete)
	}

	return &Reclaimer{namespace: namespace, store: store}, nil
}

var _ output.Reclaimer = (*Reclaimer)(nil)

// DeleteExactGeneration removes one generation, conditionally.
//
// Every branch below returns an outcome, including the failures, because the
// caller's next move differs per outcome and an error alone cannot say which:
// `already_absent` finalizes a reclaim job, `generation_conflict` becomes debt
// and never broadens into an unconditional delete, and an infrastructure
// failure after the object is gone is `infrastructure_failure` rather than
// `deleted`, because "we did not hear back" is not proof.
func (reclaimer *Reclaimer) DeleteExactGeneration(ctx context.Context, ref hangar.TreeRef, precondition output.DeletePrecondition) (output.DeleteOutcome, error) {
	if err := ref.Validate(); err != nil {
		return output.DeleteInfrastructure, err
	}
	if err := precondition.Validate(); err != nil {
		return output.DeleteInfrastructure, err
	}
	if precondition.Generation != ref.Generation {
		return output.DeleteInfrastructure, fmt.Errorf("%w: the precondition names generation %d "+
			"and the ref names %d; a delete conditioned on a generation other than the one it is "+
			"about is an unconditional delete with extra steps", output.ErrIncomplete,
			precondition.Generation, ref.Generation)
	}

	key, err := hangar.TreeKey(reclaimer.namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil {
		return output.DeleteInfrastructure, err
	}

	conditions := objectstore.Conditions{
		GenerationMatch:     precondition.Generation,
		MetagenerationMatch: precondition.Metageneration,
	}
	if err := conditions.Validate(); err != nil {
		return output.DeleteInfrastructure, err
	}

	err = reclaimer.store.Object(reclaimer.namespace.Bucket(), key).
		Generation(ref.Generation).
		If(conditions).
		Delete(ctx)

	switch {
	case err == nil:
		return output.DeleteConfirmed, nil

	case errors.Is(err, objectstore.ErrNotFound):
		// Absent. Whether this plane removed it is a question for the reclaim
		// job's own evidence: absence with a prior admitted delete is inferred
		// reclamation, and absence without one is an out-of-band lifetime
		// violation. This method reports what it saw and does not decide.
		return output.DeleteAlreadyAbsent, nil

	case errors.Is(err, objectstore.ErrPreconditionFailed):
		return output.DeleteGenerationConflict, fmt.Errorf("%w: %s is not at generation %d "+
			"metageneration %d; the delete was refused and is never retried unconditionally",
			output.ErrGenerationConflict, key, precondition.Generation, precondition.Metageneration)

	case errors.Is(err, objectstore.ErrUnauthorized):
		return output.DeleteUnauthorized, fmt.Errorf("%w: deleting %s: %v",
			output.ErrUnauthorized, key, err)

	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return output.DeleteTimedOut, fmt.Errorf("%w: deleting %s: %v", output.ErrTimeout, key, err)

	default:
		return output.DeleteInfrastructure, fmt.Errorf("%w: deleting %s: %v",
			output.ErrInfrastructure, key, err)
	}
}

// ObserveExactAbsence is the stat half of inferred reclamation.
//
// It is a separate method because "the object is gone" and "we deleted it" are
// separate facts, and the reclaim evidence rule in the schema will not accept
// one for the other.
func (reclaimer *Reclaimer) ObserveExactAbsence(ctx context.Context, ref hangar.TreeRef) (bool, error) {
	if err := ref.Validate(); err != nil {
		return false, err
	}

	key, err := hangar.TreeKey(reclaimer.namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil {
		return false, err
	}

	_, err = reclaimer.store.Object(reclaimer.namespace.Bucket(), key).
		Generation(ref.Generation).
		Attrs(ctx)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, objectstore.ErrNotFound):
		return true, nil
	case errors.Is(err, objectstore.ErrUnauthorized):
		return false, fmt.Errorf("%w: %v", output.ErrUnauthorized, err)
	default:
		return false, fmt.Errorf("%w: %v", output.ErrInfrastructure, err)
	}
}
