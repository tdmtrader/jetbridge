package jetbridge

import (
	"context"
	"fmt"
	"path/filepath"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"k8s.io/client-go/kubernetes"
)

// Compile-time check that Worker satisfies runtime.Worker.
var _ runtime.Worker = (*Worker)(nil)

// Worker implements runtime.Worker using Kubernetes Pods as the execution
// backend instead of Garden containers.
type Worker struct {
	dbWorker       db.Worker
	clientset      kubernetes.Interface
	config         Config
	executor       PodExecutor
	volumeRepo     db.VolumeRepository
	storageBackend StorageBackend
	nodeIPResolver *NodeIPResolver
}

// NewWorker creates a new Worker backed by the given Kubernetes clientset.
func NewWorker(dbWorker db.Worker, clientset kubernetes.Interface, config Config) *Worker {
	nodeIPResolver := NewNodeIPResolver(clientset)

	var backend StorageBackend
	if config.ArtifactDaemonHostPath != "" {
		backend = NewDaemonSetBackend(config, NewArtifactLocator(), nodeIPResolver)
	}

	return &Worker{
		dbWorker:       dbWorker,
		clientset:      clientset,
		config:         config,
		storageBackend: backend,
		nodeIPResolver: nodeIPResolver,
	}
}

// SetExecutor sets the PodExecutor used for exec-mode I/O in containers.
// When set, containers that receive ProcessIO with Stdin will use the
// exec API to pipe stdin/stdout/stderr instead of baking the command
// into the Pod spec.
func (w *Worker) SetExecutor(executor PodExecutor) {
	w.executor = executor
}

// SetVolumeRepo sets the VolumeRepository used by LookupVolume to find
// cache-backed volumes in the database. This is set by the factory when
// cache PVC is configured.
func (w *Worker) SetVolumeRepo(repo db.VolumeRepository) {
	w.volumeRepo = repo
}

// SetArtifactLocator sets the ArtifactLocator used for tracking artifact
// locations in DaemonSet mode. It creates a DaemonSetBackend wrapping the
// given locator and sets it as the storage backend.
func (w *Worker) SetArtifactLocator(locator *ArtifactLocator) {
	w.storageBackend = NewDaemonSetBackend(w.config, locator, w.nodeIPResolver)
}

// SetStorageBackend sets the storage backend directly.
func (w *Worker) SetStorageBackend(backend StorageBackend) {
	w.storageBackend = backend
}

func (w *Worker) Name() string {
	return w.dbWorker.Name()
}

// SkipResourceCache returns false, enabling resource cache hits in DaemonSet
// mode. Cache hits skip the get step entirely and serve cached data via the
// artifact-daemon. The "destination path already exists" bug is fixed by the
// cleanup-stale init container (added in buildCleanupInitContainer), and
// volume handle → disk path mapping is handled by daemon alias registration
// (added in registerDaemonAlias).
func (w *Worker) SkipResourceCache() bool {
	return false
}

func (w *Worker) FindOrCreateContainer(
	ctx context.Context,
	owner db.ContainerOwner,
	metadata db.ContainerMetadata,
	containerSpec runtime.ContainerSpec,
	delegate runtime.BuildStepDelegate,
) (runtime.Container, []runtime.VolumeMount, error) {
	logger := lagerctx.FromContext(ctx).Session("find-or-create-container", lager.Data{
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

	// If we already have a created container in the DB, return it directly.
	// The Pod may or may not exist yet (it gets created in Container.Run).
	// Mark it as reused so Run() can clean up stale hostPath data.
	if createdContainer != nil {
		mounts, volumes := w.buildVolumeMountsForSpec(containerHandle, containerSpec)
		container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes, w.storageBackend, true)
		return container, mounts, nil
	}

	// Transition the creating container to created state in the DB.
	// Pod creation is deferred to Container.Run() since the command isn't
	// known until then.
	createdContainer, err = creatingContainer.Created()
	if err != nil {
		logger.Error("failed-to-mark-container-as-created", err)
		markContainerAsFailed(logger, creatingContainer)
		return nil, nil, fmt.Errorf("mark container as created: %w", err)
	}

	mounts, volumes := w.buildVolumeMountsForSpec(containerHandle, containerSpec)
	container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes, w.storageBackend, false)
	return container, mounts, nil
}

