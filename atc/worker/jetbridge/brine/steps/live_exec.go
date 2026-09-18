package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type LiveTaskPlan struct{ Database JetbridgeDB }

func LiveExecDefinitions() []brine.StepDefinition {
	const lateSidecar = "the sidecar {string} has an image-pull failure after the main container exited {int}"
	const sidecarFirst = "a sidecar exits {int} before the main container exits {int}"
	const sidecarCompletion = "compatibility pod {string} exits {int} while sidecar {string} runs {string}"
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, StepOutcome]("the controller fails an unscheduled pod before any container starts", func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (StepOutcome, error) {
			return observeControllerFailedPod(in, rec)
		}),
		brine.DefineMapUsing[brine.Empty, LiveTaskPlan]("a task using Kubernetes", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (LiveTaskPlan, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return LiveTaskPlan{}, fmt.Errorf("missing real database")
			}
			return LiveTaskPlan{Database: database}, nil
		}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome]("its exec connection is cut and its pod {string}", func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
			failure, ok := p.GetString(0)
			if !ok || (failure != "disappears" && failure != "runs out of memory") {
				return ProcessOutcome{}, fmt.Errorf("expected pod disappearance or memory exhaustion")
			}
			return in.interrupt(rec, failure)
		}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome]("the compatibility pod ends with its main container exiting {int}",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				code, ok := p.GetInt(0)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("expected a container exit code")
				}
				return completeLiveCompatibility(in, rec, code)
			}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome](lateSidecar,
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				return applyAction(lateSidecar, in, p, func(in LiveTaskPlan, a Args) (ProcessOutcome, error) {
					return completeLiveCompatibility(in, rec, a.Int(1), compatibilitySidecar{
						handle: "sidecar-late-failure", name: a.String(0), pullFailure: true,
					})
				})
			}),
		brine.DefineMap[LiveTaskPlan, StepOutcome](sidecarFirst,
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (StepOutcome, error) {
				return applyAction(sidecarFirst, in, p, func(in LiveTaskPlan, a Args) (StepOutcome, error) {
					code := a.Int(0)
					out, err := completeLiveCompatibility(in, rec, a.Int(1), compatibilitySidecar{
						handle: "sidecar-mask", name: "log-shipper", image: "busybox:1.37.0", exitBeforeMain: &code,
					})
					return StepOutcome{Err: out.Err, Message: out.Message, ExitStatus: out.ExitStatus, Stderr: out.Stderr}, err
				})
			}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome](sidecarCompletion,
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				return applyAction(sidecarCompletion, in, p, func(in LiveTaskPlan, a Args) (ProcessOutcome, error) {
					return completeLiveCompatibility(in, rec, a.Int(1), compatibilitySidecar{handle: a.String(0), name: a.String(2), image: a.String(3)})
				})
			}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome]("pod {string} succeeds but the next {int} status reads fail",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				handle, handleOK := p.GetString(0)
				count, countOK := p.GetInt(1)
				if !handleOK || !countOK || count <= 0 {
					return ProcessOutcome{}, fmt.Errorf("read fault requires a pod name and a positive denial count")
				}
				w, err := newLiveRuntimeWorker(in.Database, rec)
				if err != nil {
					return ProcessOutcome{}, err
				}
				config, err := liveKubernetesConfig()
				if err != nil {
					return ProcessOutcome{}, err
				}
				return observeInitialReadFailures(w, rec, config, handle, count)
			}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome]("the {string} runtime diagnoses an image-pull failure in {string}",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				mode, ok := p.GetString(0)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("expected an execution mode")
				}
				target, ok := p.GetString(1)
				if !ok {
					return ProcessOutcome{}, fmt.Errorf("expected a failed container")
				}
				return diagnoseLiveStartupProcess(in, rec, "image pull backoff", mode, target)
			}),
		CheckThat[ProcessOutcome]("the build log names the requested image", func(in ProcessOutcome) error {
			if in.Clientset == nil || in.Ctx == nil || in.Namespace == "" || in.Handle == "" || in.FailedContainer == "" {
				return fmt.Errorf("no actual image-pull pod was observed")
			}
			pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.Handle, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("read the requested image from the failed pod: %w", err)
			}
			if pod.UID == "" {
				return fmt.Errorf("failed pod has no API identity")
			}
			for _, container := range pod.Spec.Containers {
				if container.Name != in.FailedContainer {
					continue
				}
				if container.Image == "" {
					return fmt.Errorf("failed container has no requested image")
				}
				if !strings.Contains(in.Stderr, container.Image) || !strings.Contains(in.Stderr, container.Name) {
					return fmt.Errorf("expected the build log to name requested image %q, got %q", container.Image, in.Stderr)
				}
				return nil
			}
			return fmt.Errorf("failed pod has no requested container %q", in.FailedContainer)
		}),
		brine.DefineMap[LiveTaskPlan, ProcessOutcome]("the pod is scheduled but never reaches Running",
			func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (ProcessOutcome, error) {
				return diagnoseLiveStartupTimeout(in, rec)
			}),
		brine.DefineMap[LiveTaskPlan, StepOutcome](
			"the compatibility runtime reads a task the kubelet cannot start because of {string}",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (StepOutcome, error) {
				cause, ok := p.GetString(0)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a startup failure cause")
				}
				return diagnoseLiveStartup(in, rec, cause)
			}),
		brine.DefineMap[LiveTaskPlan, StepOutcome]("the compatibility watcher diagnoses a task repeatedly killed by its memory limit",
			func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (StepOutcome, error) {
				return diagnoseLiveOOMPriority(in, rec)
			}),
		Transform[ProcessOutcome, StepOutcome]("the compatibility watcher diagnoses the same OOM-killed pod",
			func(in ProcessOutcome, _ Args) (StepOutcome, error) {
				return diagnoseLiveOOM(in)
			}),
		CheckThat[ProcessOutcome]("the build log names the task's actual node", func(in ProcessOutcome) error {
			if in.NodeName == "" || !strings.Contains(in.Stderr, "Node: "+in.NodeName+"\n") {
				return fmt.Errorf("build log does not name actual scheduled node %q: %q", in.NodeName, in.Stderr)
			}
			return nil
		}),
	}
}

