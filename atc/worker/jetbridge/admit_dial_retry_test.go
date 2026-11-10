package jetbridge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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
)

// restart brings a stopped harness daemon back on the same port, ledger and
// keys: a daemon restart, as the node sees one.
func (harness *outputDaemonHarness) restart() error {
	daemon := exec.Command(harness.cmd.Path, harness.cmd.Args[1:]...)
	daemon.Stdout, daemon.Stderr = os.Stderr, os.Stderr
	if err := daemon.Start(); err != nil {
		return err
	}
	harness.cmd = daemon

	return waitForReady(harness.PKI.client, harness.Endpoint)
}

// admitCounter is the real client, counting the admissions the daemon
// answered and the ones that never reached it.
type admitCounter struct {
	OutputControl
	mu       sync.Mutex
	answered int
	refused  int
}

func (counter *admitCounter) Admit(ctx context.Context,
	envelope executioncontrol.Envelope) (executioncontrol.ClassifyResult, error) {
	result, err := counter.OutputControl.Admit(ctx, envelope)
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if err == nil {
		counter.answered++
	} else {
		counter.refused++
	}

	return result, err
}

func (counter *admitCounter) counts() (answered, refused int) {
	counter.mu.Lock()
	defer counter.mu.Unlock()

	return counter.answered, counter.refused
}

func (counter *admitCounter) ForNode(context.Context, string) (OutputControl, error) {
	return counter, nil
}

var _ = Describe("Admitting an exact execution while the node's artifact daemon restarts", func() {
	var (
		harness *outputDaemonHarness
		counter *admitCounter
		process *execProcess
	)

	identity := executioncontrol.Identity{
		ExecutionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Fence:       1,
	}

	useBudget := func(budget time.Duration) {
		previousBudget, previousBackoff := admitDialBudget, admitDialBackoff
		admitDialBudget, admitDialBackoff = budget, 20*time.Millisecond
		DeferCleanup(func() { admitDialBudget, admitDialBackoff = previousBudget, previousBackoff })
	}

	BeforeEach(func() {
		started, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		harness = started
		DeferCleanup(func() { harness.Stop() })
		counter = &admitCounter{OutputControl: harness.Client}

		clientset := fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "admit-pod", Namespace: "test-ns",
				UID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"},
			Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}},
		})
		config := NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		container := &Container{
			handle:   "admit-handle",
			podName:  "admit-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{
				Type: db.ContainerTypeTask,
				ExecutionControl: &runtime.ExecutionControl{
					Version:    runtime.ExecutionControlVersion,
					Phase:      runtime.ControlPhaseAdmitted,
					Identity:   identity,
					Endpoint:   harness.Endpoint,
					Capability: "base-capability",
				},
			},
			clientset:      clientset,
			config:         config,
			properties:     map[string]string{},
			outputControls: counter,
		}
		process = newExecProcess("proc-1", "admit-pod", clientset, config, container,
			&controlExecutor{run: func(int) error { return nil }},
			runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)

		// The listener is down when the admission is first attempted.
		harness.Stop()
	})

	It("admits once the daemon listens again inside the budget", func() {
		useBudget(30 * time.Second)

		admitted := make(chan error, 1)
		go func() { admitted <- process.admitExactExecution(context.Background()) }()
		Eventually(func() int { _, refused := counter.counts(); return refused }).Should(BeNumerically(">=", 2))
		Expect(harness.restart()).To(Succeed())

		Eventually(admitted, 20*time.Second).Should(Receive(BeNil()))
		answered, _ := counter.counts()
		Expect(answered).To(Equal(1), "the execution was admitted more than once")
		Expect(process.exact).ToNot(BeNil())

		classified, err := harness.Client.Classify(context.Background(), identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(classified.Identity).To(Equal(identity))
		Expect(classified.Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
	})

	It("names the daemon and the budget when the listener stays down", func() {
		useBudget(300 * time.Millisecond)

		started := time.Now()
		err := process.admitExactExecution(context.Background())
		Expect(err).To(HaveOccurred())
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
		Expect(err.Error()).To(ContainSubstring("artifact daemon on node node-1"))
		Expect(err.Error()).To(ContainSubstring("300ms"))
		Expect(process.exact).To(BeNil())
		answered, refused := counter.counts()
		Expect(answered).To(Equal(0))
		Expect(refused).To(BeNumerically(">=", 2))
	})

	It("does not retry an answer the daemon gave", func() {
		useBudget(30 * time.Second)
		Expect(harness.restart()).To(Succeed())
		// A stale fence is refused by the daemon's ledger: an answer, not a
		// dial failure, so it is final at once.
		_, err := harness.Client.Admit(context.Background(), executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        executioncontrol.Identity{ExecutionID: identity.ExecutionID, Fence: 2},
			NodeUID:         harnessNodeUID,
			Capability:      "base-capability",
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(process.admitExactExecution(context.Background())).ToNot(Succeed())
		answered, refused := counter.counts()
		Expect(answered + refused).To(Equal(1))
	})
})
