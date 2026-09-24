package jetbridge

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
	dbWorker       db.Worker
	clientset      kubernetes.Interface
	config         Config
	executor       PodExecutor
	volumeRepo     db.VolumeRepository
	storageBackend StorageBackend
	nodeIPResolver *NodeIPResolver

	// outputControls is set when the output plane is configured. Nil is the
	// ordinary path: a worker with no output plane hands every container a
	// nil resolver, and nothing in the exact-execution path is reachable
	// without an ExecutionControl on the spec anyway.
	outputControls    OutputControlResolver
	executionPreparer ExecutionPreparer
}

// WorkerDeps is everything a Worker reaches beyond its row, its clientset and
// its config. The Worker takes all of it at construction and nothing replaces
// it afterwards, so a built Worker is a complete one.
//
// Every field may be nil, and each nil has one meaning:
//
//   - Executor: no exec-mode I/O; a container bakes its command into the Pod.
//   - VolumeRepo: LookupVolume finds no cache-backed volumes.
//   - ArtifactLocator: with no ArtifactDaemonHostPath either, the worker has
//     no storage backend. With a host path, a private locator is made.
//   - DaemonClient: the storage backend cannot probe, warm or alias through
//     the artifact daemons.
//   - OutputControls: no output plane; no exact-execution call is reachable.
//   - ExecutionPreparer: no admission gate before a container or its command.
//
// These were six setters. Two of them replaced each other -- setting the
// locator rebuilt the storage backend and silently dropped a daemon client
// set before it -- and one, the output-control resolver, was called by no
// production line at all, so the output plane was unreachable on every
// deployment. A field left out of this struct is visible where the struct is
// built; a setter left uncalled was visible nowhere.
type WorkerDeps struct {
	Executor          PodExecutor
	VolumeRepo        db.VolumeRepository
	ArtifactLocator   *ArtifactLocator
	DaemonClient      *DaemonClient
	OutputControls    OutputControlResolver
	ExecutionPreparer ExecutionPreparer
}

// NewWorker creates a Worker backed by the given Kubernetes clientset, with
// every collaborator it will ever use.
//
// The storage backend exists when there is somewhere to keep artifacts: a
// configured host path, or a locator the caller shares (production always
// hands one in, so every production worker has a backend). The backend is
// built once, with the daemon client, so the two cannot be applied in an
// order that loses one.
func NewWorker(dbWorker db.Worker, clientset kubernetes.Interface, config Config, deps WorkerDeps) *Worker {
	nodeIPResolver := NewNodeIPResolver(clientset)

	var backend StorageBackend
	if config.ArtifactDaemonHostPath != "" || deps.ArtifactLocator != nil {
		locator := deps.ArtifactLocator
		if locator == nil {
			locator = NewArtifactLocator()
		}
		backend = NewDaemonSetBackend(config, locator, nodeIPResolver, deps.DaemonClient)
	}

	return &Worker{
		dbWorker:          dbWorker,
		clientset:         clientset,
		config:            config,
		executor:          deps.Executor,
		volumeRepo:        deps.VolumeRepo,
		storageBackend:    backend,
		nodeIPResolver:    nodeIPResolver,
		outputControls:    deps.OutputControls,
		executionPreparer: deps.ExecutionPreparer,
	}
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
	if w.executionPreparer != nil {
		var err error
		containerSpec, err = w.executionPreparer.PrepareContainer(ctx, owner, metadata, containerSpec)
		if err != nil {
			return nil, nil, err
		}
	}
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

	if w.executionPreparer != nil {
		volumeNames := make([]string, len(containerSpec.Inputs))
		for i := range volumeNames {
			volumeNames[i] = inputVolumeName(containerSpec, i)
		}
		containerSpec, err = w.executionPreparer.PrepareInputs(ctx, owner, containerHandle, volumeNames, containerSpec)
		if err != nil {
			return nil, nil, err
		}
	}

	// If we already have a created container in the DB, return it directly.
	// The Pod may or may not exist yet (it gets created in Container.Run).
	// Mark it as reused so Run() can clean up stale hostPath data.
	if createdContainer != nil {
		mounts, volumes := w.buildVolumeMountsForSpec(containerHandle, containerSpec)
		container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes, w.storageBackend, true, false)
		container.outputControls = w.outputControls
		w.bindStartCheck(container, owner, containerSpec)
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
	container := newContainer(containerHandle, metadata, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor, volumes, w.storageBackend, false, false)
	container.outputControls = w.outputControls
	w.bindStartCheck(container, owner, containerSpec)
	return container, mounts, nil
}

