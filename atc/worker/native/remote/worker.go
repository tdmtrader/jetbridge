package remote

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
)

var _ runtime.Worker = (*Worker)(nil)

// Worker implements runtime.Worker by proxying execution to a remote native
// agent via gRPC. DB lifecycle (containers, volumes) stays on the web side.
type Worker struct {
	dbWorker    db.Worker
	client      agentpb.NativeAgentClient
	volumeRepo  db.VolumeRepository
	compression compression.Compression
}

func NewWorker(dbWorker db.Worker, client agentpb.NativeAgentClient, volumeRepo db.VolumeRepository, enc compression.Compression) *Worker {
	return &Worker{
		dbWorker:    dbWorker,
		client:      client,
		volumeRepo:  volumeRepo,
		compression: enc,
	}
}

func (w *Worker) Name() string {
	return w.dbWorker.Name()
}

func (w *Worker) FindOrCreateContainer(
	ctx context.Context,
	owner db.ContainerOwner,
	metadata db.ContainerMetadata,
	containerSpec runtime.ContainerSpec,
	delegate runtime.BuildStepDelegate,
) (runtime.Container, []runtime.VolumeMount, error) {
	logger := lagerctx.FromContext(ctx).Session("remote-find-or-create-container", lager.Data{
		"worker": w.Name(),
	})

	// DB lifecycle: find or create container, same as native/worker.go.
	creatingContainer, createdContainer, err := w.dbWorker.FindContainer(owner)
	if err != nil {
		logger.Error("failed-to-find-container-in-db", err)
		return nil, nil, fmt.Errorf("find container in db: %w", err)
	}

	var containerHandle string

	if creatingContainer != nil {
		containerHandle = creatingContainer.Handle()
	} else if createdContainer != nil {
		containerHandle = createdContainer.Handle()
	} else {
		creatingContainer, err = w.dbWorker.CreateContainer(owner, metadata)
		if err != nil {
			logger.Error("failed-to-create-container-in-db", err)
			return nil, nil, fmt.Errorf("create container in db: %w", err)
		}
		containerHandle = creatingContainer.Handle()
	}

	if createdContainer == nil {
		createdContainer, err = creatingContainer.Created()
		if err != nil {
			logger.Error("failed-to-mark-container-as-created", err)
			if creatingContainer != nil {
				_, _ = creatingContainer.Failed()
			}
			return nil, nil, fmt.Errorf("mark container as created: %w", err)
		}
	}

	// Build volume mounts with logical paths only (no local directories).
	// The agent creates the actual filesystem during Exec.
	mounts, inputVolumes := w.buildVolumeMounts(containerHandle, containerSpec)

	container := newContainer(
		containerHandle,
		containerSpec,
		createdContainer,
		w.Name(),
		w.client,
		w.compression,
		inputVolumes,
	)

	return container, mounts, nil
}

// buildVolumeMounts creates runtime.VolumeMount entries with RemoteVolume
// stubs. No local directories are created — the agent side handles filesystem.
// Returns the full mount list and input volumes separately (indexed 1:1 with
// spec.Inputs for streamInputs).
func (w *Worker) buildVolumeMounts(handle string, spec runtime.ContainerSpec) ([]runtime.VolumeMount, []*Volume) {
	var mounts []runtime.VolumeMount
	var inputVolumes []*Volume

	addMount := func(vol *Volume, mountPath string) {
		mounts = append(mounts, runtime.VolumeMount{
			Volume:    vol,
			MountPath: mountPath,
		})
	}

	if spec.Dir != "" {
		addMount(NewVolume(handle+"-dir", w.Name(), nil), spec.Dir)
	}

	// Input volumes carry the gRPC client and container handle for StreamIn.
	for i, input := range spec.Inputs {
		vol := NewStreamableVolume(
			fmt.Sprintf("%s-input-%d", handle, i),
			w.Name(),
			w.client,
			handle,
		)
		inputVolumes = append(inputVolumes, vol)
		addMount(vol, input.DestinationPath)
	}

	// Output volumes carry the gRPC client and container handle for StreamOut.
	for name, path := range spec.Outputs {
		addMount(NewStreamableVolume(fmt.Sprintf("%s-output-%s", handle, name), w.Name(), w.client, handle), path)
	}

	for i, cachePath := range spec.Caches {
		addMount(NewVolume(fmt.Sprintf("%s-cache-%d", handle, i), w.Name(), nil), cachePath)
	}

	for i, scratchPath := range spec.ScratchPaths {
		addMount(NewVolume(fmt.Sprintf("%s-scratch-%d", handle, i), w.Name(), nil), scratchPath)
	}

	return mounts, inputVolumes
}

func (w *Worker) CreateVolumeForArtifact(ctx context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	if w.volumeRepo == nil {
		return nil, nil, fmt.Errorf("create artifact volume: volume repository not configured")
	}

	logger := lagerctx.FromContext(ctx).Session("remote-create-volume-for-artifact", lager.Data{
		"worker": w.Name(),
		"team":   teamID,
	})

	creatingVolume, err := w.volumeRepo.CreateVolume(teamID, w.Name(), db.VolumeTypeArtifact)
	if err != nil {
		logger.Error("failed-to-create-volume", err)
		return nil, nil, fmt.Errorf("create artifact volume: %w", err)
	}

	createdVolume, err := creatingVolume.Created()
	if err != nil {
		logger.Error("failed-to-transition-volume", err)
		return nil, nil, fmt.Errorf("transition artifact volume to created: %w", err)
	}

	artifact, err := createdVolume.InitializeArtifact("", 0)
	if err != nil {
		logger.Error("failed-to-initialize-artifact", err)
		return nil, nil, fmt.Errorf("initialize artifact: %w", err)
	}

	return NewVolume(createdVolume.Handle(), w.Name(), createdVolume), artifact, nil
}

func (w *Worker) LookupContainer(ctx context.Context, handle string) (runtime.Container, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("remote-lookup-container", lager.Data{
		"handle": handle,
		"worker": w.Name(),
	})

	_, dbContainer, err := w.dbWorker.FindContainer(db.NewFixedHandleContainerOwner(handle))
	if err != nil {
		logger.Error("failed-to-lookup-container-in-db", err)
		return nil, false, fmt.Errorf("lookup db container %q: %w", handle, err)
	}
	if dbContainer == nil {
		return nil, false, nil
	}

	return newContainer(handle, runtime.ContainerSpec{}, dbContainer, w.Name(), w.client, w.compression, nil), true, nil
}

func (w *Worker) LookupVolume(ctx context.Context, handle string) (runtime.Volume, bool, error) {
	if w.volumeRepo == nil {
		return nil, false, nil
	}

	logger := lagerctx.FromContext(ctx).Session("remote-lookup-volume", lager.Data{
		"handle": handle,
		"worker": w.Name(),
	})

	dbVolume, found, err := w.volumeRepo.FindVolume(handle)
	if err != nil {
		logger.Error("failed-to-lookup-volume-in-db", err)
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	return NewVolume(handle, w.Name(), dbVolume), true, nil
}
