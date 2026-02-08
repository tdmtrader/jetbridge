//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes"
)

// setupLiveResilienceWorker creates a Worker with configurable PodStartupTimeout
// for testing failure and timeout scenarios against a real K8s cluster.
func setupLiveResilienceWorker(t *testing.T, handle string, podStartupTimeout time.Duration) (
	*jetbridge.Worker,
	runtime.BuildStepDelegate,
	kubernetes.Interface,
	*jetbridge.Config,
) {
	t.Helper()

	clientset, cfg := kubeClient(t)
	cfg.PodStartupTimeout = podStartupTimeout

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("live-k8s-worker")

	setupFakeDBContainer(fakeDBWorker, handle)

	worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	return worker, &noopDelegate{}, clientset, cfg
}

// TestLiveInvalidImageFailsFast verifies that a pod with a nonexistent image
// fails fast with ErrImagePull or ImagePullBackOff rather than hanging
// indefinitely. Diagnostics should appear on stderr.
func TestLiveInvalidImageFailsFast(t *testing.T) {
	handle := "live-bad-img-" + time.Now().Format("150405")
	worker, delegate, clientset, cfg := setupLiveResilienceWorker(t, handle, 2*time.Minute)

	cleanupPod(t, clientset, cfg.Namespace, handle)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create a container with a completely invalid image.
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeGet},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///invalid-repo-xyz-no-such-image:nonexistent-tag"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Use exec mode (stdin provided) to exercise the waitForRunning path.
	var stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "cat",
	}, runtime.ProcessIO{
		Stdin:  strings.NewReader("{}"),
		Stdout: new(bytes.Buffer),
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("waiting for process to fail (expecting ErrImagePull)...")
	result, err := process.Wait(ctx)
	if err == nil {
		t.Fatalf("expected error for invalid image, got exit status %d", result.ExitStatus)
	}

	t.Logf("got expected error: %v", err)

	// Verify the error mentions the image pull failure.
	errStr := err.Error()
	if !strings.Contains(errStr, "ErrImagePull") && !strings.Contains(errStr, "ImagePullBackOff") {
		t.Fatalf("expected error to mention ErrImagePull or ImagePullBackOff, got: %s", errStr)
	}

	// Verify diagnostics were written to stderr.
	stderrStr := stderr.String()
	t.Logf("stderr diagnostics:\n%s", stderrStr)
	if !strings.Contains(stderrStr, "Pod Failure Diagnostics") {
		t.Fatalf("expected stderr to contain 'Pod Failure Diagnostics', got: %s", stderrStr)
	}
}

// TestLivePodStartupTimeout verifies that the pod startup timeout fires
// when a pod cannot reach Running state within the configured duration.
func TestLivePodStartupTimeout(t *testing.T) {
	handle := "live-timeout-" + time.Now().Format("150405")
	// Use an impossibly short timeout — the pod can't start in 1ms.
	worker, delegate, clientset, cfg := setupLiveResilienceWorker(t, handle, 1*time.Millisecond)

	cleanupPod(t, clientset, cfg.Namespace, handle)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use a valid image — the timeout fires before K8s can pull and start it.
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeGet},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	var stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "cat",
	}, runtime.ProcessIO{
		Stdin:  strings.NewReader("{}"),
		Stdout: new(bytes.Buffer),
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("waiting for process (expecting timeout)...")
	result, err := process.Wait(ctx)
	if err == nil {
		t.Fatalf("expected timeout error, got exit status %d", result.ExitStatus)
	}

	t.Logf("got expected error: %v", err)

	errStr := err.Error()
	if !strings.Contains(errStr, "timed out") {
		t.Fatalf("expected error to contain 'timed out', got: %s", errStr)
	}

	// Diagnostics may or may not appear depending on whether the first
	// poll completed before the deadline fired.
	stderrStr := stderr.String()
	if stderrStr != "" {
		t.Logf("stderr diagnostics:\n%s", stderrStr)
	}
}
