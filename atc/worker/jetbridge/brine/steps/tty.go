package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/creack/pty"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TTYDefinitions closes PE-08, which behavioral_runtime_spec_test.go covered
// with a pure spy: it recorded the ExecInPod call and asserted
// `calls[0].tty == true`. Every PodExecutor double in this package declares
// that parameter as `_`, so nothing observed what the flag DOES.
//
// The conversion is Addendum 2's. ttyAwareShellAdapter actually allocates a
// pseudo-terminal when the runtime asks for one, and the command under test is
// its own witness: `test -t 1` is the check every interactive tool makes, and
// it is the reason `fly hijack` needs a terminal at all. A shell that believes
// it is talking to a pipe disables line editing, job control and colour, which
// is precisely the broken hijack a user would report.

// ttyAwareShellAdapter is a REAL PodExecutor that honours the tty flag rather
// than recording it.
type ttyAwareShellAdapter struct{}

func (ttyAwareShellAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	_ io.Reader,
	stdout, stderr io.Writer,
	tty bool,
	_ jetbridge.ExecAttrs,
) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)

	if !tty {
		// No terminal: the two streams stay separate, as they do over SPDY.
		cmd.Stdout, cmd.Stderr = stdout, stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err := cmd.Run()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return execExitError(err)
	}

	// A terminal: pty.Start sets Setsid and Setctty itself, so Setpgid must
	// not also be set. A pty carries ONE stream, which is why stderr has
	// nowhere separate to go here either.
	f, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("allocate pty: %w", err)
	}
	defer func() { _ = f.Close() }()
	// Reading a pty after the child exits returns EIO on both macOS and
	// Linux; that is the normal end of stream, not a failure.
	_, _ = io.Copy(stdout, f)
	return execExitError(cmd.Wait())
}

func execExitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &jetbridge.ExecExitError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

// TerminalOutcome is what the command itself reported about its stdout.
type TerminalOutcome struct {
	Output string
	Err    error
}

func TTYDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, TerminalOutcome](
			"a step asks whether it has a terminal, with one attached",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (TerminalOutcome, error) {
				return runTerminalProbe(res, true)
			},
		),

		brine.DefineMapUsing[brine.Empty, TerminalOutcome](
			"a step asks whether it has a terminal, with none attached",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (TerminalOutcome, error) {
				return runTerminalProbe(res, false)
			},
		),

		// Keeps its own body: on a mismatch it explains what a shell that
		// believes it is talking to a pipe costs a hijack session, which is
		// the whole point of the scenario and more than want/got can say.
		brine.DefineCheck[TerminalOutcome](
			"the step reports {string}",
			func(in TerminalOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an expected-output parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("the step failed: %v", in.Err)
				}
				if !strings.Contains(in.Output, want) {
					return fmt.Errorf(
						"expected the command to report %q, it reported %q — a shell that believes "+
							"it is talking to a pipe gives `fly hijack` no line editing, no job "+
							"control and no colour", want, strings.TrimSpace(in.Output))
				}
				return nil
			},
		),
	}
}

// runTerminalProbe runs a command that reports whether its stdout is a
// terminal, through the real runtime, with the runtime deciding whether to ask
// for one. Stdin must be non-nil so the step is NOT supervised: the supervisor
// merges both streams into its log and replays that, which would mask the
// difference this scenario exists to show.
func runTerminalProbe(res brine.Resources, withTTY bool) (TerminalOutcome, error) {
	cluster, err := NewCluster(res, WithExecutor(ttyAwareShellAdapter{}))
	if err != nil {
		return TerminalOutcome{}, err
	}
	ctx, namespace := cluster.Ctx, cluster.Namespace
	clientset, worker := cluster.Clientset, cluster.Worker

	handle := "tty-probe-handle"
	if !withTTY {
		handle = "notty-probe-handle"
	}

	container, _, err := worker.FindOrCreateContainer(ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeGet},
		runtime.ContainerSpec{
			TeamID:    1,
			Dir:       "/tmp/build/get",
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			Type:      db.ContainerTypeGet,
		},
		&noopDelegate{},
	)
	if err != nil {
		return TerminalOutcome{}, fmt.Errorf("find or create container: %w", err)
	}

	spec := runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "test -t 1 && echo terminal || echo pipe"},
	}
	if withTTY {
		spec.TTY = &runtime.TTYSpec{}
	}

	stdout := new(strings.Builder)
	process, err := container.Run(ctx, spec, runtime.ProcessIO{
		Stdin:  strings.NewReader("{}"),
		Stdout: stdout,
		Stderr: new(strings.Builder),
	})
	if err != nil {
		return TerminalOutcome{}, fmt.Errorf("run container: %w", err)
	}

	pods := clientset.CoreV1().Pods(namespace)
	pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return TerminalOutcome{}, fmt.Errorf("get pod: %w", err)
	}
	pod.Status.Phase = corev1.PodRunning
	if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		return TerminalOutcome{}, fmt.Errorf("update pod: %w", err)
	}

	_, waitErr := process.Wait(ctx)
	return TerminalOutcome{Output: stdout.String(), Err: waitErr}, nil
}
