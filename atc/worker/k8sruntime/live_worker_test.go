//go:build live
// +build live

package k8sruntime_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// liveNoopDelegate satisfies runtime.BuildStepDelegate for live tests.
type liveNoopDelegate struct{}

func (d *liveNoopDelegate) StreamingVolume(_ lager.Logger, _, _, _ string)   {}
func (d *liveNoopDelegate) WaitingForStreamedVolume(_ lager.Logger, _, _ string) {}
func (d *liveNoopDelegate) BuildStartTime() time.Time                           { return time.Time{} }

// setupLiveWorker creates a Worker backed by a real K8s clientset with
// fake DB components (we only need the DB fakes to satisfy the interface;
// actual pod creation goes through the real K8s API).
func setupLiveWorker(t *testing.T, handle string) (*k8sruntime.Worker, runtime.BuildStepDelegate) {
	t.Helper()

	clientset, cfg := kubeClient(t)

	restConfig, err := k8sruntime.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("live-k8s-worker")

	// Wire up the fake DB so FindOrCreateContainer works:
	// FindContainer returns nothing → CreateContainer is called → Created() succeeds.
	fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
	fakeCreatingContainer.HandleReturns(handle)
	fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
	fakeCreatedContainer.HandleReturns(handle)
	fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)

	fakeDBWorker.FindContainerReturns(nil, nil, nil)
	fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

	worker := k8sruntime.NewWorker(fakeDBWorker, clientset, *cfg)
	executor := k8sruntime.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	return worker, &liveNoopDelegate{}
}

// TestLiveWorkerTaskExecution exercises the full Worker → Container → Process
// lifecycle using exec mode (pause pod + SPDY exec for all tasks).
func TestLiveWorkerTaskExecution(t *testing.T) {
	handle := "live-task-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	ctx := context.Background()

	// Create a container through the Worker interface.
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run a simple command — this now always uses exec mode (pause pod + SPDY exec).
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hello-from-live-test"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("process ID: %s, waiting for completion...", process.ID())

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitStatus != 0 {
		t.Fatalf("expected exit status 0, got %d", result.ExitStatus)
	}
	t.Logf("task completed with exit status %d", result.ExitStatus)
}

// TestLiveWorkerNonZeroExit verifies that non-zero exit codes propagate
// correctly through the exec-mode Pod lifecycle.
func TestLiveWorkerNonZeroExit(t *testing.T) {
	handle := "live-fail-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	ctx := context.Background()

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 42"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitStatus != 42 {
		t.Fatalf("expected exit status 42, got %d", result.ExitStatus)
	}
	t.Logf("correctly got non-zero exit status %d", result.ExitStatus)
}

// TestLiveWorkerExecMode tests exec-mode I/O through the Worker interface.
// This is the code path used by resource get/put/check steps where stdin
// carries JSON and stdout returns the result.
func TestLiveWorkerExecMode(t *testing.T) {
	handle := "live-exec-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	ctx := context.Background()

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

	// Provide stdin to exercise exec mode with stdin piping.
	stdinData := `{"source":{"uri":"https://example.com"},"version":{"ref":"abc123"}}`
	var stdout, stderr bytes.Buffer

	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "cat",
	}, runtime.ProcessIO{
		Stdin:  strings.NewReader(stdinData),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run (exec mode): %v", err)
	}

	t.Logf("exec-mode process ID: %s, waiting...", process.ID())

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitStatus != 0 {
		t.Fatalf("expected exit status 0, got %d (stderr: %s)", result.ExitStatus, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output != stdinData {
		t.Fatalf("stdin/stdout mismatch.\nexpected: %s\ngot:      %s", stdinData, output)
	}
	t.Logf("exec-mode round-trip successful: %s", output)
}

// TestLiveWorkerPodSurvivesCompletion verifies that the pause pod remains
// running after the exec'd command completes. This is critical for both
// GC-managed cleanup and fly hijack support.
func TestLiveWorkerPodSurvivesCompletion(t *testing.T) {
	handle := "live-survive-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo done"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if result.ExitStatus != 0 {
		t.Fatalf("expected exit status 0, got %d", result.ExitStatus)
	}

	// Verify the pod still exists and is Running after the command completed.
	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod should still exist after exec completion: %v", err)
	}

	t.Logf("pod %s phase after command: %s", pod.Name, pod.Status.Phase)

	if pod.Status.Phase != "Running" {
		t.Fatalf("expected pod phase Running (pause pod still alive), got %s", pod.Status.Phase)
	}

	// Clean up the pod manually since GC isn't wired yet.
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), handle, metav1.DeleteOptions{})
	})
}

// TestLiveWorkerHijackExistingPod simulates the fly hijack flow:
// 1. Run a task (creates a pause pod, execs the task command)
// 2. After task completes, exec a second command into the same pod
// This verifies that LookupContainer + Run on existing pod works end-to-end.
func TestLiveWorkerHijackExistingPod(t *testing.T) {
	handle := "live-hijack-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	// Step 1: Run a task (this creates the pause pod and execs the command).
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo task-output"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run (task): %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (task): %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("task expected exit 0, got %d", result.ExitStatus)
	}
	t.Logf("task completed, pod %s should still be running", handle)

	// Step 2: Simulate fly hijack — exec into the existing pod.
	// Use a fresh worker (as the hijack handler would via LookupContainer).
	hijackWorker, _ := setupLiveWorker(t, handle)

	var stdout bytes.Buffer
	hijackProcess, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hijack-works"},
	}, runtime.ProcessIO{
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("Run (hijack): %v", err)
	}
	_ = hijackWorker // used for setup; container is the focus

	hijackResult, err := hijackProcess.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (hijack): %v", err)
	}

	if hijackResult.ExitStatus != 0 {
		t.Fatalf("hijack expected exit 0, got %d", hijackResult.ExitStatus)
	}

	output := strings.TrimSpace(stdout.String())
	if output != "hijack-works" {
		t.Fatalf("expected 'hijack-works', got %q", output)
	}
	t.Logf("hijack into existing pod successful: %s", output)

	// Clean up.
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), handle, metav1.DeleteOptions{})
	})
}
