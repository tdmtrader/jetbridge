package jetbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// A pause pod that is bound, admitted, and then evicted before Running is
// replaced once by the readiness watch. The replacement is a new Pod UID, and
// the start must name it: every recovery and interrupt compares the live Pod
// against the start.
var _ = Describe("An exact execution whose pause pod is replaced after admission", func() {
	const (
		evictedUID     = types.UID("11111111-1111-4111-8111-111111111111")
		replacementUID = types.UID("22222222-2222-4222-8222-222222222222")
	)

	var (
		ctx       context.Context
		clientset *fake.Clientset
		executor  *controlExecutor
		process   *execProcess

		mu     sync.Mutex
		starts []executioncontrol.Acknowledgement
	)

	executionID := executioncontrol.ExecutionID(fmt.Sprintf("%08x-bbbb-4bbb-8bbb-bbbbbbbbbbbb", os.Getpid()))
	identity := executioncontrol.Identity{ExecutionID: executionID, Fence: 1}

	// invoked reports whether the clientset has been asked verb on resource.
	// The fake records an action under the same lock its reaction runs under,
	// so a recorded watch is a watch that will see the next update.
	invoked := func(verb, resource string) bool {
		for _, action := range clientset.Actions() {
			if action.GetVerb() == verb && action.GetResource().Resource == resource {
				return true
			}
		}
		return false
	}

	// evictAfterAdmissionOn runs Wait and evicts the bound, Pending Pod once
	// admission has read it and the readiness watch is open. Admission reads
	// the Node only after it has read the Pod, so a Node read means the
	// admitted Pod UID is the evicted one. The readiness watch then replaces the
	// Pod once, and the replacement is a Running Pod with a new UID on node.
	evictAfterAdmissionOn := func(node string) error {
		clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
			pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
			pod.UID = replacementUID
			pod.Spec.NodeName = node
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: mainContainerName, Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}}
			return false, nil, nil
		})

		waited := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, err := process.Wait(ctx)
			waited <- err
		}()

		Eventually(func() bool { return invoked("get", "nodes") && invoked("watch", "pods") }, 10*time.Second, 10*time.Millisecond).
			Should(BeTrue(), "admission never read the Pod, or the readiness watch never opened")

		evicted, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "replaced-pod", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(evicted.UID).To(Equal(evictedUID))
		evicted.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}
		_, err = clientset.CoreV1().Pods("test-ns").UpdateStatus(ctx, evicted, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		var waitErr error
		Eventually(waited, 30*time.Second).Should(Receive(&waitErr))
		return waitErr
	}

	BeforeEach(func() {
		ctx = context.Background()
		starts = nil

		harness, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(harness.Stop)

		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "replaced-pod", Namespace: "test-ns", UID: evictedUID},
			Spec: corev1.PodSpec{
				NodeName:   "node-1",
				Containers: []corev1.Container{{Name: mainContainerName}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		})

		config := NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		config.ArtifactDaemonPort = 7780
		container := &Container{
			handle:   "replaced-handle",
			podName:  "replaced-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{
				Type:      db.ContainerTypeTask,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
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
			outputControls: harness,
			recordWitness: func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				mu.Lock()
				defer mu.Unlock()
				if witness.Kind == executioncontrol.AcknowledgementStart {
					starts = append(starts, witness)
				}
				return nil
			},
		}

		executor = &controlExecutor{run: func(int) error { return nil }}
		process = newExecProcess("proc-replaced", "replaced-pod", clientset, config, container,
			executor, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
	})

	It("records the start against the replacement, so its supervisor can still be reached", func() {
		Expect(evictAfterAdmissionOn("node-1")).To(Succeed())
		Expect(process.pausePodRecreated).To(BeTrue())

		mu.Lock()
		defer mu.Unlock()
		Expect(starts).ToNot(BeEmpty())
		Expect(starts[0].PodUID).To(Equal(executioncontrol.PodUID(replacementUID)),
			"the start names the evicted Pod, not the one the command was sent to")
		Expect(checkSupervisorPod(ctx, clientset, "test-ns", "replaced-pod", "node-1", starts[0])).To(Succeed())
	})

	It("refuses to start a replacement on a node the execution was not admitted on", func() {
		Expect(evictAfterAdmissionOn("node-2")).To(MatchError(ContainSubstring(`not node "node-1" where the exact execution was admitted`)))
		Expect(executor.count()).To(BeZero(), "the command was sent to a Pod on an unadmitted node")

		mu.Lock()
		defer mu.Unlock()
		Expect(starts).To(BeEmpty())
	})
})
