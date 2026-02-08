package k8sruntime

import (
	"context"
	"fmt"

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
	dbWorker   db.Worker
	clientset  kubernetes.Interface
	config     Config
	executor   PodExecutor
	volumeRepo db.VolumeRepository
}

// NewWorker creates a new Worker backed by the given Kubernetes clientset.
func NewWorker(dbWorker db.Worker, clientset kubernetes.Interface, config Config) *Worker {
	return &Worker{
		dbWorker:  dbWorker,
		clientset: clientset,
		config:    config,
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

func (w *Worker) Name() string {
	return w.dbWorker.Name()
}

func (w *Worker) DBWorker() db.Worker {
	return w.dbWorker
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
	if createdContainer != nil {
		mounts, volumes := w.buildVolumeMountsForSpec(containerHandle, containerSpec)
		container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes)
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
	container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes)
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

	for i, input := range spec.Inputs {
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-input-%d", handle, i), input.DestinationPath), input.DestinationPath)
	}

	for name, path := range spec.Outputs {
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-output-%s", handle, name), path), path)
	}

	for i, cachePath := range spec.Caches {
		addMount(w.newVolumeForMount(fmt.Sprintf("%s-cache-%d", handle, i), cachePath), cachePath)
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
	return nil, nil, fmt.Errorf("k8sruntime: CreateVolumeForArtifact not yet implemented")
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

	return newContainer(handle, db.ContainerMetadata{}, runtime.ContainerSpec{}, dbContainer, w.clientset, w.config, w.Name(), w.executor, nil), true, nil
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

	// When the artifact store is configured, return an ArtifactStoreVolume
	// so that downstream steps use init containers instead of SPDY streaming.
	if w.config.ArtifactStoreClaim != "" {
		key := ArtifactKey(handle)
		return NewArtifactStoreVolume(key, handle, w.Name(), dbVolume), true, nil
	}

	vol := NewCacheVolume(dbVolume, handle, w.Name(), w.executor, w.config.Namespace, mainContainerName)
	return vol, true, nil
}

func markContainerAsFailed(logger lager.Logger, container db.CreatingContainer) {
	if container != nil {
		_, err := container.Failed()
		if err != nil {
			logger.Error("failed-to-mark-container-as-failed", err)
		}
	}
}
