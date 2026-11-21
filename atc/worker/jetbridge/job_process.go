package jetbridge

import (
	"archive/tar"
	"bytes"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

var _ runtime.Process = (*jobProcess)(nil)

// jobProcess is a step running as a Job (step_job.go). It follows the Job's
// pod: main's logs are the step's output, main's terminated status is its
// exit, and a resource command's answer is read back from the step's answer
// directory through the artifact daemon.
type jobProcess struct {
	id      string
	jobName string
	// jobUID is the UID of the step Job this process follows: the one
	// createStepJob created, or the running one it found and followed.
	jobUID         types.UID
	clientset      kubernetes.Interface
	config         Config
	container      *Container
	processIO      runtime.ProcessIO
	storageBackend StorageBackend
	// answer is set for a resource command, whose stdout the launcher wrote
	// to the step's answer directory rather than to the container log.
	answer bool
	// exact is an exact task's control state for its current Job
	// (exact_job_process.go); nil for every other step. The rest is what an
	// exact task's Wait needs to make another Job for it (exact_job_wait.go).
	exact           *exactJobExecution
	spec            runtime.ProcessSpec
	stdin           []byte
	replaced        bool
	relocatedFrom   []string
	relocationSpent bool
	// relocatable, when set, says whether an Unschedulable pod may be moved
	// to another node; followPod then hands it back with errExactRelocate
	// once it has waited relocationGrace.
	relocatable func(*corev1.Pod) bool
}

func newJobProcess(id string, container *Container, pio runtime.ProcessIO) *jobProcess {
	container.mu.RLock()
	uid := container.jobUID
	container.mu.RUnlock()
	return &jobProcess{
		id:             id,
		jobName:        container.podName,
		jobUID:         uid,
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

	if p.exact != nil {
		result, err := p.waitExact(lagerctx.NewContext(ctx, logger))
		spanErr = err
		return result, err
	}

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
	var pod *corev1.Pod
	defer func() {
		if finished {
			return
		}
		if ctx.Err() != nil && p.container.metadata.Type == db.ContainerTypeTask && pod != nil && p.storageBackend != nil {
			// The first observation may have been Pending. Ask once more
			// before deleting the Job to learn whether main actually started.
			// Observation must not consume the budget that stops the command.
			observeCtx, cancelObserve := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			latest, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(observeCtx, pod.Name, metav1.GetOptions{})
			cancelObserve()
			if err != nil {
				logger.Error("failed-to-read-abandoned-step-pod", err)
			} else if latest.UID == pod.UID {
				pod = latest
			}
		}
		// This step's Job, by the UID it had when this process was made: a
		// Job of the same name made since by another invocation is not this
		// step's to delete.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := deleteStepJob(cleanupCtx, p.clientset, p.config.Namespace, p.jobName, p.jobUID); err != nil {
			logger.Error("failed-to-delete-unfinished-step-job", err)
		}
		cancelCleanup()
		// Only a task's outputs are filed: a get or put counts its Dir as an
		// output once it completes, and an abandoned one holds a partial fetch.
		if ctx.Err() != nil && p.container.metadata.Type == db.ContainerTypeTask &&
			pod != nil && pod.Spec.NodeName != "" && mainStarted(pod) && p.storageBackend != nil {
			// Deleting the Job stops its writer without letting the controller
			// replace it. Garbage collection may still be ending the Pod;
			// filing its live hostPath alias is safe for the same reason as in
			// execProcess.Wait: no snapshot is taken and the directory survives
			// the Pod, including any final writes during termination.
			// The backend logs recording failures, never replacing ctx.Err().
			recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), outputRecordBudget)
			defer cancelRecord()
			p.storageBackend.RecordOutputs(recordCtx, p.container.handle, pod.Spec.NodeName, p.container.volumes, p.container.containerSpec)
		}
	}()

	var err error
	pod, err = p.container.awaitJobPod(ctx)
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
	var mainFollow logFollow
	go func() {
		defer close(logDone)
		mainFollow = p.streamLogs(logCtx, podName, logWriter, sidecarWriters)
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

	// Let the log stream drain: it ends on its own when main terminates.
	// Whatever the follow did -- never opened, delivered nothing, ended short
	// or was cut here -- one plain read of the finished container's log,
	// which the kubelet keeps until the pod is deleted, appends what it did
	// not deliver.
	select {
	case <-logDone:
	case <-time.After(5 * time.Second):
		cancelLogs()
		<-logDone
	}
	if logWriter != nil {
		completeContainerLog(ctx, p.clientset, p.config.Namespace, podName, mainContainerName, logWriter, p.processIO.Stderr, mainFollow)
	}

	if p.answer && p.processIO.Stdout != nil && state.result.ExitStatus == 0 {
		if err := p.readAnswer(ctx, state.nodeName); err != nil {
			spanErr = err
			return runtime.ProcessResult{}, err
		}
	}

	if p.storageBackend != nil && p.container != nil {
		// Cancellation can arrive before or during terminal output recording.
		recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), outputRecordBudget)
		defer cancelRecord()
		lagerctx.FromContext(ctx).Session("record-output-locations", lager.Data{
			"handle": p.container.handle, "pod": podName, "node": state.nodeName,
		}).Info("recording-daemonset-artifacts")
		p.storageBackend.RecordOutputs(recordCtx, p.container.handle, state.nodeName, p.container.volumes, p.container.containerSpec)
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
	var secretRecheck time.Duration
	tracker := newPodEventTracker()
	for {
		nextCtx := ctx
		if !running {
			nextCtx = startupCtx
		}
		// An Unschedulable pod is updated once and then left alone, so a move
		// that falls due must not wait for an event that may never come.
		cancelNext := context.CancelFunc(func() {})
		relocating := !unschedulableFirstSeen.IsZero() && lastPod != nil && p.relocatable != nil && p.relocatable(lastPod)
		rechecking := !relocating && secretRecheck > 0 && lastPod != nil
		if relocating {
			nextCtx, cancelNext = context.WithTimeout(nextCtx, max(relocationGrace-time.Since(unschedulableFirstSeen), 0)+10*time.Millisecond)
		} else if rechecking {
			// A pod waiting on its params Secret is judged again when the
			// grace runs out, event or not.
			nextCtx, cancelNext = context.WithTimeout(nextCtx, secretRecheck+10*time.Millisecond)
		}
		pod, err := watcher.Next(nextCtx)
		cancelNext()
		if err != nil && (relocating || rechecking) && errors.Is(err, context.DeadlineExceeded) && startupCtx.Err() == nil && ctx.Err() == nil {
			pod, err = lastPod, nil
		}
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

		secretRecheck = 0
		if left, pending := stepEnvSecretSyncPending(pod, time.Now()); pending {
			secretRecheck = left
			continue
		}
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
			if p.relocatable != nil && time.Since(unschedulableFirstSeen) > relocationGrace && p.relocatable(pod) {
				return jobPodOutcome{}, errExactRelocate
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

// streamLogs follows main's log into w and each sidecar's into its writer,
// and reports what main's follow delivered. Sidecars are native sidecars, so
// their streams end when the pod does.
func (p *jobProcess) streamLogs(ctx context.Context, podName string, w io.Writer, sidecarWriters map[string]io.Writer) logFollow {
	if w == nil {
		return logFollow{}
	}
	var sidecars sync.WaitGroup
	for name, writer := range sidecarWriters {
		sidecars.Add(1)
		go func(name string, writer io.Writer) {
			defer sidecars.Done()
			followContainerLogs(ctx, p.clientset, p.config.Namespace, podName, name, writer, p.processIO.Stderr)
		}(name, writer)
	}
	follow := followContainerLogs(ctx, p.clientset, p.config.Namespace, podName, mainContainerName, w, p.processIO.Stderr)
	waitWithGrace(&sidecars, sidecarLogGrace)
	return follow
}

// logFollow is what one container's log follow delivered: how many bytes of
// the kubelet's log stream the step's writer accepted, and the last of them.
// The follow and the plain read that completes it ask for the same stream --
// the container alone: no timestamps, sinceTime, tailLines or limitBytes,
// never previous -- so delivered is an offset into the plain read.
type logFollow struct {
	delivered int64
	tail      []byte
	// ended is set when the follow's stream reached its end on its own: the
	// copy returned no error and the follow was not cancelled.
	ended bool
}

// logFollowTail is how many of the delivered bytes are kept to check that a
// plain read still lines up with the follow. The kubelet serves only the
// current file of a rotated log, so a plain read of a log rotated since can
// hold other bytes at the same offset.
const logFollowTail = 4096

// add counts p, accepted by the writer, and keeps the last logFollowTail
// bytes delivered.
func (f *logFollow) add(p []byte) {
	f.delivered += int64(len(p))
	if len(p) >= logFollowTail {
		f.tail = append(f.tail[:0], p[len(p)-logFollowTail:]...)
		return
	}
	f.tail = append(f.tail, p...)
	if extra := len(f.tail) - logFollowTail; extra > 0 {
		f.tail = append(f.tail[:0], f.tail[extra:]...)
	}
}

// countingWriter hands writes to w and counts what w accepted. "Accepted" is
// only as honest as w's count: the engine's dbEventWriter
// (atc/engine/log_event_writer.go) can hold a dangling fragment and then
// return (0, err) when saving the log fails, so after a writer error the
// appended remainder can repeat that held fragment.
type countingWriter struct {
	w      io.Writer
	follow *logFollow
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.follow.add(p[:max(n, 0)])
	return n, err
}

// followContainerLogs streams one container's log with follow, retrying
// until the container has started, and reports what the stream delivered
// (nothing when none ever opened). Once a stream opens it is never reopened,
// so what it delivered is one prefix of the log. A follow that keeps failing
// is said once on the step's stderr, so a kubelet that cannot follow (an
// exhausted inotify budget, say) does not pass for a silent command.
func followContainerLogs(ctx context.Context, clientset kubernetes.Interface, namespace, podName, containerName string, w, stderr io.Writer) logFollow {
	var follow logFollow
	failures := 0
	for {
		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Follow: true, Container: containerName})
		stream, err := req.Stream(ctx)
		if err == nil {
			_, copyErr := io.Copy(countingWriter{w: w, follow: &follow}, stream)
			follow.ended = copyErr == nil && ctx.Err() == nil
			if copyErr != nil && stderr != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "\nwarning: log stream interrupted for %s: %v\n", containerName, copyErr)
			}
			stream.Close()
			return follow
		}
		failures++
		if failures == logStreamWarnAfter && stderr != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "\nwarning: cannot follow the log of %s yet (%v); still trying\n", containerName, err)
		}
		select {
		case <-ctx.Done():
			return follow
		case <-time.After(logStreamRetryDelay):
		}
	}
}

