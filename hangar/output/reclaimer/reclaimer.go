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

// Restrict narrows a delete client to the reclaimer's role.
//
// It takes objectstore.DeleteClient and NOT the full client, which is the point
// of the split: the full client no longer carries a delete at all, so a root
// that holds one cannot reach this call however it is narrowed afterwards.
func Restrict(client objectstore.DeleteClient) Store { return restricted{client: client} }

type restricted struct{ client objectstore.DeleteClient }

func (store restricted) Object(bucket, key string) Handle {
	return restrictedHandle{handle: store.client.ObjectToDelete(bucket, key)}
}

type restrictedHandle struct{ handle objectstore.DeleteHandle }

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

	// The generation and only the generation. The registered metageneration is
	// evidence about the object, not a condition on removing it: a benign
	// metadata change moves it without moving the generation, and a delete
	// conditioned on the recorded value 412s forever against an object nobody
	// has touched the bytes of. See DeletePrecondition.
	conditions := objectstore.Conditions{
		GenerationMatch: precondition.Generation,
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

	case errors.Is(err, objectstore.ErrBucketNotFound):
		// The BUCKET is gone, or was never this one. That is not absence of an
		// object and must never be reported as any kind of reclamation: a
		// controller pointed at the wrong bucket would otherwise finalize every
		// admitted job in a registered set as its own successful deletion while
		// every object was still there.
		return output.DeleteInfrastructure, fmt.Errorf("%w: deleting %s: the bucket does not "+
			"exist. This is a misconfiguration or a deleted bucket, and it is never absence of "+
			"an object: %v", output.ErrInfrastructure, key, err)

	case errors.Is(err, objectstore.ErrNotFound):
		// Absent. Whether this plane removed it is a question for the reclaim
		// job's own evidence: absence with a prior admitted delete whose
		// response was LOST is inferred reclamation, and absence with no such
		// attempt is an out-of-band lifetime violation. This method reports
		// what it saw and does not decide.
		//
		// It cannot decide, and the reason is measured rather than assumed: the
		// JSON API answers an object delete in a bucket that does not exist
		// with an ordinary object 404, so this arm is reached by "the object is
		// gone", "somebody else removed it" and "this process is pointed at the
		// wrong bucket" alike. Only the control plane's own record of what this
		// job previously attempted can tell them apart, which is why the
		// inference lives there and under the schema's evidence trigger rather
		// than here.
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

// Deferred: the delete pass's own answer already reports absence; a separate
// stat belongs to the ambiguous-response recovery path in Phase 8
//
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
	case errors.Is(err, objectstore.ErrBucketNotFound):
		return false, fmt.Errorf("%w: the bucket does not exist, which is not absence of an "+
			"object: %v", output.ErrInfrastructure, err)
	case errors.Is(err, objectstore.ErrNotFound):
		return true, nil
	case errors.Is(err, objectstore.ErrUnauthorized):
		return false, fmt.Errorf("%w: %v", output.ErrUnauthorized, err)
	default:
		return false, fmt.Errorf("%w: %v", output.ErrInfrastructure, err)
	}
}
