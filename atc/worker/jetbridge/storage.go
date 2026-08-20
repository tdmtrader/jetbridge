package jetbridge

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

// StorageBackend encapsulates all storage-specific decisions for artifact
// lifecycle: how step volumes are created, how artifacts are fetched into
// containers, how outputs are recorded, and how scheduling affinity is
// determined.
//
// When nil, container orchestration falls back to emptyDir volumes with no
// init containers, no affinity, and no output recording — matching the
// behavior for non-DaemonSet deployments.
type StorageBackend interface {
	StepVolume(name, handle, subdir string) corev1.Volume
	CacheVolume(name string, identity atc.TaskCacheIdentity, stepName, cachePath string) corev1.Volume
	ArtifactStoreVolume(containerType db.ContainerType) *corev1.Volume
	ArtifactStoreVolumeName() string
	BuildFetchInitContainers(handle string, inputs []runtime.Input, podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) ([]corev1.Container, error)
	BuildCleanupInitContainer(handle string, containerType db.ContainerType, reused bool) *corev1.Container
	BuildAffinity(inputs []runtime.Input) *corev1.Affinity
	RecordOutputs(ctx context.Context, handle, nodeName string, volumes []*Volume, spec runtime.ContainerSpec)
	WrapVolumeForArtifact(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume
	WrapVolumeForLookup(ctx context.Context, key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume

	// RegisterResourceCache registers a resource cache alias on the daemon,
	// mapping the cache key to the physical disk path of the get step output.
	// This makes the cache discoverable via HEAD /resource-caches/{key} on
	// subsequent runs.
	//
	// durableKey names this artifact in long-term storage, or is empty for "do
	// not keep it". Its presence is the entire eligibility protocol, and its
	// retention-class prefix is what an object lifecycle rule acts on. The
	// daemon never inspects it: what an artifact is, whether it is re-derivable,
	// and how long it should be kept are all knowledge the daemon lacks.
	RegisterResourceCache(ctx context.Context, cacheKey, durableKey, volumeHandle, nodeName string) error

	// FindResourceCache returns a volume bound to a daemon holding the cache,
	// or (nil, false).
	//
	// It never returns an error. Every failure — endpoint discovery, transport,
	// an unreachable bucket — is a miss, because the caller's next move is to
	// re-run the get step either way. The error is absent from the signature
	// so that no future caller can turn a cold cache into a red build.
	FindResourceCache(ctx context.Context, cacheKey, durableKey, workerName string) (runtime.Volume, bool)
}

func emptyDirVolume(name string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}
