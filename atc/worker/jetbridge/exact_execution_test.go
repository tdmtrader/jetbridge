package jetbridge

// execProcess under exact control, against a real output daemon.
//
// The ordering is the subject and every spec here is a crash half around it:
//
//	admitted -> held -> ticketed -> START RECORDED -> the command runs
//	the command exits -> OUTCOME RECORDED -> acknowledged -> the result returns
//
// brine says none of this. Its steps are sequential by construction, and "the
// web died between these two lines" has no sentence -- which is exactly what
// distinguishes a producer that is never re-executed from one that is.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

// controlExecutor is the exec transport, and it is the thing a spec drives:
// what the command did, and what the daemon was asked WHILE it was doing it.
type controlExecutor struct {
	mu    sync.Mutex
	calls []([]string)
	run   func(attempt int) error
}

func (executor *controlExecutor) ExecInPod(_ context.Context, _, _, _ string, command []string,
	stdin io.Reader, stdout, stderr io.Writer, _ bool, _ ExecAttrs) error {
	executor.mu.Lock()
	executor.calls = append(executor.calls, command)
	attempt := len(executor.calls)
	executor.mu.Unlock()

	if stdin != nil {
		_, _ = io.ReadAll(stdin)
	}
	_ = stdout
	_ = stderr

	return executor.run(attempt)
}

func (executor *controlExecutor) count() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()

	return len(executor.calls)
}

