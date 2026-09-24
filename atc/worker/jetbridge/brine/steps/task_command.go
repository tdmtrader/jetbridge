package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const liveTaskWorkspace = "/tmp"

// TaskCommandDefinitions expresses what a task command does, rather than what
// string was assembled to do it.
func TaskCommandDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, TaskCluster](
			"a worker running supervised tasks from {string} on Kubernetes",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (TaskCluster, error) {
				image, ok := p.GetString(0)
				if !ok || image == "" {
					return TaskCluster{}, fmt.Errorf("expected a task image")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return TaskCluster{}, fmt.Errorf("missing task database")
				}
				worker, err := newLiveRuntimeWorker(database, rec)
				if err != nil {
					return TaskCluster{}, err
				}
				config, err := liveKubernetesConfig()
				if err != nil {
					return TaskCluster{}, err
				}
				trace := new(execObservation)
				// Observe only task execution; premise and cleanup retain the original executor.
				worker = worker.rebuildWith(jetbridge.NewSPDYExecutor(worker.Clientset, trace.config(config)))
				return TaskCluster{WorkerReady: worker, execTrace: trace, image: image}, nil
			},
		),

		Transform[TaskCluster, TaskCluster](
			"the task belongs to pipeline {string} job {string} build {string}",
			func(in TaskCluster, a Args) (TaskCluster, error) {
				in.metadata = db.ContainerMetadata{
					PipelineName: a.String(0), JobName: a.String(1), BuildName: a.String(2),
				}
				return in, nil
			},
		),

		Transform[TaskCluster, TaskCluster]("the task in {string} produces output {string} at {string}",
			func(in TaskCluster, a Args) (TaskCluster, error) {
				dir, name, mount := a.String(0), a.String(1), a.String(2)
				if name == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !strings.HasPrefix(mount, dir+"/") || filepath.Clean(mount) != mount {
					return in, fmt.Errorf("expected an output name and a clean absolute mount inside the task directory")
				}
				if in.directory != "" && in.directory != dir {
					return in, fmt.Errorf("task outputs must share their declared working directory")
				}
				if in.outputs == nil {
					in.outputs = runtime.OutputPaths{}
				}
				if _, exists := in.outputs[name]; exists {
					return in, fmt.Errorf("output %q already exists", name)
				}
				in.directory, in.outputs[name] = dir, mount
				return in, nil
			}),
		Assert[TaskOutcome]("the returned output {string} at {string} on pod {string} contains {string} with {string}",
			func(in TaskOutcome, a Args) error {
				return requireTaskOutput(in, a.String(0), a.String(1), a.String(2), a.String(3), a.String(4))
			}),

		Transform[TaskCluster, TaskCluster]("the task mounts application {string} at {string} with {string}", func(in TaskCluster, a Args) (TaskCluster, error) {
			handle, directory, service := a.String(0), a.String(1), a.String(2)
			if handle == "" || filepath.Clean(directory) != directory || filepath.Dir(directory) != "/tmp/build/workdir" || (service != "PostgreSQL" && service != "no services") {
				return in, fmt.Errorf("expected an application handle, a direct mount below /tmp/build/workdir, and PostgreSQL or no services")
			}
			in.application = &taskApplication{handle: handle, directory: directory, postgres: service == "PostgreSQL"}
			return in, nil
		}),

		Transform[TaskCluster, TaskOutcome](
			"a task {string} runs {string}",
			func(in TaskCluster, a Args) (TaskOutcome, error) {
				return runTask(in, a.String(0), strings.ReplaceAll(a.String(1), "$WORKSPACE", liveTaskWorkspace), nil)
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
				return runTask(in.Cluster, in.Handle, strings.ReplaceAll(a.String(0), "$WORKSPACE", liveTaskWorkspace), nil)
			},
		),

		Assert[TaskOutcome]("the task forwards {string} once without stdin or a terminal",
			func(in TaskOutcome, a Args) error {
				if a.String(0) == "" {
					return fmt.Errorf("expected a quoted task command")
				}
				return in.Cluster.execTrace.requireSupervisedExec(in.Cluster.Namespace, in.podName(), a.String(0), false)
			}),

		CheckThat[TaskOutcome]("the supervisor state belongs to this task pod",
			func(in TaskOutcome) error { return requirePodSupervisorState(in.Cluster.WorkerReady, in.podName()) }),

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

