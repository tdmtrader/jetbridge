//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveSidecarSharesVolumes creates a pod with a main container and a
// sidecar, both mounting the same emptyDir volume. The sidecar writes a file
// and the main container reads it, verifying that volume sharing works.
func TestLiveSidecarSharesVolumes(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ctx := context.Background()
	ns := liveTestNamespace()

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)

	podName := "live-sidecar-vol-" + time.Now().Format("150405")
	cleanupPod(t, clientset, ns, podName)

	// Create a pod with:
	// - main: busybox pause container.
	// - sidecar: busybox that writes "hello-from-sidecar" to /shared/data.txt.
	// Both share an emptyDir at /shared.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{
				{
					Name: "shared",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:    "main",
					Image:   "busybox",
					Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "shared", MountPath: "/shared"},
					},
				},
				{
					Name:    "sidecar",
					Image:   "busybox",
					Command: []string{"sh", "-c", "echo hello-from-sidecar > /shared/data.txt; trap 'exit 0' TERM; sleep 86400 & wait"},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "shared", MountPath: "/shared"},
					},
				},
			},
		},
	}

	t.Logf("creating sidecar volume test pod %s", podName)
	_, err = clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating pod: %v", err)
	}

	// Wait for Running.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		p, err := clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting pod: %v", err)
		}
		if p.Status.Phase == corev1.PodRunning {
			t.Logf("pod %s is Running", podName)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Give the sidecar a moment to write.
	time.Sleep(2 * time.Second)

	// Read the file written by sidecar from the main container's view.
	var stdout bytes.Buffer
	err = executor.ExecInPod(ctx, ns, podName, "main",
		[]string{"cat", "/shared/data.txt"},
		nil, &stdout, nil, false)
	if err != nil {
		t.Fatalf("exec cat: %v", err)
	}

	content := strings.TrimSpace(stdout.String())
	if content != "hello-from-sidecar" {
		t.Fatalf("expected 'hello-from-sidecar', got %q", content)
	}
	t.Logf("main container read from sidecar-written volume: %q", content)

	// Verify pod has both containers.
	p, err := clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}
	if len(p.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(p.Spec.Containers))
	}
}

// TestLiveSidecarViaWorkerAPI creates a container through the Worker interface
// with a SidecarConfig, verifying that the resulting pod has both the main
// container and the sidecar container.
func TestLiveSidecarViaWorkerAPI(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	handle := "live-sidecar-api-" + time.Now().Format("150405")

	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("live-sidecar-worker")
	setupFakeDBContainer(fakeDBWorker, handle)

	worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	cleanupPod(t, clientset, cfg.Namespace, handle)

	// Create a container with a sidecar.
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Sidecars: []atc.SidecarConfig{
				{
					Name:    "helper-sidecar",
					Image:   "busybox",
					Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
				},
			},
		},
		&noopDelegate{},
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run a simple command to trigger pod creation.
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo sidecar-test"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	// Verify the pod was created with both containers.
	ns := liveTestNamespace()
	pod, err := clientset.CoreV1().Pods(ns).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	containerNames := make([]string, len(pod.Spec.Containers))
	for i, c := range pod.Spec.Containers {
		containerNames[i] = c.Name
	}
	t.Logf("pod containers: %v", containerNames)

	// Should have at least main + helper-sidecar.
	found := false
	for _, name := range containerNames {
		if name == "helper-sidecar" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'helper-sidecar' container, found only: %v", containerNames)
	}
}
