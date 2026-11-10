package jetbridge

// The life of an exact task on the job step runtime once its step Job exists:
// the outcome, a stop, a web restart, and a pod or node lost before the
// command started. Each case mirrors the exec runtime's; where the two differ
// on purpose, it says so.
//
//	outcome    main terminated + the launcher's record -> OUTCOME RECORD -> result
//	           (exec: the supervisor's exit journal, read through the exec)
//	stop       ledger stop request -> the pod's activeDeadlineSeconds cut to now
//	           -> the kubelet SIGTERMs the launcher, which ends the command and
//	           writes its record -> OUTCOME RECORD from it
//	           (exec: the stop record in the journal and a kill of the group;
//	           the pod is not deleted either way, so the terminated status that
//	           witnesses the outcome stays readable)
//	restart    a new web finds the Job of the step's name, admits its pod again,
//	           and asks the ledger: never_started -> the gate serves as usual;
//	           executing -> the ledger's replay of the start must name this
//	           Job's pod and job-v1:<its UID>, then the pod is followed;
//	           closed -> the ledger's outcome is reported, nothing is followed
//	           (exec: the supervisor is re-exec'd and its log replayed)
//	replace    a pod that died before any start is replaced once, never under
//	           a capture hold, never after its own init container failed, never
//	           once the ledger holds a start (exec: pause-pod replacement)
//	relocate   a pod pinned to its node and never bound there moves once to
//	           another node the Run admits it on (exec: relocation)
//
// The proof a command never ran is the closed start gate with no start in
// it: a launcher runs nothing without its envelope, and the envelope is only
// served after the start is recorded for that pod. So a Job is deleted only
// on that proof, and kept on every path on which a start exists and no
// outcome was recorded.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// errExactRelocate is followPod handing back an Unschedulable pod that may
// move to another node.
var errExactRelocate = errors.New("the step job's pod is to move to another node")

// exactJobStopBudget bounds a stop: the pod's termination grace, the
// kubelet's latency in acting on the deadline, and the outcome's record. A
// variable only so specs need not wait it out.
var exactJobStopBudget = time.Duration(stepJobTerminationGrace)*time.Second + 30*time.Second

type exactNext int

const (
	exactNextDone exactNext = iota
	exactNextReplace
	exactNextRelocate
)

// runExactAsJob runs an exact task as a step Job. A Job of the step's name
// that is already there was made by a web before this one, which may have
// started its command: it is resumed under its own UID, never replaced, and
// Wait asks the ledger what became of it before anything else.
func (c *Container) runExactAsJob(ctx context.Context, processID string, spec runtime.ProcessSpec, pio runtime.ProcessIO, stdin []byte) (runtime.Process, error) {
	if c.envelopes == nil {
		return nil, errors.New("job step runtime: no step envelope store; an exact task's start gate is its envelope")
	}
	p := newJobProcess(processID, c, pio)
	p.spec, p.stdin = spec, stdin
	p.relocatable = p.canRelocateExact

	existing, err := c.stepJob(ctx)
	switch {
	case err == nil:
		e := newExactJobExecution(c, processID, spec, pio, existing.UID)
		e.reattached = true
		c.envelopes.RegisterWithStartGate(c.podName, StepEnvelope{Stdin: stdin}, e.startGate())
		p.exact, p.jobUID = e, existing.UID
	case apierrors.IsNotFound(err):
		if err := p.startExactJob(ctx); err != nil {
			metric.Metrics.FailedContainers.Inc()
			return nil, wrapIfTransient(err)
		}
		metric.Metrics.ContainersCreated.Inc()
	default:
		return nil, fmt.Errorf("reading the step job before an exact start: %w", err)
	}
	c.bindVolumesToPod(c.podName)
	return p, nil
}

// startExactJob makes a new step Job for the task, with a start gate of its
// own, registered before the Job exists so its pod never asks for nothing.
func (p *jobProcess) startExactJob(ctx context.Context) error {
	c := p.container
	job, err := c.buildStepJob(p.spec, true)
	if err != nil {
		return err
	}
	e := newExactJobExecution(c, p.id, p.spec, p.processIO, "")
	c.envelopes.RegisterWithStartGate(c.podName, StepEnvelope{Stdin: p.stdin}, e.startGate())
	created, err := c.createStepJob(ctx, job)
	if err == nil && !created {
		err = fmt.Errorf("step job %q appeared while an exact task's Job was being made", c.podName)
	}
	if err != nil {
		c.envelopes.Forget(c.podName)
		return err
	}
	c.mu.RLock()
	uid := c.jobUID
	c.mu.RUnlock()
	e.mu.Lock()
	e.jobUID = uid
	e.mu.Unlock()
	p.exact, p.jobUID = e, uid
	return nil
}