var _ = Describe("An execProcess under exact control", func() {
	var (
		harness   *outputDaemonHarness
		clientset *fake.Clientset
		executor  *controlExecutor
		control   *runtime.ExecutionControl
		container *Container
		ctx       context.Context
		podUID    types.UID
	)

	const (
		executionID = executioncontrol.ExecutionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		handoffID   = hangaroutput.HandoffID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
		leaseID     = hangaroutput.SourceLeaseID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	)

	identity := executioncontrol.Identity{ExecutionID: executionID, Fence: 1}

	newProcess := func() *execProcess {
		return newExecProcess("proc-1", "capture-pod", clientset, container.config, container,
			executor, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
	}

	// captureAdmission is the same admission the reservation and the hold both
	// carry. It names no Pod: no Pod exists when it is first sent.
	captureAdmission := func() hangaroutput.CaptureAdmission {
		return hangaroutput.CaptureAdmission{
			ProtocolVersion: hangaroutput.ProtocolVersion,
			Execution:       identity,
			ActivationEpoch: harnessEpoch,
			HandoffID:       handoffID,
			SourceLeaseID:   leaseID,
			Output:          "result",
			CaptureDeadline: hangaroutput.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		}
	}

	// hold drives the sequence in the ONLY order a real producer has, which is
	// the order the ledger must therefore accept:
	//
	//	admit (identity, fence, node -- no Pod exists yet)
	//	  -> reserve the incarnation
	//	    -> the API server assigns a Pod UID
	//	      -> the control init holds, presenting THAT UID
	//
	// It used to admit with a Pod UID the spec invented before any Pod, which
	// is a thing no ATC can do: `buildPod` needs the reservation, so the
	// reservation cannot wait for the Pod. Every spec in this file goes
	// through here, so the whole file is now driven from a real producer's
	// point of view.
	//
	// The Pod UID is bound ONCE, at hold time, from the value the init
	// container reads off the Downward API.
	hold := func() {
		_, err := harness.Client.Admit(ctx, executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        identity,
			ActivationEpoch: harnessEpoch,
			NodeUID:         executioncontrol.NodeUID(harnessNodeUID),
			Capability:      "base-capability",
		})
		Expect(err).ToNot(HaveOccurred())

		// The reservation, which in production happens before the Pod is even
		// built: the ATC asks for the location, mounts it as the selected
		// output's volume, and puts it in the control init's environment. The
		// hold then PRESENTS it, which is how the daemon knows this init
		// container is running in the Pod the reservation was made for.
		admission := captureAdmission()
		reserved, err := harness.Client.ReserveIncarnation(ctx, admission)
		Expect(err).ToNot(HaveOccurred())

		grant, err := harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(postHold(harness.Endpoint, string(grant), admission,
			reserved.Incarnation, executioncontrol.PodUID(podUID))).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()

		started, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		harness = started
		DeferCleanup(harness.Stop)

		podUID = types.UID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "capture-pod", Namespace: "test-ns", UID: podUID,
			},
			Spec: corev1.PodSpec{
				NodeName:   "node-1",
				Containers: []corev1.Container{{Name: mainContainerName}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: mainContainerName, Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		})

		executor = &controlExecutor{run: func(int) error { return nil }}

		control = &runtime.ExecutionControl{
			Version:         runtime.ExecutionControlVersion,
			Phase:           runtime.ControlPhaseAdmitted,
			Identity:        identity,
			ActivationEpoch: harnessEpoch,
			Endpoint:        harness.Endpoint,
			Capability:      "base-capability",
		}
		Expect(control.SelectCapture(runtime.DurableOutputCapture{
			Version:            runtime.DurableOutputCaptureVersion,
			Identity:           identity,
			ActivationEpoch:    harnessEpoch,
			HandoffID:          handoffID,
			SourceLeaseID:      leaseID,
			Output:             "result",
			SourceControlGrant: "source-control-grant",
			CaptureDeadline:    time.Now().Add(time.Hour),
		})).To(Succeed())

		config := NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		config.OutputDaemonPort = 7781
		container = &Container{
			handle:   "capture-handle",
			podName:  "capture-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{
				Type:             db.ContainerTypeTask,
				Outputs:          runtime.OutputPaths{"result": "/tmp/build/result"},
				ExecutionControl: control,
			},
			clientset:      clientset,
			config:         config,
			properties:     map[string]string{},
			outputControls: harness,
		}
	})

	It("records the exact start before the command is launched, and the outcome before the result is returned", func() {
		hold()

		var duringExec executioncontrol.Classification
		executor.run = func(int) error {
			// The daemon is asked WHILE the command is running. This is the
			// half a Pod phase cannot answer: the ledger says `executing`
			// because a start was durably recorded before the launch, not
			// because a container happens to be up.
			result, err := harness.Client.Classify(ctx, identity)
			Expect(err).ToNot(HaveOccurred())
			duringExec = result.Classification

			return nil
		}

		process := newProcess()
		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		Expect(duringExec).To(Equal(executioncontrol.ClassificationExecuting),
			"the command ran before its start was durably recorded")

		after, err := harness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(after.Classification.Authoritative()).To(BeTrue())
		Expect(after.Acknowledgement).ToNot(BeNil())
		Expect(after.Acknowledgement.Outcome.ExitCode).To(Equal(0))
	})

	It("records a non-success outcome the same way, and the daemon's witness agrees with the step", func() {
		hold()
		executor.run = func(int) error { return &ExecExitError{ExitCode: 3} }

		result, err := newProcess().Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(3))

		observed, err := harness.Client.Observe(ctx, identity, 0)
		Expect(err).ToNot(HaveOccurred())
		Expect(observed.Acknowledgement).ToNot(BeNil())
		Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(3),
			"the witness says something the step does not")
	})

	It("refuses to launch the producer when the source hold is not acknowledged", func() {
		// No hold. Req 3: the producer's main process may not start before the
		// daemon durably acknowledges one, and start fails CLOSED.
		_, err := newProcess().Wait(ctx)
		Expect(err).To(HaveOccurred())
		// The message names the guard, not just the failure. The hold is
		// revalidated BEFORE any writer ticket is taken -- an operator whose
		// producer will not start needs to know which of the two refused, and
		// a runtime that reached the ticket first would report the wrong one.
		Expect(err.Error()).To(ContainSubstring("revalidating the source hold"))
		Expect(executor.count()).To(Equal(0),
			"the command was launched without an acknowledged source hold")
	})

	// The hold's Pod UID arm, which Phase 4 recorded as having no vector of its
	// own. It has one, and it does not need Phase 5's takeover.
	//
	// A hold's statement carries the Pod UID the execution was ADMITTED for. A
	// pause pod that goes terminal is REPLACED, and a replacement is a new Pod
	// UID under the same execution identity and the same fence -- no takeover,
	// no epoch bump, nothing Phase 5 owns. Container.Run now refuses that
	// replacement over a held source; this arm is the second door, for a Pod
	// replaced by some other route before the producer reached its start.
	//
	// The control is in the same spec and runs first: with the Pod the hold
	// names, this exact process starts and the command runs.
	It("refuses to start a producer whose Pod is not the one the hold names", func() {
		hold()

		// The control, and it is the arm's premise: before anything is
		// disturbed, the hold in force names THIS Pod. Without it a refusal
		// below could be a hold that names nothing.
		acknowledged, err := harness.Client.InspectHold(ctx, identity, handoffID)
		Expect(err).ToNot(HaveOccurred())
		Expect(acknowledged.PodUID).To(Equal(executioncontrol.PodUID(podUID)))

		// The Pod was replaced: same execution, same fence, a new incarnation.
		// Nothing re-admitted the execution, so the ledger's hold still names
		// the old UID -- which is exactly the state this arm is for.
		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "capture-pod",
			metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.UID = types.UID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
		_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// The producer's first start, in the replaced Pod.
		_, err = newProcess().Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a recreated Pod is a new incarnation"))
		Expect(executor.count()).To(Equal(0),
			"the producer ran in a Pod the hold does not name")
	})

	// The whole sequence, in the only order a real producer has, asserted as an
	// order rather than as an outcome.
	//
	// This is the Phase 4 round-1 review's probe P2. Every fixture in the phase
	// used to admit the execution WITH a Pod UID, which is a thing no ATC can
	// do: `buildPod` mounts the reserved incarnation, so the reservation
	// precedes the Pod, so the admission that authorizes the reservation
	// precedes it too. Driven honestly, the old ledger bound the hold to an
	// empty Pod UID and then refused the producer's own start.
	It("admits, reserves and holds in the only order a producer has, and then admits its own producer", func() {
		// 1. Admission. No Pod exists: nothing has been created yet, and the
		//    envelope has no field to name one with.
		_, err := harness.Client.Admit(ctx, executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        identity,
			ActivationEpoch: harnessEpoch,
			NodeUID:         executioncontrol.NodeUID(harnessNodeUID),
			Capability:      "base-capability",
		})
		Expect(err).ToNot(HaveOccurred())

		// 2. The reservation, which is what the Pod will mount. It is issued
		//    against the admission above and names no Pod either.
		admission := captureAdmission()
		reserved, err := harness.Client.ReserveIncarnation(ctx, admission)
		Expect(err).ToNot(HaveOccurred())
		Expect(reserved.Directory).To(Equal(reserved.Incarnation.Directory()))

		// 3. The Pod. In this spec it is already in the fake API server, which
		//    is the point at which a UID first exists at all.
		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "capture-pod", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pod.UID).ToNot(BeEmpty())

		// 4. The control init's hold, presenting the Downward API's value.
		grant, err := harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(postHold(harness.Endpoint, string(grant), admission,
			reserved.Incarnation, executioncontrol.PodUID(pod.UID))).To(Succeed())

		// The hold binds THAT Pod, not the empty one an admission could offer.
		acknowledged, err := harness.Client.InspectHold(ctx, identity, handoffID)
		Expect(err).ToNot(HaveOccurred())
		Expect(acknowledged.PodUID).To(Equal(executioncontrol.PodUID(pod.UID)))
		Expect(acknowledged.Incarnation).To(Equal(reserved.Incarnation))

		// 5. And the producer the ATC admitted is the producer it now runs.
		//    This is the line that was red: `beginExactCommand` revalidates the
		//    hold against its own Pod UID and used to refuse itself.
		result, err := newProcess().Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))
		Expect(executor.count()).To(Equal(1))
	})

	It("holds a writer ticket for every writer in the pod before the command runs", func() {
		hold()

		process := newProcess()
		_, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())

		// One per writer container. The ATC takes them because the containers
		// cannot: Req 24 gives the task and its sidecars no credential.
		Expect(process.exact).ToNot(BeNil())
	})

	It("never runs the command a second time when the transport loses its answer", func() {
		hold()
		executor.run = func(int) error {
			// The transport carried bytes and then broke: the command has
			// started talking, so nothing may run it again.
			return fmt.Errorf("unable to upgrade connection: the apiserver went away")
		}

		process := newProcess()
		process.execTransportLive.Store(true)
		_, err := process.Wait(ctx)
		Expect(err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(executor.count()).To(Equal(1),
			"the producer was executed again after its exact start was recorded")

		// And the ledger is not left claiming an outcome it does not have.
		classified, err := harness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(classified.Classification.Authoritative()).To(BeFalse())
	})

	It("reports the recorded outcome after a lost answer rather than re-invoking", func() {
		hold()

		// The first attempt runs and records its outcome.
		Expect(func() error {
			_, err := newProcess().Wait(ctx)

			return err
		}()).To(Succeed())
		Expect(executor.count()).To(Equal(1))

		// The second ATC attaches to the same identity. It asks the ledger
		// BEFORE launching anything, finds the outcome already there, and
		// reports it -- so the command is not run a second time and the
		// transport is never even dialled.
		executor.run = func(int) error {
			Fail("the producer was executed again after a durable outcome existed")

			return nil
		}
		result, err := newProcess().Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))
		Expect(executor.count()).To(Equal(1),
			"the recovery attempt executed the command instead of reading the ledger")
	})

	It("reports the supervisor's own unresolved exit as unresolved, not as a failure", func() {
		hold()
		executor.run = func(int) error {
			return &ExecExitError{ExitCode: ExactUnresolvedExitCode}
		}

		_, err := newProcess().Wait(ctx)
		Expect(err).To(MatchError(ErrExactOutcomeUnresolved))

		// "We do not know" is not "it failed": nothing recorded an outcome, so
		// nothing downstream may release a hold or authorize cleanup.
		classified, err := harness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(classified.Classification.Authoritative()).To(BeFalse())
	})

	It("runs the exact supervisor script for a controlled task and the ordinary one otherwise", func() {
		hold()
		container.containerSpec.Type = db.ContainerTypeTask
		process := newExecProcess("proc-sup", "capture-pod", clientset, container.config, container,
			executor, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
		_, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())

		executor.mu.Lock()
		command := executor.calls[0]
		executor.mu.Unlock()
		Expect(command[2]).To(ContainSubstring("/start"),
			"a controlled execution ran under the ordinary supervisor, which relaunches a "+
				"command whose runner died")
	})

	// The stop/cleanup matrix. Every row is the SAME sequence -- classify,
	// stop only what is executing, observe, ask about cleanup -- against a
	// different state, and what changes is only the answer.
	//
	// `wantStops` is the reviewer's F6: "classifies before it interrupts" was
	// the name of this table and not an assertion in it, so issuing the stop
	// whatever the classification left all three rows green. It matters
	// because the daemon ACCEPTS a stop for a never-started execution -- only a
	// terminal one refuses -- and marks StopRequested on its record, so the
	// ATC's ordering is the only thing that keeps a producer from being told to
	// stop before it starts. What is counted is the CALL, at the one collaborator
	// that would receive it.
	DescribeTable("a source-preserving stop classifies before it interrupts, and destroys nothing",
		func(prepare func(), wantEligible bool, wantHeld bool, wantStops int) {
			hold()
			prepare()

			counted := &countingOutputControl{OutputControl: harness.Client}
			container.outputControls = staticOutputControls{control: counted}

			process := newProcess()
			Expect(process.admitWhenScheduled(ctx)()).To(Succeed())

			eligible, err := process.stopPreservingSource(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(eligible).To(Equal(wantEligible))
			Expect(counted.stops).To(Equal(wantStops),
				"an execution that is not executing was interrupted anyway; the daemon accepts "+
					"that stop and records it, so the ordering here is the only guard")

			// Nothing was destroyed. The Pod is still there, and so is the
			// hold -- "source-preserving" is the name of the operation, and a
			// stop that removed either would be the other operation.
			_, err = clientset.CoreV1().Pods("test-ns").Get(ctx, "capture-pod", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred(), "the stop deleted the producer's Pod")

			inspected, holdErr := harness.Client.InspectHold(ctx, identity, handoffID)
			if wantHeld {
				Expect(holdErr).ToNot(HaveOccurred())
				Expect(inspected.Kind).To(Equal(hangaroutput.CaptureHoldAcknowledged),
					"the stop released the source hold")
			}

			// And it is idempotent: the same call again, same answer, and
			// still nothing destroyed. A stop whose response was lost is
			// repeated, not escalated.
			again, err := process.stopPreservingSource(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(again).To(Equal(eligible))
		},
		Entry("never started: there is nothing to interrupt and nothing to destroy",
			func() {}, false, true, 0),
		Entry("executing: the interrupt is issued and the source survives it",
			func() {
				_, err := harness.Client.RecordStart(context.Background(), executioncontrol.Identity{
					ExecutionID: executionID, Fence: 1,
				}, executioncontrol.PodUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"), "proc-1")
				Expect(err).ToNot(HaveOccurred())
			}, false, true, 1),
		Entry("naturally finished with its hold still open: cleanup stays withheld",
			func() {
				id := executioncontrol.Identity{ExecutionID: executionID, Fence: 1}
				_, err := harness.Client.RecordStart(context.Background(), id,
					executioncontrol.PodUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"), "proc-1")
				Expect(err).ToNot(HaveOccurred())
				_, err = harness.Client.RecordOutcome(context.Background(), id,
					executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{})
				Expect(err).ToNot(HaveOccurred())
			}, false, true, 0),
	)

	It("keeps an abandoned producer's Pod while its source is still held", func() {
		hold()

		// The step is abandoned mid-command: the context ends while the exec
		// is in flight, which is an abort or a timeout.
		abortable, abort := context.WithCancel(ctx)
		executor.run = func(int) error {
			abort()

			return fmt.Errorf("context canceled")
		}
		process := newProcess()
		process.container.containerSpec.Type = db.ContainerTypeTask
		_, _ = process.Wait(abortable)

		// The Pod is NOT deleted. Its source is held, so nothing about it is
		// disposable yet -- and today's unconditional delete would have taken
		// the workspace with it.
		_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "capture-pod", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred(),
			"the abandoned Pod was deleted while its source was still held")
		Expect(process.exactCleanupEligible).To(BeFalse())
	})

	It("leaves a step that opted into nothing on its old path entirely", func() {
		// The control for every spec above. With no envelope the daemon is
		// never asked, the supervisor is the ordinary one, and the result is
		// the exit code the command gave.
		container.containerSpec.ExecutionControl = nil
		process := newProcess()
		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))
		Expect(process.exact).To(BeNil())

		classified, err := harness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(classified.Classification).To(Equal(executioncontrol.ClassificationNeverStarted),
			"an ordinary step reached the exact-execution ledger")
	})
})

