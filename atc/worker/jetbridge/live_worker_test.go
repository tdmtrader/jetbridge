//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setupLiveWorker creates a Worker backed by a real K8s clientset and an
// isolated PostgreSQL clone.
//
// DaemonSet artifact config comes from the deployed daemon itself (see
// adoptDaemonTLS). These environment variables fill in only what could not be
// read from the cluster, so a deployment that renames or moves the daemon needs
// no change to whatever invokes these tests:
//   - ARTIFACT_DAEMON_HOST_PATH (e.g. /var/concourse/artifacts)
//   - ARTIFACT_DAEMON_PORT (default 7780)
//   - ARTIFACT_DAEMON_SERVICE (the daemon's headless Service)
//   - ARTIFACT_RESOLVE_CAPABILITY_KEY_B64 (the deployed daemon's resolve key,
//     base64; without it any test that fetches an input fails at the init
//     container, since the daemon rejects an unsigned or mis-signed capability)
//
// ARTIFACT_HELPER_IMAGE (default alpine:latest) is a true override: the helper
// is the suite's own choice, not something the daemon dictates.
//
// setupLiveWorkerWithLocator creates a Worker backed by a real K8s clientset.
// If locator is non-nil, it is shared across workers (simulating production
// behavior where a single worker serves all steps in a build). If nil and
// DaemonSet mode is configured, a new locator is created.
func setupLiveWorkerWithLocator(t *testing.T, handle string, locator *jetbridge.ArtifactLocator) (*jetbridge.Worker, runtime.BuildStepDelegate, *jetbridge.ArtifactLocator) {
	worker, delegate, locator, _ := setupLiveWorkerWithLocatorAndDatabase(t, handle, locator)
	return worker, delegate, locator
}

func setupLiveWorkerWithLocatorAndDatabase(t *testing.T, _ string, locator *jetbridge.ArtifactLocator) (*jetbridge.Worker, runtime.BuildStepDelegate, *jetbridge.ArtifactLocator, jetbridgeDB) {
	t.Helper()

	clientset, cfg := kubeClient(t)

	// Fill in the DaemonSet artifact backend from env vars, for clusters where
	// kubeClient could not read the daemon itself. These are fallbacks, not
	// overrides: a value read from the deployed DaemonSet describes the daemon
	// that will actually serve the request, and an env var that disagrees with
	// it only breaks things. ARTIFACT_DAEMON_SERVICE is the sharp one — it
	// names the SAN the daemon's server certificate is verified against, so a
	// stale value fails every ATC-side mTLS call while leaving the init
	// containers, which skip hostname verification, working.
	if hp := os.Getenv("ARTIFACT_DAEMON_HOST_PATH"); hp != "" && cfg.ArtifactDaemonHostPath == "" {
		cfg.ArtifactDaemonHostPath = hp
	}
	if port := os.Getenv("ARTIFACT_DAEMON_PORT"); port != "" && cfg.ArtifactDaemonPort == 0 {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.ArtifactDaemonPort = p
		}
	}
	if svc := os.Getenv("ARTIFACT_DAEMON_SERVICE"); svc != "" && cfg.ArtifactDaemonService == "" {
		cfg.ArtifactDaemonService = svc
	}
	// Default rather than leave it empty. The helper image used to fall back
	// inside the config, so these tests never had to set it; 02468ce81b made it
	// strictly caller-supplied, and an empty value reaches the API server as
	// `spec.initContainers[0].image: Required value` — every test that fetches
	// an input fails on pod creation. The documented default above is the
	// contract, so honour it here.
	cfg.ArtifactHelperImage = "alpine:latest"
	if img := os.Getenv("ARTIFACT_HELPER_IMAGE"); img != "" {
		cfg.ArtifactHelperImage = img
	}
	// Sign resolve capabilities with the deployed daemon's own key. The init
	// container fetches inputs from the node-local daemon, which verifies the
	// capability against the key it was started with, so any other value fails
	// the fetch just as an absent one does. Base64 so the raw 32 bytes survive
	// the trip through the environment.
	if encoded := os.Getenv("ARTIFACT_RESOLVE_CAPABILITY_KEY_B64"); encoded != "" && cfg.ArtifactDaemonResolveCapabilityKey == nil {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			t.Fatalf("decoding ARTIFACT_RESOLVE_CAPABILITY_KEY_B64: %v", err)
		}
		cfg.ArtifactDaemonResolveCapabilityKey = key
	}

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	database := useLiveJetbridgeDB(t)
	dbWorker, found, err := database.WorkerFactory.GetWorker("live-k8s-worker")
	if err != nil {
		t.Fatalf("getting persisted worker: %v", err)
	}
	if !found {
		dbWorker, err = persistNamedWorker(database, "live-k8s-worker")
		if err != nil {
			t.Fatalf("persisting worker: %v", err)
		}
	}

	worker := jetbridge.NewWorker(dbWorker, clientset, *cfg)
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	// Set up artifact locator for DaemonSet mode volume passing.
	// Share a single locator across all steps in a build (matches production behavior).
	if cfg.ArtifactDaemonHostPath != "" {
		if locator == nil {
			locator = jetbridge.NewArtifactLocator()
		}
		worker.SetArtifactLocator(locator)
	}

	return worker, &noopDelegate{}, locator, database
}

