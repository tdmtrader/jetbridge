package steps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

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
				cluster, err := NewCluster(res, WithExecutor(localExecutor{supervisorRoot: workspace.Dir}))
				if err != nil {
					return TaskCluster{}, err
				}
				return TaskCluster{
					ClusterReady: cluster.Ready(),
					Workspace:    workspace,
				}, nil
			},
		),

		Transform[TaskCluster, TaskOutcome](
			"a task {string} runs {string}",
			func(in TaskCluster, a Args) (TaskOutcome, error) {
				return runTask(in, a.String(0), expandWorkspace(a.String(1), in.Workspace), nil)
			},
		),

		// The reason the supervisor exists: a web restart re-execs the same
		// command on the same container, and must resume rather than start a
		// second copy in a dirty workspace.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web restarts and the task is re-executed",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				return runTask(in.Cluster, in.Handle, in.Script, nil)
			},
		),

		// The other half of the supervisor's keying rule: state is derived
		// from the process ID AND a hash of the command, so a DIFFERENT
		// command on the same container gets fresh state and actually runs
		// (supervisor.go's "e.g. a hijack shell" case).
		Transform[TaskOutcome, TaskOutcome](
			"the web restarts and a different command {string} is executed",
			func(in TaskOutcome, a Args) (TaskOutcome, error) {
				return runTask(in.Cluster, in.Handle, expandWorkspace(a.String(0), in.Cluster.Workspace), nil)
			},
		),

		CheckThat[TaskOutcome]("the supervisor state belongs to this task workspace",
			func(in TaskOutcome) error { return in.Cluster.Workspace.requireSupervisorState() }),

		CheckContains[TaskOutcome]("the build log contains {string}",
			"the build log",
			func(in TaskOutcome) (string, error) {
				if in.Err != nil && in.ExitStatus == 0 {
					return "", fmt.Errorf("the task failed before it could log: %v", in.Err)
				}
				return in.Log, nil
			}),

		// The count and the log that produced it: CheckIntFor routes the text
		// to the getter and takes the count as the expectation, and the detail
		// carries the log, which is where a second copy came from.
		CheckIntFor[TaskOutcome]("the build log contains {string} exactly {int} time(s)",
			"occurrences in the build log",
			func(in TaskOutcome, text string) (int, error) {
				return strings.Count(in.Log, text), nil
			},
			func(in TaskOutcome) string { return fmt.Sprintf("full log: %q", in.Log) }),

		// An unexpected exit status is only actionable alongside the error and
		// the log that produced it, which is what the detail funcs carry.
		CheckInt[TaskOutcome]("the task exits {int}",
			"the task's exit status",
			func(in TaskOutcome) (int, error) {
				return in.ExitStatus, in.Err
			},
			func(in TaskOutcome) string { return fmt.Sprintf("err: %v", in.Err) },
			func(in TaskOutcome) string { return fmt.Sprintf("log: %q", in.Log) }),

		Assert[TaskOutcome](
			"the recorded pod startup duration is at least {int} milliseconds",
			func(in TaskOutcome, args Args) error {
				want := args.Int(0)

				if in.Err != nil {
					return fmt.Errorf("expected a startup duration, the step failed with %q", in.Message)
				}
				if in.PodStartupDuration < float64(want) {
					return fmt.Errorf("expected a recorded startup duration of at least %dms, got %vms",
						want, in.PodStartupDuration)
				}
				return nil
			},
		),
	}
}

// findTaskContainer returns a fresh runtime object for the same persisted task.
func findTaskContainer(in TaskCluster, handle string) (runtime.Container, error) {
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
		return nil, fmt.Errorf("find or create container %q: %w", handle, err)
	}
	return container, nil
}

