//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// TestLiveSidecarLogStreamTimeout verifies SC-11 from the K8s Runtime
// Behavioral Specification:
//
//	"When waiting for sidecar log streams to complete, the system MUST bound
//	 the wait to 5 seconds. If exceeded, proceed without waiting (sidecar
//	 streams do not block process completion)."
//
// The exec-mode bounded wait (execProcess.Wait, process.go) only engages when
// ProcessIO.SidecarWriters has a dedicated writer for the sidecar. The sidecar
// here runs effectively forever (sleep 86400), so its log stream never EOFs and
// the per-sidecar streaming goroutine blocks in io.Copy. Without the 5s bound,
// process.Wait() would block for the sidecar's entire lifetime; with it, Wait()
// must return within ~5s of the (fast) main command completing.
//
// Strategy:
//   - "control" run: same long-running sidecar, but NO dedicated SidecarWriter.
//     No streaming goroutine is started, so no bounded wait engages. This run's
//     duration is approximately pod-startup + exec, and is used to factor out
//     startup time so the ~5s bounded wait can be isolated.
//   - "test" run: identical, but WITH a dedicated SidecarWriter. Its duration is
//     approximately pod-startup + exec + 5s.
//
// Assertions:
//   - test.Wait() returns well under the sidecar's lifetime (hard contract:
//     it does NOT wait for the sidecar) — bounded, not 86400s.
//   - (test - control) ≈ the 5s bound, proving the bounded wait actually
//     engaged rather than returning immediately.
//
// Requires a live cluster (build tag `live`); KUBECONFIG / in-cluster config and
// K8S_TEST_NAMESPACE select the target. See live_test.go:kubeClient.
func TestLiveSidecarLogStreamTimeout(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ns := liveTestNamespace()

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)

	// runOnce builds a fresh container whose sole sidecar outlives the main
	// command, runs a near-instant main command, and returns how long
	// process.Wait() took. When withSidecarWriter is true, a dedicated
	// per-sidecar writer is supplied so the 5s bounded wait engages.
	runOnce := func(t *testing.T, handle string, withSidecarWriter bool) time.Duration {
		t.Helper()

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel after the measurement so the still-blocked sidecar streaming
		// goroutine (io.Copy on a never-ending follow stream) unwinds promptly.
		defer cancel()

		fakeDBWorker := new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("live-sc11-worker")
		setupFakeDBContainer(fakeDBWorker, handle)

		worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
		worker.SetExecutor(executor)
		cleanupPod(t, clientset, ns, handle)

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
				Sidecars: []atc.SidecarConfig{
					{
						Name:  "slow-sidecar",
						Image: "busybox",
						// Outlives the main command: its log stream never EOFs,
						// so the sidecar streaming goroutine stays blocked.
						Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
					},
				},
			},
			&noopDelegate{},
		)
		if err != nil {
			t.Fatalf("FindOrCreateContainer: %v", err)
		}

		pio := runtime.ProcessIO{
			Stdout: &bytes.Buffer{},
			Stderr: &bytes.Buffer{},
		}
		if withSidecarWriter {
			pio.SidecarWriters = map[string]io.Writer{"slow-sidecar": &bytes.Buffer{}}
		}

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo main-done"},
		}, pio)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		start := time.Now()
		result, err := process.Wait(ctx)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("expected main command exit 0, got %d", result.ExitStatus)
		}
		return elapsed
	}

	stamp := time.Now().Format("150405")

	control := runOnce(t, "live-sc11-ctl-"+stamp, false)
	t.Logf("control  (no SidecarWriter)        Wait() = %s  (≈ startup + exec)", control)

	test := runOnce(t, "live-sc11-test-"+stamp, true)
	t.Logf("test     (SidecarWriter, slow sc)   Wait() = %s  (≈ startup + exec + 5s bound)", test)

	// Primary SC-11 contract: a sidecar that outlives main MUST NOT make
	// Wait() block for the sidecar's lifetime. With the 5s bound this is
	// ~startup+5s; without it, Wait() would block ~86400s.
	const hangBudget = 25 * time.Second
	if test >= hangBudget {
		t.Fatalf("SC-11 violated: Wait() took %s (>= %s) — it appears to wait for the sidecar instead of bounding to 5s",
			test, hangBudget)
	}

	// Secondary: the bounded wait actually engaged. Subtracting the control
	// run factors out pod-startup time, isolating the ~5s bound.
	delta := test - control
	t.Logf("delta (test - control) = %s  (expected ≈ 5s bounded wait)", delta)
	if delta < 3*time.Second {
		t.Fatalf("expected the ~5s sidecar-log bounded wait to add >= 3s over the control run, but delta was %s "+
			"(control=%s test=%s) — the bounded wait may not have engaged", delta, control, test)
	}
	if delta > 9*time.Second {
		t.Fatalf("bounded-wait delta %s exceeds what the 5s bound allows (control=%s test=%s) — "+
			"either the bound was not honored or pod-startup variance was unexpectedly high", delta, control, test)
	}
}
