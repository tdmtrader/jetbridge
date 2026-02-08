package k8sruntime_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Container", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *k8sruntime.Worker
		ctx           context.Context
		cfg           k8sruntime.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = k8sruntime.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
	})

	Describe("Run", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("run-test-handle")

			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("run-test-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)

			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("run-test-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					TeamName: "main",
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{
						ImageURL: "docker:///busybox",
					},
					Env: []string{"FOO=bar", "BAZ=qux"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a Pod with the correct image, command, args, and env", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
				Dir:  "/workdir",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process).ToNot(BeNil())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			Expect(pod.Name).To(Equal("run-test-handle"))
			Expect(pod.Spec.Containers).To(HaveLen(1))
			Expect(pod.Spec.Containers[0].Image).To(Equal("docker:///busybox"))
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"/bin/sh"}))
			Expect(pod.Spec.Containers[0].Args).To(Equal([]string{"-c", "echo hello"}))
			Expect(pod.Spec.Containers[0].WorkingDir).To(Equal("/workdir"))
			Expect(pod.Spec.Containers[0].Env).To(ContainElements(
				corev1.EnvVar{Name: "FOO", Value: "bar"},
				corev1.EnvVar{Name: "BAZ", Value: "qux"},
			))
			Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
		})

		It("returns a Process with an ID", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "true"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process.ID()).ToNot(BeEmpty())
		})
	})

	Describe("Run with input volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("input-vol-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("input-vol-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("input-vol-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{
							DestinationPath: "/tmp/build/workdir/input-a",
						},
						{
							DestinationPath: "/tmp/build/workdir/input-b",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a Pod with emptyDir volumes mounted at input paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "ls /tmp/build/workdir/input-a"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]

			By("adding emptyDir volumes for each input")
			Expect(pod.Spec.Volumes).To(HaveLen(2))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			By("mounting volumes at the correct paths in the container")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))

			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir/input-a",
				"/tmp/build/workdir/input-b",
			))
		})
	})

	Describe("Run with output volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("output-vol-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("output-vol-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("output-vol-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Outputs: runtime.OutputPaths{
						"result":   "/tmp/build/workdir/result",
						"metadata": "/tmp/build/workdir/metadata",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a Pod with emptyDir volumes mounted at output paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("adding emptyDir volumes for each output")
			Expect(pod.Spec.Volumes).To(HaveLen(2))

			By("mounting volumes at the correct paths in the container")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))

			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir/result",
				"/tmp/build/workdir/metadata",
			))
		})
	})

	Describe("Run with cache volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("cache-vol-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("cache-vol-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("cache-vol-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Caches:   []string{"/tmp/build/workdir/.cache"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a Pod with emptyDir volumes mounted at cache paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("adding an emptyDir volume for the cache")
			Expect(pod.Spec.Volumes).To(HaveLen(1))
			Expect(pod.Spec.Volumes[0].EmptyDir).ToNot(BeNil())

			By("mounting at the cache path")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(1))
			Expect(mainContainer.VolumeMounts[0].MountPath).To(Equal("/tmp/build/workdir/.cache"))
		})
	})

	Describe("Properties and SetProperty", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("props-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("props-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("props-handle"),
				db.ContainerMetadata{},
				runtime.ContainerSpec{
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("stores and retrieves properties", func() {
			err := container.SetProperty("my-key", "my-value")
			Expect(err).ToNot(HaveOccurred())

			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveKeyWithValue("my-key", "my-value"))
		})
	})

	Describe("Attach", func() {
		var container runtime.Container

		BeforeEach(func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("attach-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("attach-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("attach-handle"),
				db.ContainerMetadata{},
				runtime.ContainerSpec{
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when the process has already exited", func() {
			BeforeEach(func() {
				// Store exit status in properties (as gardenruntime does)
				err := container.SetProperty("concourse:exit-status", "0")
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns an already-exited process", func() {
				process, err := container.Attach(ctx, "some-process-id", runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))
			})
		})
	})
})

