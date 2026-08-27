package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// localShellAdapter is a REAL PodExecutor that actually runs the command.
//
// It is the second application of the recipe in coverage_matrix.md Addendum 2,
// aimed at the `expectSupervisedExec` family. Those nine call sites assert
// three things about a string the runtime never gets to execute:
//
//	command[0] == "sh" && command[1] == "-c"
//	command[2] contains `'/bin/sh' '-c' 'echo hello'`
//	command[2] contains `trap '' HUP`
//
// None of that proves the command runs, that the quoting survives a command
// with spaces or shell operators, that the exit code comes back, or that the
// supervisor does the thing it exists to do. Running the command proves all
// four.
//
// The named behavioral difference: the command runs in this process's shell
// rather than in a pod. It records nothing.
type localShellAdapter struct{}

func (localShellAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	_ io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	// Run in its own process group, and tear the whole group down afterwards.
	//
	// The in-pod task supervisor backgrounds a `tail -f` on its log and kills
	// it on the way out. Inside a real pod that cleanup is belt-and-braces:
	// the pod dies and takes any survivor with it. Here the "pod" is this
	// host, so a survivor survives for real — and one leaks per supervised
	// scenario. Measured: 164 orphaned `tail -f` processes after a few full
	// suite runs, at which point the machine is loaded enough that the
	// supervisor's own kill/wait sequence starts losing races and a scenario
	// fails with the supervisor's exit-255 fallback.
	//
	// That failure looks like a flaky test and is really an exhausted host, so
	// it is fixed at the source rather than by clearing /tmp between runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	if cmd.Process != nil {
		// Negative pid signals the group. The leader is already gone; this is
		// for whatever it backgrounded.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Surface a non-zero exit the way the SPDY executor does, so the runtime's
	// exit-code extraction is on the real path (PE-08's last clause).
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &jetbridge.ExecExitError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

// TaskCommandDefinitions expresses what a task command does, rather than what
// string was assembled to do it.
func TaskCommandDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, TaskCluster](
			"a jetbridge worker that really runs task commands",
			[]string{"jetbridge-db", "task-workspace"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (TaskCluster, error) {
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return TaskCluster{}, fmt.Errorf("task-workspace resource is %T", res.Get("task-workspace"))
				}
				cluster, err := NewCluster(res, WithExecutor(localShellAdapter{}))
				if err != nil {
					return TaskCluster{}, err
				}
				namespace, clientset, worker := cluster.Namespace, cluster.Clientset, cluster.Worker

				return TaskCluster{
					Namespace: namespace,
					Worker:    worker,
					Clientset: clientset,
					Ctx:       context.Background(),
					Workspace: workspace,
				}, nil
			},
		),

		brine.DefineMap[TaskCluster, TaskOutcome](
			"a task {string} runs {string}",
			func(in TaskCluster, p brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				handle, _ := p.GetString(0)
				script, ok := p.GetString(1)
				if !ok {
					return TaskOutcome{}, fmt.Errorf("expected a handle and a command")
				}
				return runTask(in, handle, expandWorkspace(script, in.Workspace), true)
			},
		),

		// The reason the supervisor exists: a web restart re-execs the same
		// command on the same container, and must resume rather than start a
		// second copy in a dirty workspace.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web restarts and the task is re-executed",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				return runTask(in.Cluster, in.Handle, in.Script, false)
			},
		),

		// The other half of the supervisor's keying rule: state is derived
		// from the process ID AND a hash of the command, so a DIFFERENT
		// command on the same container gets fresh state and actually runs
		// (supervisor.go's "e.g. a hijack shell" case).
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web restarts and a different command {string} is executed",
			func(in TaskOutcome, p brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				script, ok := p.GetString(0)
				if !ok {
					return TaskOutcome{}, fmt.Errorf("expected a command parameter")
				}
				return runTask(in.Cluster, in.Handle, expandWorkspace(script, in.Cluster.Workspace), false)
			},
		),

		brine.DefineCheck[TaskOutcome](
			"the build log contains {string}",
			func(in TaskOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a text parameter")
				}
				if in.Err != nil && in.ExitStatus == 0 {
					return fmt.Errorf("the task failed before it could log: %v", in.Err)
				}
				if !strings.Contains(in.Log, want) {
					return fmt.Errorf("expected the build log to contain %q, got %q", want, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[TaskOutcome](
			"the build log contains {string} exactly {int} time(s)",
			func(in TaskOutcome, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetString(0)
				times, ok := p.GetInt(1)
				if !ok {
					return fmt.Errorf("expected a text and a count")
				}
				got := strings.Count(in.Log, want)
				if got != times {
					return fmt.Errorf("expected %q %d time(s) in the build log, found %d — full log: %q",
						want, times, got, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[TaskOutcome](
			"the task exits {int}",
			func(in TaskOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected an exit status parameter")
				}
				if in.ExitStatus != want {
					return fmt.Errorf("expected exit %d, got %d (err: %v, log: %q)",
						want, in.ExitStatus, in.Err, in.Log)
				}
				return nil
			},
		),
	}
}

// runTask drives one exec-mode task to completion and returns what the
// consumer saw: the build log, the exit status, any error.
func runTask(in TaskCluster, handle, script string, fresh bool) (TaskOutcome, error) {
	container, _, err := in.Worker.FindOrCreateContainer(
		in.Ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			Dir:       "/workdir",
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			Type:      db.ContainerTypeTask,
		},
		&noopDelegate{},
	)
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("find or create container %q: %w", handle, err)
	}

	log := new(bytes.Buffer)
	// Stdin must be nil for the step to be supervised (process.go supervised()).
	process, err := container.Run(in.Ctx,
		runtime.ProcessSpec{
			ID:   handle,
			Path: "/bin/sh",
			Args: []string{"-c", script},
		},
		runtime.ProcessIO{Stdout: log, Stderr: log},
	)
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("run task %q: %w", handle, err)
	}

	if err := markPodRunning(in, handle); err != nil {
		return TaskOutcome{}, err
	}

	result, waitErr := process.Wait(in.Ctx)
	return TaskOutcome{
		Cluster: in, Handle: handle, Script: script,
		Log: log.String(), ExitStatus: result.ExitStatus, Err: waitErr,
	}, nil
}

func markPodRunning(in TaskCluster, handle string) error {
	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(in.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod %q: %w", handle, err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod status: %w", err)
	}
	return nil
}

// expandWorkspace lets a scenario name a scratch path without hard-coding one,
// so two scenarios running the same command do not share supervisor state.
func expandWorkspace(script string, w TaskWorkspace) string {
	return strings.ReplaceAll(script, "$WORKSPACE", w.Dir)
}

// TaskWorkspaceResourceDefinition gives each scenario its own scratch dir.
func TaskWorkspaceResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{
		Name:  "task-workspace",
		Scope: brine.ScopeScenario,
		Factory: func(map[string]any) (any, error) {
			dir, err := os.MkdirTemp("", "brine-task")
			if err != nil {
				return nil, fmt.Errorf("create task workspace: %w", err)
			}
			return TaskWorkspace{Dir: dir}, nil
		},
		Disposer: func(value any) error {
			w, ok := value.(TaskWorkspace)
			if !ok {
				return fmt.Errorf("task-workspace disposer got %T", value)
			}
			return os.RemoveAll(w.Dir)
		},
	}
}