// waitExact is Wait for an exact task: each of the task's Jobs in turn, at
// most one replacement and the relocations the Run allows.
func (p *jobProcess) waitExact(ctx context.Context) (runtime.ProcessResult, error) {
	defer p.container.forgetEnvelope()
	for {
		result, next, err := p.followExactJob(ctx)
		switch next {
		case exactNextReplace:
			if err := p.replaceExactJob(ctx, err); err != nil {
				return runtime.ProcessResult{}, err
			}
			continue
		case exactNextRelocate:
			if err := p.relocateExactJob(ctx); err != nil {
				return runtime.ProcessResult{}, err
			}
			continue
		}
		return result, err
	}
}

// followExactJob follows the current Job to its outcome, or says what is to
// become of it.
func (p *jobProcess) followExactJob(ctx context.Context) (runtime.ProcessResult, exactNext, error) {
	e := p.exact
	logger := lagerctx.FromContext(ctx).Session("exact-step-job", lager.Data{
		"job": p.jobName, "job-uid": string(e.jobUID),
		"execution": string(e.control.control.Identity.ExecutionID),
	})

	pod, err := p.container.awaitJobPod(ctx)
	if err != nil {
		if e.reattached {
			// A Job a web before this one made, with no pod. Nothing can be
			// admitted, so nothing can say whether it ever ran: it is kept.
			e.closeGate()
			return runtime.ProcessResult{}, exactNextDone, fmt.Errorf("%w: the resumed step job has no pod: %v", ErrExactOutcomeUnresolved, err)
		}
		e.closeGate()
		return runtime.ProcessResult{}, exactNextDone, p.abandonExactJob(ctx, wrapIfTransient(fmt.Errorf("waiting for the step job's pod: %w", err)))
	}
	podName := pod.Name
	logger = logger.WithData(lager.Data{"pod": podName})
	e.admit(ctx, podName)

	if e.reattached {
		if result, done, err := p.reconcileReattached(ctx); done {
			return result, exactNextDone, err
		}
	}

	logs, err := p.followExactLogs(ctx, podName)
	if err != nil {
		e.closeGate()
		return runtime.ProcessResult{}, exactNextDone, p.abandonExactJob(ctx, err)
	}

	_, err = p.followPod(ctx, podName)
	if errors.Is(err, errExactRelocate) {
		logs.abandon()
		return runtime.ProcessResult{}, exactNextRelocate, nil
	}
	if err != nil {
		logs.abandon()
		if !e.closeGate() {
			// No start: the command never ran, and nothing of this Job is
			// owed to anyone.
			if ctx.Err() != nil {
				return runtime.ProcessResult{}, exactNextDone, p.abandonExactJob(ctx, ctx.Err())
			}
			if why := p.exactNotReplaceable(ctx); why != nil {
				logger.Info("not-replacing-exact-step-job", lager.Data{"why": why.Error()})
				return runtime.ProcessResult{}, exactNextDone, p.abandonExactJob(ctx, wrapIfTransient(err))
			}
			return runtime.ProcessResult{}, exactNextReplace, err
		}
		if ctx.Err() != nil {
			result, err := p.stopExactJob(ctx, podName)
			return result, exactNextDone, err
		}
		// The pod failed or went after the start. What it recorded, while it
		// can still be read, is the outcome; anything less is unresolved.
		result, recordErr := p.completeExact(ctx, podName)
		if recordErr != nil {
			logger.Error("exact-step-unresolved", recordErr, lager.Data{"pod-error": err.Error()})
			return runtime.ProcessResult{}, exactNextDone, fmt.Errorf("%w: following the started step job's pod: %v (%v)",
				ErrExactOutcomeUnresolved, err, recordErr)
		}
		return result, exactNextDone, nil
	}
	logs.drain()
	result, err := p.completeExact(ctx, podName)
	return result, exactNextDone, err
}

