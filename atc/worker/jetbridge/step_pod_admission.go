package jetbridge

import (
	"fmt"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Names of the init containers that may reach the artifact daemon root.
const (
	fetchInitContainerName   = "fetch-inputs"
	cleanupInitContainerName = "cleanup-stale"
)

// stepPodRoots are the host directories one step pod may mount: the artifact
// daemon root itself (for the fetch and cleanup init containers only), its own
// step directory, its reserved capture directory, and the cache root.
type stepPodRoots struct {
	root      string
	steps     string
	stepDir   string
	reserved  string
	cacheRoot string
}

func stepPodRootsFor(config Config, handle, reservedDir string) stepPodRoots {
	root := filepath.Clean(config.ArtifactDaemonHostPath)
	cacheRoot := filepath.Clean(config.CacheHostPath)
	if config.CacheHostPath == "" {
		cacheRoot = filepath.Join(root, "caches")
	}
	roots := stepPodRoots{root: root, steps: filepath.Join(root, "steps"), cacheRoot: cacheRoot}
	if handle != "" {
		roots.stepDir = filepath.Join(roots.steps, handle)
	}
	if reservedDir != "" {
		roots.reserved = filepath.Join(roots.steps, reservedDir)
	}
	return roots
}

// admitStepPod is the containment admission every step pod passes before it
// is created. Each hostPath volume must be the artifact daemon root, a path
// strictly below the step's own directory, its reserved capture directory, or
// a path strictly below the cache root; and each container may mount only what
// its role needs. A step output's name becomes a path segment under
// `steps/<handle>/`, and filepath.Join cleans `..` away, so an unchecked name
// could otherwise mount any path on the node.
func admitStepPod(pod *corev1.Pod, roots stepPodRoots) error {
	kinds := map[string]hostPathKind{}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath == nil {
			continue
		}
		kind, err := roots.classify(volume)
		if err != nil {
			return err
		}
		kinds[volume.Name] = kind
	}

	check := func(container corev1.Container, init bool) error {
		for _, mount := range container.VolumeMounts {
			kind, ok := kinds[mount.Name]
			if !ok {
				continue
			}
			switch {
			case kind == hostPathArtifactRoot && init && container.Name == fetchInitContainerName:
				if !mount.ReadOnly {
					return fmt.Errorf("the fetch init container mounts the artifact daemon root writable")
				}
			case kind == hostPathArtifactRoot && init && container.Name == cleanupInitContainerName:
			case kind == hostPathArtifactRoot:
				return fmt.Errorf("container %q mounts the artifact daemon root; only the fetch and cleanup init containers may", container.Name)
			case kind == hostPathCache && init && strings.HasPrefix(container.Name, "materialize-"):
				return fmt.Errorf("init container %q mounts a cache directory; it may mount only step directories", container.Name)
			}
		}
		return nil
	}
	for _, container := range pod.Spec.InitContainers {
		if err := check(container, true); err != nil {
			return err
		}
	}
	for _, container := range pod.Spec.Containers {
		if err := check(container, false); err != nil {
			return err
		}
	}
	return nil
}

type hostPathKind int

const (
	hostPathArtifactRoot hostPathKind = iota + 1
	hostPathStep
	hostPathCache
)

func (roots stepPodRoots) classify(volume corev1.Volume) (hostPathKind, error) {
	path := filepath.Clean(volume.HostPath.Path)
	switch {
	case path == roots.root && volume.Name == artifactDaemonHostPathVolumeName:
		return hostPathArtifactRoot, nil
	case roots.stepDir != "" && strictlyWithin(roots.stepDir, path):
		return hostPathStep, nil
	case roots.reserved != "" && path == roots.reserved && strictlyWithin(roots.steps, path):
		return hostPathStep, nil
	case strictlyWithin(roots.cacheRoot, path):
		return hostPathCache, nil
	}
	return 0, fmt.Errorf("volume %q mounts host path %q, outside the step's own directories", volume.Name, volume.HostPath.Path)
}
