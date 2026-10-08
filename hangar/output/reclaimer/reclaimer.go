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
// The outcome is the point of the package. `deleted` and `already_absent` are
// different facts from "we asked and never heard back": a delete whose response
// was lost may have removed the object and cannot be reported as either, so
// the caller retries it next pass and gets `already_absent`. That is why this
// returns a typed outcome beside its error rather than only an error.
package reclaimer

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
)

// Outcome is what one conditional delete settled, for the caller's switch.
// Nothing stores it.
type Outcome string

const (
	// Deleted is an acknowledged conditional delete.
	Deleted Outcome = "deleted"
	// AlreadyAbsent is an object that was not there: a delete whose answer
	// was lost last time, or an object gone out of band. The exact generation
	// is gone either way.
	AlreadyAbsent Outcome = "already_absent"
	// GenerationConflict is a different generation at the key: the exact one
	// is gone, and the object there is someone else's. Never broadened into
	// an unconditional delete.
	GenerationConflict Outcome = "generation_conflict"
	// Unauthorized is the store refusing the web's delete credential.
	Unauthorized Outcome = "unauthorized"
	// Failed is a timeout, a missing bucket or an infrastructure failure: no
	// answer, so nothing is settled and the delete is retried next pass.
	Failed Outcome = "failed"
)

// Store is the reclaimer's view of object operations.
type Store interface {
	StatExact(context.Context, string, string, int64) (objectstore.Attrs, error)
	DeleteExact(context.Context, string, string, int64) error
}

func Restrict(client objectstore.DeleteClient) Store { return client }

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
// a settled outcome stamps the generation reclaimed, Unauthorized records a
// finding, and Failed is retried next pass, because "we did not hear back" is
// not proof.
func (reclaimer *Reclaimer) DeleteExactGeneration(ctx context.Context, ref hangar.TreeRef, precondition output.DeletePrecondition) (Outcome, error) {
	if err := ref.Validate(); err != nil {
		return Failed, err
	}
	if err := precondition.Validate(); err != nil {
		return Failed, err
	}
	if precondition.Generation != ref.Generation {
		return Failed, fmt.Errorf("%w: the precondition names generation %d "+
			"and the ref names %d; a delete conditioned on a generation other than the one it is "+
			"about is an unconditional delete with extra steps", output.ErrIncomplete,
			precondition.Generation, ref.Generation)
	}

	key, err := hangar.TreeKey(reclaimer.namespace.Prefix(), ref.Scope, ref.Digest)
	if err != nil {
		return Failed, err
	}

	// The generation and only the generation; the recorded metageneration is
	// evidence about the object, not a condition on removing it.
	err = reclaimer.store.DeleteExact(ctx, reclaimer.namespace.Bucket(), key, ref.Generation)

	switch {
	case err == nil:
		return Deleted, nil

	case errors.Is(err, objectstore.ErrBucketNotFound):
		// The BUCKET is gone, or was never this one. That is not absence of an
		// object and must never be reported as any kind of reclamation: a
		// controller pointed at the wrong bucket would otherwise finalize every
		// admitted job in a registered set as its own successful deletion while
		// every object was still there.
		return Failed, fmt.Errorf("%w: deleting %s: the bucket does not "+
			"exist. This is a misconfiguration or a deleted bucket, and it is never absence of "+
			"an object: %v", output.ErrInfrastructure, key, err)

	case errors.Is(err, objectstore.ErrNotFound):
		// Absent. A delete whose answer was lost last pass, or an object gone
		// out of band: the store's 404 is the same for both (measured, not
		// assumed: the JSON API answers an object delete in a bucket that does
		// not exist with an ordinary object 404 too, which is why the bucket
		// case above is checked first). The exact generation is gone either
		// way, and the caller stamps it reclaimed.
		return AlreadyAbsent, nil

	case errors.Is(err, objectstore.ErrPreconditionFailed):
		return GenerationConflict, fmt.Errorf("%w: %s is not at generation %d; "+
			"the delete was refused and is never retried unconditionally",
			output.ErrGenerationConflict, key, precondition.Generation)

	case errors.Is(err, objectstore.ErrUnauthorized):
		return Unauthorized, fmt.Errorf("%w: deleting %s: %v",
			output.ErrUnauthorized, key, err)

	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return Failed, fmt.Errorf("%w: deleting %s: %v", output.ErrTimeout, key, err)

	default:
		return Failed, fmt.Errorf("%w: deleting %s: %v",
			output.ErrInfrastructure, key, err)
	}
}
