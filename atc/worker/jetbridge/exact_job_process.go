package jetbridge

// An exact execution's task on the job step runtime.
//
// The exec runtime starts an exact command by exec'ing its supervisor after
// the web has recorded the start (process_control.go: beginExactCommand). A
// step Job's pod runs its command from its own spec, so there is no exec to
// put the start in front of. What every launcher does before it runs the
// command is fetch its step envelope, and the command never runs without one.
// So the envelope fetch is the start gate:
//
//	admit (the Job's pod, once bound)  ->  the launcher asks for its envelope
//	  ->  classify  ->  confirm the admitted pod  ->  START RECORD  ->  Run witness
//	    ->  the envelope is served  ->  the command
//	the command  ->  main's terminated status and the launcher's record
//	  ->  OUTCOME RECORD (recordExactOutcome, unchanged)  ->  the result
//
// The web stays the ledger's only writer. The start names the pod the
// TokenReview bound the launcher's token to and the process identity
// job-v1:<step Job UID>; nothing else about the ledger or the Run witness
// differs from the exec runtime's. A launcher that lost its answer and asks
// again gets the ledger's idempotent replay of the same start. Another pod is
// refused before anything is recorded, and an execution the ledger does not
// classify never_started is never served unless it is that replay.
//
// Not here yet: stopping or aborting a started command, recovering its
// outcome after a web restart, resuming it, relocation, replacement, and the
// bound session. A step whose start was recorded keeps its Job on every path
// on which its outcome was not, so nothing that witnesses it is destroyed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// jobIdentityPrefix starts the process identity an exact start on the job
// runtime names: the step Job's UID, which is one command run once.
const jobIdentityPrefix = "job-v1:"

// ErrStepEnvelopeNotReady is a start gate that cannot answer yet: the
// execution is not admitted, or the ledger or the Run did not answer. The
// launcher is told to retry.
var ErrStepEnvelopeNotReady = errors.New("the step envelope is not ready yet")

// exactJobAdmissionWait bounds how long one envelope request waits for the
// execution's admission before the launcher is told to retry.
var exactJobAdmissionWait = 10 * time.Second

// exactJobExecution is one exact task's control state on the job runtime.
// Its admission, witness and outcome are the exec runtime's own code, run
// on an execProcess that never execs anything: it holds the control envelope
// and the node's client, and names the Job's pod.
type exactJobExecution struct {
	container *Container
	control   *execProcess

	admitOnce sync.Once
	admitted  chan struct{}
	admitErr  error

	// mu serializes the start gate: two requests (a launcher retry racing
	// its first, or another pod) never interleave a classify and a start.
	mu      sync.Mutex
	started atomic.Bool
}

// newExactJobExecution is the control state for an exact task routed to the
// job runtime, or nil for every other step. A step Job of this name that
// already exists is a step this web did not start, and resuming one is not
// done yet: it is refused rather than replaced, which could look like a
// second run.
func (c *Container) newExactJobExecution(ctx context.Context, processID string, spec runtime.ProcessSpec, pio runtime.ProcessIO) (*exactJobExecution, error) {
	if !c.exactTaskOnJobRuntime() {
		return nil, nil
	}
	if _, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{}); err == nil {
		return nil, fmt.Errorf("%w: step job %q already exists, and an exact execution on the job runtime is not resumed",
			ErrExactOutcomeUnresolved, c.podName)
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("reading the step job before an exact start: %w", err)
	}
	return &exactJobExecution{
		container: c,
		control:   newExecProcess(processID, "", c.clientset, c.config, c, nil, spec, pio, c.storageBackend),
		admitted:  make(chan struct{}),
	}, nil
}

// startGate is what the envelope store runs before it serves this step's
// envelope; nil for a step with no exact execution.
func (e *exactJobExecution) startGate() func(context.Context, *corev1.Pod) error {
	if e == nil {
		return nil
	}
	return e.beforeServe
}