// logStreamWarnAfter is how many consecutive follow failures (at
// logStreamRetryDelay apart) pass before the step is told. A container's
// log is not followable until it has started, which takes a few of them.
const logStreamWarnAfter = 60

// logReadBudget bounds the plain read that completes a finished container's
// log, so a kubelet that stops answering cannot hold a step that has ended.
const logReadBudget = time.Minute

// completeContainerLog makes w hold the finished container's whole log, as
// far as the kubelet still has it. It reads the log once without follow --
// the kubelet keeps it until the pod is deleted -- and appends only what lies
// beyond the bytes the follow already delivered. It is done whatever the
// follow did: a follow that ended cleanly is not proof that it had
// everything.
//
// The kubelet serves only the current file of a rotated log. A plain read
// shorter than what was delivered appends nothing; that is said when the
// follow was cut, since the end of the output then is not shown. One whose
// bytes just before the follow's end differ from the follow's (a log
// rotated since) appends nothing and says so. A failed read is said on
// stderr and never fails the step; nothing is said once the step itself is
// aborted.
func completeContainerLog(ctx context.Context, clientset kubernetes.Interface, namespace, podName, containerName string, w, stderr io.Writer, follow logFollow) {
	warn := func(format string, args ...any) {
		if stderr != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, format, args...)
		}
	}
	failed := func(err error) {
		if follow.delivered == 0 {
			warn("\nwarning: the command's log could not be read: %v\n", err)
			return
		}
		warn("\nwarning: the command's log could not be re-read to confirm the output above is complete: %v\n", err)
	}
	shorter := func() {
		if !follow.ended {
			warn("\nwarning: the command's log stream was cut and the kubelet has since rotated its log; the end of the output is not shown\n")
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, logReadBudget)
	defer cancel()
	stream, err := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName}).Stream(readCtx)
	if err != nil {
		failed(err)
		return
	}
	defer stream.Close()
	// Skip what was delivered, holding back its tail to compare.
	tail := int64(len(follow.tail))
	if _, err := io.CopyN(io.Discard, stream, follow.delivered-tail); err != nil {
		if errors.Is(err, io.EOF) {
			shorter()
		} else {
			failed(err)
		}
		return
	}
	seen := make([]byte, tail)
	if _, err := io.ReadFull(stream, seen); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			shorter()
		} else {
			failed(err)
		}
		return
	}
	if !bytes.Equal(seen, follow.tail) {
		warn("\nwarning: the command's finished log no longer matches what was streamed (rotated by the kubelet?); the rest of it is not shown\n")
		return
	}
	if _, err := io.Copy(w, stream); err != nil {
		failed(err)
	}
}

// readAnswer reads the resource command's stdout from the step's answer
// directory on the node it ran on, through that node's artifact daemon, and
// hands it to the step as its stdout.
func (p *jobProcess) readAnswer(ctx context.Context, nodeName string) error {
	backend, ok := p.storageBackend.(*DaemonSetBackend)
	if !ok || backend == nil {
		return errors.New("job step runtime: a resource command's answer needs the artifact daemon")
	}
	host, err := daemonAddress(ctx, nodeName, backend.daemonClient, backend.nodeIPResolver)
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
