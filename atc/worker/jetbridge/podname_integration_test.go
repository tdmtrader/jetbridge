package jetbridge_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Pod Name Integration", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
	})

	Describe("Run creates pod with readable name", func() {
		It("uses GeneratePodName when metadata has pipeline/job/build", func() {
			setupFakeDBContainer(fakeDBWorker, "550e8400-e29b-41d4-a716-446655440000")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("550e8400-e29b-41d4-a716-446655440000"),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					StepName:     "run-tests",
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			By("pod name is human-readable, not the UUID handle")
			Expect(pod.Name).To(MatchRegexp(`^my-pipeline-unit-test-b42-task-[a-f0-9]{8}$`))
			Expect(pod.Name).ToNot(Equal("550e8400-e29b-41d4-a716-446655440000"))
		})

		It("falls back to handle when metadata has no pipeline/job", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			setupFakeDBContainer(fakeDBWorker, handle)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal(handle))
		})

		It("uses readable name in exec mode too", func() {
			execWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(&fakeExecExecutor{})

			setupFakeDBContainer(fakeDBWorker, "aabbccdd-1122-3344-5566-778899aabbcc")

			container, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("aabbccdd-1122-3344-5566-778899aabbcc"),
				db.ContainerMetadata{
					Type:         db.ContainerTypeGet,
					StepName:     "source-code",
					PipelineName: "ci",
					JobName:      "build",
					BuildName:    "7",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			Expect(pod.Name).To(MatchRegexp(`^ci-build-b7-get-[a-f0-9]{8}$`))
		})
	})

	Describe("Attach looks up pod by podName", func() {
		It("finds the pod using the readable name, not the handle", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			expectedPodName := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type:         db.ContainerTypeTask,
				PipelineName: "my-pipeline",
				JobName:      "unit-test",
				BuildName:    "42",
			}, handle)

			setupFakeDBContainer(fakeDBWorker, handle)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			// Store exit status so Attach returns an exitedProcess
			// (avoids launching a pod watcher that would hang).
			err = container.SetProperty("concourse:exit-status", "0")
			Expect(err).ToNot(HaveOccurred())

			// Create a pod with the readable name (not the handle).
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      expectedPodName,
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "busybox"},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Attach should return quickly since exit-status is already set.
			process, err := container.Attach(ctx, "some-process", runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})

		It("looks up pod by podName when no exit status is cached", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			expectedPodName := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type:         db.ContainerTypeTask,
				PipelineName: "my-pipeline",
				JobName:      "unit-test",
				BuildName:    "42",
			}, handle)

			setupFakeDBContainer(fakeDBWorker, handle)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			By("Attach fails when no pod exists with the readable name")
			_, err = container.Attach(ctx, "some-process", runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(expectedPodName))
			Expect(err.Error()).ToNot(ContainSubstring(handle))
		})
	})

	Describe("Pod labels include rich metadata", func() {
		It("adds pipeline, job, build, step, and handle labels", func() {
			setupFakeDBContainer(fakeDBWorker, "550e8400-e29b-41d4-a716-446655440000")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("550e8400-e29b-41d4-a716-446655440000"),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					StepName:     "run-tests",
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			labels := pods.Items[0].Labels
			Expect(labels).To(HaveKeyWithValue("concourse.ci/pipeline", "my-pipeline"))
			Expect(labels).To(HaveKeyWithValue("concourse.ci/job", "unit-test"))
			Expect(labels).To(HaveKeyWithValue("concourse.ci/build", "42"))
			Expect(labels).To(HaveKeyWithValue("concourse.ci/step", "run-tests"))
			Expect(labels).To(HaveKeyWithValue("concourse.ci/handle", "550e8400-e29b-41d4-a716-446655440000"))
		})

		It("omits empty metadata labels", func() {
			setupFakeDBContainer(fakeDBWorker, "test-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("test-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			labels := pods.Items[0].Labels
			Expect(labels).ToNot(HaveKey("concourse.ci/pipeline"))
			Expect(labels).ToNot(HaveKey("concourse.ci/job"))
			Expect(labels).To(HaveKeyWithValue("concourse.ci/handle", "test-handle"))
		})

		It("truncates label values to 63 chars (K8s label limit)", func() {
			setupFakeDBContainer(fakeDBWorker, "label-trunc-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("label-trunc-handle"),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: "extremely-long-pipeline-name-that-exceeds-the-sixty-three-character-k8s-label-value-limit",
					JobName:      "j",
					BuildName:    "1",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			for _, v := range pods.Items[0].Labels {
				Expect(len(v)).To(BeNumerically("<=", 63))
			}
		})
	})

	Describe("Volume binding uses podName", func() {
		It("binds volumes to the readable pod name after Run", func() {
			execWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(&fakeExecExecutor{})

			setupFakeDBContainer(fakeDBWorker, "550e8400-e29b-41d4-a716-446655440000")

			container, volumeMounts, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("550e8400-e29b-41d4-a716-446655440000"),
				db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
				},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/my-input"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			inputMount := filterMountsByPaths(volumeMounts, []string{"/tmp/build/workdir/my-input"})
			Expect(inputMount).To(HaveLen(1))

			vol := inputMount[0].Volume.(*jetbridge.Volume)
			By("pod name is empty before Run")
			Expect(vol.PodName()).To(BeEmpty())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("pod name is the readable name after Run, not the UUID handle")
			Expect(vol.PodName()).To(MatchRegexp(`^my-pipeline-unit-test-b42-task-[a-f0-9]{8}$`))
			Expect(vol.PodName()).ToNot(Equal("550e8400-e29b-41d4-a716-446655440000"))
		})
	})
})
