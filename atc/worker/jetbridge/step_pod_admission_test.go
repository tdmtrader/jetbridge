package jetbridge

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The containment admission over constructed pods: shapes no step request can
// produce, so only this seam can show they are refused.
func TestStepPodAdmissionContainment(t *testing.T) {
	config := Config{ArtifactDaemonHostPath: "/var/lib/artifacts", CacheHostPath: "/var/cache/concourse"}
	roots := stepPodRootsFor(config, "handle-1", "reserved-1")

	hostPath := func(name, path string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path}}}
	}
	mount := func(name string, readOnly bool) corev1.VolumeMount {
		return corev1.VolumeMount{Name: name, MountPath: "/m/" + name, ReadOnly: readOnly}
	}
	ordinary := func() *corev1.Pod {
		// Fresh volumes per pod: a case mutates a HostPath through its pointer.
		rootVolume := hostPath(artifactDaemonHostPathVolumeName, "/var/lib/artifacts")
		stepVolume := hostPath("output-1", "/var/lib/artifacts/steps/handle-1/out")
		reservedVolume := hostPath("reserved", "/var/lib/artifacts/steps/reserved-1")
		cacheVolume := hostPath("cache-1", "/var/cache/concourse/abc123")
		return &corev1.Pod{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{rootVolume, stepVolume, reservedVolume, cacheVolume},
			InitContainers: []corev1.Container{
				{Name: cleanupInitContainerName, VolumeMounts: []corev1.VolumeMount{mount(rootVolume.Name, false)}},
				{Name: fetchInitContainerName, VolumeMounts: []corev1.VolumeMount{mount(rootVolume.Name, true), mount(stepVolume.Name, false)}},
				{Name: "materialize-hangar-inputs", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, true)}},
				{Name: "capture-control"},
			},
			Containers: []corev1.Container{
				{Name: "main", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, false), mount(reservedVolume.Name, false), mount(cacheVolume.Name, false)}},
				{Name: "sidecar", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, false)}},
			},
		}}
	}

	if err := admitStepPod(ordinary(), roots); err != nil {
		t.Fatalf("an ordinary step pod was refused: %v", err)
	}

	for name, tc := range map[string]struct {
		mutate func(*corev1.Pod)
		reason string
	}{
		"the main container mounts the artifact root": {func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, mount(artifactDaemonHostPathVolumeName, true))
		}, `container "main" mounts the artifact daemon root`},
		"a sidecar mounts the artifact root": {func(p *corev1.Pod) {
			p.Spec.Containers[1].VolumeMounts = append(p.Spec.Containers[1].VolumeMounts, mount(artifactDaemonHostPathVolumeName, true))
		}, `container "sidecar" mounts the artifact daemon root`},
		"the control init mounts the artifact root": {func(p *corev1.Pod) {
			p.Spec.InitContainers[3].VolumeMounts = []corev1.VolumeMount{mount(artifactDaemonHostPathVolumeName, true)}
		}, `container "capture-control" mounts the artifact daemon root`},
		"the fetch init mounts the artifact root writable": {func(p *corev1.Pod) {
			p.Spec.InitContainers[1].VolumeMounts[0].ReadOnly = false
		}, "fetch init container mounts the artifact daemon root writable"},
		"a materialize init mounts a cache": {func(p *corev1.Pod) {
			p.Spec.InitContainers[2].VolumeMounts = append(p.Spec.InitContainers[2].VolumeMounts, mount("cache-1", true))
		}, `"materialize-hangar-inputs" mounts a cache directory`},
		"a step key resolving into another step": {func(p *corev1.Pod) {
			p.Spec.Volumes[1].HostPath.Path = "/var/lib/artifacts/steps/handle-1/../other-handle/out"
		}, "outside the step's own directories"},
		"a cache key resolving outside the cache root": {func(p *corev1.Pod) {
			p.Spec.Volumes[3].HostPath.Path = "/var/cache/concourse/../../etc"
		}, "outside the step's own directories"},
		"the cache root itself": {func(p *corev1.Pod) {
			p.Spec.Volumes[3].HostPath.Path = "/var/cache/concourse"
		}, "outside the step's own directories"},
		"the root under another volume name": {func(p *corev1.Pod) {
			p.Spec.Volumes[1].HostPath.Path = "/var/lib/artifacts"
		}, "outside the step's own directories"},
	} {
		t.Run(name, func(t *testing.T) {
			pod := ordinary()
			tc.mutate(pod)
			err := admitStepPod(pod, roots)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("admitted, or refused for the wrong reason (%v); want %q", err, tc.reason)
			}
		})
	}
}
