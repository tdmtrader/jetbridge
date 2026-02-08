package k8sruntime_test

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc/gcfakes"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
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
		fakeVolumeRepository    *dbfakes.FakeVolumeRepository
		cfg                     k8sruntime.Config
		reaper                  *k8sruntime.Reaper
		executor                *fakeExecExecutor
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
		fakeContainerRepository = new(dbfakes.FakeContainerRepository)
		fakeDestroyer = new(gcfakes.FakeDestroyer)
		fakeVolumeRepository = new(dbfakes.FakeVolumeRepository)
		cfg = k8sruntime.NewConfig("test-namespace", "")
		executor = &fakeExecExecutor{}

		testLogger := lagertest.NewTestLogger("reaper")
		reaper = k8sruntime.NewReaper(testLogger, fakeClientset, cfg, fakeContainerRepository, fakeDestroyer)
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

	Describe("cache volume cleanup", func() {
		BeforeEach(func() {
			cfg.CacheVolumeClaim = "my-cache-pvc"
			testLogger := lagertest.NewTestLogger("reaper")
			reaper = k8sruntime.NewReaper(testLogger, fakeClientset, cfg, fakeContainerRepository, fakeDestroyer)
			reaper.SetVolumeRepo(fakeVolumeRepository)
			reaper.SetExecutor(executor)
		})

		It("execs rm -rf on PVC subdirectories for destroying volumes", func() {
			createLabelledPod("active-pod")
			fakeVolumeRepository.GetDestroyingVolumesReturns([]string{"vol-1", "vol-2"}, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("exec-ing rm -rf for each destroying volume")
			Expect(executor.execCalls).To(HaveLen(2))
			Expect(executor.execCalls[0].podName).To(Equal("active-pod"))
			Expect(executor.execCalls[0].command).To(Equal([]string{"rm", "-rf", "/concourse/cache/vol-1"}))
			Expect(executor.execCalls[1].command).To(Equal([]string{"rm", "-rf", "/concourse/cache/vol-2"}))

			By("removing cleaned volumes from the DB")
			Expect(fakeVolumeRepository.RemoveDestroyingVolumesCallCount()).To(Equal(1))
			workerName, failedHandles := fakeVolumeRepository.RemoveDestroyingVolumesArgsForCall(0)
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(failedHandles).To(BeEmpty())
		})

		It("skips volume cleanup when no pods are running", func() {
			fakeVolumeRepository.GetDestroyingVolumesReturns([]string{"vol-1"}, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("not executing any cleanup commands")
			Expect(executor.execCalls).To(BeEmpty())
			By("not removing volumes from the DB")
			Expect(fakeVolumeRepository.RemoveDestroyingVolumesCallCount()).To(Equal(0))
		})

		It("skips volume cleanup when CacheVolumeClaim is not configured", func() {
			cfg.CacheVolumeClaim = ""
			testLogger := lagertest.NewTestLogger("reaper")
			reaper = k8sruntime.NewReaper(testLogger, fakeClientset, cfg, fakeContainerRepository, fakeDestroyer)
			reaper.SetVolumeRepo(fakeVolumeRepository)
			reaper.SetExecutor(executor)

			createLabelledPod("active-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeVolumeRepository.GetDestroyingVolumesCallCount()).To(Equal(0))
		})

		It("keeps failed volumes in the DB when exec fails", func() {
			createLabelledPod("active-pod")
			fakeVolumeRepository.GetDestroyingVolumesReturns([]string{"vol-ok", "vol-fail"}, nil)
			executor.execErr = fmt.Errorf("exec connection lost")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("attempting cleanup for all volumes")
			Expect(executor.execCalls).To(HaveLen(2))

			By("passing all handles as failed since all execs errored")
			Expect(fakeVolumeRepository.RemoveDestroyingVolumesCallCount()).To(Equal(1))
			_, failedHandles := fakeVolumeRepository.RemoveDestroyingVolumesArgsForCall(0)
			Expect(failedHandles).To(ConsistOf("vol-ok", "vol-fail"))
		})

		It("does nothing when no volumes are in destroying state", func() {
			createLabelledPod("active-pod")
			fakeVolumeRepository.GetDestroyingVolumesReturns([]string{}, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(executor.execCalls).To(BeEmpty())
			Expect(fakeVolumeRepository.RemoveDestroyingVolumesCallCount()).To(Equal(0))
		})
	})
})
