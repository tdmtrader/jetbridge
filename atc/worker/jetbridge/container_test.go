package jetbridge_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("Container", func() {
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

	Describe("Run", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "run-test-handle")

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

			By("hardening: SA token disabled and seccomp set")
			Expect(pod.Spec.AutomountServiceAccountToken).ToNot(BeNil())
			Expect(*pod.Spec.AutomountServiceAccountToken).To(BeFalse())
			Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
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

	Describe("Run with Dir volume", func() {
		It("creates a Pod with an emptyDir volume for spec.Dir when Dir is set", func() {
			setupFakeDBContainer(fakeDBWorker, "dir-vol-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("dir-vol-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/tmp/build/workdir",
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

			By("adding an emptyDir volume for the Dir path")
			Expect(pod.Spec.Volumes).To(HaveLen(1))
			Expect(pod.Spec.Volumes[0].EmptyDir).ToNot(BeNil())

			By("mounting the Dir volume at the correct path")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(1))
			Expect(mainContainer.VolumeMounts[0].MountPath).To(Equal("/tmp/build/workdir"))
		})

		It("does not create a Dir volume when spec.Dir is empty", func() {
			setupFakeDBContainer(fakeDBWorker, "no-dir-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("no-dir-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
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
			Expect(pod.Spec.Volumes).To(BeEmpty())

			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(BeEmpty())
		})
	})

	Describe("Run with input volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "input-vol-handle")

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

			By("adding emptyDir volumes for Dir and each input")
			Expect(pod.Spec.Volumes).To(HaveLen(3))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			By("mounting volumes at the correct paths in the container")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(3))

			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/tmp/build/workdir/input-a",
				"/tmp/build/workdir/input-b",
			))
		})
	})

	Describe("Run with output volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "output-vol-handle")

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

			By("adding emptyDir volumes for Dir and each output")
			Expect(pod.Spec.Volumes).To(HaveLen(3))

			By("mounting volumes at the correct paths in the container")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(3))

			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/tmp/build/workdir/result",
				"/tmp/build/workdir/metadata",
			))
		})
	})

	Describe("Run with same-name input and output", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "shared-io-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("shared-io-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/repo"},
					},
					Outputs: runtime.OutputPaths{
						"repo": "/tmp/build/workdir/repo/",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("shares a single volume when input and output paths overlap", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "ls /tmp/build/workdir/repo"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]

			By("creating only 2 volumes (dir + shared input/output), not 3")
			Expect(pod.Spec.Volumes).To(HaveLen(2))

			By("creating only 2 mounts, not 3")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))

			By("having no duplicate mount paths")
			mountPaths := map[string]int{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths[vm.MountPath]++
			}
			for path, count := range mountPaths {
				Expect(count).To(Equal(1), "mount path %s should appear only once", path)
			}
		})

		It("uses the input volume for the shared mount (not a new output volume)", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo ok"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod := pods.Items[0]
			mainContainer := pod.Spec.Containers[0]

			By("the shared mount being named input-*, not output-*")
			for _, vm := range mainContainer.VolumeMounts {
				if vm.MountPath == "/tmp/build/workdir/repo" || vm.MountPath == "/tmp/build/workdir/repo/" {
					Expect(vm.Name).To(HavePrefix("input-"))
				}
			}
		})
	})

	Describe("Run with non-overlapping inputs and outputs", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "nonoverlap-io-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("nonoverlap-io-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/source"},
					},
					Outputs: runtime.OutputPaths{
						"binary": "/tmp/build/workdir/binary/",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates separate volumes for non-overlapping input and output", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo ok"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod := pods.Items[0]

			By("creating 3 volumes (dir + input + output)")
			Expect(pod.Spec.Volumes).To(HaveLen(3))

			By("creating 3 mounts")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(3))
		})
	})

	Describe("Run with cache volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "cache-vol-handle")

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

			By("adding emptyDir volumes for Dir and the cache")
			Expect(pod.Spec.Volumes).To(HaveLen(2))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			By("mounting at the Dir and cache paths")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))
			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/tmp/build/workdir/.cache",
			))
		})
	})

	Describe("Run with scratch path volumes", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "scratch-vol-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("scratch-vol-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:       1,
					Dir:          "/tmp/build/workdir",
					ImageSpec:    runtime.ImageSpec{ImageURL: "docker:///busybox"},
					ScratchPaths: []string{"/scratch/buildkit"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a Pod with emptyDir volumes for scratch paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("adding emptyDir volumes for Dir and the scratch path")
			Expect(pod.Spec.Volumes).To(HaveLen(2))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			By("mounting at the Dir and scratch paths")
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))
			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/scratch/buildkit",
			))
		})

		It("does not create cache entries for scratch paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("having no init containers for scratch restore")
			Expect(pod.Spec.InitContainers).To(BeEmpty())
		})
	})

	Describe("Run with scratch paths and caches together", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "scratch-cache-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("scratch-cache-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:       1,
					Dir:          "/tmp/build/workdir",
					ImageSpec:    runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Caches:       []string{"/tmp/build/workdir/.cache"},
					ScratchPaths: []string{"/scratch/work"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates separate volumes for caches and scratch paths", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("adding emptyDir volumes for Dir, cache, and scratch")
			Expect(pod.Spec.Volumes).To(HaveLen(3))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			mainContainer := pod.Spec.Containers[0]
			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir",
				"/tmp/build/workdir/.cache",
				"/scratch/work",
			))
		})
	})

	Describe("Run with relative scratch paths", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "scratch-rel-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("scratch-rel-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:       1,
					Dir:          "/tmp/build/workdir",
					ImageSpec:    runtime.ImageSpec{ImageURL: "docker:///busybox"},
					ScratchPaths: []string{"scratch"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("resolves relative scratch paths against the working directory", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			mainContainer := pod.Spec.Containers[0]
			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElement("/tmp/build/workdir/scratch"))
		})
	})

	Describe("Run with cache hostPath configured", func() {
		var container runtime.Container

		Context("when CacheHostPath is set", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "cache-hostpath-handle")

			cfgWithHostPath := jetbridge.NewConfig("test-namespace", "")
			cfgWithHostPath.CacheHostPath = "/var/concourse/cache"

			hostPathWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithHostPath)

			var err error
			container, _, err = hostPathWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("cache-hostpath-handle"),
				db.ContainerMetadata{
					Type:     db.ContainerTypeTask,
					JobID:    7,
					StepName: "compile",
				},
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

		It("uses hostPath volumes with stable keys for caches", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("creating hostPath volumes for caches")
			var hostPathVol *corev1.Volume
			for i := range pod.Spec.Volumes {
				if pod.Spec.Volumes[i].HostPath != nil {
					hostPathVol = &pod.Spec.Volumes[i]
					break
				}
			}
			Expect(hostPathVol).ToNot(BeNil(), "expected a hostPath volume for cache")
			Expect(hostPathVol.HostPath.Path).To(HavePrefix("/var/concourse/cache/job-7-compile-"))
			dirType := corev1.HostPathDirectoryOrCreate
			Expect(*hostPathVol.HostPath.Type).To(Equal(dirType))

			By("mounting at the cache path")
			mainContainer := pod.Spec.Containers[0]
			var cacheMount *corev1.VolumeMount
			for i := range mainContainer.VolumeMounts {
				if mainContainer.VolumeMounts[i].MountPath == "/tmp/build/workdir/.cache" {
					cacheMount = &mainContainer.VolumeMounts[i]
					break
				}
			}
			Expect(cacheMount).ToNot(BeNil())
			Expect(cacheMount.Name).To(Equal(hostPathVol.Name))
		})
	})

	Context("when CacheHostPath is set but JobID is 0 (one-off build)", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "cache-oneoff-handle")

			cfgWithHostPath := jetbridge.NewConfig("test-namespace", "")
			cfgWithHostPath.CacheHostPath = "/var/concourse/cache"

			hostPathWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithHostPath)

			var err error
			container, _, err = hostPathWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("cache-oneoff-handle"),
				db.ContainerMetadata{
					Type:  db.ContainerTypeTask,
					JobID: 0,
				},
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

		It("falls back to emptyDir for one-off builds", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			for _, vol := range pod.Spec.Volumes {
				Expect(vol.HostPath).To(BeNil(), "one-off builds should not use hostPath")
			}
		})
	})
	})

	Describe("Run with explicit CacheStore selector", func() {
		var container runtime.Container

		Context("when CacheStore=hostpath overrides artifact store", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "explicit-hostpath-handle")

				cfgExplicit := jetbridge.NewConfig("test-namespace", "")
				cfgExplicit.CacheHostPath = "/var/concourse/cache"
				cfgExplicit.CacheStore = jetbridge.CacheStoreHostPath

				explicitWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgExplicit)

				var err error
				container, _, err = explicitWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("explicit-hostpath-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    7,
						StepName: "compile",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:    []string{"/tmp/build/workdir/.cache"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uses hostPath even though artifact store is configured", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("creating hostPath volumes for caches")
				var hostPathVol *corev1.Volume
				for i := range pod.Spec.Volumes {
					if pod.Spec.Volumes[i].HostPath != nil {
						hostPathVol = &pod.Spec.Volumes[i]
						break
					}
				}
				Expect(hostPathVol).ToNot(BeNil(), "expected a hostPath volume for cache")
				Expect(hostPathVol.HostPath.Path).To(HavePrefix("/var/concourse/cache/job-7-compile-"))
			})
		})

		Context("when CacheStore=emptydir overrides artifact store", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "explicit-emptydir-handle")

				cfgExplicit := jetbridge.NewConfig("test-namespace", "")
				cfgExplicit.CacheStore = jetbridge.CacheStoreEmptyDir

				explicitWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgExplicit)

				var err error
				container, _, err = explicitWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("explicit-emptydir-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    42,
						StepName: "test",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:    []string{"/tmp/build/workdir/.cache"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uses emptyDir without init containers or cache uploads", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("using emptyDir volumes")
				var cacheVols []corev1.Volume
				for _, vol := range pod.Spec.Volumes {
					if vol.EmptyDir != nil && len(vol.Name) >= 5 && vol.Name[:5] == "cache" {
						cacheVols = append(cacheVols, vol)
					}
				}
				Expect(cacheVols).To(HaveLen(1))

				By("not creating cache restore init containers")
				for _, initC := range pod.Spec.InitContainers {
					Expect(initC.Name).ToNot(HavePrefix("restore-cache-"),
						"emptydir mode should not have cache restore init containers")
				}
			})
		})

		Context("when CacheStore=emptydir is explicitly set", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "explicit-emptydir-handle")

				cfgExplicit := jetbridge.NewConfig("test-namespace", "")
				cfgExplicit.CacheStore = jetbridge.CacheStoreEmptyDir

				explicitWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgExplicit)

				var err error
				container, _, err = explicitWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("explicit-emptydir-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    42,
						StepName: "build-step",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:    []string{"/tmp/build/workdir/.cache"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uses emptyDir for caches", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("cache volumes should be emptyDir with no subPath")
				mainContainer := pod.Spec.Containers[0]
				for _, m := range mainContainer.VolumeMounts {
					Expect(m.SubPath).To(BeEmpty(), "emptyDir caches should not use subPath")
				}
			})
		})
	})

	Describe("Run with resource limits", func() {
		var container runtime.Container

		Context("when CPU and Memory limits are specified", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "limits-handle")

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
				setupFakeDBContainer(fakeDBWorker, "no-limits-handle")

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

		Context("when both limits and independent requests are specified (Burstable QoS)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "burstable-handle")

				cpuLimit := uint64(2048)
				memLimit := uint64(4294967296) // 4GB
				cpuReq := uint64(512)
				memReq := uint64(1073741824) // 1GB

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("burstable-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID: 1,
						Dir:    "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Limits: runtime.ContainerLimits{
							CPU:           &cpuLimit,
							Memory:        &memLimit,
							CPURequest:    &cpuReq,
							MemoryRequest: &memReq,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets limits and requests independently", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				mainContainer := pods.Items[0].Spec.Containers[0]

				By("setting CPU/memory limits")
				Expect(mainContainer.Resources.Limits.Cpu().Cmp(*resource.NewMilliQuantity(2048, resource.DecimalSI))).To(Equal(0))
				Expect(mainContainer.Resources.Limits.Memory().Cmp(*resource.NewQuantity(4294967296, resource.BinarySI))).To(Equal(0))

				By("setting CPU/memory requests independently from limits")
				Expect(mainContainer.Resources.Requests.Cpu().Cmp(*resource.NewMilliQuantity(512, resource.DecimalSI))).To(Equal(0))
				Expect(mainContainer.Resources.Requests.Memory().Cmp(*resource.NewQuantity(1073741824, resource.BinarySI))).To(Equal(0))
			})
		})

		Context("when only requests are specified with no limits (Burstable no-cap QoS)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "requests-only-handle")

				cpuReq := uint64(256)
				memReq := uint64(536870912) // 512MB

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("requests-only-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID: 1,
						Dir:    "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Limits: runtime.ContainerLimits{
							CPURequest:    &cpuReq,
							MemoryRequest: &memReq,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets requests with no limits", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				mainContainer := pods.Items[0].Spec.Containers[0]

				By("not setting any limits")
				Expect(mainContainer.Resources.Limits).To(BeNil())

				By("setting only requests")
				Expect(mainContainer.Resources.Requests.Cpu().Cmp(*resource.NewMilliQuantity(256, resource.DecimalSI))).To(Equal(0))
				Expect(mainContainer.Resources.Requests.Memory().Cmp(*resource.NewQuantity(536870912, resource.BinarySI))).To(Equal(0))
			})
		})

		Context("when ephemeral-storage limits and requests are specified", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "ephemeral-handle")

				cpuLimit := uint64(1024)
				memLimit := uint64(1073741824) // 1GB
				ephLimit := uint64(5368709120) // 5GB
				ephReq := uint64(2147483648)   // 2GB

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("ephemeral-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID: 1,
						Dir:    "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Limits: runtime.ContainerLimits{
							CPU:                     &cpuLimit,
							Memory:                  &memLimit,
							EphemeralStorage:        &ephLimit,
							EphemeralStorageRequest: &ephReq,
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets ephemeral-storage in K8s resource requirements", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				mainContainer := pods.Items[0].Spec.Containers[0]

				By("setting ephemeral-storage limit")
				ephLimitQty := mainContainer.Resources.Limits[corev1.ResourceEphemeralStorage]
				Expect(ephLimitQty.Cmp(*resource.NewQuantity(5368709120, resource.BinarySI))).To(Equal(0),
					"expected ephemeral-storage limit of 5Gi, got %s", ephLimitQty.String())

				By("setting ephemeral-storage request")
				ephReqQty := mainContainer.Resources.Requests[corev1.ResourceEphemeralStorage]
				Expect(ephReqQty.Cmp(*resource.NewQuantity(2147483648, resource.BinarySI))).To(Equal(0),
					"expected ephemeral-storage request of 2Gi, got %s", ephReqQty.String())
			})
		})
	})

	Describe("Run with security context", func() {
		var container runtime.Container

		Context("when the container is not privileged", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "secure-handle")

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

				By("disabling service account token mount (task pods don't need K8s API access)")
				Expect(pod.Spec.AutomountServiceAccountToken).ToNot(BeNil())
				Expect(*pod.Spec.AutomountServiceAccountToken).To(BeFalse())

				By("setting seccomp RuntimeDefault profile")
				Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
			})
		})

		Context("when the container is privileged", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "priv-handle")

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

				By("disabling service account token mount even for privileged pods")
				Expect(pod.Spec.AutomountServiceAccountToken).ToNot(BeNil())
				Expect(*pod.Spec.AutomountServiceAccountToken).To(BeFalse())

				By("setting seccomp RuntimeDefault profile even for privileged pods")
				Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
				Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
			})
		})
	})

	Describe("Run with imagePullSecrets and serviceAccount", func() {
		var container runtime.Container

		Context("when image pull secrets and service account are configured", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "secrets-handle")

				cfgWithSecrets := jetbridge.NewConfig("test-namespace", "")
				cfgWithSecrets.ImagePullSecrets = []string{"registry-creds", "gcr-key"}
				cfgWithSecrets.ServiceAccount = "ci-runner"

				secretsWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithSecrets)

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
				setupFakeDBContainer(fakeDBWorker, "no-secrets-handle")

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

		Context("when ImageRegistry is configured with a SecretName", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "registry-handle")

				cfgWithRegistry := jetbridge.NewConfig("test-namespace", "")
				cfgWithRegistry.ImagePullSecrets = []string{"existing-secret"}
				cfgWithRegistry.ImageRegistry = &jetbridge.ImageRegistryConfig{
					Prefix:     "gcr.io/my-project/concourse",
					SecretName: "gcr-auth",
				}

				registryWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithRegistry)

				var err error
				container, _, err = registryWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("registry-handle"),
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

			It("auto-includes the registry secret in imagePullSecrets", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				Expect(pod.Spec.ImagePullSecrets).To(HaveLen(2))
				Expect(pod.Spec.ImagePullSecrets).To(ContainElements(
					corev1.LocalObjectReference{Name: "existing-secret"},
					corev1.LocalObjectReference{Name: "gcr-auth"},
				))
			})
		})

		Context("when ImageRegistry SecretName duplicates an existing imagePullSecret", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "dedup-handle")

				cfgDup := jetbridge.NewConfig("test-namespace", "")
				cfgDup.ImagePullSecrets = []string{"shared-secret"}
				cfgDup.ImageRegistry = &jetbridge.ImageRegistryConfig{
					SecretName: "shared-secret",
				}

				dedupWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgDup)

				var err error
				container, _, err = dedupWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("dedup-handle"),
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

			It("deduplicates the secret name", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				Expect(pod.Spec.ImagePullSecrets).To(HaveLen(1))
				Expect(pod.Spec.ImagePullSecrets[0].Name).To(Equal("shared-secret"))
			})
		})
	})

	Describe("Run uses exec-mode for all tasks (universal pause pod)", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(execExecutor)

			setupFakeDBContainer(fakeDBWorker, "exec-task-handle")

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
			execExecutor.execErr = &jetbridge.ExecExitError{ExitCode: 42}

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
		var execWorker *jetbridge.Worker

		BeforeEach(func() {
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(&fakeExecExecutor{})
		})

		Context("when container spec has inputs", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "vm-input-handle")

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

				vol, ok := inputMounts[0].Volume.(*jetbridge.Volume)
				Expect(ok).To(BeTrue(), "volume should be *jetbridge.Volume")
				Expect(vol).ToNot(BeNil())
				Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")
			})
		})

		Context("when container spec has outputs", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "vm-output-handle")

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

				vol, ok := outputMounts[0].Volume.(*jetbridge.Volume)
				Expect(ok).To(BeTrue(), "volume should be *jetbridge.Volume")
				Expect(vol).ToNot(BeNil())
				Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")
			})
		})

		Context("when container spec has caches", func() {
			var volumeMounts []runtime.VolumeMount

			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "vm-cache-handle")

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

				vol, ok := cacheMounts[0].Volume.(*jetbridge.Volume)
				Expect(ok).To(BeTrue(), "volume should be *jetbridge.Volume")
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
				setupFakeDBContainer(fakeDBWorker, "deferred-pod-handle")

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

				vol := inputMount[0].Volume.(*jetbridge.Volume)
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

	Describe("Input streaming is a no-op (handled by init containers)", func() {
		It("does not exec any streaming commands for inputs", func() {
			execExecutor := &fakeExecExecutor{}
			execWorkerIS := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorkerIS.SetExecutor(execExecutor)

			setupFakeDBContainer(fakeDBWorker, "noop-stream-handle")

			artifact := &fakeArtifact{
				handle:    "input-vol-1",
				source:    "k8s-worker-1",
				streamOut: []byte("tar-stream-data"),
			}

			container, _, err := execWorkerIS.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("noop-stream-handle"),
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

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("simulate pod running so Wait can proceed")
			pod, podErr := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "noop-stream-handle", metav1.GetOptions{})
			Expect(podErr).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			}
			_, podErr = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(podErr).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("only the command exec call, no streaming")
			Expect(execExecutor.execCalls).To(HaveLen(1))
			Expect(execExecutor.execCalls[0].command).To(Equal([]string{"/bin/sh", "-c", "echo done"}))
		})
	})

	Describe("Output volume extraction after exec", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorkerOE  *jetbridge.Worker
			volumeMounts  []runtime.VolumeMount
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorkerOE = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorkerOE.SetExecutor(execExecutor)

			setupFakeDBContainer(fakeDBWorker, "output-extract-handle")

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
			vol := outputMounts[0].Volume.(*jetbridge.Volume)
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
			setupFakeDBContainer(fakeDBWorker, "props-handle")

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
			hijackWorker    *jetbridge.Worker
		)

		BeforeEach(func() {
			hijackExecutor = &fakeExecExecutor{}
			hijackWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
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
			hijackExecutor.execErr = &jetbridge.ExecExitError{ExitCode: 130}

			process, err := hijackContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/bash",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(130))
		})
	})

	Describe("Run replaces terminal pod", func() {
		// Regression test: when a check container's pause pod completes
		// (e.g. sleep expires) or fails, the next Run must delete it and
		// create a fresh one instead of trying to exec into a dead pod.

		for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed} {
			phase := phase

			It(fmt.Sprintf("replaces a %s pod with a new pause pod", phase), func() {
				terminalExecutor := &fakeExecExecutor{}
				terminalWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				terminalWorker.SetExecutor(terminalExecutor)

				// Use a UUID-style handle so GeneratePodName produces a
				// predictable chk-<resource>-<suffix> pod name.
				handle := "aaaa1111-bbbb-cccc-dddd-eeee2222ffff"
				metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-time"}
				podName := jetbridge.GeneratePodName(metadata, handle)

				// Create a pod in terminal state (simulates expired sleep or crash).
				terminalPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      podName,
						Namespace: "test-namespace",
						Labels: map[string]string{
							"concourse.ci/worker": "k8s-worker-1",
						},
					},
					Status: corev1.PodStatus{Phase: phase},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, terminalPod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				setupFakeDBContainer(fakeDBWorker, handle)
				container, _, err := terminalWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner(handle),
					metadata,
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/time-resource"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/opt/resource/check",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				By("verifying the terminal pod was replaced with a fresh one")
				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())

				var found bool
				for _, p := range pods.Items {
					if p.Name == podName {
						By(fmt.Sprintf("pod %s phase should not be %s", p.Name, phase))
						Expect(p.Status.Phase).ToNot(Equal(phase), "terminal pod should have been replaced")
						found = true
					}
				}
				Expect(found).To(BeTrue(), "replacement pod should exist with name "+podName)

				By("simulating the kubelet transitioning the new pod to Running")
				freshPod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				freshPod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, freshPod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_ = strings.ToLower("unused") // ensure strings import is used
				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))
			})
		}
	})

	Describe("Run metrics", func() {
		var container runtime.Container

		Context("when pod creation succeeds (direct mode)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "metric-success-handle")

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("metric-success-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Drain any prior counter state.
				metric.Metrics.ContainersCreated.Delta()
				metric.Metrics.FailedContainers.Delta()
			})

			It("increments ContainersCreated", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				Expect(metric.Metrics.ContainersCreated.Delta()).To(Equal(float64(1)))
				Expect(metric.Metrics.FailedContainers.Delta()).To(Equal(float64(0)))
			})
		})

		Context("when pod creation succeeds (exec mode)", func() {
			var (
				execContainer runtime.Container
				execWorker    *jetbridge.Worker
			)

			BeforeEach(func() {
				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

				setupFakeDBContainer(fakeDBWorker, "metric-exec-success")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("metric-exec-success"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				metric.Metrics.ContainersCreated.Delta()
				metric.Metrics.FailedContainers.Delta()
			})

			It("increments ContainersCreated", func() {
				_, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				Expect(metric.Metrics.ContainersCreated.Delta()).To(Equal(float64(1)))
				Expect(metric.Metrics.FailedContainers.Delta()).To(Equal(float64(0)))
			})
		})

		Context("when pod creation fails", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "metric-fail-handle")

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("metric-fail-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Make pod creation fail by injecting a reactor.
				fakeClientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
					return true, nil, fmt.Errorf("simulated pod creation failure")
				})

				metric.Metrics.ContainersCreated.Delta()
				metric.Metrics.FailedContainers.Delta()
			})

			It("increments FailedContainers", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).To(HaveOccurred())

				Expect(metric.Metrics.FailedContainers.Delta()).To(Equal(float64(1)))
				Expect(metric.Metrics.ContainersCreated.Delta()).To(Equal(float64(0)))
			})
		})
	})

	Describe("Attach", func() {
		var container runtime.Container

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "attach-handle")

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
				// Store exit status in properties
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
				execWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

				setupFakeDBContainer(fakeDBWorker, "exec-attach-handle")

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
				execWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

				setupFakeDBContainer(fakeDBWorker, "exec-noann-handle")

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

	Describe("FindOrCreateContainer failure handling", func() {
		It("calls Failed() on the creating container when Created() fails", func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("fail-create-handle")
			fakeCreatingContainer.CreatedReturns(nil, fmt.Errorf("owner disappeared"))

			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

			_, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("fail-create-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mark container as created"))

			By("marking the container as failed in the DB")
			Expect(fakeCreatingContainer.FailedCallCount()).To(Equal(1))
		})

		It("returns error when FindContainer fails", func() {
			fakeDBWorker.FindContainerReturns(nil, nil, fmt.Errorf("db connection lost"))

			_, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("db-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("find container in db"))
		})

		It("returns error when CreateContainer fails", func() {
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(nil, fmt.Errorf("duplicate key"))

			_, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("dup-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("create container in db"))
		})

		It("recovers a stale creating container by transitioning to created", func() {
			// Simulate a creating container left from a previous crash.
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("stale-creating-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("stale-creating-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)

			// FindContainer returns the stale creating container.
			fakeDBWorker.FindContainerReturns(fakeCreatingContainer, nil, nil)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("stale-creating-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(container).ToNot(BeNil())

			By("transitioning the creating container to created state")
			Expect(fakeCreatingContainer.CreatedCallCount()).To(Equal(1))

			By("not calling CreateContainer since the container already exists")
			Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))
		})

		It("marks stale creating container as failed when Created() fails on recovery", func() {
			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("stale-fail-handle")
			fakeCreatingContainer.CreatedReturns(nil, fmt.Errorf("state conflict"))

			// FindContainer returns the stale creating container.
			fakeDBWorker.FindContainerReturns(fakeCreatingContainer, nil, nil)

			_, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("stale-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mark container as created"))

			By("marking the stale container as failed")
			Expect(fakeCreatingContainer.FailedCallCount()).To(Equal(1))
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



var _ = Describe("Concurrent container operations", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		ctx           context.Context
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		delegate = &noopDelegate{}
	})

	It("handles concurrent SetProperty and Properties without races", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")
		worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

		setupFakeDBContainer(fakeDBWorker, "concurrent-props-handle")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("concurrent-props-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		const goroutines = 20
		var wg sync.WaitGroup
		wg.Add(goroutines * 2)

		// Half goroutines set properties
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				key := fmt.Sprintf("key-%d", n)
				_ = container.SetProperty(key, fmt.Sprintf("value-%d", n))
			}(i)
		}

		// Half goroutines read properties
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				props, err := container.Properties()
				Expect(err).ToNot(HaveOccurred())
				// Properties returns a copy, so iterating it is safe
				for range props {
					// just iterate
				}
			}()
		}

		wg.Wait()

		// All properties should have been set
		props, err := container.Properties()
		Expect(err).ToNot(HaveOccurred())
		Expect(len(props)).To(BeNumerically(">=", goroutines))
	})

	It("creates independent containers concurrently without interference", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")

		const goroutines = 5
		var wg sync.WaitGroup
		wg.Add(goroutines)

		containers := make([]runtime.Container, goroutines)
		errs := make([]error, goroutines)

		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()

				// Each goroutine needs its own FakeWorker to avoid shared state
				localFakeDBWorker := new(dbfakes.FakeWorker)
				localFakeDBWorker.NameReturns("k8s-worker-1")
				setupFakeDBContainer(localFakeDBWorker, fmt.Sprintf("concurrent-handle-%d", n))

				localWorker := jetbridge.NewWorker(localFakeDBWorker, fakeClientset, cfg)

				containers[n], _, errs[n] = localWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner(fmt.Sprintf("concurrent-handle-%d", n)),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
			}(i)
		}

		wg.Wait()

		for i := 0; i < goroutines; i++ {
			Expect(errs[i]).ToNot(HaveOccurred(), "goroutine %d should succeed", i)
			Expect(containers[i]).ToNot(BeNil(), "goroutine %d should produce a container", i)
		}
	})

	It("handles concurrent Run and pod creation on the fake clientset", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")

		const goroutines = 5
		var wg sync.WaitGroup
		wg.Add(goroutines)

		runErrs := make([]error, goroutines)

		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()

				localFakeDBWorker := new(dbfakes.FakeWorker)
				localFakeDBWorker.NameReturns("k8s-worker-1")
				handle := fmt.Sprintf("concurrent-run-%d", n)
				setupFakeDBContainer(localFakeDBWorker, handle)

				localWorker := jetbridge.NewWorker(localFakeDBWorker, fakeClientset, cfg)
				container, _, err := localWorker.FindOrCreateContainer(
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
				if err != nil {
					runErrs[n] = err
					return
				}

				_, runErrs[n] = container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", fmt.Sprintf("echo %d", n)},
				}, runtime.ProcessIO{})
			}(i)
		}

		wg.Wait()

		for i := 0; i < goroutines; i++ {
			Expect(runErrs[i]).ToNot(HaveOccurred(), "goroutine %d Run should succeed", i)
		}

		// Verify all pods were created
		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pods.Items).To(HaveLen(goroutines))
	})

})