// buildVolumeMountsForSpec creates runtime.VolumeMount entries for the
// container's Dir, inputs, outputs, and caches. The layout is pure
// (volume_mounts.go); the worker only supplies its identity and executor.
func (w *Worker) buildVolumeMountsForSpec(handle string, spec runtime.ContainerSpec) ([]runtime.VolumeMount, []*Volume) {
	return buildVolumeMounts(w.Name(), w.config.Namespace, w.executor, handle, spec)
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

	// The DB row carries the metadata the step was created with, and
	// GeneratePodName is a pure function of that metadata plus the handle.
	// Passing it through is what makes the looked-up Container point at the
	// pod the step actually created (<pipeline>-<job>-b<n>-<type>-<suffix>)
	// rather than the raw handle, which is only ever a real pod name when the
	// metadata was too sparse to generate a readable one.
	container := newContainer(handle, dbContainer.Metadata(), runtime.ContainerSpec{}, dbContainer, w.clientset, w.config, w.Name(), w.executor, nil, w.storageBackend, false, true)
	// There is no ContainerSpec behind a lookup, so this Container must never
	// create or replace a pod — it exists only to attach to one.
	container.lookedUp = true
	if w.executionPreparer != nil {
		container.checkStart = func(ctx context.Context) error { return w.executionPreparer.CheckIntercept(ctx, handle) }
	}
	// It is the hijack path -- the one Req 18 takes away from a capture-enabled
	// task -- and it gets its ledger classifier from newContainer above, like
	// every other container this worker builds. It used to be assigned a second
	// time here, and at both FindOrCreateContainer returns, all of which had
	// already been through newContainer. Harmless while the two agreed; the
	// class of defect the phase found on this very field is a nil-tolerant one
	// nobody assigns, and two assignment sites is how the two come to disagree.
	return container, true, nil
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
		return w.storageBackend.WrapVolumeForLookup(ctx, key, handle, w.Name(), dbVolume), true, nil
	}
	return NewDaemonSetVolume(key, handle, w.Name(), dbVolume, "", w.config, w.nodeIPResolver), true, nil
}

// RegisterResourceCache registers an alias on the daemon so subsequent get
// steps for the same resource cache skip the fetch. The alias maps the cache
// key to the get step output's path on the daemon's hostPath; nothing on disk
// is created or moved.
//
// A cache carrying a content key is also offered to the durable tier. One
// without is registered node-locally only: its key is a row id, which cannot
// name anything meant to outlive the row.
func (w *Worker) RegisterResourceCache(ctx context.Context, cache db.ResourceCache, volume runtime.Volume) error {
	if w.storageBackend == nil {
		return nil
	}

	cacheKey := ResourceCacheKey(cache)
	durableKey := DurableStorageKey(cache)

	logger := lagerctx.FromContext(ctx).Session("register-resource-cache", lager.Data{
		"cache-id": cache.ID(),
		"key":      cacheKey,
		"durable":  durableKey,
		"handle":   volume.Handle(),
	})

	handle := volume.Handle()

	// Look up which node the artifact lives on via the locator (for affinity
	// recording). RecordOutputs has already been called by this point.
	var nodeName string
	if dsb, ok := w.storageBackend.(*DaemonSetBackend); ok && dsb.artifactLocator != nil {
		nodeName, _ = dsb.artifactLocator.LocateNode(ArtifactKey(handle))
	}

	logger.Info("registering", lager.Data{"node": nodeName})
	return w.storageBackend.RegisterResourceCache(ctx, cacheKey, durableKey, handle, nodeName)
}

