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
				{Name: "materialize-run-input-0", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, true)}},
				{Name: "capture-control"},
			},
			Containers: []corev1.Container{
				{Name: "main", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, false), mount(reservedVolume.Name, false), mount(cacheVolume.Name, false)}},
				{Name: "sidecar", VolumeMounts: []corev1.VolumeMount{mount(stepVolume.Name, false)}},
			},
		}}
	}

	if err := admitStepPod(ordinary(), roots, privilegeAllowance{}); err != nil {
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
		}, `"materialize-run-input-0" mounts a cache directory`},
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
			err := admitStepPod(pod, roots, privilegeAllowance{})
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("admitted, or refused for the wrong reason (%v); want %q", err, tc.reason)
			}
		})
	}
}

// The privilege gate over constructed pods.
func TestStepPodAdmissionPrivilege(t *testing.T) {
	roots := stepPodRootsFor(Config{ArtifactDaemonHostPath: "/var/lib/artifacts"}, "handle-1", "")
	yes := true
	unprivileged := func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: fetchInitContainerName}},
			Containers:     []corev1.Container{{Name: "main"}, {Name: "sidecar"}},
		}}
	}
	ungranted := privilegeAllowance{enforced: true, refusal: "Owner main/p/unit has no step pod grant: add one"}
	granted := privilegeAllowance{enforced: true, privileged: true}
	every := map[string]privilegeAllowance{
		"under granted, outside every grant":       ungranted,
		"under granted, inside a privileged grant": granted,
		"under any, outside every grant":           {},
		"under any, inside a privileged grant":     {privileged: true},
	}

	for name, allowance := range every {
		if err := admitStepPod(unprivileged(), roots, allowance); err != nil {
			t.Errorf("an unprivileged step pod was refused %s: %v", name, err)
		}
	}

	// Under granted, a privileged container or init container is admitted
	// only inside a grant that declares privileged=true; under any, always.
	t.Run("privileged", func(t *testing.T) {
		for name, mutate := range map[string]func(*corev1.Pod){
			`container "main" is privileged`: func(p *corev1.Pod) {
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
			},
			`container "sidecar" is privileged`: func(p *corev1.Pod) {
				p.Spec.Containers[1].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
			},
			`init container "fetch-inputs" is privileged`: func(p *corev1.Pod) {
				p.Spec.InitContainers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes}
			},
		} {
			pod := unprivileged()
			mutate(pod)
			err := admitStepPod(pod, roots, ungranted)
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), ungranted.refusal) ||
				!strings.Contains(err.Error(), "--kubernetes-step-privilege=granted") {
				t.Errorf("%s: admitted outside a grant, or refused for the wrong reason: %v", name, err)
			}
			for allowanceName, allowance := range every {
				if allowance == ungranted {
					continue
				}
				pod := unprivileged()
				mutate(pod)
				if err := admitStepPod(pod, roots, allowance); err != nil {
					t.Errorf("%s %s: refused: %v", name, allowanceName, err)
				}
			}
		}
	})

	// An added capability or a host namespace is a different power from
	// privileged: no grant and no mode admits one.
	t.Run("never admitted", func(t *testing.T) {
		for name, mutate := range map[string]func(*corev1.Pod){
			`container "main" adds the Linux capability SYS_ADMIN`: func(p *corev1.Pod) {
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}}
			},
			`init container "fetch-inputs" adds the Linux capabilities NET_ADMIN, NET_RAW`: func(p *corev1.Pod) {
				p.Spec.InitContainers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"}}}
			},
			`container "main" adds the Linux capability SYS_ADMIN, though it is privileged`: func(p *corev1.Pod) {
				p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &yes, Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}}
			},
			"the pod joins the host's network namespace": func(p *corev1.Pod) { p.Spec.HostNetwork = true },
			"the pod joins the host's PID namespace":     func(p *corev1.Pod) { p.Spec.HostPID = true },
			"the pod joins the host's IPC namespace":     func(p *corev1.Pod) { p.Spec.HostIPC = true },
		} {
			want, _, _ := strings.Cut(name, ", though")
			for allowanceName, allowance := range every {
				pod := unprivileged()
				mutate(pod)
				err := admitStepPod(pod, roots, allowance)
				if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "no step pod grant admits it") {
					t.Errorf("%s %s: admitted, or refused for the wrong reason: %v", name, allowanceName, err)
				}
			}
		}
	})
}
