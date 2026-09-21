package jetbridge

// A Run-owned check, get or put journals its own exit in the Pod, under the
// state directory its signed start names.
//
// Without that journal a resource command had no outcome writer that outlives
// the ATC: a web restart mid-command, a lost answer, or an in-band stop that
// failed and let the grace expire left the node's ledger `executing` for good,
// and Run cancellation could neither interrupt the command (no locator in the
// start) nor recover its outcome. The ATC still never fabricates one: only the
// in-pod wrapper writes `exit`, and recovery only reads it.
//
// The real wrapper needs setsid and procfs, so the specs that run it are
// Linux-only; the locator and the second-delivery refusal run everywhere.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

// hostPodExecutor runs every exec on the host, as the Pod's own shell would.
// The step command's transport can lose its answer after the command
// finished, or before it did -- leaving the command running on the "node"
// the way a web restart leaves it running in the Pod.
type hostPodExecutor struct {
	loseStepAnswer    bool
	detachStepCommand bool
	// failStepDial fails the step command's dial without running anything,
	// the way an exec whose SPDY upgrade was refused does.
	failStepDial bool
	// failStops refuses every separate stop request the ATC sends.
	failStops bool
	// discardStepOutput runs the step command to completion on the "node"
	// with nothing receiving its output, and then loses the answer.
	discardStepOutput bool

	mu           sync.Mutex
	stepCommands [][]string
	detached     []*exec.Cmd
}

var errStreamReset = errors.New("stream reset: the web node went away")

func (executor *hostPodExecutor) ExecInPod(ctx context.Context, _, _, _ string, command []string,
	stdin io.Reader, stdout, stderr io.Writer, _ bool, attrs ExecAttrs) error {
	if attrs.Purpose == "step-command" {
		executor.mu.Lock()
		executor.stepCommands = append(executor.stepCommands, command)
		executor.mu.Unlock()
		if executor.failStepDial {
			return errors.New("unable to upgrade connection: the kubelet went away")
		}
		if executor.discardStepOutput {
			_ = runOnHost(ctx, command, stdin, io.Discard, io.Discard)
			return errStreamReset
		}
		if executor.detachStepCommand {
			cmd := exec.Command(command[0], command[1:]...)
			if err := cmd.Start(); err != nil {
				return err
			}
			executor.mu.Lock()
			executor.detached = append(executor.detached, cmd)
			executor.mu.Unlock()
			go func() { _ = cmd.Wait() }()
			return errStreamReset
		}
		err := runOnHost(ctx, command, stdin, stdout, stderr)
		if executor.loseStepAnswer {
			return errStreamReset
		}
		return err
	}
	if executor.failStops && (attrs.Purpose == "exact-stop-request" || attrs.Purpose == "cancel-resource") {
		return errors.New("the stop request's exec was refused")
	}
	return runOnHost(ctx, command, stdin, stdout, stderr)
}

func (executor *hostPodExecutor) lastStepCommand() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.stepCommands[len(executor.stepCommands)-1]
}

func (executor *hostPodExecutor) stepCommandCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return len(executor.stepCommands)
}

func (executor *hostPodExecutor) killDetached() {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	for _, cmd := range executor.detached {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

func runOnHost(ctx context.Context, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &ExecExitError{ExitCode: exit.ExitCode()}
		}
		return err
	}
	return nil
}

// requireResourceSessions skips a spec that runs the real resource wrapper.
func requireResourceSessions() {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		Skip("the resource command wrapper requires Linux procfs")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		Skip("the resource command wrapper requires setsid")
	}
}

