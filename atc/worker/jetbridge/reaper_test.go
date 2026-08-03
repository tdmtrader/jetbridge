package jetbridge_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc/gcfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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

	createPrivateMountSecret := func(name, handle, podName string, uid types.UID) {
		controller := true
		immutable := true
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "test-namespace",
				Labels: map[string]string{
					"concourse.ci/private-mount-for":     handle,
					"concourse.ci/private-mount-pod-uid": string(uid),
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: podName, UID: uid, Controller: &controller}},
			},
			Immutable: &immutable,
			Data:      map[string][]byte{"profile.yml": []byte("trusted")},
		}
		_, err := fakeClientset.CoreV1().Secrets("test-namespace").Create(ctx, secret, metav1.CreateOptions{})
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

	// A completed step's pod carries concourse.ci/exit-status, which is the
	// only record of its result that survives a web restart: Container.Attach
	// reads it so the resumed plan skips the step instead of running it
	// again. The reaper may only reap that pod once its build is done.
	Describe("completed pod reaping", func() {
		var fakeBuildFactory *dbfakes.FakeBuildFactory

		// createCompletedPod builds a pod as Container.buildPodLabels would:
		// a readable name plus the handle and (for job builds) build-id
		// labels, annotated as a finished step.
		createCompletedPod := func(podName, handle, buildID string) {
			labels := map[string]string{
				"concourse.ci/worker": fmt.Sprintf("k8s-%s", cfg.Namespace),
				"concourse.ci/handle": handle,
			}
			if buildID != "" {
				labels["concourse.ci/build-id"] = buildID
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        podName,
					Namespace:   "test-namespace",
					Labels:      labels,
					Annotations: map[string]string{"concourse.ci/exit-status": "0"},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		startedBuild := func(id int) db.Build {
			build := new(dbfakes.FakeBuild)
			build.IDReturns(id)
			return build
		}

		livePodNames := func() []string {
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			names := make([]string, len(pods.Items))
			for i, p := range pods.Items {
				names[i] = p.Name
			}
			return names
		}

		BeforeEach(func() {
			fakeBuildFactory = new(dbfakes.FakeBuildFactory)
			reaper.SetBuildLookup(fakeBuildFactory)
		})

		It("keeps a completed step's pod while its build is still running", func() {
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")
			fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{startedBuild(653430)}, nil)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("leaving the pod and its exit-status annotation in place")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "my-pipeline-unit-test-b42-task-550e8400", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pod.Annotations).To(HaveKeyWithValue("concourse.ci/exit-status", "0"))

			By("reporting it as active so its DB row is not marked missing")
			_, reported := fakeContainerRepository.UpdateContainersMissingSinceArgsForCall(0)
			Expect(reported).To(ConsistOf("550e8400-e29b-41d4-a716-446655440000"))
		})

		It("reaps a completed step's pod once its build is no longer running", func() {
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")
			fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{startedBuild(999999)}, nil)

			Expect(reaper.Run(ctx)).To(Succeed())

			Expect(livePodNames()).To(BeEmpty())
		})

		It("fast-reaps a completed check pod, which has no build to resume", func() {
			createCompletedPod("chk-my-resource-aabbccdd", "aabbccdd-e29b-41d4-a716-446655440000", "")
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")
			fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{startedBuild(653430)}, nil)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("reaping the check pod while the running build's pod stays")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
		})

		It("keeps completed pods when the running-build set cannot be read", func() {
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")
			fakeBuildFactory.GetAllStartedBuildsReturns(nil, errors.New("database is down"))

			Expect(reaper.Run(ctx)).To(Succeed())

			By("failing closed rather than deleting an annotation it cannot prove is stale")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
		})

		It("keeps completed pods when no build lookup is configured", func() {
			unwiredReaper := jetbridge.NewReaper(
				lagertest.NewTestLogger("reaper"), fakeClientset, cfg, fakeContainerRepository, fakeDestroyer,
			)
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")

			Expect(unwiredReaper.Run(ctx)).To(Succeed())

			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
		})

		It("still deletes a retained pod through the DB destroying path", func() {
			createCompletedPod("my-pipeline-unit-test-b42-task-550e8400", "550e8400-e29b-41d4-a716-446655440000", "653430")
			fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{startedBuild(653430)}, nil)
			fakeContainerRepository.FindDestroyingContainersReturns(
				[]string{"550e8400-e29b-41d4-a716-446655440000"}, nil,
			)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("resolving the readable pod name from the handle label")
			Expect(livePodNames()).To(BeEmpty())
		})
	})

	Describe("private authority Secret lifecycle", func() {
		createOwnerlessPrivateMountSecret := func(name, handle, intendedPod string) {
			immutable := true
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: name, Namespace: "test-namespace", CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
					Labels: map[string]string{
						"concourse.ci/private-mount-for":      handle,
						"concourse.ci/private-mount-pod-name": intendedPod,
					},
					Annotations: map[string]string{"concourse.ci/private-mount-data-digest": "sha256:test"},
				},
				Immutable: &immutable,
				Data:      map[string][]byte{"profile.yml": []byte("trusted")},
			}
			_, err := fakeClientset.CoreV1().Secrets("test-namespace").Create(ctx, secret, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		It("keeps an ownerless pre-bind Secret while a live Pod references it", func() {
			createLabelledPod("private-pod")
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "private-pod", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Spec.Volumes = []corev1.Volume{{Name: "authority", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "private-secret"}}}}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			createOwnerlessPrivateMountSecret("private-secret", "private-handle", "private-pod")

			Expect(reaper.Run(ctx)).To(Succeed())
			_, err = fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("grace-cleans an ownerless pre-Pod Secret after no Pod references it", func() {
			createOwnerlessPrivateMountSecret("private-secret", "private-handle", "never-created")

			Expect(reaper.Run(ctx)).To(Succeed())
			_, err := fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).To(HaveOccurred())
		})

		It("does not remove an owner-bound Secret when pod deletion fails", func() {
			const handle = "private-handle"
			const podName = "private-pod"
			uid := types.UID("private-pod-uid")
			createLabelledPod(podName)
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.UID = uid
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			createPrivateMountSecret("private-secret", handle, podName, uid)
			fakeContainerRepository.FindDestroyingContainersReturns([]string{podName}, nil)
			fakeClientset.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("pod delete failed")
			})

			err = reaper.Run(ctx)
			Expect(err).To(HaveOccurred())
			_, err = fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("keeps a Secret while its exact owner Pod still exists", func() {
			const handle = "private-handle"
			const podName = "private-pod"
			uid := types.UID("private-pod-uid")
			createLabelledPod(podName)
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.UID = uid
			pod.Labels["concourse.ci/handle"] = handle
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			createPrivateMountSecret("private-secret", handle, podName, uid)

			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
			_, err = fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("removes only an exact owner-bound orphan after Pod absence is confirmed", func() {
			createPrivateMountSecret("private-secret", "private-handle", "gone-pod", types.UID("gone-uid"))

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
			_, err = fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).To(HaveOccurred())
		})

		It("does not delete a replacement Secret after owner-bound orphan observation", func() {
			oldUID := types.UID("old-private-secret")
			replacementUID := types.UID("replacement-private-secret")
			createPrivateMountSecret("private-secret", "private-handle", "gone-pod", types.UID("gone-uid"))
			observed, err := fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			observed.UID = oldUID
			_, err = fakeClientset.CoreV1().Secrets("test-namespace").Update(ctx, observed, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			var deletedUID *types.UID
			fakeClientset.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				deleteAction := action.(ktesting.DeleteAction)
				if deleteAction.GetName() != "private-secret" {
					return false, nil, nil
				}
				if deleteAction.GetDeleteOptions().Preconditions != nil {
					deletedUID = deleteAction.GetDeleteOptions().Preconditions.UID
				}
				replacement := observed.DeepCopy()
				replacement.UID = replacementUID
				if err := fakeClientset.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), replacement, "test-namespace"); err != nil {
					return true, nil, err
				}
				return true, nil, nil
			})

			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(deletedUID).ToNot(BeNil())
			Expect(*deletedUID).To(Equal(oldUID))
			replacement, err := fakeClientset.CoreV1().Secrets("test-namespace").Get(ctx, "private-secret", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(replacement.UID).To(Equal(replacementUID))
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
