//go:build live
// +build live

package jetbridge_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// TestLiveAgentProcessResume verifies that an AGENT step survives a web
// restart under the well-known "agent" process ID: the first web starts the
// command and is then ABANDONED mid-command (its exec session is never
// consulted again — from the pod's side this is indistinguishable from a web
// that has stopped mattering), and a second web — a fresh Worker/Container
// for the same handle — takes over via the attachOrRun path, replays the
// log, and reports the command's real exit code without the command ever
// restarting.
//
// Why not sever web1's session in-process? A context-cancel sever is a
// TERMINAL end for agent containers (killAgentOnTerminalEnd tears the tree
// down by design — see TestLiveAgentTerminalEndKill), and client-go's SPDY
// path does not honor rest.Config.Dial, so a transport-level kill can't be
// injected here. The supervisor's takeover contract is idempotent re-exec,
// which holds identically whether the old session died or still hangs; the
// genuinely-severed session is exercised by TestLiveTaskResume (same
// supervisor mechanics) and the process_test transport-sever specs.
//
// BINDING (finding F18): db.ContainerTypeAgent at BOTH FindOrCreateContainer
// sites — supervision is gated on the container type (supervised()), so a
// copy that kept ContainerTypeTask would exercise only the already-proven
// task path and pass vacuously.
//
// Also proves (finding F23): the step's flight output volume is readable
// while the agent is still mid-command, BEFORE the takeover web attaches —
// the read path timed-out/aborted agent steps depend on for measurement.
//
//	kubectl create ns jetbridge-agent-step-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-agent-step-test \
//	  go test -tags live -run '^TestLiveAgentProcessResume$' -v -count=1 \
//	  -timeout 5m ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-agent-step-test
func TestLiveAgentProcessResume(t *testing.T) {
	handle := "live-agent-resume-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	containerSpec := runtime.ContainerSpec{
		TeamID:    1,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
		Dir:       "/tmp/build/agent",
		// Flight output volume for the F23 readability assertion.
		Outputs: runtime.OutputPaths{"flight": "/tmp/build/agent/flight"},
	}
	// The command appends a start marker each time it begins, writes the F23
	// flight marker BEFORE its sleep, and exits 7. A restart would show up as
	// a second "started" line in the replayed log. The TTY spec mirrors
	// production agent steps (agent_step.go). The well-known ID "agent" is
	// the agent step's attachOrRun contract — the supervisor state dir is
	// keyed on it, so web2's byte-identical re-exec resolves to the same
	// state and resumes.
	processSpec := runtime.ProcessSpec{
		ID:   "agent",
		Path: "sh",
		Args: []string{"-c", "echo started; echo flight-data > /tmp/build/agent/flight/tripwire; sleep 20; echo finished; exit 7"},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}

	// --- web 1: start the agent, then abandon the session mid-command ---

	worker1, delegate1 := setupLiveWorker(t, handle)
	container1, mounts1, err := worker1.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeAgent},
		containerSpec,
		delegate1,
	)
	if err != nil {
		t.Fatalf("web1 FindOrCreateContainer: %v", err)
	}

	var flightVolume runtime.Volume
	for _, m := range mounts1 {
		if m.MountPath == "/tmp/build/agent/flight" {
			flightVolume = m.Volume
			break
		}
	}
	if flightVolume == nil {
		t.Fatal("no volume mount found for output path /tmp/build/agent/flight")
	}

	web1Out := &syncBuffer{}
	process1, err := container1.Run(ctx, processSpec, runtime.ProcessIO{
		Stdout: web1Out,
		Stderr: web1Out,
	})
	if err != nil {
		t.Fatalf("web1 Run: %v", err)
	}

	// Wait (in a goroutine we will abandon) actually starts the command.
	web1Done := make(chan error, 1)
	go func() {
		_, waitErr := process1.Wait(ctx)
		web1Done <- waitErr
	}()

	deadline := time.Now().Add(90 * time.Second)
	for !strings.Contains(web1Out.String(), "started") {
		select {
		case waitErr := <-web1Done:
			t.Fatalf("web1 Wait returned before the command started: err=%v output=%q",
				waitErr, web1Out.String())
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for command to start; web1 output: %q", web1Out.String())
		}
	}
	t.Logf("web1 saw the agent start; abandoning web1's session (web death)")

	// F23: the flight output must be readable mid-command, before the
	// takeover web attaches — the read path used to measure timed-out and
	// aborted agent steps.
	rc, err := flightVolume.StreamOut(ctx, "tripwire", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("flight output not readable mid-command (F23): %v", err)
	}
	tripwire, err := readSingleFileFromTarGz(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("reading flight tripwire stream: %v", err)
	}
	if !strings.Contains(tripwire, "flight-data") {
		t.Fatalf("expected 'flight-data' in flight tripwire, got: %q", tripwire)
	}
	t.Logf("F23: flight output readable mid-command, before takeover")

	// --- web 2: fresh worker takes over the same handle ---

	worker2, delegate2 := setupLiveWorker(t, handle)
	container2, _, err := worker2.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeAgent},
		containerSpec,
		delegate2,
	)
	if err != nil {
		t.Fatalf("web2 FindOrCreateContainer: %v", err)
	}

	// Attach on the well-known "agent" ID must report "no completion status"
	// so attachOrRun falls through to Run (the agent step's reattach flow).
	if _, err := container2.Attach(ctx, "agent", runtime.ProcessIO{}); err == nil {
		t.Fatalf("web2 Attach unexpectedly succeeded; want no-completion-status error")
	} else if !strings.Contains(err.Error(), "no completion status") {
		t.Fatalf("web2 Attach failed with unexpected error: %v", err)
	}

	web2Out := &syncBuffer{}
	process2, err := container2.Run(ctx, processSpec, runtime.ProcessIO{
		Stdout: web2Out,
		Stderr: web2Out,
	})
	if err != nil {
		t.Fatalf("web2 Run: %v", err)
	}

	result, err := process2.Wait(ctx)
	if err != nil {
		t.Fatalf("web2 Wait: %v (output: %q)", err, web2Out.String())
	}

	// The real exit code from the command started by web1's session.
	if result.ExitStatus != 7 {
		t.Fatalf("expected exit status 7 from the original command, got %d (output: %q)",
			result.ExitStatus, web2Out.String())
	}

	// web2 replays the log from the start: exactly one "started" proves the
	// agent was resumed, not restarted; "finished" proves it ran to
	// completion under the takeover session.
	out := web2Out.String()
	if got := strings.Count(out, "started"); got != 1 {
		t.Fatalf("expected exactly 1 'started' marker (no restart), got %d (output: %q)", got, out)
	}
	if !strings.Contains(out, "finished") {
		t.Fatalf("expected 'finished' in replayed output, got: %q", out)
	}

	// web2 must have recorded the exit status on the pod for future attaches.
	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod after completion: %v", err)
	}
	if got := pod.Annotations["concourse.ci/exit-status"]; got != "7" {
		t.Fatalf("expected exit-status annotation \"7\", got %q", got)
	}

	t.Logf("agent resumed across simulated web restart: exit=7, output=%q", out)
}

