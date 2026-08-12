package jetbridge_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/metric"
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

var _ = Describe("Reaper", func() {
	var (
		ctx                 context.Context
		database            jetbridgeDB
		dbWorker            db.Worker
		fakeClientset       *fake.Clientset
		containerRepository db.ContainerRepository
		destroyer           gc.Destroyer
		cfg                 jetbridge.Config
		reaper              *jetbridge.Reaper
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		fakeClientset = fake.NewSimpleClientset()
		containerRepository = database.ContainerRepository
		cfg = jetbridge.NewConfig("test-namespace", "")
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-test-namespace")
		Expect(err).ToNot(HaveOccurred())

		testLogger := lagertest.NewTestLogger("reaper")
		destroyer = gc.NewDestroyer(testLogger, containerRepository, database.VolumeRepository)
		reaper = jetbridge.NewReaper(testLogger, fakeClientset, cfg, containerRepository, destroyer)
		reaper.SetBuildLookup(database.BuildFactory)

		metric.Metrics.ContainersDeleted.Delta()
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

	persistCreatedContainerOn := func(worker db.Worker, handle string) db.CreatedContainer {
		GinkgoHelper()
		creating, err := worker.CreateContainer(
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
		)
		Expect(err).ToNot(HaveOccurred())
		created, err := creating.Created()
		Expect(err).ToNot(HaveOccurred())
		return created
	}

	persistCreatedContainer := func(handle string) db.CreatedContainer {
		GinkgoHelper()
		return persistCreatedContainerOn(dbWorker, handle)
	}

	persistDestroyingContainer := func(handle string) db.DestroyingContainer {
		GinkgoHelper()
		destroying, err := persistCreatedContainer(handle).Destroying()
		Expect(err).ToNot(HaveOccurred())
		return destroying
	}

	containerRow := func(handle string) (string, string, sql.NullTime, bool) {
		GinkgoHelper()
		var state, workerName string
		var missingSince sql.NullTime
		err := database.Conn.QueryRow(
			`SELECT state, worker_name, missing_since FROM containers WHERE handle = $1`,
			handle,
		).Scan(&state, &workerName, &missingSince)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", sql.NullTime{}, false
		}
		Expect(err).ToNot(HaveOccurred())
		return state, workerName, missingSince, true
	}

	persistStartedBuild := func(name string) (db.Build, int) {
		GinkgoHelper()
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "reaper-" + name})
		Expect(err).ToNot(HaveOccurred())
		pipeline, _, err := team.SavePipeline(
			atc.PipelineRef{Name: "pipeline-" + name},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			0,
			false,
		)
		Expect(err).ToNot(HaveOccurred())
		job, found, err := pipeline.Job("some-job")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("reaper-test")
		Expect(err).ToNot(HaveOccurred())
		started, err := build.Start(atc.Plan{ID: atc.PlanID("plan-" + name)})
		Expect(err).ToNot(HaveOccurred())
		Expect(started).To(BeTrue())
		return build, team.ID()
	}

	persistBuildContainer := func(build db.Build, teamID int, planID atc.PlanID) db.CreatedContainer {
		GinkgoHelper()
		creating, err := dbWorker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), planID, teamID),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
		)
		Expect(err).ToNot(HaveOccurred())
		created, err := creating.Created()
		Expect(err).ToNot(HaveOccurred())
		return created
	}

	Describe("container reporting", func() {
		It("reports active pod handles to UpdateContainersMissingSince", func() {
			persistCreatedContainer("pod-aaa")
			persistCreatedContainer("pod-bbb")
			persistCreatedContainer("unreported-decoy")
			_, err := database.Conn.Exec(
				`UPDATE containers SET missing_since = NOW() - INTERVAL '1 minute' WHERE handle IN ($1, $2)`,
				"pod-aaa",
				"pod-bbb",
			)
			Expect(err).ToNot(HaveOccurred())
			createLabelledPod("pod-aaa")
			createLabelledPod("pod-bbb")

			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			for _, handle := range []string{"pod-aaa", "pod-bbb"} {
				state, workerName, missingSince, found := containerRow(handle)
				Expect(found).To(BeTrue())
				Expect(state).To(Equal(atc.ContainerStateCreated))
				Expect(workerName).To(Equal("k8s-test-namespace"))
				Expect(missingSince.Valid).To(BeFalse())
			}
			state, workerName, missingSince, found := containerRow("unreported-decoy")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeTrue())
		})

		It("deletes the DB rows of destroying containers no pod reports, on this worker only", func() {
			persistCreatedContainer("pod-ccc")
			persistDestroyingContainer("pod-ddd")
			otherWorker, err := persistNamedWorker(database, "k8s-other-namespace")
			Expect(err).ToNot(HaveOccurred())
			_, err = persistCreatedContainerOn(otherWorker, "other-worker-pod").Destroying()
			Expect(err).ToNot(HaveOccurred())
			createLabelledPod("pod-ccc")

			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("removing the row for the destroying container with no pod")
			_, _, _, found := containerRow("pod-ddd")
			Expect(found).To(BeFalse())
			Expect(metric.Metrics.ContainersDeleted.Delta()).To(Equal(float64(1)))

			By("leaving the reported container and the other worker's row alone")
			state, workerName, _, found := containerRow("pod-ccc")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			state, workerName, _, found = containerRow("other-worker-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-other-namespace"))
		})

		It("reports empty handles when no pods exist", func() {
			persistCreatedContainer("unreported-container")
			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			state, workerName, missingSince, found := containerRow("unreported-container")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeTrue())
		})

		It("calls DestroyUnknownContainers with active pod handles to catch orphans", func() {
			persistCreatedContainer("known-pod")
			createLabelledPod("orphan-pod")
			createLabelledPod("known-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			state, workerName, missingSince, found := containerRow("orphan-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())

			state, workerName, missingSince, found = containerRow("known-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("known-pod"))
		})
	})

	Describe("pod reaping", func() {
		It("deletes pods that are in 'destroying' state in the DB", func() {
			persistDestroyingContainer("pod-to-destroy")
			persistCreatedContainer("pod-to-keep")
			createLabelledPod("pod-to-destroy")
			createLabelledPod("pod-to-keep")

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
			state, workerName, _, found := containerRow("pod-to-destroy")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
		})

		It("removes the DB row of a destroying container whose pod is already gone", func() {
			persistDestroyingContainer("already-gone-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			_, _, _, found := containerRow("already-gone-pod")
			Expect(found).To(BeFalse())
			Expect(metric.Metrics.ContainersDeleted.Delta()).To(Equal(float64(1)))
		})

		It("does not fail when a pod vanishes between the list and the delete", func() {
			persistDestroyingContainer("racing-pod")
			createLabelledPod("racing-pod")
			fakeClientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), "racing-pod")
			})

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			state, workerName, missingSince, found := containerRow("racing-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})

		It("does nothing when no containers are in destroying state", func() {
			persistCreatedContainer("healthy-pod")
			createLabelledPod("healthy-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("verifying no pods were deleted")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			state, workerName, missingSince, found := containerRow("healthy-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})
	})

	// A completed step's pod carries concourse.ci/exit-status, which is the
	// only record of its result that survives a web restart: Container.Attach
	// reads it so the resumed plan skips the step instead of running it
	// again. The reaper may only reap that pod once its build is done.
	Describe("completed pod reaping", func() {
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

		livePodNames := func() []string {
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			names := make([]string, len(pods.Items))
			for i, p := range pods.Items {
				names[i] = p.Name
			}
			return names
		}

		It("keeps a completed step's pod while its build is still running", func() {
			build, teamID := persistStartedBuild("running-owner")
			container := persistBuildContainer(build, teamID, "running-step")
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				container.Handle(),
				strconv.Itoa(build.ID()),
			)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("leaving the pod and its exit-status annotation in place")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "my-pipeline-unit-test-b42-task-550e8400", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pod.Annotations).To(HaveKeyWithValue("concourse.ci/exit-status", "0"))

			By("reporting it as active so its DB row is not marked missing")
			state, workerName, missingSince, found := containerRow(container.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})

		It("reaps a completed step's pod once its build is no longer running", func() {
			finishedBuild, teamID := persistStartedBuild("finished-owner")
			container := persistBuildContainer(finishedBuild, teamID, "finished-step")
			Expect(finishedBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			_, _ = persistStartedBuild("running-decoy")
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				container.Handle(),
				strconv.Itoa(finishedBuild.ID()),
			)

			Expect(reaper.Run(ctx)).To(Succeed())

			Expect(livePodNames()).To(BeEmpty())
			state, workerName, missingSince, found := containerRow(container.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeTrue())
		})

		It("fast-reaps a completed check pod, which has no build to resume", func() {
			const checkHandle = "aabbccdd-e29b-41d4-a716-446655440000"
			persistCreatedContainer(checkHandle)
			build, teamID := persistStartedBuild("check-running-owner")
			buildContainer := persistBuildContainer(build, teamID, "check-running-step")
			createCompletedPod("chk-my-resource-aabbccdd", checkHandle, "")
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				buildContainer.Handle(),
				strconv.Itoa(build.ID()),
			)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("reaping the check pod while the running build's pod stays")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
			_, _, checkMissingSince, found := containerRow(checkHandle)
			Expect(found).To(BeTrue())
			Expect(checkMissingSince.Valid).To(BeTrue())
			state, workerName, buildMissingSince, found := containerRow(buildContainer.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(buildMissingSince.Valid).To(BeFalse())
		})

		It("keeps completed pods when the running-build set cannot be read", func() {
			build, teamID := persistStartedBuild("lookup-error-owner")
			container := persistBuildContainer(build, teamID, "lookup-error-step")
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				container.Handle(),
				strconv.Itoa(build.ID()),
			)
			reaper.SetBuildLookup(db.NewBuildFactory(
				closedJetbridgeCloneConn(), database.LockFactory, 0, time.Hour,
			))

			Expect(reaper.Run(ctx)).To(Succeed())

			By("failing closed rather than deleting an annotation it cannot prove is stale")
			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
			state, workerName, missingSince, found := containerRow(container.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})

		It("keeps completed pods when no build lookup is configured", func() {
			unwiredReaper := jetbridge.NewReaper(
				lagertest.NewTestLogger("reaper"), fakeClientset, cfg, containerRepository, destroyer,
			)
			build, teamID := persistStartedBuild("unwired-owner")
			container := persistBuildContainer(build, teamID, "unwired-step")
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				container.Handle(),
				strconv.Itoa(build.ID()),
			)

			Expect(unwiredReaper.Run(ctx)).To(Succeed())

			Expect(livePodNames()).To(ConsistOf("my-pipeline-unit-test-b42-task-550e8400"))
			state, workerName, missingSince, found := containerRow(container.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})

		It("still deletes a retained pod through the DB destroying path", func() {
			build, teamID := persistStartedBuild("destroying-owner")
			container := persistBuildContainer(build, teamID, "destroying-step")
			_, err := container.Destroying()
			Expect(err).ToNot(HaveOccurred())
			createCompletedPod(
				"my-pipeline-unit-test-b42-task-550e8400",
				container.Handle(),
				strconv.Itoa(build.ID()),
			)

			Expect(reaper.Run(ctx)).To(Succeed())

			By("resolving the readable pod name from the handle label")
			Expect(livePodNames()).To(BeEmpty())
			state, workerName, _, found := containerRow(container.Handle())
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
		})
	})

	Describe("reaper idempotency", func() {
		It("is safe to run twice when first run already deleted the pod", func() {
			persistDestroyingContainer("pod-to-destroy")
			createLabelledPod("pod-to-destroy")

			By("first reaper sweep deletes the pod")
			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(BeEmpty())

			By("second reaper sweep removes the DB row now that no pod reports it")
			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
			_, _, _, found := containerRow("pod-to-destroy")
			Expect(found).To(BeFalse())
			Expect(metric.Metrics.ContainersDeleted.Delta()).To(Equal(float64(1)))
		})

		It("does not destroy a newly created pod that is not marked destroying", func() {
			persistDestroyingContainer("existing-pod")
			persistCreatedContainer("brand-new-pod")
			createLabelledPod("existing-pod")
			createLabelledPod("brand-new-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("verifying only the destroying pod was deleted")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("brand-new-pod"))

			By("verifying both pods were reported to the DB before deletion")
			state, workerName, missingSince, found := containerRow("existing-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
			state, workerName, missingSince, found = containerRow("brand-new-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
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
			const firstHandle = "abcdef12-3456-7890-abcd-ef1234567890"
			const secondHandle = "11223344-5566-7788-99aa-bbccddeeff00"
			persistCreatedContainer(firstHandle)
			persistCreatedContainer(secondHandle)
			persistCreatedContainer("readable-decoy")
			_, err := database.Conn.Exec(
				`UPDATE containers SET missing_since = NOW() - INTERVAL '1 minute' WHERE handle IN ($1, $2)`,
				firstHandle,
				secondHandle,
			)
			Expect(err).ToNot(HaveOccurred())
			createPodWithHandle("my-pipeline-build-b1-task-abcdef12", firstHandle)
			createPodWithHandle("ci-test-b7-get-11223344", secondHandle)

			err = reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			for _, handle := range []string{firstHandle, secondHandle} {
				state, workerName, missingSince, found := containerRow(handle)
				Expect(found).To(BeTrue())
				Expect(state).To(Equal(atc.ContainerStateCreated))
				Expect(workerName).To(Equal("k8s-test-namespace"))
				Expect(missingSince.Valid).To(BeFalse())
			}
			_, _, decoyMissingSince, found := containerRow("readable-decoy")
			Expect(found).To(BeTrue())
			Expect(decoyMissingSince.Valid).To(BeTrue())
		})

		It("deletes pods by pod name when DB returns handles for destruction", func() {
			const destroyingHandle = "abcdef12-3456-7890-abcd-ef1234567890"
			const keptHandle = "11223344-5566-7788-99aa-bbccddeeff00"
			persistDestroyingContainer(destroyingHandle)
			persistCreatedContainer(keptHandle)
			createPodWithHandle("my-pipeline-build-b1-task-abcdef12", destroyingHandle)
			createPodWithHandle("ci-test-b7-get-11223344", keptHandle)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("deleting the pod with the readable name, not the UUID handle")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("ci-test-b7-get-11223344"))
			state, workerName, _, found := containerRow(destroyingHandle)
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateDestroying))
			Expect(workerName).To(Equal("k8s-test-namespace"))
		})

		It("falls back to pod name when handle label is missing (backward compat)", func() {
			persistCreatedContainer("legacy-uuid-pod")
			createLabelledPod("legacy-uuid-pod")

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			state, workerName, missingSince, found := containerRow("legacy-uuid-pod")
			Expect(found).To(BeTrue())
			Expect(state).To(Equal(atc.ContainerStateCreated))
			Expect(workerName).To(Equal("k8s-test-namespace"))
			Expect(missingSince.Valid).To(BeFalse())
		})
	})

})