// A task's persisted handle is not necessarily its Kubernetes pod name.
func (in TaskCluster) taskMetadata() db.ContainerMetadata {
	metadata := in.metadata
	metadata.Type = db.ContainerTypeTask
	return metadata
}

func (in TaskCluster) podName(handle string) string {
	return jetbridge.GeneratePodName(in.taskMetadata(), handle)
}

func (in TaskOutcome) podName() string {
	return in.Cluster.podName(in.Handle)
}

func (in TaskCluster) taskImage() string {
	if in.image != "" {
		return in.image
	}
	return "busybox:1.37.0"
}

// findTaskContainer returns a fresh runtime object for the same persisted task.
func findTaskContainer(in TaskCluster, handle string) (runtime.Container, []runtime.VolumeMount, error) {
	cpu, memory := uint64(250), uint64(64*1024*1024)
	dir := in.directory
	if dir == "" {
		dir = liveTaskWorkspace
	}
	spec := runtime.ContainerSpec{
		TeamID:    in.TeamID,
		Dir:       dir,
		Outputs:   in.outputs,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///" + in.taskImage()},
		Type:      db.ContainerTypeTask,
		Limits:    runtime.ContainerLimits{CPU: &cpu, Memory: &memory},
	}
	if in.application != nil {
		configureMountedApplication(in, handle, &spec)
	}
	container, mounts, err := in.Worker.FindOrCreateContainer(
		in.Ctx,
		db.NewFixedHandleContainerOwner(handle),
		in.taskMetadata(),
		spec,
		nil,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("find or create container %q: %w", handle, err)
	}
	return container, mounts, nil
}

// runTask runs the production supervisor through SPDY in the real task pod.
// Recovery constructs fresh runtime handles for the same persisted container;
// it does not kill and restart a full ATC process.
func runTask(in TaskCluster, handle, script string, container runtime.Container) (TaskOutcome, error) {
	var err error
	var mounts []runtime.VolumeMount
	if container == nil {
		container, mounts, err = findTaskContainer(in, handle)
		if err != nil {
			return TaskOutcome{}, err
		}
	}
	podName := in.podName(handle)
	previous, lookupErr := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, podName, metav1.GetOptions{})
	if lookupErr != nil && !apierrors.IsNotFound(lookupErr) {
		return TaskOutcome{}, lookupErr
	}
	log := new(liveLogBuffer)
	// Stdin must be nil for the step to be supervised (process.go supervised()).
	processDir := ""
	if in.application != nil {
		if in.application.postgres {
			processDir = in.application.directory
		}
	}
	process, err := container.Run(in.Ctx,
		runtime.ProcessSpec{
			Dir: processDir,
			// Omit ID: the production default must retain the persisted handle.
			Path: "/bin/sh",
			Args: []string{"-c", script},
		},
		runtime.ProcessIO{Stdout: log, Stderr: log},
	)
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("run task %q: %w", handle, err)
	}
	if process == nil {
		return TaskOutcome{}, fmt.Errorf("run task %q returned no process", handle)
	}
	if process.ID() != handle {
		return TaskOutcome{}, fmt.Errorf("task process ID %q, want persisted handle %q", process.ID(), handle)
	}
	pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, podName, metav1.GetOptions{})
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("read task pod: %w", err)
	}
	if in.image != "" {
		// The original workflow inspects Containers[0], not an arbitrary match.
		if len(pod.Spec.Containers) == 0 || pod.Spec.Containers[0].Name != "main" {
			return TaskOutcome{}, fmt.Errorf("expected the main task container first in the pod")
		}
		main, err := mainContainer(pod)
		if err != nil {
			return TaskOutcome{}, err
		}
		if main.Image != in.image {
			return TaskOutcome{}, fmt.Errorf("task image %q, want declared image %q", main.Image, in.image)
		}
	}
	if pod.UID == "" || pod.ResourceVersion == "" {
		return TaskOutcome{}, fmt.Errorf("task pod has no real API identity")
	}
	if lookupErr == nil && previous.UID != pod.UID {
		return TaskOutcome{}, fmt.Errorf("task recovery replaced pod %s", podName)
	}
	if in.application != nil {
		if err := prepareMountedApplication(in, pod); err != nil {
			return TaskOutcome{}, err
		}
	}
	metric.Metrics.K8sPodStartupDuration.Max()
	waitCtx, cancel := context.WithCancel(in.Ctx)
	defer cancel()
	result, waitErr := process.Wait(waitCtx)
	if waitErr == nil {
		actual, err := awaitLivePod(in.Ctx, liveKubernetes{Clientset: in.Clientset, Namespace: in.Namespace}, podName)
		if err != nil {
			return TaskOutcome{}, err
		}
		if actual.UID != pod.UID {
			return TaskOutcome{}, fmt.Errorf("task pod identity changed during execution")
		}
		fmt.Printf("live task %s/%s UID %s node %s exited %d\n", in.Namespace, podName, actual.UID, actual.Spec.NodeName, result.ExitStatus)
	}
	out := TaskOutcome{
		Cluster: in, Handle: handle, Script: script, Container: container,
		completedContainer: container, completedPodUID: pod.UID, mounts: mounts,
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
		if pod.Name == podName {
			out.PodLabels = pod.Labels
		}
	}
	return out, nil
}