// The output sink supplies backpressure after observing actual task bytes.
// client-go waits for stdout to drain before returning its real stream error;
// this lets deletion or a real OOM finish before the diagnostic Get.
// No Kubernetes response, exit status or executor error is manufactured.
type liveOutputBarrier struct {
	liveLogBuffer
	marker  string
	started chan struct{}
	release chan struct{}
	once    sync.Once
	ctx     context.Context
}

func (b *liveOutputBarrier) Write(p []byte) (int, error) {
	n, err := b.liveLogBuffer.Write(p)
	if strings.Contains(b.String(), b.marker) {
		b.once.Do(func() { close(b.started) })
		select {
		case <-b.release:
		case <-b.ctx.Done():
			return n, b.ctx.Err()
		}
	}
	return n, err
}

func (in LiveTaskPlan) interrupt(rec *brine.Recorder, failure string) (ProcessOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return ProcessOutcome{}, err
	}
	trace := new(execObservation)
	executor, route, err := liveExecutionRoute(ctx, rec, trace.config(cluster.Config))
	if err != nil {
		return ProcessOutcome{}, err
	}
	dw, err := in.Database.PersistNamedWorker("live-exec-worker")
	if err != nil {
		return ProcessOutcome{}, err
	}
	config := jetbridge.NewConfig(cluster.Namespace, "")
	config.PodStartupTimeout = time.Minute
	config.PodSchedulingTimeout = time.Minute
	client, err := kubernetes.NewForConfig(trace.config(cluster.Config))
	if err != nil {
		return ProcessOutcome{}, err
	}
	worker := jetbridge.NewWorker(dw, client, config)
	worker.SetExecutor(executor)
	cpu, memory := uint64(250), uint64(64*1024*1024)
	handle := "live-exec-task"
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask}, runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}, Limits: runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}, nil)
	if err != nil {
		return ProcessOutcome{}, err
	}
	out := &liveOutputBarrier{marker: "running-" + cluster.Marker, started: make(chan struct{}), release: make(chan struct{}), ctx: ctx}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(out.release) }) }
	stderr := new(liveLogBuffer)
	if failure == "runs out of memory" {
		// Reuse a real pod whose PID 1 owns the allocation. On runtimes that
		// disable group OOM killing, an exec-child OOM would leave the pause
		// container alive and could not establish the container-death premise.
		_, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Create(ctx, liveOOMPod(handle, corev1.RestartPolicyNever), metav1.CreateOptions{})
		if err != nil {
			return ProcessOutcome{}, err
		}
	}
	command := "echo $$ > /tmp/brine-live-task.pid; printf '%s\n' '" + out.marker + "'; while :; do sleep 1; done"
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", command}}, runtime.ProcessIO{Stdout: out, Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, err
	}
	done := make(chan ProcessOutcome, 1)
	go func() {
		result, err := process.Wait(ctx)
		done <- ProcessOutcome{Namespace: cluster.Namespace, Clientset: cluster.Clientset, Ctx: context.Background(), Handle: handle, ExitStatus: result.ExitStatus, Err: err, Message: errorMessage(err), Stderr: stderr.String()}
	}()
	joined := false
	defer func() {
		release()
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("live exec did not stop after cleanup")
			}
		}
	}()
	select {
	case <-out.started:
	case result := <-done:
		joined = true
		return ProcessOutcome{}, fmt.Errorf("task ended before its real stdout checkpoint: %v, log %q", result.Err, out.String())
	case <-ctx.Done():
		return ProcessOutcome{}, fmt.Errorf("wait for real task checkpoint: %w", ctx.Err())
	}
	if err := route.Close(); err != nil {
		return ProcessOutcome{}, err
	}
	// A separate real exec proves the child is still alive after the socket cut.
	direct := jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.Config)
	probe := new(liveLogBuffer)
	if err := direct.ExecInPod(ctx, cluster.Namespace, handle, "main", []string{"sh", "-c", "kill -0 $(cat /tmp/brine-live-task.pid) && printf alive"}, nil, probe, stderr, false, jetbridge.ExecAttrs{Purpose: "fixture-task-liveness"}); err != nil || probe.String() != "alive" {
		return ProcessOutcome{}, fmt.Errorf("task did not survive transport loss: %v, %q", err, probe.String())
	}
	// Both genuine success above and a nonzero remote exit must remain distinct
	// from losing the connection before any status arrives. No extra pod needed.
	var exitErr *jetbridge.ExecExitError
	err = direct.ExecInPod(ctx, cluster.Namespace, handle, "main", []string{"sh", "-c", "exit 7"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "fixture-nonzero-exit"})
	if !errors.As(err, &exitErr) || exitErr.ExitCode != 7 {
		return ProcessOutcome{}, fmt.Errorf("real nonzero exit lost its status: %v", err)
	}
	pods := cluster.Clientset.CoreV1().Pods(cluster.Namespace)
	pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if pod.UID == "" || pod.Spec.NodeName == "" {
		return ProcessOutcome{}, fmt.Errorf("missing real scheduled pod identity")
	}
	if failure == "disappears" {
		zero := int64(0)
		if err := pods.Delete(ctx, handle, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			return ProcessOutcome{}, err
		}
		for {
			_, err := pods.Get(ctx, handle, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				break
			}
			if err != nil {
				return ProcessOutcome{}, err
			}
			select {
			case <-ctx.Done():
				return ProcessOutcome{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	} else if err := exhaustLiveTaskMemory(ctx, cluster, direct, pod); err != nil {
		return ProcessOutcome{}, err
	}
	select {
	case result := <-done:
		joined = true
		return ProcessOutcome{}, fmt.Errorf("runtime completed before stdout was released: %v", result.Err)
	default:
	}
	release()
	select {
	case result := <-done:
		joined = true
		result.NodeName, result.podUID = pod.Spec.NodeName, string(pod.UID)
		result.runtimeTrace, result.runtimeCreates = trace, 1
		if failure == "runs out of memory" {
			result.runtimeCreates = 0
		}
		if failure == "runs out of memory" {
			// An explicitly executor-free worker exposes the compatibility
			// watcher. Production task execution above remains execProcess.
			compatibility := jetbridge.NewWorker(dw, cluster.Clientset, config)
			var found bool
			result.compatibilityContainer, found, err = compatibility.LookupContainer(ctx, handle)
			if err != nil || !found {
				return ProcessOutcome{}, fmt.Errorf("look up compatibility container: found=%t error=%v", found, err)
			}
		}
		fmt.Printf("real exec evidence: task stdout %q; alive after socket cut; pod %s/%s UID %s %s; runtime exit=%d error=%v\n", out.String(), cluster.Namespace, handle, pod.UID, failure, result.ExitStatus, result.Err)
		return result, nil
	case <-ctx.Done():
		return ProcessOutcome{}, fmt.Errorf("runtime did not finish after the real disconnect: %w", ctx.Err())
	}
}

// Arm only after verifying the namespace-admitted 64 MiB cgroup limit. The
// real PID 1 allocation causes a kernel OOM; no status or signal is fabricated.
func armLiveTaskMemory(ctx context.Context, cluster liveKubernetes, direct *jetbridge.SPDYExecutor, original *corev1.Pod) error {
	probe := new(liveLogBuffer)
	// Read the real container limit on cgroup v2 or v1, never infer it from
	// the requested PodSpec alone. Do not arm an unbounded allocator.
	command := "if [ -f /sys/fs/cgroup/memory.max ]; then cat /sys/fs/cgroup/memory.max; else cat /sys/fs/cgroup/memory/memory.limit_in_bytes; fi"
	if err := direct.ExecInPod(ctx, cluster.Namespace, original.Name, "main", []string{"sh", "-c", command}, nil, probe, nil, false, jetbridge.ExecAttrs{Purpose: "fixture-verify-memory-limit"}); err != nil || strings.TrimSpace(probe.String()) != "67108864" {
		return fmt.Errorf("real OOM needs a verified 64 MiB cgroup memory limit: %v, %q", err, probe.String())
	}
	if err := direct.ExecInPod(ctx, cluster.Namespace, original.Name, "main", []string{"touch", "/tmp/brine-oom-go"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "fixture-arm-bounded-oom"}); err != nil {
		return fmt.Errorf("arm verified bounded OOM: %w", err)
	}
	return nil
}

func exhaustLiveTaskMemory(ctx context.Context, cluster liveKubernetes, direct *jetbridge.SPDYExecutor, original *corev1.Pod) error {
	if err := armLiveTaskMemory(ctx, cluster, direct, original); err != nil {
		return err
	}
	for {
		pod, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Get(ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID != original.UID {
			return fmt.Errorf("OOM task pod was replaced")
		}
		for _, c := range pod.Status.ContainerStatuses {
			if c.Name != "main" || c.State.Terminated == nil {
				continue
			}
			dead := c.State.Terminated
			if c.ContainerID == "" || dead.Reason != "OOMKilled" || dead.ExitCode != 137 {
				return fmt.Errorf("expected genuine main container OOM, got %+v", c)
			}
			fmt.Printf("real OOM evidence: pod %s/%s UID %s node %s container %s reason %s exit %d\n", pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, c.ContainerID, dead.Reason, dead.ExitCode)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for genuine container OOM: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Diagnose the same real current termination through the direct compatibility
// Process, not through execProcess or a fabricated Pod status.
func diagnoseLiveOOM(in ProcessOutcome) (StepOutcome, error) {
	if in.compatibilityContainer == nil || in.podUID == "" {
		return StepOutcome{}, fmt.Errorf("no real OOM pod retained for compatibility diagnosis")
	}
	ctx, cancel := context.WithTimeout(execLogger("live-oom-compatibility"), 15*time.Second)
	defer cancel()
	pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	if string(pod.UID) != in.podUID || pod.Status.Phase != corev1.PodRunning {
		return StepOutcome{}, fmt.Errorf("expected the same Running pod after main OOM: UID=%s phase=%s", pod.UID, pod.Status.Phase)
	}
	current, sidecar := false, false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "main" {
			dead := status.State.Terminated
			current = dead != nil && dead.Reason == "OOMKilled" && dead.ExitCode == 137 &&
				status.LastTerminationState.Terminated == nil && status.RestartCount == 0
		}
		sidecar = sidecar || status.Name == "keep-running" && status.State.Running != nil
	}
	if !current || !sidecar {
		return StepOutcome{}, fmt.Errorf("expected current main OOM without restart history and a running sidecar: %+v", pod.Status.ContainerStatuses)
	}
	stderr := new(bytes.Buffer)
	process, err := in.compatibilityContainer.Attach(ctx, in.Handle, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return StepOutcome{Err: err, Message: err.Error()}, nil
	}
	if _, ok := process.(*jetbridge.Process); !ok {
		return StepOutcome{}, fmt.Errorf("expected direct compatibility Process, got %T", process)
	}
	result, waitErr := process.Wait(ctx)
	fmt.Printf("real compatibility OOM evidence: pod %s/%s UID %s phase %s current OOMKilled exit 137; error=%v\n",
		pod.Namespace, pod.Name, pod.UID, pod.Status.Phase, waitErr)
	return StepOutcome{Err: waitErr, Message: errorMessage(waitErr), Stderr: stderr.String(), ExitStatus: result.ExitStatus}, nil
}