// FindDaemonResourceCache probes all live daemon pods for a cached resource.
// The cache is a registry alias pointing at the get step output's directory,
// discoverable via HEAD /resource-caches/{key}.
//
// On hit, returns a stub volume whose handle is the cache key. When
// this volume is used as input to a task step, BuildFetchInitContainers
// resolves it via the daemon's /resolve endpoint which follows the symlink
// to the original get step output.
//
// This method always probes live daemon pods via EndpointSlice discovery.
// The in-memory ArtifactLocator is intentionally NOT consulted here because
// it may contain stale entries for nodes that no longer exist (e.g. after a
// node roll). The locator is only used for scheduling affinity and fetch
// routing — never as a source of truth for cache existence.
func (w *Worker) FindDaemonResourceCache(ctx context.Context, cache db.ResourceCache) (runtime.Volume, bool, error) {
	logger := lagerctx.FromContext(ctx).Session("find-daemon-resource-cache", lager.Data{
		"cache-id":    cache.ID(),
		"has-backend": w.storageBackend != nil,
	})

	if w.storageBackend == nil {
		logger.Info("no-storage-backend")
		return nil, false, nil
	}

	// Probe live daemon pods for the cache, then — only on a miss, and only
	// when the cache has a content key — ask one to warm it from the durable
	// tier. Never returns an error: a cold cache is a re-download, not a
	// failure.
	logger.Info("probing-daemons")
	vol, found := w.storageBackend.FindResourceCache(ctx, ResourceCacheKey(cache), DurableStorageKey(cache), w.Name())
	if !found {
		return nil, false, nil
	}

	// Intentionally do NOT record in the ArtifactLocator here. The
	// locator's NodeName field is contractually a K8s Node object name;
	// we only learned a daemon pod IP from the probe, not a node name.
	// Writing the IP under NodeName poisons downstream lookups: any
	// later WrapVolumeForLookup on the same key would feed the IP into
	// NodeIPResolver and fail with `nodes "<IP>" not found`.
	//
	// Downstream lookups (worker.LookupVolume, ArtifactFromVolume) re-probe
	// live daemons for rc-* keys when the locator has no entry — see
	// DaemonSetBackend.WrapVolumeForLookup. Re-probing on lookup is cheap
	// (one EndpointSlice list + a few HEADs) and avoids stale-entry risk.

	return vol, true, nil
}

// ArtifactFromVolume wraps a step-local container-mount volume as an Artifact
// reference that streams via the DaemonSet artifact cache instead of exec-ing
// into the producing pod. This is the piece that decouples downstream artifact
// reads from the producer pod's lifecycle: by the time a step registers its
// output, RecordOutputs has already published the artifact's location in the
// locator and registered an HTTP alias on the node's daemon, so the wrapped
// reference can fetch directly from the DaemonSet even after the producer pod
// has been reaped.
//
// When no DaemonSet backend is configured (legacy exec-only mode), the volume
// is returned unchanged — callers will still see the producer-pod coupling.
// Phase 4 of the "route artifact reads through DaemonSet" track makes a
// configured DaemonSet backend a hard requirement so the fallback goes away.
func (w *Worker) ArtifactFromVolume(vol runtime.Volume) runtime.Artifact {
	if vol == nil {
		return nil
	}
	if w.storageBackend == nil {
		return vol
	}
	key := ArtifactKey(vol.Handle())
	return w.storageBackend.WrapVolumeForLookup(context.Background(), key, vol.Handle(), w.Name(), nil)
}

func markContainerAsFailed(logger lager.Logger, container db.CreatingContainer) {
	if container != nil {
		_, err := container.Failed()
		if err != nil {
			logger.Error("failed-to-mark-container-as-failed", err)
		}
	}
}