// expandWorkspace lets a scenario name a scratch path without hard-coding one.
func expandWorkspace(script string, w TaskWorkspace) string {
	return strings.ReplaceAll(script, "$WORKSPACE", w.Dir)
}

// TaskWorkspaceResourceDefinition gives each scenario its own scratch dir.
func TaskWorkspaceResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{
		Name:  "task-workspace",
		Scope: brine.ScopeScenario,
		Factory: func(map[string]any) (any, error) {
			dir, err := AttributedTempDir("brine-task")
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

// Read the volume returned by FindOrCreateContainer itself, without a daemon or
// a reconstructed volume. Independent real exec supplies the exact archive
// bytes; it neither repairs the output nor uses the production StreamOut code.
func requireTaskOutput(in TaskOutcome, name, mount, podName, file, content string) error {
	if in.Err != nil || in.ExitStatus != 0 {
		return fmt.Errorf("output task exit=%d error=%v", in.ExitStatus, in.Err)
	}
	if name == "" || podName == "" || file == "" || in.Cluster.outputs[name] != mount {
		return fmt.Errorf("output expectation must name the declared output and path")
	}
	var matched []runtime.VolumeMount
	for _, candidate := range in.mounts {
		if candidate.MountPath == mount {
			matched = append(matched, candidate)
		}
	}
	if len(matched) != 1 {
		return fmt.Errorf("expected one returned output at %s, got %d", mount, len(matched))
	}
	volume, ok := matched[0].Volume.(*jetbridge.Volume)
	if !ok || volume == nil {
		return fmt.Errorf("expected a concrete returned *Volume, got %T", matched[0].Volume)
	}
	if volume.PodName() != podName || !volume.HasExecutor() {
		return fmt.Errorf("returned output binding: pod=%q executor=%t, want %s with executor", volume.PodName(), volume.HasExecutor(), podName)
	}
	var expected bytes.Buffer
	if err := in.Cluster.Executor.ExecInPod(in.Cluster.Ctx, in.Cluster.Namespace, podName, "main",
		[]string{"tar", "cf", "-", "-C", mount, "."}, nil, &expected, nil, false, jetbridge.ExecAttrs{Purpose: "output-premise"}); err != nil {
		return fmt.Errorf("read independent output archive: %w", err)
	}
	files, err := filesInTar(bytes.NewReader(expected.Bytes()))
	if err != nil || !reflect.DeepEqual(files, map[string]string{file: content}) {
		return fmt.Errorf("actual task output: files=%v error=%v, want only %s=%q", files, err, file, content)
	}
	reader, err := volume.StreamOut(in.Cluster.Ctx, ".", nil)
	if err != nil {
		return fmt.Errorf("stream returned output: %w", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("read returned output: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(data, expected.Bytes()) {
		return fmt.Errorf("returned output bytes differ from the actual pod archive: got %d bytes, want %d", len(data), expected.Len())
	}
	if err := in.Cluster.execTrace.requireDownload(in.Cluster.Namespace, podName, mount); err != nil {
		return err
	}
	pods, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).List(in.Cluster.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 1 || pods.Items[0].Name != podName || pods.Items[0].UID != in.completedPodUID || pods.Items[0].Status.Phase != corev1.PodRunning {
		return fmt.Errorf("output extraction did not retain the sole original Running task pod")
	}
	fmt.Printf("returned output %s read %d exact bytes from retained pod %s UID %s\n", name, len(data), podName, in.completedPodUID)
	return nil
}
