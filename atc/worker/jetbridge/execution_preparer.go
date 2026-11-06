package jetbridge

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// ExecutionPreparer is the owning domain's admission gate. The worker calls it
// before creating a container and again before an exact command can start.
type ExecutionPreparer interface {
	PrepareInputs(context.Context, db.ContainerOwner, string, []string, runtime.ContainerSpec) (runtime.ContainerSpec, error)
	PrepareContainer(context.Context, db.ContainerOwner, db.ContainerMetadata, runtime.ContainerSpec) (runtime.ContainerSpec, error)
	CheckStart(context.Context, db.ContainerOwner, runtime.ContainerSpec) error
	CheckIntercept(context.Context, string) error
	RecordWitness(context.Context, db.ContainerOwner, executioncontrol.Acknowledgement) error
	// RelocateContainer moves an execution whose pod was never bound onto a
	// node other than the one it was admitted on and the ones in exclude, and
	// returns the spec its replacement pod is built from.
	RelocateContainer(context.Context, db.ContainerOwner, runtime.ContainerSpec, []string) (runtime.ContainerSpec, error)
}

func (w *Worker) bindStartCheck(c *Container, owner db.ContainerOwner) {
	if w.executionPreparer == nil {
		return
	}
	// The spec is read when each callback runs, not when it is bound: a
	// relocation replaces the container's spec with one admitted elsewhere.
	c.checkStart = func(ctx context.Context) error { return w.executionPreparer.CheckStart(ctx, owner, c.containerSpec) }
	c.relocate = func(ctx context.Context, exclude []string) (runtime.ContainerSpec, error) {
		return w.executionPreparer.RelocateContainer(ctx, owner, c.containerSpec, exclude)
	}
	c.recordWitness = func(ctx context.Context, witness executioncontrol.Acknowledgement) error {
		return w.executionPreparer.RecordWitness(ctx, owner, witness)
	}
}