// countingOutputControl is the real client with one call counted.
//
// It counts rather than answers: every operation still reaches the real daemon
// and the daemon's own refusals still apply, so a row that stops asserts an
// interruption that really happened. A double here would let the ordering be
// asserted against nothing.
type countingOutputControl struct {
	OutputControl
	stops int
}

func (counted *countingOutputControl) RequestStop(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	counted.stops++

	return counted.OutputControl.RequestStop(ctx, id)
}

// staticOutputControls is one node, one control, which is what a spec with one
// Pod has.
type staticOutputControls struct{ control OutputControl }

func (controls staticOutputControls) ForNode(context.Context, string) (OutputControl, error) {
	return controls.control, nil
}

// heldClassifier is a read of the output ledger with a fixed answer.
//
// The real one goes to the artifact daemon's classification route, and what it
// answers is pinned by cmd/artifact-daemon's own suite against a real ledger.
// What these specs are about is the two operations that ASK it: hijack and pause
// pod replacement have no execution identity to take a writer ticket with, so
// asking and refusing is all they can do, and whether they ask is the assertion.
type heldClassifier struct {
	class string
	err   error
	asked []string
}

func (classifier *heldClassifier) CaptureClass(_ context.Context, handle, _ string) (string, error) {
	classifier.asked = append(classifier.asked, handle)

	return classifier.class, classifier.err
}