var _ = Describe("A Run-owned resource command's exit journal", func() {
	var (
		harness   *outputDaemonHarness
		clientset *fake.Clientset
		config    Config
		container *Container
		identity  executioncontrol.Identity
		starts    []executioncontrol.Acknowledgement
		ctx       context.Context
	)

	const podUID = types.UID("f0f0f0f0-f0f0-4f0f-8f0f-f0f0f0f0f0f0")

	newResourceCommand := func(executor PodExecutor, args ...string) *execProcess {
		return newExecProcess("resource-proc", "resource-pod", clientset, config, container, executor,
			runtime.ProcessSpec{Path: "/bin/sh", Args: args},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
	}

	BeforeEach(func() {
		ctx = context.Background()
		started, err := startTLSOutputDaemon()
		Expect(err).NotTo(HaveOccurred())
		harness = started
		DeferCleanup(harness.Stop)

		endpoint, err := url.Parse(harness.Endpoint)
		Expect(err).NotTo(HaveOccurred())
		port, err := strconv.Atoi(endpoint.Port())
		Expect(err).NotTo(HaveOccurred())

		// A fresh identity per spec: the journal lives at a path derived from
		// it, on the host, and must never be shared between specs.
		identity = executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1}
		state := exactResourceStateDir(identity)
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}},
			},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "resource-pod", Namespace: "test-ns", UID: podUID},
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

		config = NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		config.OutputDaemonPort = port
		config.OutputDaemonTLSCert = harness.PKI.clientCert
		config.OutputDaemonTLSKey = harness.PKI.clientKey
		config.OutputDaemonTLSCACert = harness.PKI.caCert

		starts = nil
		container = &Container{
			handle:   "resource-handle",
			podName:  "resource-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
			containerSpec: runtime.ContainerSpec{
				Type: db.ContainerTypeGet,
				ExecutionControl: &runtime.ExecutionControl{
					Version:         runtime.ExecutionControlVersion,
					Phase:           runtime.ControlPhaseAdmitted,
					Identity:        identity,
					ActivationEpoch: harnessEpoch,
					Endpoint:        harness.Endpoint,
					Capability:      "base-capability",
				},
			},
			clientset:      clientset,
			config:         config,
			properties:     map[string]string{},
			outputControls: harness,
			recordWitness: func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					starts = append(starts, witness)
				}
				return nil
			},
		}
	})

	DescribeTable("names its journal in the node's signed start",
		func(containerType db.ContainerType) {
			container.metadata.Type = containerType
			container.containerSpec.Type = containerType
			executor := &controlExecutor{run: func(int) error { return nil }}

			result, err := newResourceCommand(executor, "-c", "true").Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(BeZero())

			state := exactResourceStateDir(identity)
			Expect(starts).To(HaveLen(1))
			Expect(string(starts[0].ProcessIdentity)).To(Equal(resourceIdentityPrefix + state))
			located, resource, err := executionJournalFromIdentity(starts[0].ProcessIdentity)
			Expect(err).NotTo(HaveOccurred())
			Expect(located).To(Equal(state))
			Expect(resource).To(BeTrue())

			Expect(executor.count()).To(Equal(1))
			command := executor.calls[0]
			Expect(command).To(ContainElement(state))
			Expect(strings.Join(command, "\n")).To(ContainSubstring(`"$S/exit"`),
				"the command was dispatched without the wrapper that journals its exit")
		},
		Entry("check", db.ContainerTypeCheck),
		Entry("get", db.ContainerTypeGet),
		Entry("put", db.ContainerTypePut),
	)

	It("refuses a second delivery once its start is journaled, and writes no exit for it", func() {
		state := exactResourceStateDir(identity)
		Expect(os.MkdirAll(state, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(state, "start"), []byte("started\n"), 0o600)).To(Succeed())
		marker := filepath.Join(GinkgoT().TempDir(), "command-ran")

		command := journaledResourceCommand([]string{"sh", "-c", `printf x > "$1"`, "cmd", marker}, state)
		err := exec.Command(command[0], command[1:]...).Run()
		var exited *exec.ExitError
		Expect(errors.As(err, &exited)).To(BeTrue(), "%v", err)
		Expect(exited.ExitCode()).To(Equal(ExactUnresolvedExitCode))
		_, err = os.Stat(marker)
		Expect(os.IsNotExist(err)).To(BeTrue(), "a resource command ran twice")
		_, err = os.Stat(filepath.Join(state, "exit"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "a refused delivery invented an outcome")
	})

	It("recovers the journaled exit after the transport loses its answer, without running it again", func() {
		requireResourceSessions()
		marker := filepath.Join(GinkgoT().TempDir(), "command-ran")
		executor := &hostPodExecutor{loseStepAnswer: true}

		result, err := newResourceCommand(executor, "-c", `printf x >> "$1"; exit 3`, "cmd", marker).Wait(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(3))
		observed, err := harness.Client.Classify(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(observed.Classification.Authoritative()).To(BeTrue())
		Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(3))

		// A restarted web replays the step: it reads the ledger, never the
		// command.
		result, err = newResourceCommand(executor, "-c", `printf x >> "$1"; exit 3`, "cmd", marker).Wait(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(3))
		Expect(executor.stepCommandCount()).To(Equal(1))
		ran, err := os.ReadFile(marker)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ran)).To(Equal("x"))
	})

	// The resource protocol's answer is the command's stdout. An outcome
	// recovered from the journal without it is a get with no version and a
	// put whose version is lost, so the wrapper journals stdout beside the
	// exit and recovery hands it to the step exactly once.
	It("recovers the journaled stdout with the exit, and again on replay", func() {
		requireResourceSessions()
		executor := &hostPodExecutor{discardStepOutput: true}
		answer := `{"version":{"ref":"abc"},"metadata":[]}`

		stdout := &bytes.Buffer{}
		process := newResourceCommand(executor, "-c", `printf '%s' "$1"; printf 'fetching\n' >&2`, "cmd", answer)
		process.processIO.Stdout = stdout
		result, err := process.Wait(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitStatus).To(BeZero())
		Expect(stdout.String()).To(Equal(answer), "a recovered outcome lost the resource's answer")

		stdout = &bytes.Buffer{}
		replay := newResourceCommand(executor, "-c", `printf '%s' "$1"`, "cmd", answer)
		replay.processIO.Stdout = stdout
		result, err = replay.Wait(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitStatus).To(BeZero())
		Expect(stdout.String()).To(Equal(answer), "a replayed outcome lost the resource's answer")
		Expect(executor.stepCommandCount()).To(Equal(1))
	})

	// A closed exec stream must not reach the command: the next write to it
	// would be a SIGPIPE, and 141 journaled as the command's own exit is a
	// fabricated failure. The command writes to its journal; only the outer
	// session writes to the stream, and it ignores SIGPIPE.
	It("journals the command's own exit when its exec stream is already closed", func() {
		requireResourceSessions()
		state := exactResourceStateDir(identity)
		reader, writer, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		Expect(reader.Close()).To(Succeed())
		defer writer.Close()

		command := journaledResourceCommand([]string{"sh", "-c",
			`i=0; while [ $i -lt 2000 ]; do echo "line $i"; echo "err $i" >&2; i=$((i+1)); done; exit 0`}, state)
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Stdout, cmd.Stderr = writer, writer
		_ = cmd.Run()

		journal, err := os.ReadFile(filepath.Join(state, "exit"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(journal)).To(Equal("0\n"), "the closed stream reached the command")
	})

	It("streams the command's stderr while it runs, and its stdout once it exits", func() {
		requireResourceSessions()
		state := exactResourceStateDir(identity)
		release := filepath.Join(GinkgoT().TempDir(), "release")
		command := journaledResourceCommand([]string{"sh", "-c",
			`printf 'progress\n' >&2; while [ ! -f "$1" ]; do sleep 0.1; done; printf '{"ok":true}'`, "cmd", release}, state)
		var stdout, stderr lockedBuffer
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		Expect(cmd.Start()).To(Succeed())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		Eventually(stderr.String, 5*time.Second).Should(Equal("progress\n"), "stderr is not live")
		Expect(stdout.String()).To(BeEmpty())
		Expect(os.WriteFile(release, nil, 0o600)).To(Succeed())
		Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		Expect(stdout.String()).To(Equal(`{"ok":true}`))
		Expect(stderr.String()).To(Equal("progress\n"))
	})

	It("is interrupted and recovered through the output source after the web is gone", func() {
		requireResourceSessions()
		marker := filepath.Join(GinkgoT().TempDir(), "command-ran")
		executor := &hostPodExecutor{detachStepCommand: true}
		DeferCleanup(executor.killDetached)

		_, err := newResourceCommand(executor, "-c", `printf x >> "$1"; exec sleep 60`, "cmd", marker).Wait(ctx)
		Expect(err).To(MatchError(ErrExactOutcomeUnresolved))
		// The launch retained the start, and recovery retained the same one
		// again before it read the (still absent) journal.
		Expect(starts).NotTo(BeEmpty())
		for _, retained := range starts {
			Expect(retained).To(Equal(starts[0]))
		}
		Eventually(func() error { _, err := os.Stat(marker); return err }).Should(Succeed())

		source := NewOutputSource(clientset, config, harness.Minter, harnessEpoch)
		source.SetExecutor(executor)
		_, err = source.RecoverExecutionOutcome(ctx, "node-1", starts[0])
		Expect(err).To(MatchError(hangaroutput.ErrUnresolved),
			"a running command's outcome was recovered before it had one")

		Expect(source.InterruptExecution(ctx, "node-1", starts[0])).To(Succeed())
		var recovered executioncontrol.ClassifyResult
		Eventually(func() (executioncontrol.Classification, error) {
			var err error
			recovered, err = source.RecoverExecutionOutcome(ctx, "node-1", starts[0])
			return recovered.Classification, err
		}, 10*time.Second, 100*time.Millisecond).Should(Equal(executioncontrol.ClassificationAuthoritativeFinish))
		Expect(recovered.Acknowledgement.Outcome.ExitCode).To(Equal(137))
		ran, err := os.ReadFile(marker)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ran)).To(Equal("x"))
	})
})

