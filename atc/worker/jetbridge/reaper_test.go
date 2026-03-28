package jetbridge_test

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc/gcfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Reaper", func() {
	var (
		ctx                     context.Context
		fakeClientset           *fake.Clientset
		fakeContainerRepository *dbfakes.FakeContainerRepository
		fakeDestroyer           *gcfakes.FakeDestroyer
		cfg                     jetbridge.Config
		reaper                  *jetbridge.Reaper
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
		fakeContainerRepository = new(dbfakes.FakeContainerRepository)
		fakeDestroyer = new(gcfakes.FakeDestroyer)
		cfg = jetbridge.NewConfig("test-namespace", "")

		testLogger := lagertest.NewTestLogger("reaper")
		reaper = jetbridge.NewReaper(testLogger, fakeClientset, cfg, fakeContainerRepository, fakeDestroyer)
	})

	createLabelledPod := func(name string) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "test-namespace",
				Labels: map[string]string{
					"concourse.ci/worker": fmt.Sprintf("k8s-%s", cfg.Namespace),
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	Describe("container reporting", func() {
		It("reports active pod handles to UpdateContainersMissingSince", func() {
			createLabelledPod("pod-aaa")
			createLabelledPod("pod-bbb")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeContainerRepository.UpdateContainersMissingSinceCallCount()).To(Equal(1))
			workerName, handles := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(handles).To(ConsistOf("pod-aaa", "pod-bbb"))
		})

		It("calls DestroyContainers with active pod handles to clean up DB rows", func() {
			createLabelledPod("pod-ccc")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeDestroyer.DestroyContainersCallCount()).To(Equal(1))
			workerName, handles := fakeDestroyer.DestroyContainersArgsForCall(0)
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(handles).To(ConsistOf("pod-ccc"))
		})

		It("reports empty handles when no pods exist", func() {
			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeContainerRepository.UpdateContainersMissingSinceCallCount()).To(Equal(1))
			_, handles := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(handles).To(BeEmpty())
		})

		It("calls DestroyUnknownContainers with active pod handles to catch orphans", func() {
			createLabelledPod("orphan-pod")
			createLabelledPod("known-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeContainerRepository.DestroyUnknownContainersCallCount()).To(Equal(1))
			workerName, handles := fakeContainerRepository.DestroyUnknownContainersArgsForCall(0)
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(handles).To(ConsistOf("orphan-pod", "known-pod"))
		})
	})

	Describe("pod reaping", func() {
		It("deletes pods that are in 'destroying' state in the DB", func() {
			createLabelledPod("pod-to-destroy")
			createLabelledPod("pod-to-keep")

			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"pod-to-destroy"}, nil,
			)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("deleting the destroying pod from K8s")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			podNames := make([]string, len(pods.Items))
			for i, p := range pods.Items {
				podNames[i] = p.Name
			}
			Expect(podNames).To(ConsistOf("pod-to-keep"))
			Expect(podNames).ToNot(ContainElement("pod-to-destroy"))
		})

		It("does not fail when a destroying pod does not exist in K8s", func() {
			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"already-gone-pod"}, nil,
			)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
		})

		It("does nothing when no containers are in destroying state", func() {
			createLabelledPod("healthy-pod")

			fakeContainerRepository.FindDestroyingContainersReturns([]string{}, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("verifying no pods were deleted")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
		})
	})

	Describe("reaper idempotency", func() {
		It("is safe to run twice when first run already deleted the pod", func() {
			createLabelledPod("pod-to-destroy")

			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"pod-to-destroy"}, nil,
			)

			By("first reaper sweep deletes the pod")
			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(BeEmpty())

			By("second reaper sweep succeeds even though pod is already gone")
			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"pod-to-destroy"}, nil,
			)
			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
		})

		It("does not destroy a newly created pod that is not marked destroying", func() {
			createLabelledPod("existing-pod")
			createLabelledPod("brand-new-pod")

			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"existing-pod"}, nil,
			)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("verifying only the destroying pod was deleted")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("brand-new-pod"))

			By("verifying both pods were reported to the DB before deletion")
			Expect(fakeContainerRepository.UpdateContainersMissingSinceCallCount()).To(Equal(1))
			_, handles := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(handles).To(ConsistOf("existing-pod", "brand-new-pod"))
		})
	})

	Describe("readable pod names with handle labels", func() {
		createPodWithHandle := func(podName, handle string) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: "test-namespace",
					Labels: map[string]string{
						"concourse.ci/worker": fmt.Sprintf("k8s-%s", cfg.Namespace),
						"concourse.ci/handle": handle,
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		It("reports DB handles (from labels) not pod names to UpdateContainersMissingSince", func() {
			createPodWithHandle("my-pipeline-build-b1-task-abcdef12", "abcdef12-3456-7890-abcd-ef1234567890")
			createPodWithHandle("ci-test-b7-get-11223344", "11223344-5566-7788-99aa-bbccddeeff00")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeContainerRepository.UpdateContainersMissingSinceCallCount()).To(Equal(1))
			_, handles := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(handles).To(ConsistOf(
				"abcdef12-3456-7890-abcd-ef1234567890",
				"11223344-5566-7788-99aa-bbccddeeff00",
			))
		})

		It("deletes pods by pod name when DB returns handles for destruction", func() {
			createPodWithHandle("my-pipeline-build-b1-task-abcdef12", "abcdef12-3456-7890-abcd-ef1234567890")
			createPodWithHandle("ci-test-b7-get-11223344", "11223344-5566-7788-99aa-bbccddeeff00")

			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"abcdef12-3456-7890-abcd-ef1234567890"}, nil,
			)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("deleting the pod with the readable name, not the UUID handle")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("ci-test-b7-get-11223344"))
		})

		It("falls back to pod name when handle label is missing (backward compat)", func() {
			createLabelledPod("legacy-uuid-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeContainerRepository.UpdateContainersMissingSinceCallCount()).To(Equal(1))
			_, handles := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(handles).To(ConsistOf("legacy-uuid-pod"))
		})
	})

})
