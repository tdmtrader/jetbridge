//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
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
// The exec-mode bounded wait engages for either a dedicated SidecarWriter
// or the fallback stdout stream. Both are exercised with a real sidecar that
// outlives the main command. A third run, without a sidecar, is the control.
//
// Each run records the actual main-done output before measuring the remaining
// wait, so independent pod startup durations do not distort the difference.
// The supervisor merges command output into stdout; it must remain observed.
//
// Both streaming configurations must return within the original 25-second
// total bound and add 3–9 seconds over the control's post-command duration.
// These bounds prove the five-second wait engages without waiting for the
// sidecar's full 86400-second lifetime.
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

	// runOnce builds a fresh real container, optionally with a long-running
	// sidecar, and observes both total and post-command wait durations.
	type measurement struct {
		wait          time.Duration
		afterMainDone time.Duration
	}
	runOnce := func(t *testing.T, handle string, withSidecar, withSidecarWriter bool) measurement {
		t.Helper()

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel after the measurement so the still-blocked sidecar streaming
		// goroutine (io.Copy on a never-ending follow stream) unwinds promptly.
		defer cancel()
		database := useLiveJetbridgeDB(t)
		dbWorker, err := persistNamedWorker(database, "live-sc11-worker")
		if err != nil {
			t.Fatalf("persisting worker: %v", err)
		}

		worker := jetbridge.NewWorker(dbWorker, clientset, *cfg, jetbridge.WorkerDeps{
			Executor: executor,
		})
		cleanupPod(t, clientset, ns, handle)

		var sidecars []atc.SidecarConfig
		if withSidecar {
			sidecars = []atc.SidecarConfig{{
				Name:  "slow-sidecar",
				Image: "busybox",
				// Its real follow stream cannot reach EOF before main completes.
				Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
			}}
		}

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
				Sidecars:  sidecars,
			},
			nil,
		)
		if err != nil {
			t.Fatalf("FindOrCreateContainer: %v", err)
		}
		requirePersistedContainer(t, database, "live-sc11-worker", handle)

		mainOutput := new(sidecarMainCompletionWriter)
		pio := runtime.ProcessIO{
			Stdout: mainOutput,
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
		finished := time.Now()
		elapsed := finished.Sub(start)
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("expected main command exit 0, got %d", result.ExitStatus)
		}
		mainDone := mainOutput.completionTime()
		if mainDone.IsZero() {
			t.Fatal("main command completed without its main-done output")
		}
		return measurement{wait: elapsed, afterMainDone: finished.Sub(mainDone)}
	}

	stamp := time.Now().Format("20060102-150405.000000000")

	var control measurement
	t.Run("control", func(t *testing.T) {
		control = runOnce(t, "live-sc11-control-"+stamp, false, false)
	})
	t.Logf("control (no sidecar) Wait() = %s; after main-done = %s", control.wait, control.afterMainDone)

	for _, tc := range []struct {
		name      string
		dedicated bool
	}{
		{"with-sidecar-writer", true},
		{"with-fallback-sidecar-writer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := runOnce(t, "live-sc11-"+tc.name+"-"+stamp, true, tc.dedicated)
			t.Logf("writer Wait() = %s; after main-done = %s", writer.wait, writer.afterMainDone)

			// Keep the original total bound, including pod startup and exec.
			const hangBudget = 25 * time.Second
			if writer.wait >= hangBudget {
				t.Fatalf("SC-11 violated: Wait() took %s (>= %s) — it appears to wait for the sidecar instead of bounding to 5s",
					writer.wait, hangBudget)
			}

			// Exclude each pod's own startup, retaining the original delta bounds.
			delta := writer.afterMainDone - control.afterMainDone
			t.Logf("delta (writer - control) = %s (expected ≈ 5s bounded wait)", delta)
			if delta < 3*time.Second {
				t.Fatalf("expected the ~5s sidecar-log bounded wait to add >= 3s over the control run, but delta was %s "+
					"(control=%s writer=%s) — the bounded wait may not have engaged", delta, control.afterMainDone, writer.afterMainDone)
			}
			if delta > 9*time.Second {
				t.Fatalf("bounded-wait delta %s exceeds what the 5s bound allows (control=%s writer=%s)",
					delta, control.afterMainDone, writer.afterMainDone)
			}
		})
	}
}

// Observe the real command's output without changing its execution or stream.
// Wait and stdout forwarding can run on different goroutines.
type sidecarMainCompletionWriter struct {
	mu        sync.Mutex
	output    bytes.Buffer
	completed time.Time
}

func (w *sidecarMainCompletionWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(p)
	if w.completed.IsZero() && bytes.Contains(w.output.Bytes(), []byte("main-done")) {
		w.completed = time.Now()
	}
	return n, err
}

func (w *sidecarMainCompletionWriter) completionTime() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.completed
}
