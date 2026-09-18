package jetbridge

import (
	"fmt"
	"path/filepath"

	"github.com/concourse/concourse/atc/runtime"
)

// buildVolumeMounts is the pure layout calculation shared by new and reused
// containers. Database lookup belongs to Worker; layout needs only identity,
// exec destination and the container spec. No I/O is performed here.
func buildVolumeMounts(workerName, namespace string, executor PodExecutor, handle string, spec runtime.ContainerSpec) ([]runtime.VolumeMount, []*Volume) {
	newVolume := func(handle, mountPath string) *Volume {
		if executor != nil {
			return NewDeferredVolume(handle, workerName, executor, namespace, mainContainerName, mountPath)
		}
		return NewStubVolume(handle, workerName, mountPath)
	}
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
		addMount(newVolume(handle+"-dir", spec.Dir), spec.Dir)
	}

	// Track input mount paths so overlapping outputs reuse the same volume.
	// This must match the dedup logic in Container.buildVolumeMounts() — both
	// use filepath.Clean to normalize trailing slashes on output paths.
	inputMountPaths := make(map[string]bool, len(spec.Inputs))
	for i, input := range spec.Inputs {
		addMount(newVolume(fmt.Sprintf("%s-input-%d", handle, i), input.DestinationPath), input.DestinationPath)
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
		addMount(newVolume(fmt.Sprintf("%s-output-%s", handle, name), path), path)
	}

	for i, cachePath := range spec.Caches {
		resolvedPath := cachePath
		if !filepath.IsAbs(cachePath) && spec.Dir != "" {
			resolvedPath = filepath.Join(spec.Dir, cachePath)
		}
		addMount(newVolume(fmt.Sprintf("%s-cache-%d", handle, i), resolvedPath), resolvedPath)
	}

	return mounts, volumes
}
