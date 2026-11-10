package jetbridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
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

// restart brings a stopped harness daemon back on its ledger and keys: a
// daemon restart, as the node sees one. It binds the port it had; if another
// process took that port in the meantime, the daemon exits and is started
// again on a fresh one, and the harness's endpoint and client follow it.
func (harness *outputDaemonHarness) restart() error {
	args := append([]string(nil), harness.cmd.Args[1:]...)
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			port, err := freeLocalPort()
			if err != nil {
				return err
			}
			for i := range args {
				if args[i] == "--port" {
					args[i+1] = strconv.Itoa(port)
				}
			}
			harness.Endpoint = fmt.Sprintf("https://127.0.0.1:%d", port)
			harness.Client = NewOutputControlClient(harness.Endpoint, harness.PKI.client, harness.Signer)
		}
		daemon := exec.Command(harness.cmd.Path, args...)
		daemon.Stdout, daemon.Stderr = os.Stderr, os.Stderr
		if err := daemon.Start(); err != nil {
			return err
		}
		exited := make(chan struct{})
		go func() { _, _ = daemon.Process.Wait(); close(exited) }()
		ready := make(chan error, 1)
		go func() { ready <- waitForReady(harness.PKI.client, harness.Endpoint) }()
		select {
		case err := <-ready:
			harness.cmd = daemon
			if err != nil {
				_ = daemon.Process.Kill()
			}
			return err
		case <-exited:
			// It could not bind; try a fresh port.
		}
	}

	return errors.New("the harness daemon could not be restarted on any port")
}

// admitCounter is the real client, counting the admissions the daemon
// answered and the ones that never reached it. target is read per call, so a
// restart that moved the daemon is followed.
type admitCounter struct {
	OutputControl
	target func() OutputControl
	// loseReply, when set, lets the next admission reach the daemon and then
	// loses its answer the way a dropped connection does.
	loseReply atomic.Bool
	mu        sync.Mutex
	answered  int
	refused   int
}

func (counter *admitCounter) Admit(ctx context.Context,
	envelope executioncontrol.Envelope) (executioncontrol.ClassifyResult, error) {
	result, err := counter.target().Admit(ctx, envelope)
	if err == nil && counter.loseReply.CompareAndSwap(true, false) {
		result, err = executioncontrol.ClassifyResult{}, fmt.Errorf("admit: %w",
			&url.Error{Op: "Post", URL: "https://127.0.0.1/execution/v1/admit", Err: io.ErrUnexpectedEOF})
	}
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

// dyingFront is a TCP listener in front of the daemon that plays the shapes a
// daemon takes while it dies: it accepts and closes at once, or accepts and
// never answers. In pass mode it is a plain TCP relay, TLS passing through.
type dyingFront struct {
	listener net.Listener
	mode     atomic.Value // "eof", "stall", "pass"
	upstream func() string
}

func newDyingFront(upstream func() string) *dyingFront {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())
	front := &dyingFront{listener: listener, upstream: upstream}
	front.mode.Store("pass")
	DeferCleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go front.serve(conn)
		}
	}()

	return front
}