// lostStartAnswer commits the node's start and loses the answer, once.
type lostStartAnswer struct {
	OutputControl
	lost bool
}

func (lossy *lostStartAnswer) RecordStart(ctx context.Context, id executioncontrol.Identity,
	pod executioncontrol.PodUID, process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	ack, err := lossy.OutputControl.RecordStart(ctx, id, pod, process)
	if err == nil && !lossy.lost {
		lossy.lost = true
		return executioncontrol.Acknowledgement{}, errStreamReset
	}
	return ack, err
}

// A signed start whose command never reached the Pod has no outcome writer:
// the journal has no start, the read of it is unresolved, and nothing else
// would ever write one. Whoever finds it closes it -- in the Pod, by the one
// atomic creation of the start journal the wrapper also uses -- as a command
// stopped before its producer ran. Nothing dispatches the producer again.
var _ = Describe("An exact command whose start was never delivered", func() {
	var (
		harness   *outputDaemonHarness
		clientset *fake.Clientset
		config    Config
		container *Container
		identity  executioncontrol.Identity
		starts    []executioncontrol.Acknowledgement
		ctx       context.Context
		marker    string
	)

	const podUID = types.UID("e0e0e0e0-e0e0-4e0e-8e0e-e0e0e0e0e0e0")

	newCommand := func(executor PodExecutor) *execProcess {
		return newExecProcess("undelivered-proc", "undelivered-pod", clientset, config, container, executor,
			runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", `printf x >> "$1"`, "cmd", marker}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
	}

	// kinds: a task under the exact supervisor, and a Run-owned get.
	asKind := func(kind db.ContainerType) (state string, stopped int) {
		container.metadata.Type = kind
		container.containerSpec.Type = kind
		process := newCommand(nil)
		state, resource, ok := process.exactJournal()
		Expect(ok).To(BeTrue())
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })
		if resource {
			return state, 130
		}
		return state, 143
	}

	BeforeEach(func() {
		ctx = context.Background()
		started, err := startTLSOutputDaemon()
		Expect(err).NotTo(HaveOccurred())
		harness = started
		DeferCleanup(harness.Stop)

		endpoint, err := url.Parse(harness.Endpoint)
		Expect(err).NotTo(HaveOccurred())
		port, err := strconv.Atoi(endpoint.Port())
		Expect(err).NotTo(HaveOccurred())

		identity = executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1}
		marker = filepath.Join(GinkgoT().TempDir(), "producer-ran")

		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}},
			},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "undelivered-pod", Namespace: "test-ns", UID: podUID},
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

		config = NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		config.OutputDaemonPort = port
		config.OutputDaemonTLSCert = harness.PKI.clientCert
		config.OutputDaemonTLSKey = harness.PKI.clientKey
		config.OutputDaemonTLSCACert = harness.PKI.caCert

		starts = nil
		container = &Container{
			handle:   "undelivered-handle",
			podName:  "undelivered-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{
				Type: db.ContainerTypeTask,
				ExecutionControl: &runtime.ExecutionControl{
					Version:         runtime.ExecutionControlVersion,
					Phase:           runtime.ControlPhaseAdmitted,
					Identity:        identity,
					ActivationEpoch: harnessEpoch,
					Endpoint:        harness.Endpoint,
					Capability:      "base-capability",
				},
			},
			clientset:      clientset,
			config:         config,
			properties:     map[string]string{},
			outputControls: harness,
			recordWitness: func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					starts = append(starts, witness)
				}
				return nil
			},
		}
	})

	expectClosedAs := func(state string, code int) {
		observed, err := harness.Client.Classify(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(observed.Classification).To(Equal(executioncontrol.ClassificationAuthoritativeFinish))
		Expect(observed.Acknowledgement.Outcome.ExitCode).To(Equal(code))
		journal, err := os.ReadFile(filepath.Join(state, "exit"))
		Expect(err).NotTo(HaveOccurred(), "the ledger has an outcome no in-pod journal wrote")
		Expect(string(journal)).To(Equal(fmt.Sprintf("%d\n", code)))
		_, err = os.Stat(marker)
		Expect(os.IsNotExist(err)).To(BeTrue(), "the producer ran")
	}

	DescribeTable("closes it after the exec dial fails, without dispatching it again",
		func(kind db.ContainerType) {
			state, stopped := asKind(kind)
			executor := &hostPodExecutor{failStepDial: true}

			result, err := newCommand(executor).Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(stopped))
			Expect(executor.stepCommandCount()).To(Equal(1))
			expectClosedAs(state, stopped)

			// A replay reads the ledger and dispatches nothing.
			executor.failStepDial = false
			result, err = newCommand(executor).Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(stopped))
			Expect(executor.stepCommandCount()).To(Equal(1))
		},
		Entry("task", db.ContainerTypeTask),
		Entry("get", db.ContainerTypeGet),
	)

	DescribeTable("closes it on replay after the node's start answer was lost",
		func(kind db.ContainerType) {
			state, stopped := asKind(kind)
			container.outputControls = staticOutputControls{control: &lostStartAnswer{OutputControl: harness.Client}}
			executor := &hostPodExecutor{}

			_, err := newCommand(executor).Wait(ctx)
			Expect(err).To(MatchError(errStreamReset))
			Expect(executor.stepCommandCount()).To(BeZero())
			observed, err := harness.Client.Classify(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Classification).To(Equal(executioncontrol.ClassificationExecuting))

			result, err := newCommand(executor).Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(stopped))
			Expect(executor.stepCommandCount()).To(BeZero(), "recovery dispatched the command")
			expectClosedAs(state, stopped)
		},
		Entry("task", db.ContainerTypeTask),
		Entry("get", db.ContainerTypeGet),
	)

	DescribeTable("is closed by Run cancellation, and a late delivery does not run it",
		func(kind db.ContainerType) {
			state, stopped := asKind(kind)
			executor := &hostPodExecutor{}
			process := newCommand(executor)
			Expect(process.admitWhenScheduled(ctx)()).To(Succeed())
			start, err := harness.Client.RecordStart(ctx, identity, executioncontrol.PodUID(podUID), process.exactProcessIdentity())
			Expect(err).NotTo(HaveOccurred())

			source := NewOutputSource(clientset, config, harness.Minter, harnessEpoch)
			source.SetExecutor(executor)
			_, err = source.RecoverExecutionOutcome(ctx, "node-1", start)
			Expect(err).To(MatchError(hangaroutput.ErrUnresolved), "an undelivered start was closed by a read")

			Expect(source.InterruptExecution(ctx, "node-1", start)).To(Succeed())
			recovered, err := source.RecoverExecutionOutcome(ctx, "node-1", start)
			Expect(err).NotTo(HaveOccurred())
			Expect(recovered.Classification).To(Equal(executioncontrol.ClassificationAuthoritativeFinish))
			Expect(recovered.Acknowledgement.Outcome.ExitCode).To(Equal(stopped))
			expectClosedAs(state, stopped)

			// The original delivery, landing late, finds the journal closed.
			command := exactSupervisorCommand(process.id, process.processSpec)
			if kind != db.ContainerTypeTask {
				command = journaledResourceCommand(
					append([]string{process.processSpec.Path}, process.processSpec.Args...), state)
			}
			output, err := exec.Command(command[0], command[1:]...).CombinedOutput()
			var exited *exec.ExitError
			if err != nil {
				Expect(errors.As(err, &exited)).To(BeTrue(), "%v: %s", err, output)
			}
			_, err = os.Stat(marker)
			Expect(os.IsNotExist(err)).To(BeTrue(), "a late delivery ran the producer of a closed execution")
			expectClosedAs(state, stopped)
		},
		Entry("task", db.ContainerTypeTask),
		Entry("get", db.ContainerTypeGet),
	)

	// A Run whose database was down when the node answered never retained
	// the start. Cancellation reads it from the node that signed it -- and
	// only from that node, for that identity.
	It("reads the node's signed start for a Run that never retained it", func() {
		source := NewOutputSource(clientset, config, harness.Minter, harnessEpoch)
		_, err := source.ExecutionStart(ctx, "node-1", harnessNodeUID, harnessEpoch, identity)
		Expect(err).To(MatchError(hangaroutput.ErrNotFound), "an unadmitted execution answered for a start")

		process := newCommand(&hostPodExecutor{})
		Expect(process.admitWhenScheduled(ctx)()).To(Succeed())
		_, err = source.ExecutionStart(ctx, "node-1", harnessNodeUID, harnessEpoch, identity)
		Expect(err).To(MatchError(hangaroutput.ErrNotFound), "an unstarted execution answered for a start")

		start, err := harness.Client.RecordStart(ctx, identity, executioncontrol.PodUID(podUID), process.exactProcessIdentity())
		Expect(err).NotTo(HaveOccurred())
		read, err := source.ExecutionStart(ctx, "node-1", harnessNodeUID, harnessEpoch, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(read).To(Equal(start))

		_, err = source.ExecutionStart(ctx, "node-1", "another-node-uid", harnessEpoch, identity)
		Expect(err).To(HaveOccurred(), "a replaced node answered for the original node's start")
		_, err = source.ExecutionStart(ctx, "node-1", harnessNodeUID, harnessEpoch,
			executioncontrol.Identity{ExecutionID: identity.ExecutionID, Fence: 2})
		Expect(err).To(HaveOccurred(), "a start was returned for a different fence")
	})

	// The Run could not retain the start, and the stop that should have
	// preceded delivery was refused. The stop is part of the delivery: the one
	// exec that is sent writes the stop and closes the start itself, so there
	// is no separate request whose failure could let the producer through.
	DescribeTable("delivers its stop with it when the Run could not retain the start",
		func(kind db.ContainerType) {
			state, stopped := asKind(kind)
			container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				if witness.Kind == executioncontrol.AcknowledgementStart {
					return errors.New("db unavailable")
				}
				Fail("an outcome witness was retained without its start")
				return nil
			}
			executor := &hostPodExecutor{failStops: true}

			_, err := newCommand(executor).Wait(ctx)
			Expect(err).To(MatchError(ContainSubstring("db unavailable")))
			Expect(executor.stepCommandCount()).To(Equal(1))
			Expect(strings.Join(executor.lastStepCommand(), "\n")).NotTo(ContainSubstring(marker),
				"the producer was dispatched while its stop was only requested separately")
			journal, err := os.ReadFile(filepath.Join(state, "exit"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(journal)).To(Equal(fmt.Sprintf("%d\n", stopped)))
			_, err = os.Stat(marker)
			Expect(os.IsNotExist(err)).To(BeTrue(), "the producer ran")
		},
		Entry("task", db.ContainerTypeTask),
		Entry("get", db.ContainerTypeGet),
	)
})

