package exec

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
)

type ArtifactNotFoundError struct {
	ArtifactName string
}

func (e ArtifactNotFoundError) Error() string {
	return fmt.Sprintf("artifact '%s' not found", e.ArtifactName)
}

type ArtifactNotVolumeError struct {
	ArtifactName string
}

func (e ArtifactNotVolumeError) Error() string {
	return fmt.Sprintf("artifact '%s' is not a volume", e.ArtifactName)
}

// ArtifactNotPersistableError is an output whose volume has no volume row and
// cannot be given one, so nothing outside the build can download it.
type ArtifactNotPersistableError struct {
	ArtifactName string
}

func (e ArtifactNotPersistableError) Error() string {
	return fmt.Sprintf("artifact '%s' has no volume record and cannot be given one, so it cannot be downloaded", e.ArtifactName)
}

type ArtifactOutputStep struct {
	plan       atc.Plan
	build      db.Build
	workerPool Pool
}

func NewArtifactOutputStep(plan atc.Plan, build db.Build, workerPool Pool) Step {
	return &ArtifactOutputStep{
		plan:       plan,
		build:      build,
		workerPool: workerPool,
	}
}

func (step *ArtifactOutputStep) Run(ctx context.Context, state RunState) (bool, error) {
	logger := lagerctx.FromContext(ctx).WithData(lager.Data{
		"plan-id": step.plan.ID,
	})

	outputName := step.plan.ArtifactOutput.Name

	artifact, _, found := state.ArtifactRepository().ArtifactFor(build.ArtifactName(outputName))
	if !found {
		return false, ArtifactNotFoundError{outputName}
	}

	volume, ok := artifact.(runtime.Volume)
	if !ok {
		return false, ArtifactNotVolumeError{outputName}
	}

	dbVolume := volume.DBVolume()
	if dbVolume == nil {
		// A step output the runtime holds without a volume row (JetBridge's
		// artifact daemon). The download finds the artifact by its row, so it
		// gets one now. A volume that cannot have one is an error that names
		// the output, never a nil dereference or a silently empty download.
		persistable, ok := volume.(runtime.PersistableVolume)
		if !ok {
			return false, ArtifactNotPersistableError{outputName}
		}
		var err error
		dbVolume, err = persistable.PersistVolume(step.build.TeamID())
		if err != nil {
			return false, fmt.Errorf("recording output '%s' for download: %w", outputName, err)
		}
	}

	dbWorkerArtifact, err := dbVolume.InitializeArtifact(outputName, step.build.ID())
	if err != nil {
		return false, err
	}

	logger.Info("initialize-artifact-from-source", lager.Data{
		"handle":      volume.Handle(),
		"artifact_id": dbWorkerArtifact.ID(),
	})

	return true, nil
}
