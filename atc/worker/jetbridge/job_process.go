package jetbridge

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

var _ runtime.Process = (*jobProcess)(nil)

// jobProcess is a step running as a Job (step_job.go). It follows the Job's
// pod: main's logs are the step's output, main's terminated status is its
// exit, and a resource command's answer is read back from the step's answer
// directory through the artifact daemon.
type jobProcess struct {
	id             string
	jobName        string
	clientset      kubernetes.Interface
	config         Config
	container      *Container
	processIO      runtime.ProcessIO
	storageBackend StorageBackend
	// answer is set for a resource command, whose stdout the launcher wrote
	// to the step's answer directory rather than to the container log.
	answer bool
}

func newJobProcess(id string, container *Container, pio runtime.ProcessIO) *jobProcess {
	return &jobProcess{
		id:             id,
		jobName:        container.podName,
		clientset:      container.clientset,
		config:         container.config,
		container:      container,
		processIO:      pio,
		storageBackend: container.storageBackend,
		answer:         container.resourceStep(),
	}
}

func (p *jobProcess) ID() string { return p.id }

func (p *jobProcess) SetTTY(runtime.TTYSpec) error { return nil }

// Wait follows the step's pod to its exit. A cancelled step deletes its Job,
// which ends the pod with its termination grace; the context's error is the
// result, whatever the pod made of the signal.
func (p *jobProcess) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	logger := lagerctx.FromContext(ctx).Session("job-process-wait", lager.Data{
		"job":        p.jobName,
		"process-id": p.id,
	})
	ctx, span := tracing.StartSpan(ctx, "k8s.job-process.wait", tracing.Attrs{
		"job":        p.jobName,
		"process-id": p.id,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// The envelope is the command's to fetch while it runs and nobody's
	// after; it holds the resource's credentials, so it does not outlive
	// Wait on any path.
	defer p.container.forgetEnvelope()

	// The Job survives Wait only when the command's exit was read. On every
	// other path out -- the step abandoned (abort, timeout), the pod gone
	// before its exit was seen, the answer unreadable -- the Job is deleted:
	// nobody is coming back for it, and a Job left alive is a Job whose
	// controller may yet run the command, a second time, with no reader.
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := deleteStepJob(cleanupCtx, p.clientset, p.config.Namespace, p.jobName, ""); err != nil {
			logger.Error("failed-to-delete-unfinished-step-job", err)
		}
	}()

	pod, err := p.container.awaitJobPod(ctx)
	if err != nil {
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("waiting for the step job's pod: %w", err))
	}
	podName := pod.Name
	logger = logger.WithData(lager.Data{"pod": podName})

	sidecarWriters, err := p.sidecarLogWriters()
	if err != nil {
		spanErr = err
		return runtime.ProcessResult{}, err
	}

	// Main's log is the step's stdout for a task. For a resource command it
	// is the command's stderr (the launcher sent stdout to the answer
	// directory), so it goes to the step's stderr writer.
	logWriter := p.processIO.Stdout
	if p.answer {
		logWriter = p.processIO.Stderr
	}
	logCtx, cancelLogs := context.WithCancel(ctx)
	defer cancelLogs()
	logDone := make(chan struct{})
	var mainStreamed bool
	go func() {
		defer close(logDone)
		mainStreamed = p.streamLogs(logCtx, podName, logWriter, sidecarWriters)
	}()

	state, err := p.followPod(ctx, podName)
	if err != nil {
		if ctx.Err() != nil {
			spanErr = ctx.Err()
			return runtime.ProcessResult{}, ctx.Err()
		}
		logger.Error("failed-to-follow-step-pod", err)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(err)
	}

	// Let the log stream drain: it ends on its own when main terminates. A
	// follow that never opened (the kubelet would not follow) is replaced by
	// one plain read of the finished container's log, which the kubelet
	// keeps until the pod is deleted.
	select {
	case <-logDone:
	case <-time.After(5 * time.Second):
		cancelLogs()
		<-logDone
	}
	if !mainStreamed && logWriter != nil {
		if err := readContainerLogs(ctx, p.clientset, p.config.Namespace, podName, mainContainerName, logWriter); err != nil && p.processIO.Stderr != nil {
			fmt.Fprintf(p.processIO.Stderr, "\nwarning: the command's log could not be read: %v\n", err)
		}
	}

	if p.answer && p.processIO.Stdout != nil && state.result.ExitStatus == 0 {
		if err := p.readAnswer(ctx, state.nodeName); err != nil {
			spanErr = err
			return runtime.ProcessResult{}, err
		}
	}

	if p.storageBackend != nil && p.container != nil {
		lagerctx.FromContext(ctx).Session("record-output-locations", lager.Data{
			"handle": p.container.handle, "pod": podName, "node": state.nodeName,
		}).Info("recording-daemonset-artifacts")
		p.storageBackend.RecordOutputs(ctx, p.container.handle, state.nodeName, p.container.volumes, p.container.containerSpec)
	}

	exit := state.result.ExitStatus
	finished = true
	p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exit))
	span.SetAttributes(attribute.String("exit-code", strconv.Itoa(exit)))
	return runtime.ProcessResult{ExitStatus: exit}, nil
}

