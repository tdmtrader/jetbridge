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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	stdin io.Reader, stdout, stderr io.Writer, _ bool, attrs ExecAttrs) error {
	if attrs.Purpose != "step-command" {
		// This legacy transport has no supervisor filesystem. The real read
		// and restart behavior is exercised by execution-outcome-recovery.feature.
		return fmt.Errorf("test transport has no retained supervisor journal")
	}
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
		leaseID     = hangaroutput.SourceHoldID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
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
			SourceHoldID:    leaseID,
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

		warrant, err := harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(postHold(harness.Endpoint, string(warrant), admission,
			reserved.Incarnation, executioncontrol.PodUID(podUID))).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()

		// Every spec here shares one identity, so a resource command's
		// journal, which is named after it, is cleared on both sides.
		resourceJournal := exactResourceStateDir(identity)
		Expect(os.RemoveAll(resourceJournal)).To(Succeed())
		DeferCleanup(func() { Expect(os.RemoveAll(resourceJournal)).To(Succeed()) })

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
			SourceHoldID:       leaseID,
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

	DescribeTable("refuses a cancelled start before recording the node start or launching the command",
		func(containerType db.ContainerType) {
			hold()
			container.metadata.Type = containerType
			container.checkStart = func(context.Context) error { return db.ErrPipelineRunCancelling }

			_, err := newProcess().Wait(ctx)
			Expect(err).To(MatchError(db.ErrPipelineRunCancelling))
			Expect(executor.count()).To(BeZero())
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
		},
		Entry("task", db.ContainerTypeTask),
		Entry("check", db.ContainerTypeCheck),
		Entry("get", db.ContainerTypeGet),
		Entry("put", db.ContainerTypePut),
	)

	DescribeTable("delivers an admitted supervisor through cancellation before transport dispatch",
		func(cancelTaskContext bool) {
			hold()
			taskCtx, cancelTask := context.WithCancel(ctx)
			DeferCleanup(cancelTask)
			local := &stoppedSupervisorExecutor{stopDelivered: make(chan struct{})}
			process := newProcess()
			process.id = fmt.Sprintf("cancel-before-dispatch-%d", time.Now().UnixNano())
			process.executor = local
			producerMarker := filepath.Join(GinkgoT().TempDir(), "producer-ran")
			process.processSpec.Args = []string{"-c", `touch "$1"`, "test-command", producerMarker}
			_, state := supervisorCommandParts(process.id, process.processSpec)
			DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })
			cancelled := false
			var outcomes []executioncontrol.Acknowledgement
			container.checkStart = func(context.Context) error {
				if cancelled {
					return db.ErrPipelineRunCancelling
				}
				return nil
			}
			container.recordWitness = func(witnessCtx context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind != executioncontrol.AcknowledgementStart {
					outcomes = append(outcomes, witness)
					return nil
				}
				// The Run cancellation commits immediately after the signed start
				// is retained. Its stop reaches the original Pod before dispatch.
				cancelled = true
				if cancelTaskContext {
					cancelTask()
					return witnessCtx.Err()
				}
				_, err := process.stopPreservingSource(ctx)
				return err
			}

			result, err := process.Wait(taskCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(143))
			Expect(local.commandCount()).To(Equal(1))
			_, err = os.Stat(producerMarker)
			Expect(os.IsNotExist(err)).To(BeTrue(), "cancelled supervisor launched the producer")
			Expect(outcomes).To(HaveLen(1))
			Expect(outcomes[0].Outcome.ExitCode).To(Equal(143))
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification.Authoritative()).To(BeTrue())
			Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(143))

			// A controller reconnect reads the closed execution even though the
			// Run gate is now closed. It must not dispatch another supervisor.
			replay := newProcess()
			replay.id, replay.executor = process.id, local
			replay.processSpec = process.processSpec
			result, err = replay.Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(143))
			Expect(local.commandCount()).To(Equal(1))
		},
		Entry("with a live task context", false),
		Entry("with the task context already cancelled", true),
	)

	DescribeTable("delivers an admitted resource command after Run cancellation closes admission",
		func(containerType db.ContainerType) {
			hold()
			container.metadata.Type = containerType
			cancelled := false
			container.checkStart = func(context.Context) error {
				if cancelled {
					return db.ErrPipelineRunCancelling
				}
				return nil
			}
			container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					cancelled = true
				}
				return nil
			}

			result, err := newProcess().Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(BeZero())
			Expect(executor.count()).To(Equal(1))
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification.Authoritative()).To(BeTrue())

			// The replay dispatches nothing. It asks for no answer either: this
			// transport has no journal to read one from, and reading it back is
			// exact_resource_journal_test.go's, against the real wrapper.
			replay := newProcess()
			replay.processIO.Stdout = nil
			_, err = replay.Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(executor.count()).To(Equal(1))
		},
		Entry("check", db.ContainerTypeCheck),
		Entry("get", db.ContainerTypeGet),
		Entry("put", db.ContainerTypePut),
	)

	DescribeTable("retains a resource cancellation outcome when its context ends before transport dispatch",
		func(containerType db.ContainerType) {
			hold()
			container.metadata.Type = containerType
			taskCtx, cancelTask := context.WithCancel(ctx)
			DeferCleanup(cancelTask)
			local := &stoppedResourceExecutor{stopDelivered: make(chan struct{})}
			DeferCleanup(func() { Expect(os.RemoveAll(local.state)).To(Succeed()) })
			process := newProcess()
			process.executor = local
			var outcomes []executioncontrol.Acknowledgement
			container.recordWitness = func(witnessCtx context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					cancelTask()
					return witnessCtx.Err()
				}
				outcomes = append(outcomes, witness)
				return nil
			}

			_, err := process.Wait(taskCtx)
			Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "%v", err)
			Expect(local.commands).To(Equal(1))
			Expect(local.stops).To(Equal(1))
			Expect(outcomes).To(HaveLen(1))
			Expect(outcomes[0].Outcome.ExitCode).To(Equal(130))
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification.Authoritative()).To(BeTrue())
			Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(130))

			replay := newProcess()
			replay.executor = local
			result, err := replay.Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(130))
			Expect(local.commands).To(Equal(1))
			Expect(local.stops).To(Equal(1))
		},
		Entry("check", db.ContainerTypeCheck),
		Entry("get", db.ContainerTypeGet),
		Entry("put", db.ContainerTypePut),
	)

	// The node committed the start and the Run could not retain it: its
	// database was unavailable, or the Run's publication lock was held past
	// the witness budget. Returning there would leave an executing ledger with
	// no supervisor to write an outcome and no retained start for cancellation
	// to interrupt. What is delivered is the stopped delivery -- the stop, the
	// claim of the start and the journaled 143 in one exec -- so the producer
	// never runs. The daemon outcome waits for the start witness, because once
	// an outcome exists the node no longer answers for its start and the Run
	// could never close.
	DescribeTable("stops and delivers a supervised command whose start the Run could not retain",
		func(retainOnRetry bool) {
			hold()
			local := &hostPodExecutor{}
			process := newProcess()
			process.id = fmt.Sprintf("unretained-start-%d", time.Now().UnixNano())
			process.executor = local
			producerMarker := filepath.Join(GinkgoT().TempDir(), "producer-ran")
			process.processSpec.Args = []string{"-c", `touch "$1"`, "test-command", producerMarker}
			_, state := supervisorCommandParts(process.id, process.processSpec)
			DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

			starts := 0
			var outcomes []executioncontrol.Acknowledgement
			container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					starts++
					if !retainOnRetry || starts == 1 {
						return errors.New("db unavailable")
					}
					return nil
				}
				outcomes = append(outcomes, witness)
				return nil
			}

			_, err := process.Wait(ctx)
			Expect(err).To(MatchError(ContainSubstring("db unavailable")))
			Expect(local.stepCommandCount()).To(Equal(1), "the admitted command was never delivered")
			_, err = os.Stat(producerMarker)
			Expect(os.IsNotExist(err)).To(BeTrue(), "an unwitnessed start ran the producer")
			journal, err := os.ReadFile(filepath.Join(state, "exit"))
			Expect(err).NotTo(HaveOccurred(), "the supervisor left no outcome writer behind")
			Expect(string(journal)).To(Equal("143\n"))

			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			if retainOnRetry {
				Expect(observed.Classification.Authoritative()).To(BeTrue())
				Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(143))
				Expect(outcomes).To(HaveLen(1))
			} else {
				Expect(observed.Classification).To(Equal(executioncontrol.ClassificationExecuting),
					"an outcome recorded before its start was retained can never close the Run")
				Expect(outcomes).To(BeEmpty())
			}

			// The Run's database is back. Replay retains the node's original
			// start, then records the journaled stop, and dispatches nothing.
			container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					starts++
					return nil
				}
				outcomes = append(outcomes, witness)
				return nil
			}
			replay := newProcess()
			replay.id, replay.executor, replay.processSpec = process.id, local, process.processSpec
			result, err := replay.Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(143))
			Expect(local.stepCommandCount()).To(Equal(1))
			observed, err = harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification.Authoritative()).To(BeTrue())
			Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(143))
			Expect(outcomes).NotTo(BeEmpty())
			Expect(outcomes[len(outcomes)-1].Outcome.ExitCode).To(Equal(143))
		},
		Entry("while the Run's database stays unavailable", false),
		Entry("when the Run's database returns before the outcome", true),
	)

	DescribeTable("cancels and delivers a resource command whose start the Run could not retain",
		func(containerType db.ContainerType) {
			hold()
			container.metadata.Type = containerType
			local := &hostPodExecutor{}
			state := exactResourceStateDir(identity)
			process := newProcess()
			process.executor = local
			container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					return errors.New("db unavailable")
				}
				Fail("an outcome witness was retained without its start")
				return nil
			}

			_, err := process.Wait(ctx)
			Expect(err).To(MatchError(ContainSubstring("db unavailable")))
			Expect(local.stepCommandCount()).To(Equal(1), "the admitted command was never delivered")
			_, err = os.Stat(filepath.Join(state, "cancel"))
			Expect(err).NotTo(HaveOccurred(), "the unwitnessed command was not cancelled before dispatch")
			journal, err := os.ReadFile(filepath.Join(state, "exit"))
			Expect(err).NotTo(HaveOccurred(), "the stopped delivery left no outcome writer behind")
			Expect(string(journal)).To(Equal("130\n"))
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification).To(Equal(executioncontrol.ClassificationExecuting))
		},
		Entry("check", db.ContainerTypeCheck),
		Entry("get", db.ContainerTypeGet),
		Entry("put", db.ContainerTypePut),
	)

	// The stop grace expired with the answer still in flight. The step's
	// context is gone, and the node's ledger is still owed the question: the
	// in-pod supervisor journals an exit whether or not the transport lived to
	// carry it.
	DescribeTable("reads the ledger after the stop grace expires",
		func(journaled bool) {
			grace := exactStopGrace
			// The grace also bounds the stop request itself, which here is a
			// real `sh`: at 500ms a loaded CI node killed it before it wrote
			// the stop marker, and the supervisor then ran to exit 0.
			exactStopGrace = 2 * time.Second
			DeferCleanup(func() { exactStopGrace = grace })

			hold()
			taskCtx, cancelTask := context.WithCancel(ctx)
			DeferCleanup(cancelTask)
			process := newProcess()
			process.id = fmt.Sprintf("late-answer-%d", time.Now().UnixNano())
			_, state := supervisorCommandParts(process.id, process.processSpec)
			local := &lateAnswerExecutor{journal: journaled, dispatched: cancelTask, state: state}
			process.executor = local
			DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })
			container.recordWitness = func(context.Context, executioncontrol.Acknowledgement) error { return nil }

			result, err := process.Wait(taskCtx)
			Expect(local.commands).To(Equal(1))
			if journaled {
				Expect(err).NotTo(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(143))
				observed, err := harness.Client.Classify(ctx, identity)
				Expect(err).NotTo(HaveOccurred())
				Expect(observed.Classification.Authoritative()).To(BeTrue())
				Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(143))
				return
			}
			Expect(err).To(MatchError(ErrExactOutcomeUnresolved))
			Expect(err.Error()).NotTo(ContainSubstring("classifying after a lost answer"),
				"the ledger was asked under the step's own cancelled context")
			Expect(errors.Is(err, context.Canceled)).To(BeTrue(),
				"a cancelled task must be classified as aborted like a cancelled resource: %v", err)
		},
		Entry("and the supervisor journaled its stop", true),
		Entry("and the delivered supervisor has journaled no exit yet", false),
	)

	// The ledger and the Run's witness are asked under their own detached
	// budget, whose deadline is not the step's. A step reads
	// context.DeadlineExceeded as its own timeout and fails; a stall here is an
	// outcome nobody can prove yet, so it must reach the step as unresolved and
	// carry no deadline the step did not set.
	DescribeTable("reports a ledger or witness stall as unresolved, never as a timeout",
		func(stall string) {
			budget := exactLedgerBudget
			exactLedgerBudget = 300 * time.Millisecond
			DeferCleanup(func() { exactLedgerBudget = budget })
			hold()
			stalled := &stallingOutputControl{OutputControl: harness.Client, stall: stall}
			container.outputControls = staticOutputControls{control: stalled}
			container.recordWitness = func(witnessCtx context.Context, witness executioncontrol.Acknowledgement) error {
				if (stall == "start witness" && witness.Kind == executioncontrol.AcknowledgementStart) ||
					(stall == "outcome witness" && witness.Kind != executioncontrol.AcknowledgementStart) {
					<-witnessCtx.Done()
					return witnessCtx.Err()
				}
				return nil
			}

			_, err := newProcess().Wait(ctx)
			Expect(err).To(MatchError(ErrExactOutcomeUnresolved))
			Expect(errors.Is(err, context.DeadlineExceeded)).To(BeFalse(),
				"a ledger budget's deadline reached the step as its own timeout: %v", err)
			Expect(ctx.Err()).To(BeNil())
		},
		Entry("recording the outcome", "outcome"),
		Entry("observing the acknowledgement", "observe"),
		Entry("retaining the start in the Run", "start witness"),
		Entry("retaining the outcome in the Run", "outcome witness"),
	)

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
		warrant, err := harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(postHold(harness.Endpoint, string(warrant), admission,
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

		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "capture-pod", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Spec.InitContainers = []corev1.Container{{Name: "writer-init"}}
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "writer-sidecar"})
		_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		process := newProcess()
		executor.run = func(int) error {
			// The main container, init container, and sidecar are all writers.
			// Check the daemon while the command is running: completion retires
			// tickets, so inspecting afterward would only prove retirement.
			Expect(process.exact).ToNot(BeNil())
			Expect(process.exact.tickets).To(HaveLen(3))

			seen := map[hangaroutput.WriterTicketID]bool{}
			for _, ticket := range process.exact.tickets {
				Expect(seen[ticket.WriterTicketID]).To(BeFalse())
				seen[ticket.WriterTicketID] = true

				inspection, err := harness.Client.InspectWriter(ctx, identity, handoffID, ticket.WriterTicketID)
				Expect(err).ToNot(HaveOccurred())
				Expect(inspection.Issued.WriterTicketID).To(Equal(ticket.WriterTicketID))
				Expect(inspection.Issued.PodUID).To(Equal(executioncontrol.PodUID(podUID)))
			}

			return nil
		}
		_, err = process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
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
	// observe, ask about cleanup -- against a
	// different nonexecuting state. Active interruption runs real processes in
	// Brine run-active-interruption.feature; this legacy transport has no journal.
	//
	// `wantStops` is the reviewer's F6: "classifies before it interrupts" was
	// the name of this table and not an assertion in it, so issuing the stop
	// whatever the classification left all nonexecuting rows green. It matters
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