// runTask runs the production supervised task and captures the result, including
// the properties and pod a restarted web will observe. A fault changes only fake
// kubelet status; it does not replace the production Wait or executor.
func runTask(in TaskCluster, handle, script string, container runtime.Container, fault ...func(*corev1.Pod)) (TaskOutcome, error) {
	var err error
	if container == nil {
		container, err = findTaskContainer(in, handle)
		if err != nil {
			return TaskOutcome{}, err
		}
	}
	log := new(bytes.Buffer)
	// Stdin must be nil for the step to be supervised (process.go supervised()).
	process, err := container.Run(in.Ctx,
		runtime.ProcessSpec{
			// Host execution shares /tmp across fake pods. Isolate supervisor
			// state between scenarios while preserving it across web restarts.
			ID:   filepath.Base(in.Workspace.Dir) + "-" + handle,
			Path: "/bin/sh",
			Args: []string{"-c", script},
		},
		runtime.ProcessIO{Stdout: log, Stderr: log},
	)
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("run task %q: %w", handle, err)
	}
	metric.Metrics.K8sPodStartupDuration.Max()
	waitCtx, cancel := context.WithCancel(in.Ctx)
	defer cancel()
	var started chan error
	if len(fault) > 0 {
		err = updateTaskPodStatus(in.Ctx, in.Clientset, in.Namespace, handle, fault[0])
	} else {
		// Let Wait observe startup, so the same task run can verify its
		// timing as well as command output. Failure to stage is not a hang.
		started = make(chan error, 1)
		go func() {
			time.Sleep(25 * time.Millisecond)
			err := markPodRunning(waitCtx, in.Clientset, in.Namespace, handle)
			if err != nil {
				cancel()
			}
			started <- err
		}()
	}
	if err != nil {
		return TaskOutcome{}, err
	}

	result, waitErr := process.Wait(waitCtx)
	if started != nil {
		if err := <-started; err != nil {
			return TaskOutcome{}, err
		}
	}
	out := TaskOutcome{
		Cluster: in, Handle: handle, Script: script, Container: container,
		Log: log.String(), ExitStatus: result.ExitStatus, Err: waitErr,
		PodStartupDuration: metric.Metrics.K8sPodStartupDuration.Max(),
	}
	if waitErr != nil {
		out.Message = waitErr.Error()
	}
	out.Props, err = container.Properties()
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("read container properties: %w", err)
	}
	listed, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range listed.Items {
		out.Pods = append(out.Pods, pod.Name)
		if pod.Name == handle {
			out.PodLabels = pod.Labels
		}
	}
	return out, nil
}

func markPodRunning(ctx context.Context, clientset kubernetes.Interface, namespace, handle string) error {
	return updateTaskPodStatus(ctx, clientset, namespace, handle, func(pod *corev1.Pod) {
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	})
}

func updateTaskPodStatus(ctx context.Context, clientset kubernetes.Interface, namespace, handle string, update func(*corev1.Pod)) error {
	pods := clientset.CoreV1().Pods(namespace)
	pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod %q: %w", handle, err)
	}
	update(pod)
	if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod status: %w", err)
	}
	return nil
}

// expandWorkspace lets a scenario name a scratch path without hard-coding one.
func expandWorkspace(script string, w TaskWorkspace) string {
	return strings.ReplaceAll(script, "$WORKSPACE", w.Dir)
}

func (w TaskWorkspace) requireSupervisorState() error {
	entries, err := os.ReadDir(filepath.Join(w.Dir, supervisorStateDirectory))
	if err != nil {
		return fmt.Errorf("read supervisor state in task workspace: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("task workspace contains no supervisor state")
	}
	return nil
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
			return TaskWorkspace{Dir: dir, ownedDir: dir}, nil
		},
		Disposer: func(value any) error {
			w, ok := value.(TaskWorkspace)
			if !ok {
				return fmt.Errorf("task-workspace disposer got %T", value)
			}
			if w.ownedDir == "" {
				return fmt.Errorf("task workspace has no owned directory")
			}
			return os.RemoveAll(w.ownedDir)
		},
	}
}