// jobPodOutcome is what followPod learned: the exit and the node it ran on.
type jobPodOutcome struct {
	result   runtime.ProcessResult
	nodeName string
}

// followPod watches the pod until main terminates. The policy (OOM, failed
// fast, interruption, unschedulable) is podStateFor's, shared with the exec
// runtime's readiness watch.
func (p *jobProcess) followPod(ctx context.Context, podName string) (jobPodOutcome, error) {
	// The readiness budget is the exec runtime's: startup, scheduling and
	// the managed-input inits' retries. It applies until main has started;
	// a running command is bounded by the step's own timeout.
	managedInputs := 0
	for _, input := range p.container.containerSpec.Inputs {
		if input.HangarRead != nil {
			managedInputs++
		}
	}
	timeout := managedInputReadyTimeout(p.config, managedInputs)
	startupCtx, cancelStartup := context.WithTimeout(ctx, timeout)
	defer cancelStartup()

	watcher := NewPodWatcher(p.clientset, p.config.Namespace, podName)
	defer watcher.Stop()

	var lastPod *corev1.Pod
	var unschedulableFirstSeen time.Time
	var unschedulableNotified, running bool
	tracker := newPodEventTracker()
	for {
		nextCtx := ctx
		if !running {
			nextCtx = startupCtx
		}
		pod, err := watcher.Next(nextCtx)
		if err != nil && !running && startupCtx.Err() != nil && ctx.Err() == nil {
			if lastPod != nil {
				writePodDiagnostics(lastPod, p.processIO.Stderr)
				if !unschedulableFirstSeen.IsZero() {
					message, _ := isPodUnschedulable(lastPod)
					return jobPodOutcome{}, fmt.Errorf("pod scheduling timeout: Unschedulable for %s: %s", podSchedulingTimeout(p.config), message)
				}
				return jobPodOutcome{}, fmt.Errorf("timed out waiting for pod to start (timeout: %s, phase: %s)", timeout, lastPod.Status.Phase)
			}
			return jobPodOutcome{}, fmt.Errorf("timed out waiting for pod to start (timeout: %s)", timeout)
		}
		if err != nil {
			if errors.Is(err, ErrPodDeleted) && pod != nil {
				writePodDiagnostics(pod, p.processIO.Stderr)
				writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
				return jobPodOutcome{}, fmt.Errorf("pod deleted externally: %s", pod.Status.Phase)
			}
			return jobPodOutcome{}, err
		}
		lastPod = pod
		tracker.emitPodLifecycleEvents(ctx, pod)

		state := podStateFor(pod)
		if err := reportPodFailure(ctx, p.clientset, pod, p.processIO.Stderr, state); err != nil {
			return jobPodOutcome{}, err
		}
		// A failed init container ends the pod before main ever ran; the
		// phase is Failed and no main status exists, which podExitCode would
		// read as the command's exit 1. Name the init instead.
		if name, exit, failed := failedInitContainer(pod); failed && !mainStarted(pod) {
			writePodDiagnostics(pod, p.processIO.Stderr)
			fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, podName, p.processIO.Stderr)
			return jobPodOutcome{}, fmt.Errorf("init container %q failed with exit %d before the command ran", name, exit)
		}
		if state.complete {
			return jobPodOutcome{result: state.result, nodeName: pod.Spec.NodeName}, nil
		}
		if !running && mainStarted(pod) {
			running = true
			cancelStartup()
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			if unschedulableFirstSeen.IsZero() {
				unschedulableFirstSeen = time.Now()
			}
			if !unschedulableNotified {
				fmt.Fprintf(p.processIO.Stderr, "pod is Unschedulable: %s — waiting up to %s for cluster resources\n", message, podSchedulingTimeout(p.config))
				unschedulableNotified = true
			}
			if time.Since(unschedulableFirstSeen) > podSchedulingTimeout(p.config) {
				writePodDiagnostics(pod, p.processIO.Stderr)
				return jobPodOutcome{}, fmt.Errorf("pod scheduling timeout: Unschedulable for %s: %s", podSchedulingTimeout(p.config), message)
			}
		} else {
			unschedulableFirstSeen = time.Time{}
			unschedulableNotified = false
		}
	}
}