// completeExact closes the gate and records the outcome of the command in
// the Job's pod, or, with no start, deletes the Job of a command that never
// ran.
func (p *jobProcess) completeExact(ctx context.Context, podName string) (runtime.ProcessResult, error) {
	e := p.exact
	if !e.closeGate() {
		err := fmt.Errorf("the step job's pod %s ended before its exact start was recorded: "+
			"its launcher was never served the step envelope, so the command did not run", podName)
		select {
		case <-e.admitted:
			if e.admitErr != nil {
				err = fmt.Errorf("exact execution admission: %w", e.admitErr)
			}
		default:
		}
		return runtime.ProcessResult{}, p.abandonExactJob(ctx, err)
	}
	exit, nodeName, err := p.recordOutcomeFromPod(ctx, podName)
	if err != nil {
		return runtime.ProcessResult{}, err
	}
	if p.storageBackend != nil && p.container != nil {
		p.storageBackend.RecordOutputs(ctx, p.container.handle, nodeName, p.container.volumes, p.container.containerSpec)
	}
	p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exit))
	return runtime.ProcessResult{ExitStatus: exit}, nil
}

// recordOutcomeFromPod reads the command's exit off the pod that started it
// and records it at the node through the exec runtime's recordExactOutcome.
func (p *jobProcess) recordOutcomeFromPod(ctx context.Context, podName string) (int, string, error) {
	e := p.exact
	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("%w: reading the step job's pod for its exit: %v", ErrExactOutcomeUnresolved, err)
	}
	if executioncontrol.PodUID(pod.UID) != e.control.exact.podUID {
		return 0, "", fmt.Errorf("%w: pod %s is not the pod the start names", ErrExactOutcomeUnresolved, podName)
	}
	exit, err := jobPodExit(pod)
	if errors.Is(err, errMainNotTerminated) {
		return 0, "", fmt.Errorf("%w: pod %s has no terminated status for its command", ErrExactOutcomeUnresolved, podName)
	}
	if err != nil {
		return 0, "", err
	}
	if err := e.control.finishExactCommand(ctx, executioncontrol.ExitOutcome{ExitCode: exit}, executioncontrol.AcknowledgementFinish); err != nil {
		return 0, "", err
	}
	return exit, pod.Spec.NodeName, nil
}

// stopExactJob stops a started command whose step was aborted or timed out,
// and records how it ended. The exec runtime's order: classify, request the
// stop at the node, interrupt the command, read its exit, record it. The
// interruption is the pod's activeDeadlineSeconds cut to now, which the
// kubelet acts on by ending the pod's containers with their grace and leaves
// the pod -- and its terminated status -- in place; deleting the pod would
// take the witness with it. A command that cannot be shown to have ended is
// unresolved, with the step's own cancellation joined, and its Job is kept.
func (p *jobProcess) stopExactJob(ctx context.Context, podName string) (runtime.ProcessResult, error) {
	e := p.exact
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exactJobStopBudget)
	defer cancel()
	unresolved := func(err error) (runtime.ProcessResult, error) {
		err = fmt.Errorf("%w: stopping the started step job's command: %v", ErrExactOutcomeUnresolved, err)
		return runtime.ProcessResult{}, errors.Join(err, ctx.Err())
	}

	client, id := e.control.exact.client, e.control.control.Identity
	classified, err := client.Classify(stopCtx, id)
	if err != nil {
		return unresolved(err)
	}
	switch {
	case classified.Classification == executioncontrol.ClassificationExecuting:
		stop, err := client.RequestStop(stopCtx, id)
		if err != nil {
			return unresolved(err)
		}
		if !stop.Accepted {
			return unresolved(fmt.Errorf("the node did not accept the stop of an execution it classifies %s", stop.Classification))
		}
		if err := stopStepJobPod(stopCtx, p.clientset, p.config.Namespace, podName, types.UID(e.control.exact.podUID)); err != nil {
			return unresolved(err)
		}
		if err := awaitMainTerminated(stopCtx, p.clientset, p.config.Namespace, podName); err != nil {
			return unresolved(err)
		}
	case classified.Classification.Authoritative() && classified.Acknowledgement != nil && classified.Acknowledgement.Outcome != nil:
		// It ended, and its outcome was recorded, before the stop.
		result, err := p.reportLedgerOutcome(stopCtx, classified)
		if err != nil {
			return unresolved(err)
		}
		return result, nil
	default:
		return unresolved(fmt.Errorf("the node classifies the execution %s", classified.Classification))
	}
	result, err := p.completeExact(stopCtx, podName)
	if err != nil {
		return unresolved(err)
	}
	return result, nil
}

