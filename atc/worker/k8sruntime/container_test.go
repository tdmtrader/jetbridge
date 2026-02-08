package k8sruntime_test

import (
	"bytes"
	"context"
	"io"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
			Expect(pod.Spec.Containers[0].Image).To(Equal("busybox"))
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"/bin/sh"}))
			Expect(pod.Spec.Containers[0].Args).To(Equal([]string{"-c", "echo hello"}))
			Expect(pod.Spec.Containers[0].WorkingDir).To(Equal("/workdir"))
			Expect(pod.Spec.Containers[0].Env).To(ContainElements(
				corev1.EnvVar{Name: "FOO", Value: "bar"},
				corev1.EnvVar{Name: "BAZ", Value: "qux"},
			))
			Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))

			By("applying secure defaults (non-privileged)")
			Expect(pod.Spec.SecurityContext).ToNot(BeNil())
			Expect(pod.Spec.Containers[0].SecurityContext).ToNot(BeNil())
			Expect(pod.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
			Expect(*pod.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
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

	Describe("Run with resource limits", func() {
		var container runtime.Container

		Context("when CPU and Memory limits are specified", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("limits-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("limits-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				cpu := uint64(1024)
				memory := uint64(1073741824)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("limits-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Limits: runtime.ContainerLimits{
							CPU:    &cpu,
							Memory: &memory,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets K8s resource requests and limits on the main container", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				mainContainer := pods.Items[0].Spec.Containers[0]
				expectedCPU := resource.NewMilliQuantity(1024, resource.DecimalSI)
				expectedMemory := resource.NewQuantity(1073741824, resource.BinarySI)

				By("setting resource limits")
				Expect(mainContainer.Resources.Limits.Cpu().Cmp(*expectedCPU)).To(Equal(0),
					"expected CPU limit of 1024m, got %s", mainContainer.Resources.Limits.Cpu())
				Expect(mainContainer.Resources.Limits.Memory().Cmp(*expectedMemory)).To(Equal(0),
					"expected memory limit of 1Gi, got %s", mainContainer.Resources.Limits.Memory())

				By("setting requests equal to limits for Guaranteed QoS")
				Expect(mainContainer.Resources.Requests.Cpu().Cmp(*expectedCPU)).To(Equal(0),
					"expected CPU request of 1024m, got %s", mainContainer.Resources.Requests.Cpu())
				Expect(mainContainer.Resources.Requests.Memory().Cmp(*expectedMemory)).To(Equal(0),
					"expected memory request of 1Gi, got %s", mainContainer.Resources.Requests.Memory())
			})
		})

		Context("when no limits are specified", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("no-limits-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("no-limits-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("no-limits-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates a pod with no resource constraints (BestEffort QoS)", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				mainContainer := pods.Items[0].Spec.Containers[0]
				Expect(mainContainer.Resources.Limits).To(BeNil())
				Expect(mainContainer.Resources.Requests).To(BeNil())
			})
		})
	})

	Describe("Run with security context", func() {
		var container runtime.Container

		Context("when the container is not privileged", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("secure-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("secure-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("secure-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{
							ImageURL:   "docker:///busybox",
							Privileged: false,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets AllowPrivilegeEscalation=false on non-privileged container", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				mainContainer := pod.Spec.Containers[0]

				By("not setting RunAsNonRoot (images may run as root)")
				Expect(pod.Spec.SecurityContext).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.RunAsNonRoot).To(BeNil())

				By("setting AllowPrivilegeEscalation=false on container security context")
				Expect(mainContainer.SecurityContext).ToNot(BeNil())
				Expect(mainContainer.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
				Expect(*mainContainer.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			})
		})

		Context("when the container is privileged", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("priv-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("priv-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("priv-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{
							ImageURL:   "docker:///busybox",
							Privileged: true,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets Privileged=true on privileged container", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				mainContainer := pod.Spec.Containers[0]

				By("not setting RunAsNonRoot")
				Expect(pod.Spec.SecurityContext).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.RunAsNonRoot).To(BeNil())

				By("setting Privileged=true on container security context")
				Expect(mainContainer.SecurityContext).ToNot(BeNil())
				Expect(mainContainer.SecurityContext.Privileged).ToNot(BeNil())
				Expect(*mainContainer.SecurityContext.Privileged).To(BeTrue())
			})
		})
	})

	Describe("Run with imagePullSecrets and serviceAccount", func() {
		var container runtime.Container

		Context("when image pull secrets and service account are configured", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("secrets-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("secrets-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				cfgWithSecrets := k8sruntime.NewConfig("test-namespace", "")
				cfgWithSecrets.ImagePullSecrets = []string{"registry-creds", "gcr-key"}
				cfgWithSecrets.ServiceAccount = "ci-runner"

				secretsWorker := k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfgWithSecrets)

				var err error
				container, _, err = secretsWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("secrets-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("includes imagePullSecrets and serviceAccountName in the pod spec", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]

				By("setting imagePullSecrets from config")
				Expect(pod.Spec.ImagePullSecrets).To(HaveLen(2))
				Expect(pod.Spec.ImagePullSecrets).To(ContainElements(
					corev1.LocalObjectReference{Name: "registry-creds"},
					corev1.LocalObjectReference{Name: "gcr-key"},
				))

				By("setting serviceAccountName from config")
				Expect(pod.Spec.ServiceAccountName).To(Equal("ci-runner"))
			})
		})

		Context("when no image pull secrets or service account are configured", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("no-secrets-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("no-secrets-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("no-secrets-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates a pod with no imagePullSecrets or serviceAccountName", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				Expect(pod.Spec.ImagePullSecrets).To(BeEmpty())
				Expect(pod.Spec.ServiceAccountName).To(BeEmpty())
			})
		})
	})

	Describe("Run uses exec-mode for all tasks (universal pause pod)", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorker    *k8sruntime.Worker
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(execExecutor)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("exec-task-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("exec-task-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-task-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a pause pod even when stdin is nil", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
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
			By("using the pause command instead of the user command")
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"}))

			By("executing the real command via the executor")
			simulatePodRunning := func(podName string) {
				p, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				p.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, p, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())
			}
			simulatePodRunning("exec-task-handle")

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(execExecutor.execCalls).To(HaveLen(1))
			Expect(execExecutor.execCalls[0].command).To(Equal([]string{"/bin/sh", "-c", "echo hello"}))
		})

		It("keeps pause pod alive after command completes with exit 0", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod reaching Running state
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-task-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the pod still exists after successful completion")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1), "pause pod should NOT be deleted after exec completes — GC handles cleanup")
		})

		It("keeps pause pod alive after command completes with non-zero exit", func() {
			execExecutor.execErr = &k8sruntime.ExecExitError{ExitCode: 42}

			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "exit 42"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod reaching Running state
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-task-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(42))

			By("verifying the pod still exists after failed exec — for debugging")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1), "pause pod should NOT be deleted after exec failure — GC handles cleanup")
		})
	})

	Describe("FindOrCreateContainer returns VolumeMounts", func() {
		var execWorker *k8sruntime.Worker

		BeforeEach(func() {
			execWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(&fakeExecExecutor{})
		})

		Context("when container spec has inputs", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("vm-input-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("vm-input-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				_, volumeMounts, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("vm-input-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{DestinationPath: "/tmp/build/workdir/my-input"},
							{DestinationPath: "/tmp/build/workdir/other-input"},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns a VolumeMount for each input with correct MountPath", func() {
				inputMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/my-input",
					"/tmp/build/workdir/other-input",
				})
				Expect(inputMounts).To(HaveLen(2))

				for _, m := range inputMounts {
					By("each VolumeMount has a non-nil Volume")
					Expect(m.Volume).ToNot(BeNil())
					Expect(m.Volume.Handle()).ToNot(BeEmpty())
				}
			})

			It("returns Volumes with an executor wired up for StreamIn/StreamOut", func() {
				inputMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/my-input",
				})
				Expect(inputMounts).To(HaveLen(1))

				vol, ok := inputMounts[0].Volume.(*k8sruntime.Volume)
				Expect(ok).To(BeTrue(), "volume should be *k8sruntime.Volume")
				Expect(vol).ToNot(BeNil())
				Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")
			})
		})

		Context("when container spec has outputs", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("vm-output-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("vm-output-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				_, volumeMounts, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("vm-output-handle"),
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

			It("returns a VolumeMount for each output with correct MountPath", func() {
				outputMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/result",
					"/tmp/build/workdir/metadata",
				})
				Expect(outputMounts).To(HaveLen(2))

				for _, m := range outputMounts {
					Expect(m.Volume).ToNot(BeNil())
					Expect(m.Volume.Handle()).ToNot(BeEmpty())
				}
			})

			It("returns output Volumes with an executor wired up for StreamIn/StreamOut", func() {
				outputMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/result",
				})
				Expect(outputMounts).To(HaveLen(1))

				vol, ok := outputMounts[0].Volume.(*k8sruntime.Volume)
				Expect(ok).To(BeTrue(), "volume should be *k8sruntime.Volume")
				Expect(vol).ToNot(BeNil())
				Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")
			})
		})

		Context("when container spec has caches", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("vm-cache-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("vm-cache-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				_, volumeMounts, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("vm-cache-handle"),
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

			It("returns a VolumeMount for each cache with correct MountPath", func() {
				cacheMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/.cache",
				})
				Expect(cacheMounts).To(HaveLen(1))
				Expect(cacheMounts[0].Volume).ToNot(BeNil())
				Expect(cacheMounts[0].Volume.Handle()).ToNot(BeEmpty())
			})

			It("returns cache Volumes with an executor wired up for StreamIn/StreamOut", func() {
				cacheMounts := filterMountsByPaths(volumeMounts, []string{
					"/tmp/build/workdir/.cache",
				})
				Expect(cacheMounts).To(HaveLen(1))

				vol, ok := cacheMounts[0].Volume.(*k8sruntime.Volume)
				Expect(ok).To(BeTrue(), "volume should be *k8sruntime.Volume")
				Expect(vol).ToNot(BeNil())
				Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")
			})
		})

		Context("deferred pod name is set when Run creates the pod", func() {
			var (
				volumeMounts []runtime.VolumeMount
				container    runtime.Container
			)

			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("deferred-pod-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("deferred-pod-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				container, volumeMounts, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("deferred-pod-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
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
			})

			It("sets the pod name on deferred volumes after Run", func() {
				inputMount := filterMountsByPaths(volumeMounts, []string{"/tmp/build/workdir/my-input"})
				Expect(inputMount).To(HaveLen(1))

				vol := inputMount[0].Volume.(*k8sruntime.Volume)
				By("pod name is empty before Run")
				Expect(vol.PodName()).To(BeEmpty())

				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				By("pod name is set after Run")
				Expect(vol.PodName()).To(Equal("deferred-pod-handle"))
			})
		})
	})

	Describe("Input streaming before exec", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorkerIS  *k8sruntime.Worker
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorkerIS = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorkerIS.SetExecutor(execExecutor)
		})

		simulatePodRunning := func(podName string) {
			p, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
			ExpectWithOffset(1, err).ToNot(HaveOccurred())
			p.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, p, metav1.UpdateOptions{})
			ExpectWithOffset(1, err).ToNot(HaveOccurred())
		}

		Context("when inputs have artifacts", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("stream-input-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("stream-input-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				artifact := &fakeArtifact{
					handle:    "source-artifact",
					source:    "other-worker",
					streamOut: []byte("tar-stream-data"),
				}

				var err error
				execContainer, _, err = execWorkerIS.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("stream-input-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        artifact,
								DestinationPath: "/tmp/build/workdir/my-input",
							},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("streams input artifacts into the pod before executing the command", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "cat /tmp/build/workdir/my-input/file"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				simulatePodRunning("stream-input-handle")

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				By("exec calls include tar xf (StreamIn) BEFORE the actual command")
				Expect(execExecutor.execCalls).To(HaveLen(2))

				streamInCall := execExecutor.execCalls[0]
				Expect(streamInCall.command).To(Equal([]string{"tar", "xf", "-", "-C", "/tmp/build/workdir/my-input"}))
				Expect(streamInCall.podName).To(Equal("stream-input-handle"))

				commandCall := execExecutor.execCalls[1]
				Expect(commandCall.command).To(Equal([]string{"/bin/sh", "-c", "cat /tmp/build/workdir/my-input/file"}))
			})

			It("passes the artifact's stream data to tar stdin", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo done"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				simulatePodRunning("stream-input-handle")

				_, err = process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())

				streamInCall := execExecutor.execCalls[0]
				Expect(streamInCall.stdin).ToNot(BeNil())
				stdinData, err := io.ReadAll(streamInCall.stdin)
				Expect(err).ToNot(HaveOccurred())
				Expect(stdinData).To(Equal([]byte("tar-stream-data")))
			})
		})

		Context("when an input has a nil artifact", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("nil-artifact-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("nil-artifact-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				execContainer, _, err = execWorkerIS.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("nil-artifact-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        nil,
								DestinationPath: "/tmp/build/workdir/empty-input",
							},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("skips streaming and executes the command normally", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo done"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				simulatePodRunning("nil-artifact-handle")

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				By("only the command exec happens — no tar xf calls")
				Expect(execExecutor.execCalls).To(HaveLen(1))
				Expect(execExecutor.execCalls[0].command).To(Equal([]string{"/bin/sh", "-c", "echo done"}))
			})
		})

		Context("when there are multiple inputs with artifacts", func() {
			BeforeEach(func() {
				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("multi-input-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("multi-input-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				execContainer, _, err = execWorkerIS.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("multi-input-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        &fakeArtifact{handle: "art-1", source: "w1", streamOut: []byte("data-1")},
								DestinationPath: "/tmp/build/workdir/input-a",
							},
							{
								Artifact:        &fakeArtifact{handle: "art-2", source: "w1", streamOut: []byte("data-2")},
								DestinationPath: "/tmp/build/workdir/input-b",
							},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("streams all inputs before executing the command", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo done"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				simulatePodRunning("multi-input-handle")

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				By("two StreamIn calls + one command exec = 3 total exec calls")
				Expect(execExecutor.execCalls).To(HaveLen(3))

				By("first two are tar xf calls (StreamIn)")
				streamPaths := []string{
					execExecutor.execCalls[0].command[4], // -C <path>
					execExecutor.execCalls[1].command[4],
				}
				Expect(streamPaths).To(ConsistOf(
					"/tmp/build/workdir/input-a",
					"/tmp/build/workdir/input-b",
				))

				By("last is the actual command")
				Expect(execExecutor.execCalls[2].command).To(Equal([]string{"/bin/sh", "-c", "echo done"}))
			})
		})
	})

	Describe("Output volume extraction after exec", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorkerOE  *k8sruntime.Worker
			volumeMounts  []runtime.VolumeMount
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorkerOE = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorkerOE.SetExecutor(execExecutor)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("output-extract-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("output-extract-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			var err error
			execContainer, volumeMounts, err = execWorkerOE.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("output-extract-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Outputs: runtime.OutputPaths{
						"result": "/tmp/build/workdir/result",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("output volumes can StreamOut after exec completes", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello > /tmp/build/workdir/result/output.txt"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod reaching Running state
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "output-extract-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("finding the output volume mount")
			outputMounts := filterMountsByPaths(volumeMounts, []string{"/tmp/build/workdir/result"})
			Expect(outputMounts).To(HaveLen(1))

			By("the volume has a pod name set (from Run)")
			vol := outputMounts[0].Volume.(*k8sruntime.Volume)
			Expect(vol.PodName()).To(Equal("output-extract-handle"))
			Expect(vol.HasExecutor()).To(BeTrue())

			By("StreamOut invokes tar cf on the pod via the executor")
			execExecutor.execStdout = []byte("tar-output-data")
			readCloser, err := vol.StreamOut(ctx, ".", nil)
			Expect(err).ToNot(HaveOccurred())
			defer readCloser.Close()

			data, err := io.ReadAll(readCloser)
			Expect(err).ToNot(HaveOccurred())
			Expect(data).To(Equal([]byte("tar-output-data")))

			By("verifying the tar cf exec call targeted the correct path")
			lastCall := execExecutor.execCalls[len(execExecutor.execCalls)-1]
			Expect(lastCall.command).To(Equal([]string{"tar", "cf", "-", "-C", "/tmp/build/workdir/result", "."}))
			Expect(lastCall.podName).To(Equal("output-extract-handle"))
		})

		It("pod remains running after exec for output extraction", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "output-extract-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("pod still exists after exec completes — not deleted")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
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

	Describe("Run into existing pod (fly hijack)", func() {
		var (
			hijackContainer runtime.Container
			hijackExecutor  *fakeExecExecutor
			hijackWorker    *k8sruntime.Worker
		)

		BeforeEach(func() {
			hijackExecutor = &fakeExecExecutor{}
			hijackWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			hijackWorker.SetExecutor(hijackExecutor)

			// Simulate an existing pause pod (created by a previous task run).
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hijack-pod",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"concourse.ci/worker": "k8s-worker-1",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Set up DB fakes so LookupContainer finds the container.
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("hijack-pod")
			fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)

			hijackContainer, _, err = hijackWorker.LookupContainer(ctx, "hijack-pod")
			Expect(err).ToNot(HaveOccurred())
		})

		It("execs into the existing pod without creating a new one", func() {
			process, err := hijackContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/bash",
				Args: []string{"-l"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process).ToNot(BeNil())

			By("not creating a second pod")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("hijack-pod"))

			By("executing the hijack command via the executor")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(hijackExecutor.execCalls).To(HaveLen(1))
			Expect(hijackExecutor.execCalls[0].command).To(Equal([]string{"/bin/bash", "-l"}))
			Expect(hijackExecutor.execCalls[0].podName).To(Equal("hijack-pod"))
		})

		It("passes TTY flag through to executor for interactive sessions", func() {
			process, err := hijackContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/bash",
				TTY: &runtime.TTYSpec{
					WindowSize: runtime.WindowSize{Columns: 80, Rows: 24},
				},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(hijackExecutor.execCalls).To(HaveLen(1))
			Expect(hijackExecutor.execCalls[0].tty).To(BeTrue())
		})

		It("does not set TTY when ProcessSpec.TTY is nil", func() {
			process, err := hijackContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hi"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(hijackExecutor.execCalls).To(HaveLen(1))
			Expect(hijackExecutor.execCalls[0].tty).To(BeFalse())
		})

		It("propagates exit codes from hijacked commands", func() {
			hijackExecutor.execErr = &k8sruntime.ExecExitError{ExitCode: 130}

			process, err := hijackContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/bash",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(130))
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

		Context("exec-mode: when executor is set and pod has exit annotation", func() {
			var execContainer runtime.Container

			BeforeEach(func() {
				execWorker := k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("exec-attach-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("exec-attach-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("exec-attach-handle"),
					db.ContainerMetadata{},
					runtime.ContainerSpec{
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Create a pod with the exit status annotation (simulating
				// exec completed before web restart).
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "exec-attach-handle",
						Namespace: "test-namespace",
						Annotations: map[string]string{
							"concourse.ci/exit-status": "0",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "main", Image: "alpine"},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns exitedProcess from the pod annotation", func() {
				process, err := execContainer.Attach(ctx, "some-process", runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))
			})
		})

		Context("exec-mode: when executor is set and pod has no exit annotation", func() {
			var execContainer runtime.Container

			BeforeEach(func() {
				execWorker := k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

				fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("exec-noann-handle")
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("exec-noann-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("exec-noann-handle"),
					db.ContainerMetadata{},
					runtime.ContainerSpec{
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Create a pod WITHOUT exit annotation (exec hasn't completed).
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "exec-noann-handle",
						Namespace: "test-namespace",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "main", Image: "alpine"},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns error so engine falls through to Run", func() {
				_, err := execContainer.Attach(ctx, "some-process", runtime.ProcessIO{})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no completion status"))
			})
		})
	})
})

// filterMountsByPaths returns volume mounts whose MountPath matches any of the given paths.
func filterMountsByPaths(mounts []runtime.VolumeMount, paths []string) []runtime.VolumeMount {
	pathSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		pathSet[p] = true
	}
	var result []runtime.VolumeMount
	for _, m := range mounts {
		if pathSet[m.MountPath] {
			result = append(result, m)
		}
	}
	return result
}

// fakeArtifact is a test double for runtime.Artifact that returns
// predetermined stream data.
type fakeArtifact struct {
	handle    string
	source    string
	streamOut []byte
}

func (a *fakeArtifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a.streamOut)), nil
}

func (a *fakeArtifact) Handle() string { return a.handle }
func (a *fakeArtifact) Source() string { return a.source }