var _ = DescribeTable("A retained journal locator",
	func(identity string, wantState string, wantResource bool) {
		state, resource, err := executionJournalFromIdentity(executioncontrol.ProcessIdentity(identity))
		if wantState == "" {
			Expect(err).To(HaveOccurred(), "a signed start could direct a stop or a read to %q", state)
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(wantState))
		Expect(resource).To(Equal(wantResource))
	},
	Entry("names a task supervisor", "supervisor-v1:/tmp/concourse-task-proc-1-0a1b2c3d", "/tmp/concourse-task-proc-1-0a1b2c3d", false),
	Entry("names a resource session", "resource-v1:/tmp/concourse-resource-f0f0-1", "/tmp/concourse-resource-f0f0-1", true),
	Entry("is refused for a resource kind naming a task journal", "resource-v1:/tmp/concourse-task-proc-1-0a1b2c3d", "", false),
	Entry("is refused for a task kind naming a resource journal", "supervisor-v1:/tmp/concourse-resource-f0f0-1", "", false),
	Entry("is refused outside /tmp", "resource-v1:/var/concourse-resource-f0f0-1", "", false),
	Entry("is refused when it escapes /tmp", "resource-v1:/tmp/concourse-resource-x/../../etc", "", false),
	Entry("is refused with an unsanitized segment", "resource-v1:/tmp/concourse-resource-a b", "", false),
	Entry("is refused for an opaque process identity", "proc-1", "", false),
)