// reportLedgerOutcome answers with the outcome the node already holds, as
// the exec runtime's reportExactOutcomeWithoutRerunning does: the Run
// retains it, nothing is run or followed.
func (p *jobProcess) reportLedgerOutcome(ctx context.Context, classified executioncontrol.ClassifyResult) (runtime.ProcessResult, error) {
	control := p.exact.control
	if err := control.retainStartWitness(ctx); err != nil {
		return runtime.ProcessResult{}, err
	}
	if err := control.recordRunWitness(ctx, *classified.Acknowledgement); err != nil {
		return runtime.ProcessResult{}, unresolvedLedgerError(fmt.Errorf("retaining the node's outcome in the Run: %w", err))
	}
	control.exactOutcomeAcknowledged.Store(true)
	exit := classified.Acknowledgement.Outcome.ExitCode
	p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exit))
	return runtime.ProcessResult{ExitStatus: exit}, nil
}

// reconcileReattached asks the ledger what became of a Job a web before this
// one made, once its pod is admitted again. It answers done when the step is
// over (its outcome reported, or unresolved); otherwise the Job is followed,
// with the gate open for a never-started command or holding the start for a
// running one.
func (p *jobProcess) reconcileReattached(ctx context.Context) (runtime.ProcessResult, bool, error) {
	e := p.exact
	if err := e.awaitAdmission(ctx); err != nil {
		e.closeGate()
		return runtime.ProcessResult{}, true, fmt.Errorf("%w: admitting the resumed step job's pod: %v", ErrExactOutcomeUnresolved, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	control := e.control
	client, id := control.exact.client, control.control.Identity
	unresolved := func(err error) (runtime.ProcessResult, bool, error) {
		e.closed = true
		return runtime.ProcessResult{}, true, fmt.Errorf("%w: resuming step job %s: %v", ErrExactOutcomeUnresolved, e.jobUID, err)
	}

	classified, err := client.Classify(ctx, id)
	if err != nil {
		return unresolved(err)
	}
	if err := classified.Validate(); err != nil {
		return unresolved(err)
	}
	if classified.Identity != id {
		return unresolved(errors.New("classification names a different execution"))
	}
	switch {
	case classified.Classification == executioncontrol.ClassificationNeverStarted:
		return runtime.ProcessResult{}, false, nil
	case classified.Classification == executioncontrol.ClassificationExecuting:
		// The ledger, not memory: its replay of the start must be this
		// Job's pod and this Job's process identity.
		process := jobProcessIdentity(e.jobUID)
		start, err := client.RecordStart(ctx, id, control.exact.podUID, process)
		if err != nil {
			return unresolved(fmt.Errorf("the execution started, and not as this Job's pod %s: %w", control.exact.podUID, err))
		}
		if err := e.adoptStart(ctx, start, process); err != nil && !errors.Is(err, ErrStepEnvelopeNotReady) {
			return unresolved(err)
		}
		return runtime.ProcessResult{}, false, nil
	case classified.Classification.Authoritative() && classified.Acknowledgement != nil && classified.Acknowledgement.Outcome != nil:
		e.closed = true
		result, err := p.reportLedgerOutcome(ctx, classified)
		return result, true, err
	default:
		return unresolved(fmt.Errorf("the node classifies the execution %s", classified.Classification))
	}
}

// exactNotReplaceable says why the current Job, whose pod failed before any
// start, may not be replaced; nil when it may. The exec runtime's rules for a
// pause pod: once per step, never under a capture hold, never when the pod's
// own init container failed, never once a start exists -- here the ledger's
// word, not a flag.
func (p *jobProcess) exactNotReplaceable(ctx context.Context) error {
	e := p.exact
	if p.replaced {
		return errors.New("the step's pod has already been replaced once")
	}
	pod, err := p.container.findJobPod(ctx)
	switch {
	case err == nil:
		dead := pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded
		if !dead {
			return fmt.Errorf("pod %s has not died", pod.Name)
		}
		if name, exit, failed := failedInitContainer(pod); failed {
			return fmt.Errorf("pod %s did not die, its init container %q exited %d", pod.Name, name, exit)
		}
	case !errors.Is(err, errJobPodNotFound):
		return err
	}
	if err := p.container.refuseIfCaptureHeld(ctx, "replacing the step job's pod"); err != nil {
		return err
	}
	if e.isAdmitted() {
		classified, err := e.control.exact.client.Classify(ctx, e.control.control.Identity)
		if err != nil {
			return fmt.Errorf("classifying before a replacement: %w", err)
		}
		if classified.Classification != executioncontrol.ClassificationNeverStarted {
			return fmt.Errorf("the node classifies the execution %s", classified.Classification)
		}
	}
	return nil
}

// replaceExactJob replaces a Job whose pod died before any start with a new
// one. The replacement is spent whether or not it is made.
func (p *jobProcess) replaceExactJob(ctx context.Context, why error) error {
	p.replaced = true
	lagerctx.FromContext(ctx).Info("replacing-exact-step-job", lager.Data{"job": p.jobName, "why": why.Error()})
	if p.processIO.Stderr != nil {
		fmt.Fprintf(p.processIO.Stderr, "the step's pod was lost before its command started (%v): replacing it once\n", why)
	}
	if err := p.deleteExactJob(ctx); err != nil {
		return fmt.Errorf("replacing the step job: %w", err)
	}
	if err := p.startExactJob(ctx); err != nil {
		return fmt.Errorf("replacing the step job: %w", err)
	}
	return nil
}

// canRelocateExact reports whether the pod may move: its execution is pinned
// to a node the Run can move it off, the pod has never been bound, its
// admission has not happened (so no start can have), and no move was
// refused before.
func (p *jobProcess) canRelocateExact(pod *corev1.Pod) bool {
	e := p.exact
	return p.container.relocate != nil && e != nil && e.control.control != nil && e.control.control.Node != nil &&
		pod.Spec.NodeName == "" && !p.relocationSpent && !e.recordedStart() && !e.isAdmitted()
}

// relocateExactJob moves an exact task whose pod could not be scheduled on
// the node it was admitted on. The proof the command never ran is the gate:
// the admission still waiting for a bound pod is ended first, so no start
// can be recorded, then the Job goes and the Run admits the execution
// elsewhere. A refused move puts the Job back where it was and spends the
// relocations, as the exec runtime does with its pause pod.
func (p *jobProcess) relocateExactJob(ctx context.Context) error {
	e := p.exact
	logger := lagerctx.FromContext(ctx).Session("relocate-exact-step-job", lager.Data{"job": p.jobName})
	if admitted := e.stopAdmission(); admitted {
		// Bound and admitted after all: it is not one to move. Its gate
		// serves as before.
		logger.Info("not-relocating-admitted-pod")
		p.relocationSpent = true
		return nil
	}
	e.closeGate()
	from := e.control.control.Node.Name
	if err := p.deleteExactJob(ctx); err != nil {
		return fmt.Errorf("relocating the step job: %w", err)
	}
	spec, err := p.container.relocate(ctx, p.relocatedFrom)
	if err != nil {
		logger.Info("relocation-refused", lager.Data{"error": err.Error()})
		p.relocationSpent = true
		if err := p.startExactJob(ctx); err != nil {
			return fmt.Errorf("recreating step job %s after a refused relocation: %w", p.jobName, err)
		}
		return nil
	}
	p.relocatedFrom = append(p.relocatedFrom, from)
	p.container.containerSpec = spec
	if err := p.startExactJob(ctx); err != nil {
		return fmt.Errorf("creating step job %s on node %s: %w", p.jobName, spec.ExecutionControl.Node.Name, err)
	}
	logger.Info("relocated", lager.Data{"from": from, "to": spec.ExecutionControl.Node.Name})
	if p.processIO.Stderr != nil {
		fmt.Fprintf(p.processIO.Stderr, "pod could not be scheduled on node %s: moved the step to node %s\n",
			from, spec.ExecutionControl.Node.Name)
	}
	return nil
}

// deleteExactJob deletes the current Job, by its UID, and waits for it and
// its pod to be gone, so the name is free for the next.
func (p *jobProcess) deleteExactJob(ctx context.Context) error {
	uid := p.exact.jobUID
	if err := deleteStepJob(ctx, p.clientset, p.config.Namespace, p.jobName, uid); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Minute)
	for {
		job, err := p.clientset.BatchV1().Jobs(p.config.Namespace).Get(ctx, p.jobName, metav1.GetOptions{})
		jobGone := apierrors.IsNotFound(err) || (err == nil && job.UID != uid)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("waiting for step job %s to go: %w", p.jobName, err)
		}
		_, err = findPodOfJob(ctx, p.clientset, p.config.Namespace, p.jobName, uid)
		podGone := errors.Is(err, errJobPodNotFound)
		if jobGone && podGone {
			p.container.mu.Lock()
			p.container.jobPodName, p.container.jobUID = "", ""
			p.container.mu.Unlock()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("step job %s (job gone: %v, pod gone: %v) is still present a minute after its deletion", p.jobName, jobGone, podGone)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// abandonExactJob deletes the Job of a command that never ran and returns
// err. Called only with the gate shut and no start in it.
func (p *jobProcess) abandonExactJob(ctx context.Context, err error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if deleteErr := deleteStepJob(cleanupCtx, p.clientset, p.config.Namespace, p.jobName, p.exact.jobUID); deleteErr != nil {
		lagerctx.FromContext(ctx).Error("failed-to-delete-unstarted-exact-step-job", deleteErr)
	}
	return err
}

// exactLogs is one Job's log follow.
type exactLogs struct {
	cancel   context.CancelFunc
	done     chan struct{}
	streamed bool
	drainLog func()
}

func (logs *exactLogs) abandon() {
	logs.cancel()
	<-logs.done
}

// drain lets a follow end on its own once main terminated, and reads the
// whole log once if it never opened.
func (logs *exactLogs) drain() {
	select {
	case <-logs.done:
	case <-time.After(5 * time.Second):
		logs.cancel()
		<-logs.done
	}
	if !logs.streamed {
		logs.drainLog()
	}
	logs.cancel()
}

func (p *jobProcess) followExactLogs(ctx context.Context, podName string) (*exactLogs, error) {
	sidecarWriters, err := p.sidecarLogWriters()
	if err != nil {
		return nil, err
	}
	logCtx, cancel := context.WithCancel(ctx)
	logs := &exactLogs{cancel: cancel, done: make(chan struct{})}
	logs.drainLog = func() {
		if p.processIO.Stdout == nil {
			return
		}
		if err := readContainerLogs(ctx, p.clientset, p.config.Namespace, podName, mainContainerName, p.processIO.Stdout); err != nil && p.processIO.Stderr != nil {
			fmt.Fprintf(p.processIO.Stderr, "\nwarning: the command's log could not be read: %v\n", err)
		}
	}
	go func() {
		defer close(logs.done)
		logs.streamed = p.streamLogs(logCtx, podName, p.processIO.Stdout, sidecarWriters)
	}()
	return logs, nil
}

// stopStepJobPod ends the command in a step Job's pod without deleting the
// pod: its activeDeadlineSeconds, which may only be set or lowered, is cut to
// what the pod has already run, so the kubelet ends its containers with
// their termination grace -- the launcher forwards SIGTERM to the command and
// writes its record -- and the pod stays, Failed, with main's terminated
// status. The patch is refused unless the pod is still the one of uid.
func stopStepJobPod(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, uid types.UID) error {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading the step job's pod to stop it: %w", err)
	}
	if pod.UID != uid {
		return fmt.Errorf("%w: pod %s is not the pod the start names", output.ErrInvalidIdentity, podName)
	}
	if _, err := jobPodExit(pod); !errors.Is(err, errMainNotTerminated) {
		return nil
	}
	started := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		started = pod.Status.StartTime.Time
	}
	deadline := int64(time.Since(started)/time.Second) + 1
	if current := pod.Spec.ActiveDeadlineSeconds; current != nil && *current <= deadline {
		return nil
	}
	patch := fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%q},{"op":"add","path":"/spec/activeDeadlineSeconds","value":%d}]`, uid, deadline)
	if _, err := clientset.CoreV1().Pods(namespace).Patch(ctx, podName, types.JSONPatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("cutting the step job's pod deadline: %w", err)
	}
	return nil
}

// awaitMainTerminated waits for the command in a step Job's pod to end.
func awaitMainTerminated(ctx context.Context, clientset kubernetes.Interface, namespace, podName string) error {
	watcher := NewPodWatcher(clientset, namespace, podName)
	defer watcher.Stop()
	for {
		pod, err := watcher.Next(ctx)
		if err != nil {
			return fmt.Errorf("waiting for the stopped command to end: %w", err)
		}
		if _, err := jobPodExit(pod); !errors.Is(err, errMainNotTerminated) {
			return nil
		}
	}
}