var _ = Describe("Run with sidecar containers", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		container     runtime.Container
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

	Context("when no sidecars are configured", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "no-sidecar-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("no-sidecar-handle"),
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

		It("creates a pod with only the main container", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			Expect(pod.Spec.Containers).To(HaveLen(1))
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
		})
	})

	Context("when one sidecar is configured", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "one-sidecar-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("one-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/my-repo"},
					},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "postgres",
							Image: "postgres:15",
							Env: []atc.SidecarEnvVar{
								{Name: "POSTGRES_PASSWORD", Value: "test"},
							},
							Ports: []atc.SidecarPort{
								{ContainerPort: 5432},
							},
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a pod with the main container and the sidecar", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			Expect(pod.Spec.Containers).To(HaveLen(2))

			By("placing the main container first")
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))

			By("adding the sidecar container")
			sidecar := pod.Spec.Containers[1]
			Expect(sidecar.Name).To(Equal("postgres"))
			Expect(sidecar.Image).To(Equal("postgres:15"))

			By("mapping sidecar env vars")
			Expect(sidecar.Env).To(ContainElement(corev1.EnvVar{Name: "POSTGRES_PASSWORD", Value: "test"}))

			By("mapping sidecar ports")
			Expect(sidecar.Ports).To(ContainElement(corev1.ContainerPort{ContainerPort: 5432, Protocol: corev1.ProtocolTCP}))

			By("applying non-privileged security context to the sidecar")
			Expect(sidecar.SecurityContext).ToNot(BeNil())
			Expect(sidecar.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
			Expect(*sidecar.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

			By("setting ImagePullPolicy to IfNotPresent")
			Expect(sidecar.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))

			By("giving the sidecar the same volume mounts as the main container")
			mainMounts := pod.Spec.Containers[0].VolumeMounts
			Expect(sidecar.VolumeMounts).To(Equal(mainMounts))

			By("applying pod-level security hardening even with sidecars")
			Expect(pod.Spec.AutomountServiceAccountToken).ToNot(BeNil())
			Expect(*pod.Spec.AutomountServiceAccountToken).To(BeFalse())
			Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		})
	})

	Context("when multiple sidecars are configured", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "multi-sidecar-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("multi-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "redis",
							Image: "redis:7",
							Ports: []atc.SidecarPort{{ContainerPort: 6379}},
						},
						{
							Name:    "nginx",
							Image:   "nginx:latest",
							Command: []string{"nginx", "-g", "daemon off;"},
							Ports:   []atc.SidecarPort{{ContainerPort: 80}},
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a pod with the main container and all sidecars", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(3))
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
			Expect(pod.Spec.Containers[1].Name).To(Equal("redis"))
			Expect(pod.Spec.Containers[2].Name).To(Equal("nginx"))
		})
	})

	Context("when a sidecar has resources, command, args, and workingDir", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-full-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-full-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:       "app",
							Image:      "myapp:latest",
							Command:    []string{"/usr/bin/app"},
							Args:       []string{"--port", "8080"},
							WorkingDir: "/app",
							Resources: &atc.SidecarResources{
								Requests: atc.SidecarResourceList{CPU: "100m", Memory: "128Mi"},
								Limits:   atc.SidecarResourceList{CPU: "500m", Memory: "512Mi"},
							},
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("maps all sidecar fields to the K8s container spec", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(2))
			sidecar := pod.Spec.Containers[1]

			By("mapping command and args")
			Expect(sidecar.Command).To(Equal([]string{"/usr/bin/app"}))
			Expect(sidecar.Args).To(Equal([]string{"--port", "8080"}))

			By("mapping workingDir")
			Expect(sidecar.WorkingDir).To(Equal("/app"))

			By("mapping resource requests")
			Expect(sidecar.Resources.Requests.Cpu().String()).To(Equal("100m"))
			Expect(sidecar.Resources.Requests.Memory().String()).To(Equal("128Mi"))

			By("mapping resource limits")
			Expect(sidecar.Resources.Limits.Cpu().String()).To(Equal("500m"))
			Expect(sidecar.Resources.Limits.Memory().String()).To(Equal("512Mi"))
		})
	})

	Context("when sidecars are configured alongside the artifact store", func() {
		var (
			artifactWorker *jetbridge.Worker
		)

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-artifact-handle")

			cfgWithArtifact := jetbridge.NewConfig("test-namespace", "")
			artifactWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithArtifact)

			var err error
			container, _, err = artifactWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-artifact-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/my-input"},
					},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "redis",
							Image: "redis:7",
							Ports: []atc.SidecarPort{{ContainerPort: 6379}},
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("includes main and user sidecar containers (no artifact-helper sidecar)", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			containerNames := []string{}
			for _, c := range pod.Spec.Containers {
				containerNames = append(containerNames, c.Name)
			}
			Expect(containerNames).To(Equal([]string{"main", "redis"}))

			By("user sidecar gets the same volume mounts as main")
			mainMounts := pod.Spec.Containers[0].VolumeMounts
			redisMounts := pod.Spec.Containers[1].VolumeMounts
			Expect(redisMounts).To(Equal(mainMounts))
		})
	})

	Context("when a sidecar has no workingDir and the main container has a dir", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-inherit-dir-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-inherit-dir-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{DestinationPath: "/tmp/build/workdir/my-input"},
					},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "helper",
							Image: "helper:latest",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("inherits the main container's working directory", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(2))
			main := pod.Spec.Containers[0]
			sidecar := pod.Spec.Containers[1]

			By("main container has the expected working dir")
			Expect(main.WorkingDir).To(Equal("/tmp/build/workdir"))

			By("sidecar inherits the same working dir")
			Expect(sidecar.WorkingDir).To(Equal("/tmp/build/workdir"))
		})
	})

	Context("when a sidecar specifies its own workingDir", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-own-dir-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-own-dir-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:       "app",
							Image:      "myapp:latest",
							WorkingDir: "/app",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("uses the sidecar's own workingDir instead of inheriting", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(2))
			sidecar := pod.Spec.Containers[1]

			Expect(sidecar.WorkingDir).To(Equal("/app"))
		})
	})

	Context("when sidecars are configured in exec-mode (pause pod)", func() {
		var (
			execWorker   *jetbridge.Worker
			fakeExecutor *fakeExecExecutor
		)

		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-exec-handle")

			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			container, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-exec-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "postgres",
							Image: "postgres:15",
							Ports: []atc.SidecarPort{{ContainerPort: 5432}},
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a pause pod with sidecar containers", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "npm test"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process).ToNot(BeNil())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("the main container runs the pause command (not the real command)")
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"}))

			By("the sidecar is present in the pod")
			Expect(pod.Spec.Containers).To(HaveLen(2))
			Expect(pod.Spec.Containers[1].Name).To(Equal("postgres"))
			Expect(pod.Spec.Containers[1].Image).To(Equal("postgres:15"))

			By("the sidecar shares volume mounts with main")
			Expect(pod.Spec.Containers[1].VolumeMounts).To(Equal(pod.Spec.Containers[0].VolumeMounts))
		})
	})

	Context("when a sidecar image has a docker:/// prefix (image_artifact handoff)", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-prefix-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-prefix-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "from-artifact",
							Image: "docker:///us-docker.pkg.dev/myproject/repo/myimage@sha256:abc123",
						},
						{
							Name:  "from-artifact-no-slash",
							Image: "docker://us-docker.pkg.dev/myproject/repo/other@sha256:def456",
						},
						{
							Name:  "raw-prefix",
							Image: "raw:///some-image:latest",
						},
						{
							Name:  "plain-ref",
							Image: "redis:7",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("strips Concourse URL prefixes from sidecar images in the pod spec", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			// main + 4 sidecars
			Expect(pod.Spec.Containers).To(HaveLen(5))

			By("stripping docker:/// prefix")
			Expect(pod.Spec.Containers[1].Name).To(Equal("from-artifact"))
			Expect(pod.Spec.Containers[1].Image).To(Equal("us-docker.pkg.dev/myproject/repo/myimage@sha256:abc123"))

			By("stripping docker:// prefix (two slashes)")
			Expect(pod.Spec.Containers[2].Name).To(Equal("from-artifact-no-slash"))
			Expect(pod.Spec.Containers[2].Image).To(Equal("us-docker.pkg.dev/myproject/repo/other@sha256:def456"))

			By("stripping raw:/// prefix")
			Expect(pod.Spec.Containers[3].Name).To(Equal("raw-prefix"))
			Expect(pod.Spec.Containers[3].Image).To(Equal("some-image:latest"))

			By("leaving plain image references unchanged")
			Expect(pod.Spec.Containers[4].Name).To(Equal("plain-ref"))
			Expect(pod.Spec.Containers[4].Image).To(Equal("redis:7"))
		})
	})
})

// ---------------------------------------------------------------
// End-to-end pipeline integration scenarios
// ---------------------------------------------------------------