// admit admits the execution against the Job's pod as soon as it is bound,
// in the background, exactly as the exec runtime admits its pause pod: the
// same Admit, the same node UID check. Called once the pod exists.
func (e *exactJobExecution) admit(ctx context.Context, podName string) {
	e.admitOnce.Do(func() {
		e.control.podName = podName
		go func() {
			e.admitErr = e.control.admitExactExecution(ctx)
			close(e.admitted)
		}()
	})
}

// recordedStart reports whether this web recorded the execution's start,
// after which the command may be running and its Job is kept.
func (e *exactJobExecution) recordedStart() bool {
	return e != nil && e.started.Load()
}

// beforeServe is the start gate. pod is the pod the launcher's token is bound
// to, read from the API with the UID the TokenReview named. It returns nil
// only once the start is durable at the node and retained by the Run.
func (e *exactJobExecution) beforeServe(ctx context.Context, pod *corev1.Pod) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	wait, cancel := context.WithTimeout(ctx, exactJobAdmissionWait)
	defer cancel()
	select {
	case <-e.admitted:
	case <-wait.Done():
		return fmt.Errorf("%w: the exact execution is not admitted yet", ErrStepEnvelopeNotReady)
	}
	if e.admitErr != nil {
		return fmt.Errorf("the exact execution was not admitted: %w", e.admitErr)
	}
	p := e.control

	process, err := e.processIdentity(pod)
	if err != nil {
		return err
	}

	// Classify first, as the exec runtime does: an execution the ledger knows
	// as anything but never_started is not started again. The one exception
	// is the launcher's own retry, which the ledger answers below with the
	// start it already holds, or refuses.
	classified, err := p.exact.client.Classify(ctx, p.control.Identity)
	if err != nil {
		return fmt.Errorf("%w: classifying before serving the envelope: %v", ErrStepEnvelopeNotReady, err)
	}
	if err := classified.Validate(); err != nil {
		return err
	}
	if classified.Identity != p.control.Identity {
		return fmt.Errorf("%w: classification names a different execution", ErrExactOutcomeUnresolved)
	}
	switch classified.Classification {
	case executioncontrol.ClassificationNeverStarted:
		if p.container.checkStart != nil {
			if err := p.container.checkStart(ctx); err != nil {
				return err
			}
		}
	case executioncontrol.ClassificationExecuting:
		// Only an idempotent replay by the same pod and process.
	default:
		return fmt.Errorf("execution %s is %s: its envelope is never served", p.control.Identity.ExecutionID, classified.Classification)
	}

	if err := e.confirmAdmittedPod(pod); err != nil {
		return err
	}

	start, err := p.exact.client.RecordStart(ctx, p.control.Identity, p.exact.podUID, process)
	if err != nil {
		if errors.Is(err, output.ErrConflict) {
			return fmt.Errorf("the node refused the exact start: %w", err)
		}
		// The answer may have been lost after the node committed it; the
		// launcher's retry is the ledger's replay.
		return fmt.Errorf("%w: recording the exact start: %v", ErrStepEnvelopeNotReady, err)
	}
	if err := start.Validate(); err != nil {
		return err
	}
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != p.control.Identity ||
		start.PodUID != p.exact.podUID || start.ProcessIdentity != process {
		return fmt.Errorf("%w: the node's start does not name this pod and process", ErrExactOutcomeUnresolved)
	}
	// Start admission is committed: from here the Job is kept whatever
	// happens to the step.
	p.control.MarkStarted()
	e.started.Store(true)
	if err := p.recordRunWitness(ctx, start); err != nil {
		// The command does not run until the Run holds its start. The
		// launcher's retry replays the start and retains it again; an
		// outcome is never recorded before it is.
		p.exact.unretainedStart = &start
		return fmt.Errorf("%w: retaining the node's exact start in the Run: %v", ErrStepEnvelopeNotReady, err)
	}
	p.exact.unretainedStart = nil
	return nil
}