// Run the generated supervisor only after its real stop script has completed.
// The stop precedes setsid, so this regression also executes on macOS without
// substituting a different supervisor or pretending that a stop is an outcome.
type stoppedSupervisorExecutor struct {
	mu            sync.Mutex
	commands      int
	stopDelivered chan struct{}
	stopOnce      sync.Once
}

func (executor *stoppedSupervisorExecutor) ExecInPod(ctx context.Context, _, _, _ string, command []string,
	stdin io.Reader, stdout, stderr io.Writer, _ bool, attrs ExecAttrs) error {
	if attrs.Purpose == "step-command" {
		select {
		case <-executor.stopDelivered:
		case <-ctx.Done():
			return ctx.Err()
		}
		executor.mu.Lock()
		executor.commands++
		executor.mu.Unlock()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return &ExecExitError{ExitCode: exit.ExitCode()}
		}
		return err
	}
	if attrs.Purpose == "exact-stop-request" {
		executor.stopOnce.Do(func() { close(executor.stopDelivered) })
	}
	return nil
}

func (executor *stoppedSupervisorExecutor) commandCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.commands
}

// lateAnswerExecutor delivers the step command and loses its answer: the
// transport stays open until the stop grace has expired. When journal is set
// the delivered supervisor then runs to its exit journal on the "node", after
// the transport that would have carried its status is gone. Otherwise the
// delivery has claimed its start and has not exited: a command still running.
// Every other exec (the stop request, the outcome read) runs the real script
// on the host.
type lateAnswerExecutor struct {
	journal    bool
	dispatched func()
	commands   int
	state      string
}