// buildVolumeMountsForSpec creates runtime.VolumeMount entries for the
// container's Dir, inputs, outputs, and caches. When the worker has an
// executor configured, volumes are created as deferred volumes that support
// StreamIn/StreamOut once the pod name is set. Otherwise, stub volumes are
// used as placeholders for resource cache tracking.
func (w *Worker) buildVolumeMountsForSpec(handle string, spec runtime.ContainerSpec) ([]runtime.VolumeMount, []*Volume) {
	var mounts []runtime.VolumeMount
	var volumes []*Volume

	addMount := func(vol *Volume, mountPath string) {
		volumes = append(volumes, vol)
		mounts = append(mounts, runtime.VolumeMount{
			Volume:    vol,
			MountPath: mountPath,
		})
	}

	if spec.Dir != "" {
		addMount(w.newVolumeForMount(handle+"-dir", spec.Dir), spec.Dir)
	}

	// Track input mount paths so overlapping outputs reuse the same volume.
	// This must match the dedup logic in Container.buildVolumeMounts() — both
	// use filepath.Clean to normalize trailing slashes on output paths.
	inputMountPaths := make(map[string]bool, len(spec.Inputs))
	for i, input := range spec.Inputs {
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-input-%d", handle, i), input.DestinationPath), input.DestinationPath)
		inputMountPaths[filepath.Clean(input.DestinationPath)] = true
	}

	for name, path := range spec.Outputs {
		// Skip output volumes when an input already covers the same path.
		// The input volume is the one actually mounted in the K8s pod
		// (buildVolumeMounts skips the duplicate output), so both
		// registerOutputs (task_step.go) and recordOutputLocations
		// (process.go) must agree on using the same volume handle.
		if inputMountPaths[filepath.Clean(path)] {
			continue
		}
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-output-%s", handle, name), path), path)
	}

	for i, cachePath := range spec.Caches {
		resolvedPath := cachePath
		if !filepath.IsAbs(cachePath) && spec.Dir != "" {
			resolvedPath = filepath.Join(spec.Dir, cachePath)
		}
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-cache-%d", handle, i), resolvedPath), resolvedPath)
	}

	return mounts, volumes
}

// newVolumeForMount creates a Volume for the given handle and mount path.
// If the worker has an executor, it creates a deferred volume that will
// support StreamIn/StreamOut once the pod name is set. Otherwise it creates
// a stub volume for placeholder use.
func (w *Worker) newVolumeForMount(handle, mountPath string) *Volume {
	if w.executor != nil {
		return NewDeferredVolume(handle, w.Name(), w.executor, w.config.Namespace, mainContainerName, mountPath)
	}
	return NewStubVolume(handle, w.Name(), mountPath)
}

func (w *Worker) CreateVolumeForArtifact(ctx context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	if w.volumeRepo == nil {
		return nil, nil, fmt.Errorf("create artifact volume: volume repository not configured")
	}

	logger := lagerctx.FromContext(ctx).Session("create-volume-for-artifact", lager.Data{
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

	handle := createdVolume.Handle()
	key := ArtifactKey(handle)
	if w.storageBackend != nil {
		return w.storageBackend.WrapVolumeForArtifact(key, handle, w.Name(), createdVolume), artifact, nil
	}
	return NewDaemonSetVolume(key, handle, w.Name(), createdVolume, "", w.config, w.nodeIPResolver), artifact, nil
}

func (w *Worker) LookupContainer(ctx context.Context, handle string) (runtime.Container, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("lookup-container", lager.Data{
		"handle": handle,
		"worker": w.Name(),
	})

	// Look up the DB container. K8s pods are created lazily in Run(),
	// so we don't require the pod to exist at lookup time. This allows
	// fly intercept to find containers before or after their pods run.
	_, dbContainer, err := w.dbWorker.FindContainer(db.NewFixedHandleContainerOwner(handle))
	if err != nil {
		logger.Error("failed-to-lookup-container-in-db", err)
		return nil, false, fmt.Errorf("lookup db container %q: %w", handle, err)
	}
	if dbContainer == nil {
		return nil, false, nil
	}

	return newContainer(handle, db.ContainerMetadata{}, runtime.ContainerSpec{}, dbContainer, w.clientset, w.config, w.Name(), w.executor, nil, w.storageBackend, false), true, nil
}

func (w *Worker) LookupVolume(ctx context.Context, handle string) (runtime.Volume, bool, error) {
	if w.volumeRepo == nil {
		return nil, false, nil
	}

	logger := lagerctx.FromContext(ctx).Session("lookup-volume", lager.Data{
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

	key := ArtifactKey(handle)
	if w.storageBackend != nil {
		return w.storageBackend.WrapVolumeForLookup(key, handle, w.Name(), dbVolume), true, nil
	}
	return NewDaemonSetVolume(key, handle, w.Name(), dbVolume, "", w.config, w.nodeIPResolver), true, nil
}

func markContainerAsFailed(logger lager.Logger, container db.CreatingContainer) {
	if container != nil {
		_, err := container.Failed()
		if err != nil {
			logger.Error("failed-to-mark-container-as-failed", err)
		}
	}
}