// confirmAdmittedPod is the job runtime's confirmAdmittedPod: the pod asking
// is the one admission read when it was bound, on the node it was admitted
// on. Any other pod -- a second pod of this Job, a pod of an earlier Job of
// the same name -- is refused before anything is recorded.
func (e *exactJobExecution) confirmAdmittedPod(pod *corev1.Pod) error {
	p := e.control
	if executioncontrol.PodUID(pod.UID) != p.exact.podUID {
		return fmt.Errorf("pod %s (%s) is not the pod %s the exact execution was admitted with",
			pod.Name, pod.UID, p.exact.podUID)
	}
	if pod.Spec.NodeName != p.exact.nodeName {
		return fmt.Errorf("pod %s is on node %q, not node %q where the exact execution was admitted",
			pod.Name, pod.Spec.NodeName, p.exact.nodeName)
	}
	return nil
}

// processIdentity is job-v1:<step Job UID>, from the Job that owns the pod,
// which must be the Job this web created.
func (e *exactJobExecution) processIdentity(pod *corev1.Pod) (executioncontrol.ProcessIdentity, error) {
	var owner types.UID
	for _, reference := range pod.OwnerReferences {
		if reference.Kind == "Job" && reference.APIVersion == "batch/v1" && reference.Controller != nil && *reference.Controller {
			owner = reference.UID
		}
	}
	e.container.mu.RLock()
	created := e.container.jobUID
	e.container.mu.RUnlock()
	if owner == "" || created == "" || owner != created {
		return "", fmt.Errorf("pod %s is not owned by the step job this web created", pod.Name)
	}
	return executioncontrol.ProcessIdentity(jobIdentityPrefix + string(created)), nil
}

// launcherExitRecord is the part of the launcher's termination message the
// web reads: the exit it reports for the command.
type launcherExitRecord struct {
	Exit *int `json:"exit"`
}

// finish records the outcome of a started exact task from its pod: main's
// terminated status, corroborated by the record the launcher writes after
// the command exits. A pod that ended with no start recorded never ran the
// command. One that terminated without the launcher's record (killed, out of
// memory) or with a record that disagrees is left unresolved, never closed.
func (e *exactJobExecution) finish(ctx context.Context, podName string, exit int) error {
	select {
	case <-e.admitted:
		if e.admitErr != nil && !e.started.Load() {
			return fmt.Errorf("exact execution admission: %w", e.admitErr)
		}
	default:
	}
	if !e.started.Load() {
		return fmt.Errorf("the step job's pod %s ended before its exact start was recorded: "+
			"its launcher was never served the step envelope, so the command did not run", podName)
	}
	pod, err := e.container.clientset.CoreV1().Pods(e.container.config.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("%w: reading the step job's pod for its exit: %v", ErrExactOutcomeUnresolved, err)
	}
	var terminated *corev1.ContainerStateTerminated
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == mainContainerName {
			terminated = status.State.Terminated
		}
	}
	if terminated == nil {
		return fmt.Errorf("%w: pod %s has no terminated status for its command", ErrExactOutcomeUnresolved, podName)
	}
	var record launcherExitRecord
	if err := json.Unmarshal([]byte(terminated.Message), &record); err != nil || record.Exit == nil {
		return fmt.Errorf("%w: pod %s terminated with exit %d and no launcher record of the command's exit (reason %q)",
			ErrExactOutcomeUnresolved, podName, terminated.ExitCode, terminated.Reason)
	}
	if *record.Exit != int(terminated.ExitCode) || *record.Exit != exit {
		return fmt.Errorf("%w: pod %s terminated with exit %d and its launcher recorded exit %d",
			ErrExactOutcomeUnresolved, podName, terminated.ExitCode, *record.Exit)
	}
	return e.control.finishExactCommand(ctx, executioncontrol.ExitOutcome{ExitCode: exit}, executioncontrol.AcknowledgementFinish)
}