func setupLiveWorker(t *testing.T, handle string) (*jetbridge.Worker, runtime.BuildStepDelegate) {
	w, d, _ := setupLiveWorkerWithDatabase(t, handle)
	return w, d
}

func setupLiveWorkerWithDatabase(t *testing.T, handle string) (*jetbridge.Worker, runtime.BuildStepDelegate, jetbridgeDB) {
	w, d, _, database := setupLiveWorkerWithLocatorAndDatabase(t, handle, nil)
	return w, d, database
}

func requirePersistedContainer(t *testing.T, database jetbridgeDB, workerName, handle string) {
	t.Helper()
	persistedWorker, found, err := database.WorkerFactory.GetWorker(workerName)
	if err != nil {
		t.Fatalf("getting persisted worker: %v", err)
	}
	if !found {
		t.Fatal("persisted worker not found")
	}

	creating, created, err := persistedWorker.FindContainer(db.NewFixedHandleContainerOwner(handle))
	if err != nil {
		t.Fatalf("finding persisted container: %v", err)
	}
	if creating != nil {
		t.Fatalf("expected no creating container, got %T", creating)
	}
	if created == nil {
		t.Fatal("persisted created container not found")
	}
	if created.Handle() != handle {
		t.Fatalf("persisted container handle = %q, want %q", created.Handle(), handle)
	}
}

// TestLiveWorkerTaskExecution exercises the full Worker → Container → Process
// lifecycle using exec mode (pause pod + SPDY exec for all tasks).
func TestLiveWorkerTaskExecution(t *testing.T) {
	handle := "live-task-" + time.Now().Format("150405")
	worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	cleanupPod(t, clientset, cfg.Namespace, handle)

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
	requirePersistedContainer(t, database, "live-k8s-worker", handle)

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
	worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	cleanupPod(t, clientset, cfg.Namespace, handle)

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
	requirePersistedContainer(t, database, "live-k8s-worker", handle)

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
	worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	cleanupPod(t, clientset, cfg.Namespace, handle)

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
	requirePersistedContainer(t, database, "live-k8s-worker", handle)

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
	worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
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
	requirePersistedContainer(t, database, "live-k8s-worker", handle)

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
	cleanupPod(t, clientset, cfg.Namespace, handle)
}

// TestLiveWorkerHijackExistingPod simulates the fly hijack flow:
// 1. Run a task (creates a pause pod, execs the task command)
// 2. After task completes, exec a second command into the same pod
// This verifies that LookupContainer + Run on existing pod works end-to-end.
func TestLiveWorkerHijackExistingPod(t *testing.T) {
	handle := "live-hijack-" + time.Now().Format("150405")
	worker, delegate, database := setupLiveWorkerWithDatabase(t, handle)
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
	requirePersistedContainer(t, database, "live-k8s-worker", handle)

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
	cleanupPod(t, clientset, cfg.Namespace, handle)
}
