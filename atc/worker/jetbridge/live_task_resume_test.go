//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveTaskResume verifies that a task step survives a web restart: web 1's
// exec stream is severed mid-command (its connection closes, as when the web
// process dies during a rolling upgrade), the task pod and the command keep
// running, and a second web — a fresh Worker/Container for the same handle —
// takes over via the attachOrRun path, receives the remaining output, and
// reports the command's real exit code without the command ever restarting.
//
// The command is one the old HUP shield could not keep: it runs hupreload
// (testdata/hupreload), which takes SIGHUP for itself the way dockerd does,
// so its child starts with SIGHUP at the default action. Web 1's exec stream
// is severed and its session leader killed, and the kernel SIGHUPs the exec
// session's foreground process group; only a command detached into a session
// of its own lives through that, and the child's "child-done" in web 2's
// output is the proof. Web 1's own tail — in that group, SIGHUP at default —
// is the witness that the hangup really reached it.
//
// Note what web 1's death is NOT modelled as: cancelling the step's context.
// A real restart never does that. A build's context is rooted at
// context.Background() (atc/builds/tracker.go), and Engine.Drain closes the
// release channel rather than cancelling — engineBuild.Run returns on that
// arm and the plan's goroutine simply dies with the process, so the step's
// context is never marked done. Cancelling here would model an abort, which
// legitimately tears the pause pod down (process.go), and the test would be
// asserting the opposite of the runtime's actual contract. So web 1's exec
// transport is wrapped (severingExecutor): it closes the stream on its own
// context and then does nothing more, as a dead web does nothing more.
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
	// The command writes its start marker once each time it begins, so a
	// restart would show up as a second "run-began" line in the replayed log.
	// It waits for hupreload, which the test delivers once the pod is up, and
	// then runs a child that outlives web 1. The TTY spec mirrors production
	// task steps (task_step.go) — with a TTY, the severed session HUPs the
	// exec session for real.
	processSpec := runtime.ProcessSpec{
		Path: "sh",
		Args: []string{"-c", "echo run-began; " +
			"while [ ! -x " + liveHupReloadPath + " ]; do sleep 1; done; " +
			"exec " + liveHupReloadPath + " sh -c 'echo child-began; sleep 20; echo child-done; exit 4'"},
		TTY: &runtime.TTYSpec{
			WindowSize: runtime.WindowSize{Columns: 500, Rows: 500},
		},
	}
	inPod := podCommands{t: t, namespace: cfg.Namespace, name: handle, executor: liveExecutor(t)}

	// --- web 1: start the task, then die mid-command ---

	var web1Exec *severingExecutor
	worker1 := setupLiveWorkerWithExecutor(t, func(inner jetbridge.PodExecutor) jetbridge.PodExecutor {
		web1Exec = &severingExecutor{PodExecutor: inner}
		return web1Exec
	})
	container1, _, err := worker1.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec,
		nil,
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
	// few seconds on first run), deliver hupreload, and wait for its child.
	waitForOutput := func(marker string) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for !strings.Contains(web1Out.String(), marker) {
			select {
			case waitErr := <-web1Done:
				t.Fatalf("web1 Wait returned before %q: err=%v output=%q",
					marker, waitErr, web1Out.String())
			case <-time.After(500 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q; web1 output: %q", marker, web1Out.String())
			}
		}
	}
	waitForOutput("run-began")
	inPod.deliverHupReload()
	waitForOutput("child-began")

	if got := inPod.supervisorTails(); got == 0 {
		t.Fatalf("web1's supervisor has no tail following the log; ps: %s", inPod.ps())
	}
	t.Logf("web1 saw the child start; severing its exec session")
	web1Exec.sever()

	// web1 is "gone": nothing reads web1Done from here on, exactly as the
	// engine's plan goroutine stops existing when the web process dies.
	// Everything below belongs to web2.

	// Closing the stream is not enough to end the session here. Measured
	// 2026-09-25 on concourse.home: with only the stream gone, web 1's
	// supervisor ran on to the command's exit, no SIGHUP reached anything,
	// and the old HUP shield passed this test. So end the session the way a
	// hangup or the runtime killing the exec does: its leader dies with the
	// pty still its controlling terminal, and the kernel SIGHUPs the
	// terminal's foreground process group.
	inPod.killSupervisorSessionLeader()

	// The hangup must have reached that group, or this test proves nothing
	// about SIGHUP: web 1's tail, SIGHUP at its default action, dies of it --
	// within seconds, not when the command finishes and a live supervisor
	// would have stopped it anyway.
	deadline := time.Now().Add(5 * time.Second)
	for inPod.supervisorTails() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("web1's tail outlived its session leader, so the hangup never "+
				"reached its process group; ps: %s", inPod.ps())
		}
		time.Sleep(500 * time.Millisecond)
	}
	if strings.Contains(inPod.run(nil, "sh", "-c", `cat /tmp/concourse-task-*/log`), "child-done") {
		t.Fatalf("the child finished before the hangup was confirmed; the test proves nothing")
	}

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

	// web2 replays the log from the start: exactly one "run-began" proves the
	// command was resumed, not restarted; "child-done" proves hupreload's
	// child, SIGHUP at its default action, lived through web1's hangup and ran
	// to completion.
	out := web2Out.String()
	if got := strings.Count(out, "run-began"); got != 1 {
		t.Fatalf("expected exactly 1 'run-began' marker (no restart), got %d (output: %q)", got, out)
	}
	if !strings.Contains(out, "child-done") || strings.Contains(out, "child killed") {
		t.Fatalf("expected hupreload's child to finish after web1's hangup, got: %q", out)
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

// severingExecutor closes the step command's exec stream on demand without
// ending the step's context, then returns nothing until that context ends:
// the connection a dead web held goes away, and the web does nothing after.
type severingExecutor struct {
	jetbridge.PodExecutor

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (e *severingExecutor) ExecInPod(
	ctx context.Context,
	namespace, podName, containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool,
	attrs jetbridge.ExecAttrs,
) error {
	if attrs.Purpose != "step-command" {
		return e.PodExecutor.ExecInPod(ctx, namespace, podName, containerName, command, stdin, stdout, stderr, tty, attrs)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()

	err := e.PodExecutor.ExecInPod(streamCtx, namespace, podName, containerName, command, stdin, stdout, stderr, tty, attrs)
	if streamCtx.Err() != nil && ctx.Err() == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (e *severingExecutor) sever() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
}

// liveHupReloadPath is where TestLiveTaskResume delivers hupreload in the pod.
const liveHupReloadPath = "/tmp/hupreload"

func liveExecutor(t *testing.T) jetbridge.PodExecutor {
	t.Helper()
	clientset, cfg := kubeClient(t)
	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	return jetbridge.NewSPDYExecutor(clientset, restConfig)
}

// podCommands runs the test's own commands in a step pod's main container,
// on a transport of their own.
type podCommands struct {
	t         *testing.T
	namespace string
	name      string
	executor  jetbridge.PodExecutor
}

func (p podCommands) run(stdin io.Reader, command ...string) string {
	p.t.Helper()
	// stdout and stderr arrive on separate goroutines.
	out := &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := p.executor.ExecInPod(ctx, p.namespace, p.name, "main", command, stdin, out, out, false,
		jetbridge.ExecAttrs{Purpose: "live-test"}); err != nil {
		p.t.Fatalf("exec %v in %s: %v (output: %q)", command, p.name, err, out.String())
	}
	return out.String()
}

// deliverHupReload builds testdata/hupreload for the pod's architecture and
// installs it at liveHupReloadPath, atomically, so the waiting command never
// runs half a binary.
func (p podCommands) deliverHupReload() {
	p.t.Helper()
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[strings.TrimSpace(p.run(nil, "uname", "-m"))]
	if goarch == "" {
		p.t.Fatalf("no GOARCH for the pod's machine %q", p.run(nil, "uname", "-m"))
	}
	dir := p.t.TempDir()
	binary := filepath.Join(dir, "hupreload")
	build := exec.Command("go", "build", "-o", binary, "./testdata/hupreload")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch, "TMPDIR="+dir)
	if out, err := build.CombinedOutput(); err != nil {
		p.t.Fatalf("building hupreload: %v\n%s", err, out)
	}
	f, err := os.Open(binary)
	if err != nil {
		p.t.Fatalf("opening hupreload: %v", err)
	}
	defer f.Close()
	p.run(f, "sh", "-c", `cat >"$1.tmp" && chmod 755 "$1.tmp" && mv "$1.tmp" "$1"`, "deliver", liveHupReloadPath)
}

// killSupervisorSessionLeader SIGKILLs the exec session leader that runs the
// one supervisor in the pod: the parent of the tail following its log.
func (p podCommands) killSupervisorSessionLeader() {
	p.t.Helper()
	out := p.run(nil, "sh", "-c", `for d in /proc/[0-9]*; do
  c=$(tr '\0' ' ' <"$d/cmdline" 2>/dev/null)
  case "$c" in "tail -n +1 -f /tmp/concourse-task-"*)
    read -r stat <"$d/stat"
    set -- ${stat##*) }
    kill -9 "$2" && echo "killed $2"
  esac
done`)
	if strings.Count(out, "killed") != 1 {
		p.t.Fatalf("did not kill exactly one supervisor session leader: %q; ps: %s", out, p.ps())
	}
}

func (p podCommands) ps() string {
	return p.run(nil, "ps")
}

// supervisorTails counts the live tails that supervisors run to follow a
// task log. A dead one is a zombie, which ps shows without its arguments.
func (p podCommands) supervisorTails() int {
	return strings.Count(p.ps(), "tail -n +1 -f /tmp/concourse-task-")
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
