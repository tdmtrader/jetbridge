package jetbridge

// The exact-execution half of execProcess.
//
// One ordering runs through all of it, and it is the whole point of the phase:
//
//	admit  ->  hold  ->  writer tickets  ->  START RECORD  ->  the command
//	the command  ->  OUTCOME RECORD  ->  durable acknowledgement  ->  the result
//
// Every arrow is a durable write that happens before the thing on its right,
// and none of them is a Pod phase, a container status or an in-memory value. A
// ProcessResult that reached the engine before the daemon acknowledged the
// outcome would be a terminal answer the ledger cannot corroborate, which is
// the state requirement 4 exists to forbid.
//
// What it must never do is run the command twice. From exact process start
// onward recovery may query, attach and report -- and may not re-invoke. The
// in-pod supervisor enforces its half (a recorded start with no outcome exits
// unresolved rather than relaunching); this file enforces the ATC's, by
// classifying rather than retrying whenever the transport lost an answer.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// OutputControlResolver produces the control client for the node an execution
// landed on.
//
// An execution's truth lives on exactly one node's ledger, and which node that
// is is decided by the scheduler after the ATC has composed the Pod. So the
// client cannot be built at construction time and cannot be shared between
// executions.
type OutputControlResolver interface {
	ForNode(ctx context.Context, nodeName string) (OutputControl, error)
}

// ErrExactOutcomeUnresolved is a producer that started and whose outcome nobody
// can prove.
//
// It is deliberately not a failure. A failure is a command that ran and lost; a
// step may be failed on one, cleanup may proceed and the source may be
// released. This is a command whose fate is unknown, and every one of those
// actions would be a decision taken on an inference. Req 4: inability to prove
// success becomes a typed unresolved or lost outcome and cannot authorize
// capture.
var ErrExactOutcomeUnresolved = errors.New("jetbridge: the exact execution's outcome is unresolved")

// errExactReconcile means start admission is closed. Recovery may read the
// original supervisor or the ledger; it must not send another launch command.
var errExactReconcile = errors.New("jetbridge: the exact execution requires reconciliation")

// exactExecution is one controlled execution's live control state.
type exactExecution struct {
	control  *runtime.ExecutionControl
	client   OutputControl
	podUID   executioncontrol.PodUID
	nodeName string

	// tickets are the writer admissions this ATC holds on behalf of the Pod's
	// writers: the main container, its sidecars and every init container that
	// writes into the source tree. They are held by the ATC because Req 24
	// gives the task and its sidecars no output-plane credential -- a
	// container cannot take its own ticket without holding one.
	tickets []output.WriterAdmission

	// unretainedStart is the node's signed start while the Run has not
	// retained it. The node answers for a start only until an outcome exists,
	// so no outcome is recorded while this is set: a Run that lost its start
	// could never close the execution it admitted.
	unretainedStart *executioncontrol.Acknowledgement
}

// startWitnessError is a start the node committed and the Run could not
// retain. The command is still owed its delivery and its stop; the error is
// surfaced once both have happened. It unwraps to the unresolved outcome it
// is, never to the witness budget's deadline.
type startWitnessError struct{ err error }

func (e *startWitnessError) Error() string {
	return "retaining the node's exact start in the Run: " + e.err.Error()
}

func (e *startWitnessError) Unwrap() error { return unresolvedLedgerError(e.err) }

// exactLedgerBudget bounds a ledger or Run witness call made after the
// command's start, when the step's own context may already be cancelled: the
// node is owed the question whether or not anyone is still waiting for the
// step. A variable only so specs need not wait it out.
var exactLedgerBudget = 30 * time.Second

// unresolvedLedgerError is every failure raised under that budget.
//
// The budget's deadline is not the step's timeout, and the steps read a
// context.DeadlineExceeded in their error as one: a ledger that stalled past
// it would fail the step, and a failed step is a decision taken on an outcome
// nobody proved. So the cause is flattened into the typed unresolved outcome
// it is, and keeps only its text. A cancelled or timed-out step's own context
// error is joined by Wait, from the step's context, never carried through here.
func unresolvedLedgerError(err error) error {
	if err == nil {
		return nil
	}
	var flattened *unresolvedOutcomeError
	if errors.As(err, &flattened) && flattened == err {
		return err
	}
	message := err.Error()
	if !errors.Is(err, ErrExactOutcomeUnresolved) {
		message = ErrExactOutcomeUnresolved.Error() + ": " + message
	}
	return &unresolvedOutcomeError{message: message}
}

