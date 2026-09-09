package jetbridge_test

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/process_test.go (merge-base aef2244a63, via the
// compile-adapted copy the port stage produced for this branch).
//
// One of that file's fifty-one Its -- row JB-process-023 of
// DISPOSITION-jetbridge.md -- was recorded DELETED on FILE-level evidence only,
// and the rebase re-verification REFUTED the pairing. Restoring is always
// acceptable; deleting on inference is not.
//
// The It is self-contained: it builds its own Worker and Container from the
// short scheduling/startup timeouts it needs, so only the original file's
// outer Describe("Process") setup is carried with it. The other fifty Its stay
// deleted, their evidence intact.

import (
	"bytes"
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("Process (restored)", func() {
	var (
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		_, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("process-test-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	It("execProcess failure state detection waits for Unschedulable pod and times out", func() {

		// Use short timeouts so the test completes quickly.
		// PodSchedulingTimeout must be <= PodStartupTimeout since
		// waitForRunning extends the startup context to the scheduling
		// deadline when Unschedulable is detected.
		shortCfg := cfg
		shortCfg.PodSchedulingTimeout = 3 * time.Second
		shortCfg.PodStartupTimeout = 2 * time.Second
		shortWorker := jetbridge.NewWorker(dbWorker, fakeClientset, shortCfg)
		shortWorker.SetExecutor(&fakeExecExecutor{})

		shortContainer, _, err := shortWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("exec-sched-timeout-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeGet},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ResourceType: "git"},
				Type:      db.ContainerTypeGet,
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		stderrBuf := new(bytes.Buffer)
		process, err := shortContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/opt/resource/in",
			Args: []string{"/tmp/build/get"},
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: stderrBuf,
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-timeout-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: insufficient cpu.",
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("pod scheduling timeout"))
		Expect(err.Error()).To(ContainSubstring("Unschedulable"))
		Expect(stderrBuf.String()).To(ContainSubstring("waiting up to"))
		Expect(stderrBuf.String()).To(ContainSubstring("cluster resources"))

	})

	// KEPT FROM CORE 2a9355e1e6 (added to process_test.go after this branch's
	// merge-base). No brine evidence exists for it, so it is carried here
	// rather than deleted with the rest of the file. Core placed it beside
	// "preserves the pause pod when context is cancelled (for fly hijack)",
	// which stays deleted on its own evidence; the exec-mode fixture that It
	// shared is reproduced here.
	It("supervised step teardown on context end deletes a supervised task's pause pod when context is cancelled", func() {
		var (
			fakeExecutor *fakeExecExecutor
			execWorker   *jetbridge.Worker
		)

		fakeExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)

		// A get step's command runs on the exec stream and dies with it, so a
		// cancelled context leaves nothing running and the pod is worth
		// keeping for fly hijack. A supervised task step's command was started
		// with SIGHUP ignored and would otherwise keep running, so its pod is
		// torn down.

		taskContainer, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("exec-task-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		var deleteOptions []metav1.DeleteOptions
		fakeClientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
			deleteOptions = append(deleteOptions, action.(k8stesting.DeleteActionImpl).DeleteOptions)
			return false, nil, nil
		})

		// No Stdin: this is what makes the step supervised.
		process, err := taskContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "trap '' TERM; sleep 600"},
		}, runtime.ProcessIO{Stdout: new(bytes.Buffer)})
		Expect(err).ToNot(HaveOccurred())

		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err = process.Wait(cancelCtx)
		Expect(err).To(HaveOccurred())

		By("verifying the abandoned pause pod was deleted, with no grace period")
		_, err = fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-task-handle", metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the task's pause pod to be gone")

		Expect(deleteOptions).To(HaveLen(1))
		Expect(deleteOptions[0].GracePeriodSeconds).ToNot(BeNil())
		Expect(*deleteOptions[0].GracePeriodSeconds).To(BeEquivalentTo(0))

	})
})
