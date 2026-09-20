package jetbridge

// Closing a resource step's SPDY stream does not stop the command on the
// other end of it: `git clone` keeps running in the pause pod long after the
// build that wanted it was aborted. resource_process.go gives each invocation
// its own process group and a state directory, and execProcess.Wait is where
// that group is actually killed -- on a deferred path, so that it runs on
// whichever return the cancelled step takes.
//
// resource_process_test.go proves the two shell scripts do what they say, by
// running them; it carries no build tag, but skips itself at runtime
// (resource_process_test.go:18) because the cancel script reads /proc. The Go
// plumbing between them -- that Wait notices the cancellation at all and
// issues the cancel exec for THIS invocation's state directory -- had no test
// that runs on a developer machine. This is that test, and it is
// platform-independent because it stops at the exec boundary: it discriminates
// the fix via the cancel exec alone. The last two cases cover the ctx.Err()
// substitution next to it in process.go: a cancellation replaces what a
// torn-down transport said, never an exit the command actually reached.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// recordedExec is one call the process made on the exec transport.
type recordedExec struct {
	purpose string
	command []string
}

// cancellingExecutor stands in for the SPDY transport. The step's own command
// is where the build gets aborted: onStepCommand runs while the command is
// "in flight", and then the transport reports the way a torn-down stream
// does. Every call is recorded so the test can say which execs were issued,
// with which argv.
type cancellingExecutor struct {
	onStepCommand func()
	// stepCommandErr is what the transport returns for the step's command.
	// A cancelled exec can come back as almost anything, including nil.
	stepCommandErr error

	mu    sync.Mutex
	execs []recordedExec
}

func (e *cancellingExecutor) ExecInPod(_ context.Context, _, _, _ string, command []string,
	_ io.Reader, _ io.Writer, _ io.Writer, _ bool, attrs ExecAttrs) error {
	e.mu.Lock()
	e.execs = append(e.execs, recordedExec{purpose: attrs.Purpose, command: append([]string(nil), command...)})
	e.mu.Unlock()

	if attrs.Purpose == "step-command" {
		if e.onStepCommand != nil {
			e.onStepCommand()
		}
		return e.stepCommandErr
	}
	return nil
}

func (e *cancellingExecutor) recorded() []recordedExec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recordedExec(nil), e.execs...)
}

func (e *cancellingExecutor) withPurpose(purpose string) []recordedExec {
	var out []recordedExec
	for _, rec := range e.recorded() {
		if rec.purpose == purpose {
			out = append(out, rec)
		}
	}
	return out
}

// resourceStateDir picks the invocation's state directory out of an argv. It
// is generated per invocation by cancellableResourceCommand, so it is the one
// thing that ties a cancel exec to the command it is meant to stop.
func resourceStateDir(command []string) string {
	for _, arg := range command {
		if strings.HasPrefix(arg, "/tmp/concourse-resource-") {
			return arg
		}
	}
	return ""
}