// unresolvedOutcomeError is ErrExactOutcomeUnresolved with its cause as text.
type unresolvedOutcomeError struct{ message string }

func (e *unresolvedOutcomeError) Error() string { return e.message }
func (e *unresolvedOutcomeError) Unwrap() error { return ErrExactOutcomeUnresolved }

func detachedLedgerContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), exactLedgerBudget)
}

func (p *execProcess) capturing() bool {
	return p.control.HasDurableOutputCapture()
}

// admitWhenScheduled admits the exact identity as soon as the scheduler has
// bound the Pod, and returns a function that joins the attempt.
//
// It races the capture control init deliberately. The init container runs when
// the kubelet starts the Pod and the ATC can admit as soon as the Pod is bound;
// there is no ordering between those two events, so the init RETRIES while the
// daemon says the execution is not admitted, and this admits as early as it
// possibly can. Making the ATC wait for Running instead would put the admission
// after the init container it is waiting for.
func (p *execProcess) admitWhenScheduled(ctx context.Context) func() error {
	if p.control == nil {
		return func() error { return nil }
	}

	done := make(chan error, 1)
	go func() { done <- p.admitExactExecution(ctx) }()

	return func() error {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *execProcess) admitExactExecution(ctx context.Context) error {
	pod, err := p.awaitScheduledPod(ctx)
	if err != nil {
		return err
	}

	client, err := p.outputControls.ForNode(ctx, pod.Spec.NodeName)
	if err != nil {
		return fmt.Errorf("reaching the output daemon on node %s: %w", pod.Spec.NodeName, err)
	}

	// The node UID, not the node NAME. A name can be reused for new hardware,
	// and a ledger sequence is only meaningful alongside the node that issued
	// it -- so the daemon is configured with its own UID from the Downward API
	// and refuses an envelope that names a different one. The ATC reads it off
	// the Node object rather than assuming the name is the identity.
	node, err := p.clientset.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading the UID of node %s: %w", pod.Spec.NodeName, err)
	}
	if pinned := p.control.Node; pinned != nil && (pinned.Name != node.Name || string(pinned.UID) != string(node.UID)) {
		return fmt.Errorf("%w: execution was scheduled on a different node incarnation", ErrExactOutcomeUnresolved)
	}

	// The envelope names no Pod, and the ATC could not honestly put one here
	// even though it happens to have read one: an admission is what a location
	// is reserved against, and the reservation precedes the Pod by
	// construction. The Pod UID this process holds below is for the START
	// record and for comparing against the hold the init container took.
	envelope := executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        p.control.Identity,
		ActivationEpoch: p.control.ActivationEpoch,
		NodeUID:         executioncontrol.NodeUID(node.UID),
		Capability:      p.control.Capability,
	}
	if _, err := client.Admit(ctx, envelope); err != nil {
		return fmt.Errorf("admitting the exact execution: %w", err)
	}

	p.exact = &exactExecution{
		control:  p.control,
		client:   client,
		podUID:   executioncontrol.PodUID(pod.UID),
		nodeName: pod.Spec.NodeName,
	}

	return nil
}