var _ = Describe("Destructive operations over a capture-held source", func() {
	var (
		clientset  *fake.Clientset
		container  *Container
		classifier *heldClassifier
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		clientset = fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "held-pod", Namespace: "test-ns"},
			Spec:       corev1.PodSpec{NodeName: "node-1"},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed},
		})
		classifier = &heldClassifier{class: captureClassUnmanaged}

		config := NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		container = &Container{
			handle:       "held-handle",
			podName:      "held-pod",
			metadata:     db.ContainerMetadata{Type: db.ContainerTypeTask},
			clientset:    clientset,
			config:       config,
			properties:   map[string]string{},
			captureClass: classifier,
		}
	})

	// The control first, and it is the regression the whole refusal must not
	// become: an ordinary terminal pause pod is still replaced. Without this
	// line, "a capture-held one is refused" would pass on a runtime that had
	// stopped replacing pause pods at all.
	It("replaces an ordinary terminal pause pod, and refuses to replace a capture-held one", func() {
		process := newExecProcess("proc", "held-pod", clientset, container.config, container,
			nil, runtime.ProcessSpec{Path: "/bin/sh"},
			runtime.ProcessIO{Stderr: &bytes.Buffer{}}, nil)

		Expect(container.refuseIfCaptureHeld(ctx, "recreating the pause pod")).To(Succeed())
		Expect(classifier.asked).To(ContainElement("held-handle"))

		classifier.class = captureClassHeld
		err := process.recreatePausePod(ctx, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("durable output capture holds the source"))
		Expect(process.pausePodRecreated).To(BeFalse(),
			"a refused replacement still spent the one replacement this step gets")
	})

	// WHICH path the guard asks about, which is the completion pass's whole
	// point on this side.
	//
	// The step handle is the wrong question for a capture-selected step: the
	// producer's declared output is now the reserved incarnation, a SIBLING of
	// `steps/<handle>`, so a classifier asked about the handle correctly
	// answers `unmanaged` and the refusal never fires. It has to ask about the
	// directory the hold protects.
	//
	// The control is asserted first and it is the ordinary step: with no
	// capture on the spec there is no reservation, and the handle is still the
	// right question.
	It("asks the ledger about the reserved incarnation, and about the handle otherwise", func() {
		Expect(container.refuseIfCaptureHeld(ctx, "recreating the pause pod")).To(Succeed())
		Expect(classifier.asked).To(Equal([]string{"held-handle"}),
			"an ordinary step's guard stopped asking about its own step directory")

		container.containerSpec.ExecutionControl = admittedCapture()
		reserved := container.containerSpec.ExecutionControl.Capture.ReservedDirectory
		Expect(reserved).ToNot(BeEmpty())

		classifier.class = captureClassHeld
		classifier.asked = nil
		err := container.refuseIfCaptureHeld(ctx, "recreating the pause pod")
		Expect(err).To(HaveOccurred())
		Expect(classifier.asked).To(ContainElement(reserved),
			"the guard asked about the step handle, which is a sibling of the directory the "+
				"hold protects; a classifier answering about it can only ever say unmanaged")
		Expect(classifier.asked).ToNot(ContainElement("held-handle"),
			"the guard asked BOTH, so a fix that added the incarnation without dropping the "+
				"handle would still refuse an ordinary reused handle for the wrong reason")
	})

	// The hijack path, which is the ONE path that is hijack, and the one the
	// completion pass left with a vacuous guard.
	//
	// `LookupContainer` builds its Container with `runtime.ContainerSpec{}` --
	// there is no spec behind a lookup -- so the guard above fell through to
	// the handle, which is a sibling of the incarnation, and the classifier
	// correctly answered `unmanaged`. Req 18 takes post-completion hijack away
	// from a capture-enabled task, and it was being taken away from nobody.
	//
	// The Pod is where a looked-up container's facts live, so the reservation
	// is stamped on it as an annotation at build time and read back here. The
	// control is first: a looked-up container over an ORDINARY pod still asks
	// about its handle.
	It("asks the ledger about the reservation on the Pod when there is no spec", func() {
		looked := &Container{
			handle:       "held-handle",
			podName:      "held-pod",
			metadata:     db.ContainerMetadata{Type: db.ContainerTypeTask},
			clientset:    clientset,
			config:       container.config,
			properties:   map[string]string{},
			captureClass: classifier,
			lookedUp:     true,
		}

		// The control: an ordinary pod carries no reservation annotation, so
		// the handle is still the right question.
		Expect(looked.refuseIfCaptureHeld(ctx, "hijacking the container")).To(Succeed())
		Expect(classifier.asked).To(Equal([]string{"held-handle"}))

		// The capture-selected pod says what it mounted -- and it says so
		// because PRODUCTION stamped it. The annotation is not written here:
		// a spec that writes the key it then reads pins the reader and leaves
		// the writer to nothing, which is what the Phase 4 round-2 review
		// found (M5: `buildPod` stamping nothing reddened no committed test).
		// The Pod below comes out of `Container.buildPod` and is created as
		// it stands.
		reserved := admittedCapture().Capture.ReservedDirectory
		Expect(reserved).ToNot(BeEmpty())

		builder := &Container{
			handle:         "held-handle",
			podName:        "held-pod",
			workerName:     "worker-1",
			metadata:       db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec:  capturingSpec(admittedCapture()),
			config:         container.config,
			properties:     map[string]string{},
			storageBackend: NewDaemonSetBackend(container.config, nil, nil),
		}
		built, err := builder.buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(built.Annotations).To(HaveKeyWithValue(captureReservationAnnotation, reserved),
			"buildPod stamped no reservation, so a looked-up container has nothing to read "+
				"and the hijack refusal Req 18 requires never fires")

		Expect(clientset.CoreV1().Pods("test-ns").Delete(ctx, "held-pod",
			metav1.DeleteOptions{})).To(Succeed())
		_, err = clientset.CoreV1().Pods("test-ns").Create(ctx, built, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		classifier.class = captureClassHeld
		classifier.asked = nil
		err = looked.refuseIfCaptureHeld(ctx, "hijacking the container")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("loses post-completion hijack"))
		Expect(classifier.asked).To(ContainElement(reserved),
			"the hijack door asked about the handle, which a looked-up container is all it has; "+
				"a classifier answering about a sibling of the held directory can only say "+
				"unmanaged, so the refusal Req 18 requires never fires")
		Expect(classifier.asked).ToNot(ContainElement("held-handle"))
	})

	It("fails closed when the ledger cannot be read", func() {
		classifier.err = fmt.Errorf("the daemon is unreachable")
		err := container.refuseIfCaptureHeld(ctx, "recreating the pause pod")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("could not be read"))
	})

	It("asks nothing at all when the output plane is off", func() {
		container.config.OutputPlaneEnabled = false
		Expect(container.refuseIfCaptureHeld(ctx, "recreating the pause pod")).To(Succeed())
		Expect(classifier.asked).To(BeEmpty(),
			"a deployment with no output plane asked a ledger that does not exist")
	})
})
