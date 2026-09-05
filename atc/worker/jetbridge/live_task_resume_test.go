//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveTaskResume verifies that a task step survives a web restart: web 1
// stops waiting on its exec mid-command (simulating SIGTERM during a rolling
// upgrade), the task pod and the HUP-shielded command keep running, and a
// second web — a fresh Worker/Container for the same handle — takes over via
// the attachOrRun path, receives the remaining output, and reports the
// command's real exit code without the command ever restarting.
//
// Note what web 1's death is NOT modelled as: cancelling the step's context.
// A real restart never does that. A build's context is rooted at
// context.Background() (atc/builds/tracker.go), and Engine.Drain closes the
// release channel rather than cancelling — engineBuild.Run returns on that
// arm and the plan's goroutine simply dies with the process, so the step's
// context is never marked done. Cancelling here would model an abort, which
// legitimately tears the pause pod down (process.go), and the test would be
// asserting the opposite of the runtime's actual contract. The closest an
// in-process test can get is to abandon the result and stop reading, which
// leaves the same thing under test: web 2's re-exec resuming the running
// command instead of starting a second copy.
//
// Run against a THROWAWAY namespace (never cicd/concourse):
//
//	kubectl create ns jetbridge-resume-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-resume-test \
//	  go test -tags live -run '^TestLiveTaskResume$' -v -count=1 -timeout 5m \
//	  ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-resume-test
func TestLiveTaskResume(t *testing.T) {
	handle := "live-resume-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	containerSpec := runtime.ContainerSpec{
		TeamID:    1,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
	}
	// The command appends a start marker each time it begins, so a restart
	// would show up as a second "started" line in the replayed log. The TTY
	// spec mirrors production task steps (task_step.go) — with a TTY, the
	// severed session HUPs the supervisor for real, exercising the shield.
	processSpec := runtime.ProcessSpec{
		Path: "sh",
		Args: []string{"-c", "echo started; sleep 20; echo finished; exit 4"},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}

	// --- web 1: start the task, then die mid-command ---

	worker1, delegate1 := setupLiveWorker(t, handle)
	container1, _, err := worker1.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec,
		delegate1,
	)
	if err != nil {
		t.Fatalf("web1 FindOrCreateContainer: %v", err)
	}

	// web1Ctx stands in for the build context the first web would hold. It
	// stays live for the whole test: see the note above on why a restart
	// does not cancel it. The deferred cancel is only there so the abandoned
	// Wait cannot outlive the test.
	web1Ctx, cancelWeb1 := context.WithCancel(ctx)
	defer cancelWeb1()

	web1Out := &syncBuffer{}
	process1, err := container1.Run(web1Ctx, processSpec, runtime.ProcessIO{
		Stdout: web1Out,
		Stderr: web1Out,
	})
	if err != nil {
		t.Fatalf("web1 Run: %v", err)
	}

	web1Done := make(chan error, 1)
	go func() {
		_, waitErr := process1.Wait(web1Ctx)
		web1Done <- waitErr
	}()

	// Let the pod start and the command begin (image pull + init can take a
	// few seconds on first run), then sever the exec session.
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
	t.Logf("web1 saw the command start; abandoning its exec session")

	// web1 is "gone": nothing reads web1Done from here on, exactly as the
	// engine's plan goroutine stops existing when the web process dies.
	// Everything below belongs to web2.

	// The pod must survive web1's death.
	if _, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{}); err != nil {
		t.Fatalf("pod did not survive web1 death: %v", err)
	}

	// --- web 2: fresh worker takes over the same handle ---

	worker2, delegate2 := setupLiveWorker(t, handle)
	container2, _, err := worker2.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec,
		delegate2,
	)
	if err != nil {
		t.Fatalf("web2 FindOrCreateContainer: %v", err)
	}

	// Attach must report "no completion status" so attachOrRun falls
	// through to Run (mirroring the engine's reattach flow).
	if _, err := container2.Attach(ctx, handle, runtime.ProcessIO{}); err == nil {
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

	// The real exit code from the command started by web1.
	if result.ExitStatus != 4 {
		t.Fatalf("expected exit status 4 from the original command, got %d (output: %q)",
			result.ExitStatus, web2Out.String())
	}

	// web2 replays the log from the start: exactly one "started" proves the
	// command was resumed, not restarted; "finished" proves it ran to
	// completion after web1 died.
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
	if got := pod.Annotations["concourse.ci/exit-status"]; got != "4" {
		t.Fatalf("expected exit-status annotation \"4\", got %q", got)
	}

	t.Logf("task resumed across simulated web restart: exit=4, output=%q", out)
}

// syncBuffer is a goroutine-safe bytes.Buffer: the SPDY exec client writes
// stdout and stderr from separate goroutines, so sharing a bare buffer
// between them is a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