// awaitScheduledPod waits for the Pod to be BOUND, not running. The node is
// what the admission needs and the node is decided at binding; waiting for
// Running would wait for the init container that is waiting for this.
func (p *execProcess) awaitScheduledPod(ctx context.Context) (*corev1.Pod, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).
			Get(ctx, p.podName, metav1.GetOptions{})
		if err == nil && pod.Spec.NodeName != "" && pod.UID != "" {
			return pod, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for pod %s to be scheduled: %w", p.podName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// beginExactCommand is everything that must be durable before the command runs.
//
// The order is the contract. The hold is revalidated first because a hold that
// no longer matches current admission means this Pod is not the one the capture
// is for; the tickets come next because a writer that starts without one is a
// writer sealing cannot see; the start record is last, immediately before the
// exec, because it is the record that closes start admission.
func (p *execProcess) beginExactCommand(ctx context.Context) error {
	if p.exact == nil {
		return fmt.Errorf("the exact execution was never admitted")
	}
	logger := lagerctx.FromContext(ctx).Session("exact-execution", lager.Data{
		"execution": string(p.control.Identity.ExecutionID),
		"pod":       p.podName,
	})

	// Recovery asks first, and asking is what makes it recovery rather than a
	// second run. An execution the ledger already has an outcome for is
	// reported, never re-issued: Req 6, from exact process start onward the
	// producer command is not executed again. The daemon refuses a second
	// start too -- that is its half -- but a caller that only learned so from
	// a 409 would have had to try, and trying is the thing.
	classified, err := p.exact.client.Classify(ctx, p.control.Identity)
	if err != nil {
		return fmt.Errorf("classifying before launching the command: %w", err)
	}
	if err := classified.Validate(); err != nil {
		return err
	}
	if classified.Identity != p.control.Identity {
		return fmt.Errorf("%w: classification names a different execution", ErrExactOutcomeUnresolved)
	}
	if classified.Classification != executioncontrol.ClassificationNeverStarted {
		logger.Info("reconcile-existing-execution", lager.Data{"classification": string(classified.Classification)})

		return errExactReconcile
	}
	if p.container != nil && p.container.checkStart != nil {
		if err := p.container.checkStart(ctx); err != nil {
			return err
		}
	}

	if p.capturing() {
		hold, err := p.exact.client.InspectHold(ctx, p.control.Identity, p.control.Capture.HandoffID)
		if err != nil {
			return fmt.Errorf("revalidating the source hold before the producer starts: %w", err)
		}
		if err := hold.ValidateAs(output.CaptureHoldAcknowledged); err != nil {
			return fmt.Errorf("the source hold is not acknowledged, so the producer may not "+
				"start: %w", err)
		}
		if hold.PodUID != p.exact.podUID {
			return fmt.Errorf("the source hold names pod %s and this producer is pod %s; a "+
				"recreated Pod is a new incarnation that may not write",
				hold.PodUID, p.exact.podUID)
		}
		if err := p.acquireWriterTickets(ctx, hold); err != nil {
			return err
		}
		logger.Info("hold-revalidated", lager.Data{"tickets": len(p.exact.tickets)})
	}

	start, err := p.exact.client.RecordStart(ctx, p.control.Identity, p.exact.podUID, p.exactProcessIdentity())
	if err != nil {
		return fmt.Errorf("recording the exact start before launching the command: %w", err)
	}
	// Start admission is now committed. Closing the Run gate cannot retract
	// this start: its original command must be delivered and stopped through
	// the execution's interruption protocol, never abandoned at another gate.
	// That holds when the Run fails to retain the start too -- returning here
	// would leave an executing ledger with no supervisor to write its outcome.
	p.control.MarkStarted()
	if err = p.recordRunWitness(ctx, start); err != nil {
		p.exact.unretainedStart = &start

		return &startWitnessError{err: unresolvedLedgerError(err)}
	}

	return nil
}

// retainStartWitness retries retaining a start the Run could not retain at
// launch. It must succeed before any outcome is recorded at the node.
func (p *execProcess) retainStartWitness(ctx context.Context) error {
	if p.exact == nil || p.exact.unretainedStart == nil {
		return nil
	}
	if err := p.recordRunWitness(ctx, *p.exact.unretainedStart); err != nil {
		return unresolvedLedgerError(fmt.Errorf("the Run has not retained the node's start, so "+
			"the outcome stays with the node until it does: %w", err))
	}
	p.exact.unretainedStart = nil

	return nil
}

// exactStopGrace is how long a stopped command has to report its exit before
// its transport is abandoned. A variable only so specs need not wait it out.
var exactStopGrace = 30 * time.Second

// A signed start commits us to delivering this command once. Cancellation must
// reach its interruption protocol, even before the exec transport opens;
// abandoning delivery would leave an executing ledger with no outcome writer.
// Give the original command a bounded grace period to report its stopped exit,
// then detach if the node is unavailable. Never retry the launch.
func exactCommandContext(ctx context.Context, stop func(context.Context) error) (context.Context, func()) {
	executionCtx, cancelExecution := context.WithCancel(context.WithoutCancel(ctx))
	stopped := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		defer close(stopped)
		stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), exactStopGrace)
		defer cancelStop()
		if err := stop(stopCtx); err != nil {
			lagerctx.FromContext(ctx).Error("failed-to-stop-admitted-command", err)
		}
		select {
		case <-executionCtx.Done():
		case <-stopCtx.Done():
			cancelExecution()
		}
	})
	return executionCtx, func() {
		cancelExecution()
		if !stopCancellation() {
			<-stopped
		}
	}
}

