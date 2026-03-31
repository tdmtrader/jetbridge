package jetbridge

import (
	"context"

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
	CacheVolume(name string, jobID int, stepName, cachePath string) corev1.Volume
	ArtifactStoreVolume(containerType db.ContainerType) *corev1.Volume
	ArtifactStoreVolumeName() string
	BuildFetchInitContainers(handle string, inputs []runtime.Input, podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) []corev1.Container
	BuildCleanupInitContainer(handle string, containerType db.ContainerType, reused bool) *corev1.Container
	BuildAffinity(inputs []runtime.Input) *corev1.Affinity
	RecordOutputs(ctx context.Context, handle, nodeName string, volumes []*Volume, spec runtime.ContainerSpec)
	WrapVolumeForArtifact(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume
	WrapVolumeForLookup(key, handle, workerName string, dbVolume db.CreatedVolume) runtime.Volume
}

func emptyDirVolume(name string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}
