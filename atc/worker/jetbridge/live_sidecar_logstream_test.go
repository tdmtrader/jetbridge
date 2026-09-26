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
// The exec-mode bounded wait (execProcess.Wait, process.go) only engages when
// ProcessIO.SidecarWriters has a dedicated writer for the sidecar. The sidecar
// here runs effectively forever (sleep 86400), so its log stream never EOFs and
// the per-sidecar streaming goroutine blocks in io.Copy. Without the 5s bound,
// process.Wait() would block for the sidecar's entire lifetime; with it, Wait()
// must return within ~5s of the (fast) main command completing.
//
// Strategy: two runs, each measuring only what happens AFTER the main
// command's output reaches a timestamping step stdout -- never pod startup.
//   - "control": no sidecar at all. Its gap (output to Wait() returning) is
//     the exec path's own tail: the supervisor's exit status, stream close,
//     API round trips. That tail is 2-3s from a workstation.
//   - "with-sidecar-writer": the long-running sidecar with a dedicated
//     SidecarWriter. Its gap is the same tail plus the bounded wait.
//
// The tail is not steady -- 2.0s to 3.2s across consecutive runs, in steps
// of about a second -- so the difference of two gaps is only the bounded
// wait to within that noise, and the assertions leave room for it.
//
// This used to subtract the two runs' whole Wait() durations. Each included
// its own pod's startup, so the delta carried the difference between two pod
// starts: on a loaded node a slow control start (9.6s against a usual 4.7s,
// k8s-live-tests #745 attempt 1) pushed it to 11s and failed the upper bound
// with the bound working as specified. A single run's gap is not enough
// either, and nor is an exact difference of gaps: the noisy tail put one
// difference at 2.9s with the bound engaged.
//
// Assertions:
//   - Wait() returns at all, inside a 3-minute deadline (hard contract: it
//     does NOT wait for the sidecar's 86400s).
//   - engaged: the writer gap is >= 4.5s, since a 5s wait that starts after
//     the main command's output can only lengthen it; and it exceeds the
//     control gap by >= 1.5s, so a slow tail alone cannot pass for it.
//   - bounded: the writer gap is <= 15s -- 5s plus a generous tail. A
//     Wait() that does not bound at all instead fails at the deadline.
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

	// runOnce runs a near-instant main command in a fresh container and
	// returns how long Wait() took to return after the command's output
	// arrived. withSidecar adds the never-ending sidecar and its writer.
	runOnce := func(t *testing.T, handle string, withSidecar bool) time.Duration {
		t.Helper()

		// A Wait() that honours no bound blocks for the sidecar's 86400s;
		// the deadline turns that into a failure instead of a hung tier.
		// Cancelling after the measurement also unwinds the still-blocked
		// sidecar streaming goroutine (io.Copy on a never-ending follow
		// stream) promptly.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

		spec := runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		}
		stdout := &firstWriteClock{}
		pio := runtime.ProcessIO{Stdout: stdout, Stderr: &bytes.Buffer{}}
		if withSidecar {
			spec.Sidecars = []atc.SidecarConfig{
				{
					Name:  "slow-sidecar",
					Image: "busybox",
					// Outlives the main command: its log stream never EOFs,
					// so the sidecar streaming goroutine stays blocked.
					Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
				},
			}
			pio.SidecarWriters = map[string]io.Writer{"slow-sidecar": &bytes.Buffer{}}
		}

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			spec,
			nil,
		)
		if err != nil {
			t.Fatalf("FindOrCreateContainer: %v", err)
		}
		requirePersistedContainer(t, database, "live-sc11-worker", handle)

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo main-done"},
		}, pio)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		start := time.Now()
		result, err := process.Wait(ctx)
		returned := time.Now()
		if err != nil {
			t.Fatalf("SC-11: Wait() failed after %s: %v -- a Wait() that waits for the sidecar instead of bounding to 5s ends here at the deadline",
				returned.Sub(start), err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("expected main command exit 0, got %d", result.ExitStatus)
		}
		mainDone, wrote := stdout.first()
		if !wrote {
			t.Fatalf("the main command's output never reached stdout (Wait() = %s); the gap after it cannot be measured", returned.Sub(start))
		}
		gap := returned.Sub(mainDone)
		t.Logf("Wait() = %s, of which %s after the main command's output", returned.Sub(start), gap)
		return gap
	}

	stamp := time.Now().Format("20060102-150405.000000000")

	var controlGap, writerGap time.Duration
	t.Run("control", func(t *testing.T) {
		controlGap = runOnce(t, "live-sc11-control-"+stamp, false)
	})
	t.Run("with-sidecar-writer", func(t *testing.T) {
		writerGap = runOnce(t, "live-sc11-writer-"+stamp, true)
	})
	if t.Failed() {
		return
	}

	t.Logf("gap after the main command's output: %s with the sidecar writer, %s without a sidecar (bounded wait ≈ 5s of the difference)", writerGap, controlGap)
	if writerGap < 4500*time.Millisecond || writerGap-controlGap < 1500*time.Millisecond {
		t.Fatalf("expected the ~5s sidecar-log bounded wait after the main command's output, but Wait() returned %s after it (%s without a sidecar) "+
			"-- the bounded wait may not have engaged", writerGap, controlGap)
	}
	if writerGap > 15*time.Second {
		t.Fatalf("Wait() returned %s after the main command's output, more than the 5s bound plus the exec tail allows", writerGap)
	}
}

// firstWriteClock records when anything is first written to it: here, the
// main command's output, which arrives as its exec finishes.
type firstWriteClock struct {
	mu    sync.Mutex
	at    time.Time
	wrote bool
}

func (c *firstWriteClock) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.wrote && len(p) > 0 {
		c.at, c.wrote = time.Now(), true
	}
	return len(p), nil
}

func (c *firstWriteClock) first() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at, c.wrote
}