// acquireWriterTickets takes one ticket per writer this Pod will run.
//
// Req 12 counts main, sidecar and init processes as writers, and the ATC takes
// their tickets because they cannot: Req 24 gives the task and its sidecars no
// output-plane credential, so a container has nothing to present. Every ticket
// is bound to this exact Pod UID and writer fence, so a recreated Pod inherits
// none of them.
//
// A refusal here is surfaced BEFORE the process or its mounts exist, which is
// what makes it a refusal rather than a race.
func (p *execProcess) acquireWriterTickets(ctx context.Context, hold output.CaptureAcknowledgement) error {
	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading the pod's writers: %w", err)
	}

	for _, writer := range podWriterNames(pod) {
		admission := output.WriterAdmission{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       p.control.Identity,
			ActivationEpoch: p.control.ActivationEpoch,
			HandoffID:       p.control.Capture.HandoffID,
			Incarnation:     hold.Incarnation,
			WriterTicketID:  output.WriterTicketID(uuid.NewString()),
			// The first incarnation's writer fence. It advances on takeover,
			// which is Phase 5's; what matters here is that every ticket this
			// Pod takes carries the SAME fence, so a seal's drain set is one
			// set rather than a mixture.
			WriterFence: firstWriterFence,
			PodUID:      p.exact.podUID,
		}
		if _, err := p.exact.client.AdmitWriter(ctx, admission); err != nil {
			return fmt.Errorf("admitting writer %q before it can create a mount or a process: %w",
				writer, err)
		}
		p.exact.tickets = append(p.exact.tickets, admission)
	}

	return nil
}

// podWriterNames is every container in the Pod that can write into the source
// tree. The capture control init is not one of them: it establishes the hold
// and touches nothing the hold protects.
func podWriterNames(pod *corev1.Pod) []string {
	var writers []string
	for _, container := range pod.Spec.InitContainers {
		if container.Name == captureControlInitName {
			continue
		}
		writers = append(writers, container.Name)
	}
	for _, container := range pod.Spec.Containers {
		writers = append(writers, container.Name)
	}

	return writers
}

// finishExactCommand records the outcome and waits for the daemon to
// acknowledge it, before the caller may expose a ProcessResult. Anything that
// stops it short leaves the outcome unresolved, whatever the cause.
func (p *execProcess) finishExactCommand(ctx context.Context, outcome executioncontrol.ExitOutcome,
	kind executioncontrol.AcknowledgementKind) error {
	return unresolvedLedgerError(p.recordExactOutcome(ctx, outcome, kind))
}

func (p *execProcess) recordExactOutcome(ctx context.Context, outcome executioncontrol.ExitOutcome,
	kind executioncontrol.AcknowledgementKind) error {
	if p.exact == nil {
		return fmt.Errorf("the exact execution was never admitted")
	}
	ctx, cancel := detachedLedgerContext(ctx)
	defer cancel()
	if err := p.retainStartWitness(ctx); err != nil {
		return err
	}

	if _, err := p.exact.client.RecordOutcome(ctx, p.control.Identity, kind, outcome); err != nil {
		return fmt.Errorf("recording the exact outcome before exposing a result: %w", err)
	}

	// The acknowledgement, not the write. RecordOutcome returning is this
	// ATC's own knowledge; Observe is the ledger's, read back, and that is
	// what a result may be exposed on.
	observed, err := p.exact.client.Observe(ctx, p.control.Identity, 0)
	if err != nil {
		return fmt.Errorf("observing the durable acknowledgement: %w", err)
	}
	if !observed.Classification.Authoritative() {
		return fmt.Errorf("%w: the daemon classifies this execution as %s after its outcome was "+
			"recorded", ErrExactOutcomeUnresolved, observed.Classification)
	}
	if err = observed.Validate(); err != nil {
		return err
	}
	if observed.Identity != p.control.Identity {
		return ErrExactOutcomeUnresolved
	}
	if err = p.recordRunWitness(ctx, *observed.Acknowledgement); err != nil {
		return err
	}

	p.retireWriterTickets(ctx)

	return nil
}

