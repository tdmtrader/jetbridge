package jetbridge

// An exact execution's task on the job step runtime: its start gate.
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
// differs from the exec runtime's. A launcher that asks again is served only
// on the ledger's idempotent replay of the start it holds for that pod and
// process. Another pod is refused before anything is recorded, and an
// execution the ledger does not classify never_started is never served
// unless it is that replay. Wait shuts the gate before it decides what to do
// with the Job, so a start is never recorded for a Job it deleted.
//
// What happens after the start -- the outcome, a stop, a web restart, a
// replaced or moved pod -- is exact_job_wait.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// jobIdentityPrefix starts the process identity an exact start on the job
// runtime names: the step Job's UID, which is one command run once.
const jobIdentityPrefix = "job-v1:"

// jobProcessIdentity is the process identity of an exact start in the Job
// of this UID.
func jobProcessIdentity(job types.UID) executioncontrol.ProcessIdentity {
	return executioncontrol.ProcessIdentity(jobIdentityPrefix + string(job))
}

// stepJobOfIdentity is the step Job a retained start's process identity
// names, if it names one.
func stepJobOfIdentity(identity executioncontrol.ProcessIdentity) (types.UID, bool) {
	uid, ok := strings.CutPrefix(string(identity), jobIdentityPrefix)
	if !ok || uid == "" {
		return "", false
	}
	return types.UID(uid), true
}

// ErrStepEnvelopeNotReady is a start gate that cannot answer yet: the
// execution is not admitted, or the ledger or the Run did not answer. The
// launcher is told to retry.
var ErrStepEnvelopeNotReady = errors.New("the step envelope is not ready yet")

// exactJobAdmissionWait bounds how long one envelope request waits for the
// execution's admission before the launcher is told to retry.
var exactJobAdmissionWait = 10 * time.Second

// exactJobExecution is the control state of one step Job of an exact task:
// its admission and its start gate. A replaced or moved Job gets a new one.
// Its admission, witness and outcome are the exec runtime's own code, run on
// an execProcess that never execs anything: it holds the control envelope
// and the node's client, and names the Job's pod.
type exactJobExecution struct {
	container *Container
	control   *execProcess
	// jobUID is the step Job this gate belongs to; its pod's start names it.
	jobUID types.UID
	// reattached is a Job this web found rather than made: a web before it
	// may have started its command, so its Wait asks the ledger first.
	reattached bool

	admitOnce   sync.Once
	admitCancel context.CancelFunc
	admitted    chan struct{}
	admitErr    error

	// mu serializes the start gate: two requests (a launcher retry racing
	// its first, or another pod) never interleave a classify and a start.
	// It also orders the gate against Wait: closed, set under it, means no
	// start is recorded after Wait decided what to do with the Job.
	mu       sync.Mutex
	closed   bool
	started  atomic.Bool
	startAck *executioncontrol.Acknowledgement

	// beforeStart, when set, runs inside the gate (mu held) before the
	// ledger is asked anything. Only a test sets it, to hold the gate open.
	beforeStart func()
}

// errExactJobClosed is a start gate whose Job is done with: its Wait has
// returned, or it was replaced or moved.
var errExactJobClosed = errors.New("the exact step job is over: its envelope is never served")

func newExactJobExecution(c *Container, processID string, spec runtime.ProcessSpec, pio runtime.ProcessIO, job types.UID) *exactJobExecution {
	return &exactJobExecution{
		container: c,
		control:   newExecProcess(processID, "", c.clientset, c.config, c, nil, spec, pio, c.storageBackend),
		jobUID:    job,
		admitted:  make(chan struct{}),
	}
}

// startGate is what the envelope store runs before it serves this Job's
// envelope.
func (e *exactJobExecution) startGate() func(context.Context, *corev1.Pod) error {
	return e.beforeServe
}

// admit admits the execution against the Job's pod as soon as it is bound,
// in the background, exactly as the exec runtime admits its pause pod: the
// same Admit, the same node UID check. Called once the pod exists.
func (e *exactJobExecution) admit(ctx context.Context, podName string) {
	e.admitOnce.Do(func() {
		e.control.podName = podName
		attemptCtx, cancel := context.WithCancel(ctx)
		e.admitCancel = cancel
		go func() {
			e.admitErr = e.control.admitExactExecution(attemptCtx)
			close(e.admitted)
		}()
	})
}

// stopAdmission ends an admission still waiting for its pod to be bound, and
// reports whether it had admitted: a pod that was bound and admitted is not
// one a relocation may move.
func (e *exactJobExecution) stopAdmission() (admitted bool) {
	if e.admitCancel == nil {
		return false
	}
	e.admitCancel()
	<-e.admitted
	return e.admitErr == nil
}