func (executor *lateAnswerExecutor) ExecInPod(ctx context.Context, _, _, _ string, command []string,
	stdin io.Reader, stdout, stderr io.Writer, _ bool, attrs ExecAttrs) error {
	if attrs.Purpose == "step-command" {
		executor.commands++
		if !executor.journal {
			if err := os.MkdirAll(executor.state, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(executor.state, "start"), []byte("started\n"), 0o600); err != nil {
				return err
			}
		}
		executor.dispatched()
		<-ctx.Done()
		if executor.journal {
			_ = exec.Command(command[0], command[1:]...).Run()
		}
		return ctx.Err()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return &ExecExitError{ExitCode: exit.ExitCode()}
		}
		return err
	}
	return nil
}

// The resource cancel script can run without procfs when it precedes dispatch:
// it creates the real cancellation marker and finds no PID to signal. The
// wrapper's matching exit is exercised by resource_process_test.go on Linux;
// here the transport reports that exit after checking the marker and context.
type stoppedResourceExecutor struct {
	commands      int
	stops         int
	state         string
	stopDelivered chan struct{}
}

func (executor *stoppedResourceExecutor) ExecInPod(ctx context.Context, _, _, _ string, command []string,
	_ io.Reader, _ io.Writer, _ io.Writer, _ bool, attrs ExecAttrs) error {
	switch attrs.Purpose {
	case "step-command":
		select {
		case <-executor.stopDelivered:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		state := resourceStateDir(command)
		if state == "" || state != executor.state {
			return fmt.Errorf("cancel targeted a different resource invocation")
		}
		if _, err := os.Stat(state + "/cancel"); err != nil {
			return fmt.Errorf("resource dispatched without its cancellation marker: %w", err)
		}
		executor.commands++
		return &ExecExitError{ExitCode: 130}
	case "cancel-resource":
		executor.state = resourceStateDir(command)
		if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("writing resource cancellation marker: %w: %s", err, output)
		}
		executor.stops++
		close(executor.stopDelivered)
		return nil
	default:
		return fmt.Errorf("unexpected exec purpose %q", attrs.Purpose)
	}
}

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