// retireWriterTickets closes every ticket this ATC took. A ticket left open
// holds sealing forever, so this runs on every path out -- including the ones
// where the command failed.
func (p *execProcess) retireWriterTickets(ctx context.Context) {
	if p.exact == nil {
		return
	}
	logger := lagerctx.FromContext(ctx).Session("exact-execution")
	for _, ticket := range p.exact.tickets {
		if _, err := p.exact.client.RetireWriter(ctx, ticket); err != nil {
			logger.Error("failed-to-retire-writer-ticket", err,
				lager.Data{"ticket": string(ticket.WriterTicketID)})
		}
	}
	p.exact.tickets = nil
}

// reportExactOutcomeWithoutRerunning is the recovery path.
//
// The transport lost its answer, or the supervisor reported that it found a
// recorded start with no outcome. Either way the command may have run, so the
// ATC asks the ledger what is durably known. An executing task may also recover
// its original supervisor's completed exit journal and record that outcome at
// the daemon before returning it. It never reruns the launch script. Missing or
// foreign state stays unresolved, rather than becoming an execution failure.
func (p *execProcess) reportExactOutcomeWithoutRerunning(ctx context.Context) (runtime.ProcessResult, error) {
	if p.exact == nil {
		return runtime.ProcessResult{}, fmt.Errorf("%w: no control client for %s",
			ErrExactOutcomeUnresolved, p.control.Identity.ExecutionID)
	}

	// The step's context may be the thing that ended; the ledger's answer is
	// still owed. Recovery reads it detached and bounded, never re-launching.
	// Its own errors are flattened with %v on purpose: the budget's deadline
	// is not the step's timeout, and task_step reads DeadlineExceeded as one.
	// A cancelled step's own context error is joined by Wait instead.
	ctx, cancel := detachedLedgerContext(ctx)
	defer cancel()

	classified, err := p.exact.client.Classify(ctx, p.control.Identity)
	if err != nil {
		return runtime.ProcessResult{}, fmt.Errorf("%w: classifying after a lost answer: %v",
			ErrExactOutcomeUnresolved, err)
	}
	if err := classified.Validate(); err != nil {
		return runtime.ProcessResult{}, fmt.Errorf("%w: invalid classification: %v", ErrExactOutcomeUnresolved, err)
	}
	if classified.Identity != p.control.Identity {
		return runtime.ProcessResult{}, fmt.Errorf("%w: classification names a different execution", ErrExactOutcomeUnresolved)
	}
	if classified.Classification.Authoritative() && classified.Acknowledgement != nil &&
		classified.Acknowledgement.Outcome != nil {
		if err := p.retainStartWitness(ctx); err != nil {
			return runtime.ProcessResult{}, err
		}
		if err := p.recordRunWitness(ctx, *classified.Acknowledgement); err != nil {
			return runtime.ProcessResult{}, unresolvedLedgerError(fmt.Errorf(
				"retaining the node's outcome in the Run: %w", err))
		}
		p.retireWriterTickets(ctx)

		exitCode := classified.Acknowledgement.Outcome.ExitCode
		if err := p.exposeJournaledAnswer(ctx, *classified.Acknowledgement, exitCode); err != nil {
			return runtime.ProcessResult{}, err
		}
		return runtime.ProcessResult{ExitStatus: exitCode}, nil
	}
	if _, _, journaled := p.exactJournal(); classified.Classification == executioncontrol.ClassificationExecuting && journaled {
		outcome, start, err := p.recoverJournaledOutcome(ctx)
		if err != nil {
			return runtime.ProcessResult{}, fmt.Errorf("%w: reading the original journaled outcome: %v", ErrExactOutcomeUnresolved, err)
		}
		if err := p.finishExactCommand(ctx, outcome, executioncontrol.AcknowledgementFinish); err != nil {
			return runtime.ProcessResult{}, fmt.Errorf("%w: retaining recovered journaled outcome: %v", ErrExactOutcomeUnresolved, err)
		}
		// The outcome is now the node's. A resource command's answer is read
		// from the same journal; failing to read it errors the step and
		// leaves the outcome as recorded.
		if err := p.exposeJournaledAnswer(ctx, start, outcome.ExitCode); err != nil {
			return runtime.ProcessResult{}, err
		}
		return runtime.ProcessResult{ExitStatus: outcome.ExitCode}, nil
	}

	return runtime.ProcessResult{}, fmt.Errorf(
		"%w: execution %s is %s. The producer started and no durable outcome exists, so it is "+
			"neither re-executed nor reported as a failure; its source stays held",
		ErrExactOutcomeUnresolved, p.control.Identity.ExecutionID, classified.Classification)
}

