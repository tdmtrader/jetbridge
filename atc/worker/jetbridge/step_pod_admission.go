package jetbridge

import (
	"errors"
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
//
// It is also the privilege gate at creation. A pod that adds a Linux
// capability or joins the host's network, PID or IPC namespace is refused in
// every mode and under every grant: no step pod builder sets one
// (TestNoStepPodCodeSetsACapabilityOrAHostNamespace), so the refusal changes
// nothing that runs today and covers any path that someday would. A
// privileged container or init container is refused under allowance.enforced
// (--kubernetes-step-privilege=granted) unless the step pod identity's grant
// declares privileged=true; unenforced, it is admitted as the task or
// resource type asks, as it was before the gate.
func admitStepPod(pod *corev1.Pod, roots stepPodRoots, allowance privilegeAllowance) error {
	if err := allowance.admit(pod); err != nil {
		return err
	}
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
					return errors.New("the fetch init container mounts the artifact daemon root writable")
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

// privilegeAllowance is what privilege one step pod may hold, derived from its
// resolved step pod identity and the web's privilege mode
// (Container.privilegeAllowanceFor). The zero value is the any mode: a
// privileged container is admitted; a capability or host namespace never is.
type privilegeAllowance struct {
	// enforced is --kubernetes-step-privilege=granted.
	enforced bool
	// privileged is the identity's grant declaring privileged=true.
	privileged bool
	// refusal says, in an operator's words, whose pod this is and which grant
	// it lacks, and what to change. It ends a refusal's message.
	refusal string
}

func (allowance privilegeAllowance) admit(pod *corev1.Pod) error {
	if found := forbiddenPrivilege(pod); found != "" {
		return fmt.Errorf("refusing step pod %q: %s, and no step pod grant admits it: a step pod never adds a "+
			"Linux capability or joins a host namespace, whatever --kubernetes-step-privilege is", pod.Name, found)
	}
	if !allowance.enforced || allowance.privileged {
		return nil
	}
	held := privilegedContainer(pod)
	if held == "" {
		return nil
	}
	return fmt.Errorf("refusing step pod %q: %s, and this web admits privilege only under a step pod grant "+
		"that declares privileged=true (--kubernetes-step-privilege=granted). %s", pod.Name, held, allowance.refusal)
}

// forbiddenPrivilege names the first host namespace or added capability a pod
// holds, or is empty for a pod that holds none.
func forbiddenPrivilege(pod *corev1.Pod) string {
	switch {
	case pod.Spec.HostNetwork:
		return "the pod joins the host's network namespace"
	case pod.Spec.HostPID:
		return "the pod joins the host's PID namespace"
	case pod.Spec.HostIPC:
		return "the pod joins the host's IPC namespace"
	}
	return eachContainer(pod, func(container corev1.Container, kind string) string {
		context := container.SecurityContext
		if context == nil || context.Capabilities == nil || len(context.Capabilities.Add) == 0 {
			return ""
		}
		added := make([]string, len(context.Capabilities.Add))
		for i, capability := range context.Capabilities.Add {
			added[i] = string(capability)
		}
		noun := "capability"
		if len(added) > 1 {
			noun = "capabilities"
		}
		return fmt.Sprintf("%s %q adds the Linux %s %s", kind, container.Name, noun, strings.Join(added, ", "))
	})
}

// privilegedContainer names the first privileged container or init container
// of a pod, or is empty.
func privilegedContainer(pod *corev1.Pod) string {
	return eachContainer(pod, func(container corev1.Container, kind string) string {
		context := container.SecurityContext
		if context != nil && context.Privileged != nil && *context.Privileged {
			return fmt.Sprintf("%s %q is privileged", kind, container.Name)
		}
		return ""
	})
}

// eachContainer returns the first non-empty answer of found over a pod's init
// containers, then its containers.
func eachContainer(pod *corev1.Pod, found func(corev1.Container, string) string) string {
	for _, container := range pod.Spec.InitContainers {
		if answer := found(container, "init container"); answer != "" {
			return answer
		}
	}
	for _, container := range pod.Spec.Containers {
		if answer := found(container, "container"); answer != "" {
			return answer
		}
	}
	return ""
}