// awaitAdmission waits for the admission to finish and returns its error.
func (e *exactJobExecution) awaitAdmission(ctx context.Context) error {
	select {
	case <-e.admitted:
		return e.admitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isAdmitted reports whether the admission finished and admitted.
func (e *exactJobExecution) isAdmitted() bool {
	select {
	case <-e.admitted:
		return e.admitErr == nil
	default:
		return false
	}
}

// recordedStart reports whether this web holds the execution's start for
// this Job, after which the command may be running and the Job is kept.
func (e *exactJobExecution) recordedStart() bool {
	return e != nil && e.started.Load()
}

// closeGate shuts the start gate and reports whether a start was recorded
// before it shut. A request already inside the gate finishes first; one that
// arrives after is refused. What Wait does with the Job is decided on this
// answer, so a start is never recorded for a Job already deleted.
func (e *exactJobExecution) closeGate() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return e.started.Load()
}

// beforeServe is the start gate. pod is the pod the launcher's token is bound
// to, read from the API with the UID the TokenReview named. It returns nil
// only once the start is durable at the node and retained by the Run.
func (e *exactJobExecution) beforeServe(ctx context.Context, pod *corev1.Pod) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errExactJobClosed
	}

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
	if e.beforeStart != nil {
		e.beforeStart()
	}
	p := e.control

	process, err := e.processIdentity(pod)
	if err != nil {
		return err
	}

	// This web recorded the start already and the launcher asks again (it
	// lost the answer). The ledger has the last word: only its replay of the
	// same start, for the same pod and process, serves the envelope again --
	// an execution it has since closed refuses the replay -- and nothing is
	// recorded a second time.
	if e.started.Load() {
		if err := e.confirmAdmittedPod(pod); err != nil {
			return err
		}
		replay, err := p.exact.client.RecordStart(ctx, p.control.Identity, p.exact.podUID, process)
		if err != nil {
			if errors.Is(err, output.ErrConflict) {
				return fmt.Errorf("the node no longer answers for this start: %w", err)
			}
			return fmt.Errorf("%w: replaying the exact start: %v", ErrStepEnvelopeNotReady, err)
		}
		if e.startAck == nil || replay != *e.startAck {
			return fmt.Errorf("%w: the node's start is not the one this web recorded", ErrExactOutcomeUnresolved)
		}
		if unretained := p.exact.unretainedStart; unretained != nil {
			if err := p.recordRunWitness(ctx, *unretained); err != nil {
				return fmt.Errorf("%w: retaining the node's exact start in the Run: %v", ErrStepEnvelopeNotReady, err)
			}
			p.exact.unretainedStart = nil
		}
		return nil
	}

	// Classify first, as the exec runtime does: an execution the ledger knows
	// as anything but never_started is not started again. The one exception
	// is a start this web never saw the answer to, which the ledger answers
	// below with the start it already holds, or refuses.
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
		// The node committed a start whose answer this web never saw: only
		// the ledger's idempotent replay of it, by the same pod and process.
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
	if err := e.adoptStart(ctx, start, process); err != nil {
		return err
	}
	return nil
}

// adoptStart takes the node's start for this Job's pod as this web's: the Job
// is kept from here whatever happens to the step, and the command is not
// served until the Run retains the start. Called with mu held.
func (e *exactJobExecution) adoptStart(ctx context.Context, start executioncontrol.Acknowledgement, process executioncontrol.ProcessIdentity) error {
	p := e.control
	if err := start.Validate(); err != nil {
		return err
	}
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != p.control.Identity ||
		start.PodUID != p.exact.podUID || start.ProcessIdentity != process {
		return fmt.Errorf("%w: the node's start does not name this pod and process", ErrExactOutcomeUnresolved)
	}
	p.control.MarkStarted()
	e.startAck = &start
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
// which must be this gate's Job.
func (e *exactJobExecution) processIdentity(pod *corev1.Pod) (executioncontrol.ProcessIdentity, error) {
	owner := controllingJob(pod)
	if owner == "" || e.jobUID == "" || owner != e.jobUID {
		return "", fmt.Errorf("pod %s is not owned by the step job %s", pod.Name, e.jobUID)
	}
	return jobProcessIdentity(e.jobUID), nil
}

// controllingJob is the UID of the step Job that controls the pod, or "".
func controllingJob(pod *corev1.Pod) types.UID {
	for _, reference := range pod.OwnerReferences {
		if reference.Kind == "Job" && reference.APIVersion == "batch/v1" && reference.Controller != nil && *reference.Controller {
			return reference.UID
		}
	}
	return ""
}

// launcherExitRecord is the part of the launcher's termination message the
// web reads: the exit it reports for the command.
type launcherExitRecord struct {
	Exit *int `json:"exit"`
}

// errMainNotTerminated is a step Job's pod whose command has not ended.
var errMainNotTerminated = errors.New("the step job's command has not terminated")

// jobPodExit is the exit of the command in a step Job's pod: main's
// terminated status, corroborated by the record the launcher writes once the
// command has exited or could not be run. A main that terminated without
// that record (the launcher killed, out of memory, the node lost) or with a
// record that disagrees proves nothing, and the outcome stays unresolved.
func jobPodExit(pod *corev1.Pod) (int, error) {
	var terminated *corev1.ContainerStateTerminated
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == mainContainerName {
			terminated = status.State.Terminated
		}
	}
	if terminated == nil {
		return 0, errMainNotTerminated
	}
	var record launcherExitRecord
	if err := json.Unmarshal([]byte(terminated.Message), &record); err != nil || record.Exit == nil {
		return 0, fmt.Errorf("%w: pod %s terminated with exit %d and no launcher record of the command's exit (reason %q)",
			ErrExactOutcomeUnresolved, pod.Name, terminated.ExitCode, terminated.Reason)
	}
	if *record.Exit != int(terminated.ExitCode) {
		return 0, fmt.Errorf("%w: pod %s terminated with exit %d and its launcher recorded exit %d",
			ErrExactOutcomeUnresolved, pod.Name, terminated.ExitCode, *record.Exit)
	}
	return *record.Exit, nil
}
