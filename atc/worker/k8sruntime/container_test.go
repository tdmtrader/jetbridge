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
			Expect(pod.Spec.SecurityContext.RunAsNonRoot).ToNot(BeNil())
			Expect(*pod.Spec.SecurityContext.RunAsNonRoot).To(BeTrue())
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

			It("sets AllowPrivilegeEscalation=false and RunAsNonRoot=true", func() {
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

				By("setting RunAsNonRoot on pod security context")
				Expect(pod.Spec.SecurityContext).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.RunAsNonRoot).ToNot(BeNil())
				Expect(*pod.Spec.SecurityContext.RunAsNonRoot).To(BeTrue())

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

			It("sets Privileged=true and RunAsNonRoot=false", func() {
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

				By("setting RunAsNonRoot=false on pod security context")
				Expect(pod.Spec.SecurityContext).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.RunAsNonRoot).ToNot(BeNil())
				Expect(*pod.Spec.SecurityContext.RunAsNonRoot).To(BeFalse())

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
	})
})