// stallingOutputControl is the real client with one call that never answers
// until its caller gives up, as a daemon behind a partition does.
type stallingOutputControl struct {
	OutputControl
	stall string
}

func (stalled *stallingOutputControl) RecordOutcome(ctx context.Context, id executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind, outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	if stalled.stall == "outcome" {
		<-ctx.Done()
		return executioncontrol.Acknowledgement{}, ctx.Err()
	}
	return stalled.OutputControl.RecordOutcome(ctx, id, kind, outcome)
}

func (stalled *stallingOutputControl) Observe(ctx context.Context, id executioncontrol.Identity,
	wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error) {
	if stalled.stall == "observe" {
		<-ctx.Done()
		return executioncontrol.ObserveFinishOrStopResult{}, ctx.Err()
	}
	return stalled.OutputControl.Observe(ctx, id, wait)
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
			storageBackend: NewDaemonSetBackend(container.config, nil, nil, nil),
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

	// The ledger's rule is that only `unmanaged` permits a destructive or
	// write-capable operation. A sealed source is past its seal and still owed
	// its publication; an unavailable answer is the ledger saying it cannot
	// say. Refusing only `held` read both as "go ahead".
	DescribeTable("refuses every answer but unmanaged",
		func(class string) {
			classifier.class = class
			err := container.refuseIfCaptureHeld(ctx, "hijacking the container")
			Expect(err).To(HaveOccurred(), "a %q source was treated as destroyable", class)
			Expect(err.Error()).To(ContainSubstring(class))
		},
		Entry("held", captureClassHeld),
		Entry("sealed", "sealed"),
		Entry("unavailable", "unavailable"),
		Entry("an answer this runtime does not know", "reserved-for-later"),
		Entry("no answer at all", ""),
	)

	It("asks nothing at all when the output plane is off", func() {
		container.config.OutputPlaneEnabled = false
		Expect(container.refuseIfCaptureHeld(ctx, "recreating the pause pod")).To(Succeed())
		Expect(classifier.asked).To(BeEmpty(),
			"a deployment with no output plane asked a ledger that does not exist")
	})
})