// TestLiveAgentTerminalEndKill proves the OTHER half of the severed-exec
// contract live: a dead-context sever (step timeout / build abort) is a
// TERMINAL end — nothing will ever re-attach — so the supervised agent
// process tree must be torn down instead of kept alive, and no exit code
// recorded (the command was killed, not run to completion). This exercises
// the busybox-compatible group-kill script against a real pod; its first
// iteration (`kill -TERM -- "-$pgid"`) was a silent no-op under dash and
// busybox builtins.
//
//	kubectl create ns jetbridge-agent-step-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-agent-step-test \
//	  go test -tags live -run '^TestLiveAgentTerminalEndKill$' -v -count=1 \
//	  -timeout 5m ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-agent-step-test
func TestLiveAgentTerminalEndKill(t *testing.T) {
	handle := "live-agent-kill-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	worker, delegate := setupLiveWorker(t, handle)
	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeAgent},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			Dir:       "/tmp/build/agent",
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	stepCtx, cancelStep := context.WithCancel(ctx)
	defer cancelStep()

	out := &syncBuffer{}
	process, err := container.Run(stepCtx, runtime.ProcessSpec{
		ID:   "agent",
		Path: "sh",
		Args: []string{"-c", "echo started; sleep 60; echo survived-terminal-end; exit 0"},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}, runtime.ProcessIO{Stdout: out, Stderr: out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait(stepCtx)
		waitDone <- waitErr
	}()

	deadline := time.Now().Add(90 * time.Second)
	for !strings.Contains(out.String(), "started") {
		select {
		case waitErr := <-waitDone:
			t.Fatalf("Wait returned before the command started: err=%v output=%q", waitErr, out.String())
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for command to start; output: %q", out.String())
		}
	}
	t.Logf("agent started; cancelling the step context (terminal end)")
	cancelStep()
	if waitErr := <-waitDone; waitErr == nil {
		t.Fatalf("Wait unexpectedly succeeded; want severed-session error")
	}

	// The supervised tree must die well before its 60s sleep would finish,
	// and no exit file may exist (killed, not completed). Probe the pod's
	// supervisor state directly.
	executor := jetbridge.NewSPDYExecutor(clientset, mustRestConfig(t, cfg))
	probe := `pid=$(cat /tmp/concourse-task-*/pid 2>/dev/null); ` +
		`if [ -z "$pid" ]; then echo NO-PID; ` +
		`elif kill -0 "$pid" 2>/dev/null; then echo RUNNER-ALIVE; else echo RUNNER-DEAD; fi; ` +
		`if ls /tmp/concourse-task-*/exit >/dev/null 2>&1; then echo HAS-EXIT; else echo NO-EXIT; fi`

	probeDeadline := time.Now().Add(30 * time.Second)
	var probeOut string
	for {
		var stdout, stderr strings.Builder
		err := executor.ExecInPod(ctx, cfg.Namespace, handle, "main",
			[]string{"sh", "-c", probe}, nil, &stdout, &stderr, false, jetbridge.ExecAttrs{})
		if err != nil {
			t.Fatalf("probe exec failed: %v (stderr: %s)", err, stderr.String())
		}
		probeOut = stdout.String()
		if strings.Contains(probeOut, "RUNNER-DEAD") || strings.Contains(probeOut, "NO-PID") {
			break
		}
		if time.Now().After(probeDeadline) {
			t.Fatalf("supervised agent tree still alive 30s after terminal-end sever (kill script no-op?): %q", probeOut)
		}
		time.Sleep(1 * time.Second)
	}
	if !strings.Contains(probeOut, "NO-EXIT") {
		t.Fatalf("exit file exists after terminal-end kill — command completed instead of dying: %q", probeOut)
	}
	if strings.Contains(out.String(), "survived-terminal-end") {
		t.Fatalf("command ran to completion despite terminal-end kill: %q", out.String())
	}

	t.Logf("terminal-end kill proven live: supervised tree dead, no exit recorded (probe: %q)", probeOut)
}

func mustRestConfig(t *testing.T, cfg *jetbridge.Config) *rest.Config {
	t.Helper()
	rc, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	return rc
}

// readSingleFileFromTarGz reads a gzipped tar stream (the StreamOut format)
// and returns the contents of its first regular file entry.
func readSingleFileFromTarGz(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(tr)
			if err != nil {
				return "", err
			}
			return string(body), nil
		}
	}
}