func newResourceProcess(t *testing.T, clientset *fake.Clientset, executor PodExecutor, containerType db.ContainerType) *execProcess {
	t.Helper()
	const namespace = "resource-cancel-ns"
	config := NewConfig(namespace, "")
	container := &Container{
		handle:        "resource-cancel-handle",
		podName:       "resource-cancel-pod",
		metadata:      db.ContainerMetadata{Type: containerType},
		containerSpec: runtime.ContainerSpec{Type: containerType},
		clientset:     clientset,
		config:        config,
		properties:    map[string]string{},
	}
	return newExecProcess("proc-1", "resource-cancel-pod", clientset, config, container, executor,
		runtime.ProcessSpec{Path: "/opt/resource/in", Args: []string{"/tmp/build/get"}},
		runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
}

func resourceCancelPod(namespace string) *fake.Clientset {
	return fake.NewSimpleClientset(sidecarPausePod("resource-cancel-pod", namespace))
}

func TestExecProcessCancelsTheResourceCommandWhenTheStepIsCancelled(t *testing.T) {
	const namespace = "resource-cancel-ns"

	t.Run("a cancelled get step stops its own invocation and reports the cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		executor := &cancellingExecutor{
			onStepCommand: cancel,
			// What a torn-down SPDY stream typically produces. The point of
			// the wiring is that the step's answer does not depend on it.
			stepCommandErr: fmt.Errorf("unable to upgrade connection: context canceled"),
		}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeGet)

		result, err := process.Wait(ctx)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait() error = %v, want it to report context.Canceled", err)
		}
		if result != (runtime.ProcessResult{}) {
			t.Errorf("Wait() result = %+v, want the zero result: a cancelled step has no exit status", result)
		}

		stepCommands := executor.withPurpose("step-command")
		if len(stepCommands) != 1 {
			t.Fatalf("the step's command was exec'd %d time(s), want 1", len(stepCommands))
		}
		state := resourceStateDir(stepCommands[0].command)
		if state == "" {
			t.Fatalf("the step's command carries no resource state directory: %v", stepCommands[0].command)
		}

		cancels := executor.withPurpose("cancel-resource")
		if len(cancels) != 1 {
			t.Fatalf("the resource command was cancelled %d time(s), want exactly 1; execs issued: %+v",
				len(cancels), executor.recorded())
		}
		if got := resourceStateDir(cancels[0].command); got != state {
			t.Errorf("the cancel exec targets state %q, want this invocation's %q", got, state)
		}
		if !strings.Contains(strings.Join(cancels[0].command, " "), "cancel-resource") {
			t.Errorf("the cancel exec does not run the cancel script: %v", cancels[0].command)
		}
	})

	// Same wiring, but the transport happened to report success. A step whose
	// context is gone must not come back as a completed get, whatever the
	// stream made of it.
	t.Run("a cancelled get step whose transport reported success still reports the cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		executor := &cancellingExecutor{onStepCommand: cancel}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeGet)

		result, err := process.Wait(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait() error = %v, want it to report context.Canceled", err)
		}
		if result != (runtime.ProcessResult{}) {
			t.Errorf("Wait() result = %+v, want the zero result", result)
		}
		if n := len(executor.withPurpose("cancel-resource")); n != 1 {
			t.Errorf("the resource command was cancelled %d time(s), want exactly 1", n)
		}
	})

	// The control: nothing was cancelled, so nothing is killed. Without this
	// arm the assertions above would be satisfied by a Wait that cancelled
	// every resource step it ran.
	t.Run("a get step that runs to completion is not cancelled", func(t *testing.T) {
		executor := &cancellingExecutor{}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeGet)

		result, err := process.Wait(context.Background())
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		if result.ExitStatus != 0 {
			t.Errorf("ExitStatus = %d, want 0", result.ExitStatus)
		}
		if n := len(executor.withPurpose("cancel-resource")); n != 0 {
			t.Errorf("a completed get issued %d cancel exec(s), want 0", n)
		}
	})

	// A task step is stopped by deleting its pause pod, not by the resource
	// process group, and never gets the wrapper or the cancel exec.
	t.Run("a cancelled task step is not given the resource cancel path", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// The torn-down transport reports something that is not the
		// cancellation; the step still reports its cancellation.
		executor := &cancellingExecutor{onStepCommand: cancel, stepCommandErr: io.ErrUnexpectedEOF}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeTask)

		if _, err := process.Wait(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Wait() error = %v, want it to report context.Canceled", err)
		}
		if n := len(executor.withPurpose("cancel-resource")); n != 0 {
			t.Errorf("a cancelled task issued %d cancel exec(s), want 0", n)
		}
		stepCommands := executor.withPurpose("step-command")
		if len(stepCommands) != 1 {
			t.Fatalf("the step's command was exec'd %d time(s), want 1", len(stepCommands))
		}
		if state := resourceStateDir(stepCommands[0].command); state != "" {
			t.Errorf("a task command was wrapped in the resource process group: %v", stepCommands[0].command)
		}
	})

	// The transport selects between a completed stream and a done context,
	// so a command can finish in the same instant the step is cancelled. An
	// exit the command actually reached is its exit; only a transport that
	// reported the tear-down itself is rendered as the cancellation.
	t.Run("a command that exited 0 as the step was cancelled keeps its exit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		executor := &cancellingExecutor{onStepCommand: cancel}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeTask)

		result, err := process.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait() = %v, want the command's own exit", err)
		}
		if result.ExitStatus != 0 {
			t.Errorf("ExitStatus = %d, want 0", result.ExitStatus)
		}
	})

	t.Run("a command that exited non-zero as the step was cancelled keeps its exit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		executor := &cancellingExecutor{onStepCommand: cancel, stepCommandErr: &ExecExitError{ExitCode: 3}}
		process := newResourceProcess(t, resourceCancelPod(namespace), executor, db.ContainerTypeTask)

		result, err := process.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait() = %v, want the command's own exit", err)
		}
		if result.ExitStatus != 3 {
			t.Errorf("ExitStatus = %d, want 3", result.ExitStatus)
		}
	})
}