func (p *execProcess) recordRunWitness(ctx context.Context, witness executioncontrol.Acknowledgement) error {
	if p.container != nil && p.container.recordWitness != nil {
		if witness.Kind == executioncontrol.AcknowledgementStart {
			// The node already committed this fact. Retaining it is no longer
			// start admission, and task cancellation must not erase its reply.
			var cancel context.CancelFunc
			ctx, cancel = detachedLedgerContext(ctx)
			defer cancel()
		}
		return p.container.recordWitness(ctx, witness)
	}
	return nil
}

// refuseIfCaptureHeld is the hijack and pod-replacement door.
//
// A capture-selected step LOSES post-completion hijack (Req 18) and a
// capture-held source may not receive a new write-capable mount or a new Pod
// UID (Req 16). The ATC cannot mint a writer ticket for a looked-up container
// -- it has no execution identity for one -- so the honest answer is a typed
// refusal rather than a ticket taken on nobody's behalf.
func (c *Container) refuseIfCaptureHeld(ctx context.Context, why string) error {
	if !c.config.OutputPlaneEnabled {
		return nil
	}
	if c.captureClass == nil {
		return nil
	}

	// WHICH path is asked matters more than that one is.
	//
	// The step handle is the wrong question for a capture-selected step. Its
	// declared output is mounted from the reserved incarnation --
	// `steps/<execution>.<generation>/<output>` -- which is a SIBLING of
	// `steps/<handle>`, so a classifier asked about the handle correctly
	// answers `unmanaged` and this refusal never fires. Asking about the
	// reservation is what makes the guard load-bearing rather than a call that
	// always says yes.
	//
	// An ordinary step has no reservation and the handle is still the right
	// question: its whole workspace is `steps/<handle>`.
	asked := c.handle
	if reserved := c.reservedDirectory(ctx); reserved != "" {
		asked = reserved
	}

	class, err := c.captureClass.CaptureClass(ctx, asked, c.captureNodeName(ctx))
	if err != nil {
		// Fail closed: an unreadable ledger is not an empty one, and the
		// operation being refused is destructive or write-capable in every
		// caller.
		return fmt.Errorf("%s is refused: the output ledger could not be read for %s: %w",
			why, asked, err)
	}
	// Only unmanaged permits it (hangar/output/ledger: Class.Destructive). A
	// sealed source is still owed its publication, and unavailable -- or an
	// answer this runtime does not know -- is not "probably fine".
	if class != captureClassUnmanaged {
		return fmt.Errorf("%s is refused: the output ledger classifies the source for %s as %q, "+
			"and only an unmanaged source may be touched: a durable output capture holds the "+
			"source until it is released. A capture-enabled task loses post-completion hijack "+
			"and a held incarnation may not receive a new write-capable mount or a new Pod UID",
			why, asked, class)
	}

	return nil
}

// reservedDirectory is the incarnation this container's step mounted, from
// whichever of the two places knows it.
//
// The spec knows it on every path that HAS a spec -- pod replacement, the
// producer's own start -- and a looked-up container has none: `LookupContainer`
// builds its Container from a handle and a DB row, with `runtime.ContainerSpec{}`.
// That is the hijack path, and it is the one path Req 18 is about, so the guard
// there was asking about the handle and being correctly told `unmanaged`.
//
// So the Pod is asked. It is the object that exists for exactly as long as the
// thing being hijacked, and `buildPod` stamped the daemon's own answer on it.
// An unreadable Pod yields "", and the caller then asks about the handle: this
// is not the fail-closed decision, the classifier call after it is, and an
// ordinary step's handle IS the right question.
func (c *Container) reservedDirectory(ctx context.Context) string {
	if reserved := captureReservedDirectory(c.containerSpec); reserved != "" {
		return reserved
	}
	if !c.lookedUp {
		return ""
	}

	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		return ""
	}

	return pod.Annotations[captureReservationAnnotation]
}

