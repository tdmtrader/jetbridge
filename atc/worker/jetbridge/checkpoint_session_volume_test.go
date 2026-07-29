package jetbridge

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

func TestCheckpointSessionVolumeIsDaemonBackedAndMainOnly(t *testing.T) {
	c := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, runtime.ContainerSpec{
		Type:              db.ContainerTypeAgent,
		CheckpointCapture: true,
		Dir:               "/tmp/build",
		Inputs:            []runtime.Input{{DestinationPath: "/tmp/build/input"}},
		Outputs:           runtime.OutputPaths{"output": "/tmp/build/output"},
		ImageSpec:         runtime.ImageSpec{ImageURL: "busybox"},
		Sidecars:          []atc.SidecarConfig{{Name: "sidecar", Image: "busybox"}},
		SecretMounts:      []runtime.SecretMount{{SecretName: "platform", MountPath: "/run/concourse/platform"}},
	}, nil, nil, Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, "worker", nil, nil, NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, nil, nil), false)

	volume, mount, err := c.checkpointSessionVolume()
	if err != nil {
		t.Fatal(err)
	}
	if volume == nil || volume.HostPath == nil || volume.HostPath.Path != "/var/lib/concourse/artifacts/steps/agent-42/session" {
		t.Fatalf("checkpoint volume = %#v", volume)
	}
	if mount == nil || mount.MountPath != "/tmp/build/.concourse/session" || mount.ReadOnly {
		t.Fatalf("checkpoint mount = %#v", mount)
	}
	pod, err := c.buildPod(runtime.ProcessSpec{Dir: "/tmp/build"}, []string{"sleep"}, []string{"100"})
	if err != nil {
		t.Fatal(err)
	}
	if !podContainerHasMount(pod.Spec.Containers[0], checkpointSessionVolumeName) {
		t.Fatal("main container is missing checkpoint session mount")
	}
	if podContainerHasMount(pod.Spec.Containers[1], checkpointSessionVolumeName) {
		t.Fatal("sidecar received checkpoint session mount")
	}
	for _, init := range pod.Spec.InitContainers {
		if podContainerHasMount(init, checkpointSessionVolumeName) {
			t.Fatal("init container received checkpoint session mount")
		}
	}
}

func podContainerHasMount(container corev1.Container, name string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}

func TestCheckpointSessionVolumeFailsClosedWithoutDaemonOrOnLogicalCollision(t *testing.T) {
	base := runtime.ContainerSpec{Type: db.ContainerTypeAgent, CheckpointCapture: true, Dir: "/tmp/build"}
	withoutDaemon := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, base, nil, nil, Config{}, "worker", nil, nil, nil, false)
	if _, _, err := withoutDaemon.checkpointSessionVolume(); err == nil || !strings.Contains(err.Error(), "DaemonSet") {
		t.Fatalf("non-daemon checkpoint capture error = %v", err)
	}
	colliding := base
	colliding.Outputs = runtime.OutputPaths{"session": "/tmp/build/output"}
	withDaemon := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, colliding, nil, nil, Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, "worker", nil, nil, NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, nil, nil), false)
	if _, _, err := withDaemon.checkpointSessionVolume(); err == nil {
		t.Fatal("accepted session host-root collision")
	}
}
