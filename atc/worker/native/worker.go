package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that Worker satisfies runtime.Worker.
var _ runtime.Worker = (*Worker)(nil)

// Worker implements runtime.Worker by executing task steps as local OS
// processes instead of Kubernetes pods.
type Worker struct {
	dbWorker    db.Worker
	config      Config
	volumeRepo  db.VolumeRepository
	compression compression.Compression
}

// NewWorker creates a native Worker backed by the given DB worker record and
// configuration.
func NewWorker(dbWorker db.Worker, config Config, volumeRepo db.VolumeRepository, enc compression.Compression) *Worker {
	return &Worker{
		dbWorker:    dbWorker,
		config:      config,
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
	logger := lagerctx.FromContext(ctx).Session("native-find-or-create-container", lager.Data{
		"worker": w.Name(),
	})

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

	// Create the local scratch directory for this container.
	containerDir := filepath.Join(w.config.WorkDir, "containers", containerHandle)
	if err := os.MkdirAll(containerDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create container dir: %w", err)
	}

	mounts, inputVolumes := w.buildVolumeMountsForSpec(containerHandle, containerDir, containerSpec)

	container := newContainer(
		containerHandle,
		containerDir,
		containerSpec,
		createdContainer,
		w.Name(),
		w.compression,
		inputVolumes,
	)

	return container, mounts, nil
}

// buildVolumeMountsForSpec creates runtime.VolumeMount entries for the
// container's Dir, Inputs, Outputs, Caches, and ScratchPaths. Each volume is
// backed by a local directory. Caches use the durable CacheDir to survive
// container cleanup. Returns the full mount list and the input volumes
// separately (indexed 1:1 with spec.Inputs for streamInputs).
func (w *Worker) buildVolumeMountsForSpec(handle, containerDir string, spec runtime.ContainerSpec) ([]runtime.VolumeMount, []*Volume) {
	var mounts []runtime.VolumeMount
	var inputVolumes []*Volume

	addMount := func(vol *Volume, mountPath string) {
		mounts = append(mounts, runtime.VolumeMount{
			Volume:    vol,
			MountPath: mountPath,
		})
	}

	// Working directory volume.
	if spec.Dir != "" {
		dir := filepath.Join(containerDir, "work")
		os.MkdirAll(dir, 0755)
		addMount(NewVolume(handle+"-dir", w.Name(), dir, nil), spec.Dir)
	}

	// Input volumes — each input gets its own subdirectory.
	// Tracked separately for streamInputs indexing.
	for i, input := range spec.Inputs {
		dir := filepath.Join(containerDir, fmt.Sprintf("input-%d", i))
		os.MkdirAll(dir, 0755)
		vol := NewVolume(fmt.Sprintf("%s-input-%d", handle, i), w.Name(), dir, nil)
		inputVolumes = append(inputVolumes, vol)
		addMount(vol, input.DestinationPath)
	}

	// Output volumes.
	for name, path := range spec.Outputs {
		dir := filepath.Join(containerDir, "output-"+name)
		os.MkdirAll(dir, 0755)
		addMount(NewVolume(fmt.Sprintf("%s-output-%s", handle, name), w.Name(), dir, nil), path)
	}

	// Cache volumes — stored in CacheDir for durability across builds.
	for i, cachePath := range spec.Caches {
		resolvedPath := cachePath
		if !filepath.IsAbs(cachePath) && spec.Dir != "" {
			resolvedPath = filepath.Join(spec.Dir, cachePath)
		}
		cacheKey := fmt.Sprintf("%d-%s-%s", spec.JobID, spec.StepName, cachePath)
		dir := filepath.Join(w.config.CacheDir, cacheKey)
		os.MkdirAll(dir, 0755)
		addMount(NewVolume(fmt.Sprintf("%s-cache-%d", handle, i), w.Name(), dir, nil), resolvedPath)
	}

	// Scratch path volumes — ephemeral, cleaned up with container.
	for i, scratchPath := range spec.ScratchPaths {
		resolvedPath := scratchPath
		if !filepath.IsAbs(scratchPath) && spec.Dir != "" {
			resolvedPath = filepath.Join(spec.Dir, scratchPath)
		}
		dir := filepath.Join(containerDir, fmt.Sprintf("scratch-%d", i))
		os.MkdirAll(dir, 0755)
		addMount(NewVolume(fmt.Sprintf("%s-scratch-%d", handle, i), w.Name(), dir, nil), resolvedPath)
	}

	return mounts, inputVolumes
}

func (w *Worker) CreateVolumeForArtifact(ctx context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	if w.volumeRepo == nil {
		return nil, nil, fmt.Errorf("create artifact volume: volume repository not configured")
	}

	logger := lagerctx.FromContext(ctx).Session("native-create-volume-for-artifact", lager.Data{
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

	// Back the artifact volume with a local directory.
	volDir := filepath.Join(w.config.WorkDir, "artifacts", createdVolume.Handle())
	if err := os.MkdirAll(volDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create artifact dir: %w", err)
	}

	return NewVolume(createdVolume.Handle(), w.Name(), volDir, createdVolume), artifact, nil
}

func (w *Worker) LookupContainer(ctx context.Context, handle string) (runtime.Container, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("native-lookup-container", lager.Data{
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

	containerDir := filepath.Join(w.config.WorkDir, "containers", handle)
	return newContainer(handle, containerDir, runtime.ContainerSpec{}, dbContainer, w.Name(), w.compression, nil), true, nil
}

func (w *Worker) LookupVolume(ctx context.Context, handle string) (runtime.Volume, bool, error) {
	if w.volumeRepo == nil {
		return nil, false, nil
	}

	logger := lagerctx.FromContext(ctx).Session("native-lookup-volume", lager.Data{
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

	volDir := filepath.Join(w.config.WorkDir, "artifacts", handle)
	return NewVolume(handle, w.Name(), volDir, dbVolume), true, nil
}
