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
}

func (w *Worker) SetExecutionPreparer(preparer ExecutionPreparer) { w.executionPreparer = preparer }

func (w *Worker) bindStartCheck(c *Container, owner db.ContainerOwner, spec runtime.ContainerSpec) {
	if w.executionPreparer != nil {
		c.checkStart = func(ctx context.Context) error { return w.executionPreparer.CheckStart(ctx, owner, spec) }
		c.recordWitness = func(ctx context.Context, witness executioncontrol.Acknowledgement) error {
			return w.executionPreparer.RecordWitness(ctx, owner, witness)
		}
	}
}