func (c *Container) captureNodeName(ctx context.Context) string {
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		return ""
	}

	return pod.Spec.NodeName
}

// captureClassifier is a read of the output ledger. It is its own small
// interface, held as its own field, rather than a StorageBackend method: a
// deployment with no output plane never has one, and the assertion "this
// operation asked the ledger" is then about a collaborator a test can supply
// rather than about a whole storage backend it would have to stub.
type captureClassifier interface {
	CaptureClass(ctx context.Context, handle, nodeName string) (string, error)
}

const (
	captureClassHeld      = "held"
	captureClassUnmanaged = "unmanaged"
)

// firstWriterFence is the writer fence of an incarnation nobody has taken over.
const firstWriterFence = output.WriterFence(1)

// stopPreservingSource is the whole of source-preserving termination, written
// once.
//
// The order is the contract and it is the reason this is one function rather
// than a flag on the delete path:
//
//	classify  ->  request the stop  ->  observe the acknowledgement  ->  ask
//	whether anything may be destroyed  ->  only then destroy it
//
// Classifying FIRST is what makes the stop safe to issue: an execution that
// never started needs no interrupting, and one that already finished must not
// be interrupted into looking stopped. Observing before asking about cleanup is
// what stops a Pod phase standing in for a witness. And DestructiveCleanupEligible
// has the last word, because the capture extension's own gates hang off it: a
// held source keeps cleanup withheld however finished the process is.
//
// It reports whether the caller may now destroy the Pod. It never destroys
// anything itself, and it never deletes a Pod, an artifact path or a hold as
// part of stopping -- "source-preserving" is the name of the operation.
//
// The sibling `exact_execution_control` track reuses this unchanged for
// executions with no capture extension: nothing below mentions one.
func (p *execProcess) stopPreservingSource(ctx context.Context) (bool, error) {
	if p.exact == nil {
		return false, fmt.Errorf("the exact execution was never admitted")
	}
	logger := lagerctx.FromContext(ctx).Session("source-preserving-stop", lager.Data{
		"execution": string(p.control.Identity.ExecutionID),
	})

	classified, err := p.exact.client.Classify(ctx, p.control.Identity)
	if err != nil {
		return false, fmt.Errorf("classifying before interrupting: %w", err)
	}

	// Only an execution that is actually running is interrupted. A stop
	// offered to one that never started, or to one that already has a durable
	// outcome, would be a request the daemon must refuse -- and issuing it
	// anyway is how "accepted" starts meaning "we asked".
	if classified.Classification == executioncontrol.ClassificationExecuting {
		stopped, err := p.exact.client.RequestStop(ctx, p.control.Identity)
		if err != nil {
			return false, fmt.Errorf("requesting a source-preserving stop: %w", err)
		}
		logger.Info("stop-requested", lager.Data{
			"accepted":       stopped.Accepted,
			"classification": string(stopped.Classification),
		})
		if state, resource, journaled := p.exactJournal(); stopped.Accepted && journaled {
			start, err := p.exact.client.RecordStart(ctx, p.control.Identity, p.exact.podUID, p.exactProcessIdentity())
			if err != nil {
				return false, err
			}
			stop := requestSupervisorStop
			if resource {
				stop = requestResourceStop
			}
			if err := stop(ctx, p.clientset, p.executor, p.config.Namespace, p.podName, p.exact.nodeName, state, start); err != nil {
				return false, err
			}
		}
	}

	// The acknowledgement, not the request. Accepted is not an outcome.
	observed, err := p.exact.client.Observe(ctx, p.control.Identity, 0)
	if err != nil {
		return false, fmt.Errorf("observing the stop acknowledgement: %w", err)
	}
	if !observed.Classification.Authoritative() {
		logger.Info("no-durable-acknowledgement", lager.Data{
			"classification": string(observed.Classification),
		})

		return false, nil
	}

	eligible, err := p.exact.client.CleanupEligible(ctx, p.control.Identity)
	if err != nil {
		return false, fmt.Errorf("asking whether cleanup is eligible: %w", err)
	}
	if !eligible.Eligible {
		logger.Info("cleanup-withheld", lager.Data{
			"reason": eligible.WithheldReason,
			"gates":  eligible.OpenExtensionGates,
		})
	}

	return eligible.Eligible, nil
}