// mainStarted reports whether the main container has started: running, or
// already terminated.
func mainStarted(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == mainContainerName && (status.State.Running != nil || status.State.Terminated != nil) {
			return true
		}
	}
	return false
}

func (p *jobProcess) sidecarLogWriters() (map[string]io.Writer, error) {
	if p.processIO.Stdout == nil || p.container == nil {
		return nil, nil
	}
	return sidecarLogWriters(p.container.containerSpec.Sidecars, p.processIO)
}

// streamLogs follows main's log into w and each sidecar's into its writer.
// Sidecars are native sidecars, so their streams end when the pod does.
func (p *jobProcess) streamLogs(ctx context.Context, podName string, w io.Writer, sidecarWriters map[string]io.Writer) bool {
	if w == nil {
		return true
	}
	var sidecars sync.WaitGroup
	for name, writer := range sidecarWriters {
		sidecars.Add(1)
		go func(name string, writer io.Writer) {
			defer sidecars.Done()
			followContainerLogs(ctx, p.clientset, p.config.Namespace, podName, name, writer, p.processIO.Stderr)
		}(name, writer)
	}
	streamed := followContainerLogs(ctx, p.clientset, p.config.Namespace, podName, mainContainerName, w, p.processIO.Stderr)
	waitWithGrace(&sidecars, sidecarLogGrace)
	return streamed
}

// followContainerLogs streams one container's log with follow, retrying
// until the container has started, and reports whether a stream was ever
// opened. A follow that keeps failing is said once on the step's stderr, so
// a kubelet that cannot follow (an exhausted inotify budget, say) does not
// pass for a silent command.
func followContainerLogs(ctx context.Context, clientset kubernetes.Interface, namespace, podName, containerName string, w, stderr io.Writer) bool {
	failures := 0
	for {
		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Follow: true, Container: containerName})
		stream, err := req.Stream(ctx)
		if err == nil {
			if _, copyErr := io.Copy(w, stream); copyErr != nil && stderr != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "\nwarning: log stream interrupted for %s: %v\n", containerName, copyErr)
			}
			stream.Close()
			return true
		}
		failures++
		if failures == logStreamWarnAfter && stderr != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "\nwarning: cannot follow the log of %s yet (%v); still trying\n", containerName, err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(logStreamRetryDelay):
		}
	}
}

// logStreamWarnAfter is how many consecutive follow failures (at
// logStreamRetryDelay apart) pass before the step is told. A container's
// log is not followable until it has started, which takes a few of them.
const logStreamWarnAfter = 60

// readContainerLogs reads a finished container's whole log once, without
// follow: the kubelet keeps it until the pod is deleted.
func readContainerLogs(ctx context.Context, clientset kubernetes.Interface, namespace, podName, containerName string, w io.Writer) error {
	stream, err := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName}).Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	_, err = io.Copy(w, stream)
	return err
}

// readAnswer reads the resource command's stdout from the step's answer
// directory on the node it ran on, through that node's artifact daemon, and
// hands it to the step as its stdout.
func (p *jobProcess) readAnswer(ctx context.Context, nodeName string) error {
	backend, ok := p.storageBackend.(*DaemonSetBackend)
	if !ok || backend == nil {
		return errors.New("job step runtime: a resource command's answer needs the artifact daemon")
	}
	host, err := backend.nodeIPResolver.Resolve(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("resolving the node that holds the step's answer: %w", err)
	}
	body, err := backend.wire.StreamOut(ctx, host, stepAnswerKey(p.container.handle))
	if err != nil {
		if errors.Is(err, artifactwire.ErrNotFound) {
			return fmt.Errorf("the resource command's answer was not found on node %s", nodeName)
		}
		return fmt.Errorf("reading the resource command's answer: %w", err)
	}
	defer body.Close()
	reader := tar.NewReader(body)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the resource command wrote no answer")
		}
		if err != nil {
			return fmt.Errorf("reading the resource command's answer: %w", err)
		}
		if header.Typeflag != tar.TypeReg || tarEntryBase(header.Name) != answerFile {
			continue
		}
		if header.Size > exactStdoutLimit {
			return fmt.Errorf("the resource command's answer is %d bytes, above the %d byte limit", header.Size, exactStdoutLimit)
		}
		_, err = io.Copy(p.processIO.Stdout, io.LimitReader(reader, exactStdoutLimit+1))
		return err
	}
}

func tarEntryBase(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// resourceStep reports whether this container runs a resource command: a
// check, get or put, whose stdout is its answer.
func (c *Container) resourceStep() bool {
	switch c.metadata.Type {
	case db.ContainerTypeCheck, db.ContainerTypeGet, db.ContainerTypePut:
		return true
	}
	return false
}