func (front *dyingFront) serve(conn net.Conn) {
	defer conn.Close()
	switch front.mode.Load() {
	case "eof":
		return
	case "stall":
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	address, err := url.Parse(front.upstream())
	if err != nil {
		return
	}
	upstream, err := net.Dial("tcp", address.Host)
	if err != nil {
		return
	}
	defer upstream.Close()
	go func() { _, _ = io.Copy(upstream, conn) }()
	_, _ = io.Copy(conn, upstream)
}

func (front *dyingFront) endpoint() string { return "https://" + front.listener.Addr().String() }

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

	refusedAtLeast := func(n int) {
		Eventually(func() int { _, refused := counter.counts(); return refused }, 10*time.Second).
			Should(BeNumerically(">=", n))
	}

	admitInBackground := func(ctx context.Context) chan error {
		admitted := make(chan error, 1)
		go func() { admitted <- process.admitExactExecution(ctx) }()
		return admitted
	}

	expectAdmittedOnce := func() {
		Expect(process.exact).ToNot(BeNil())
		classified, err := harness.Client.Classify(context.Background(), identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(classified.Identity).To(Equal(identity))
		Expect(classified.Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
	}

	// viaFront points the admission at a front with a short client timeout.
	viaFront := func(client *http.Client) *dyingFront {
		front := newDyingFront(func() string { return harness.Endpoint })
		throughFront := NewOutputControlClient(front.endpoint(), client, harness.Signer)
		counter.target = func() OutputControl { return throughFront }
		return front
	}

	shortTimeout := func() *http.Client {
		client := *harness.PKI.client
		client.Timeout = 300 * time.Millisecond
		return &client
	}

	BeforeEach(func() {
		started, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		harness = started
		DeferCleanup(func() { harness.Stop() })
		counter = &admitCounter{target: func() OutputControl { return harness.Client }}

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
	})

	Context("when the daemon's listener is down", func() {
		BeforeEach(func() { harness.Stop() })

		It("admits once the daemon listens again inside the budget", func() {
			useBudget(30 * time.Second)

			admitted := admitInBackground(context.Background())
			refusedAtLeast(2)
			Expect(harness.restart()).To(Succeed())

			Eventually(admitted, 20*time.Second).Should(Receive(BeNil()))
			answered, _ := counter.counts()
			Expect(answered).To(Equal(1), "the execution was admitted more than once")
			expectAdmittedOnce()
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

		It("stops at once with the step's context error when the step is aborted mid-retry", func() {
			useBudget(30 * time.Second)

			ctx, abort := context.WithCancel(context.Background())
			admitted := admitInBackground(ctx)
			refusedAtLeast(2)
			abort()

			var err error
			Eventually(admitted, time.Second).Should(Receive(&err))
			Expect(err).To(MatchError(context.Canceled))
			Expect(process.exact).To(BeNil())
		})
	})

	It("retries a daemon that accepts and closes before it answers", func() {
		useBudget(30 * time.Second)
		front := viaFront(shortTimeout())
		front.mode.Store("eof")

		admitted := admitInBackground(context.Background())
		refusedAtLeast(2)
		front.mode.Store("pass")

		Eventually(admitted, 20*time.Second).Should(Receive(BeNil()))
		answered, _ := counter.counts()
		Expect(answered).To(Equal(1))
		expectAdmittedOnce()
	})

	It("retries a connection that never answers within the client's timeout", func() {
		useBudget(30 * time.Second)
		front := viaFront(shortTimeout())
		front.mode.Store("stall")

		admitted := admitInBackground(context.Background())
		refusedAtLeast(2)
		front.mode.Store("pass")

		Eventually(admitted, 20*time.Second).Should(Receive(BeNil()))
		answered, _ := counter.counts()
		Expect(answered).To(Equal(1))
		expectAdmittedOnce()
	})

	It("admits once when the daemon admitted but its answer was lost", func() {
		useBudget(30 * time.Second)
		counter.loseReply.Store(true)

		Expect(process.admitExactExecution(context.Background())).To(Succeed())
		answered, refused := counter.counts()
		Expect([]int{answered, refused}).To(Equal([]int{1, 1}))
		expectAdmittedOnce()
	})

	It("does not retry a TLS failure", func() {
		useBudget(30 * time.Second)
		untrusting := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}}
		stranger := NewOutputControlClient(harness.Endpoint, untrusting, harness.Signer)
		counter.target = func() OutputControl { return stranger }

		Expect(process.admitExactExecution(context.Background())).ToNot(Succeed())
		answered, refused := counter.counts()
		Expect(answered + refused).To(Equal(1))
	})

	It("does not retry an answer the daemon gave", func() {
		useBudget(30 * time.Second)
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
