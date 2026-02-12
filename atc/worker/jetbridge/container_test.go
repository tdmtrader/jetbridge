package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

	Describe("Run with cache PVC configured", func() {
		var container runtime.Container

		Context("when CacheVolumeClaim is set (no caches in spec)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "cache-pvc-handle")

				cfgWithCachePVC := jetbridge.NewConfig("test-namespace", "")
				cfgWithCachePVC.CacheVolumeClaim = "concourse-cache"

				cachePVCWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithCachePVC)

				var err error
				container, _, err = cachePVCWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("cache-pvc-handle"),
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

			It("includes a PVC volume and mount at CacheBasePath in the pod spec", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]

				By("adding a PersistentVolumeClaim volume")
				var pvcVol *corev1.Volume
				for i := range pod.Spec.Volumes {
					if pod.Spec.Volumes[i].PersistentVolumeClaim != nil {
						pvcVol = &pod.Spec.Volumes[i]
						break
					}
				}
				Expect(pvcVol).ToNot(BeNil(), "expected a PVC volume in pod spec")
				Expect(pvcVol.PersistentVolumeClaim.ClaimName).To(Equal("concourse-cache"))

				By("mounting the PVC at CacheBasePath")
				mainContainer := pod.Spec.Containers[0]
				var cacheMount *corev1.VolumeMount
				for i := range mainContainer.VolumeMounts {
					if mainContainer.VolumeMounts[i].MountPath == jetbridge.CacheBasePath {
						cacheMount = &mainContainer.VolumeMounts[i]
						break
					}
				}
				Expect(cacheMount).ToNot(BeNil(), "expected a volume mount at CacheBasePath")
				Expect(cacheMount.Name).To(Equal(pvcVol.Name))
			})
		})

		Context("when CacheVolumeClaim is set with task caches", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "cache-subpath-handle")

				cfgWithCachePVC := jetbridge.NewConfig("test-namespace", "")
				cfgWithCachePVC.CacheVolumeClaim = "concourse-cache"

				cachePVCWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithCachePVC)

				var err error
				container, _, err = cachePVCWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("cache-subpath-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:   []string{"/tmp/build/workdir/.cache", "/tmp/build/workdir/.npm"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("mounts cache paths using PVC subPath instead of emptyDir", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("not creating emptyDir volumes for caches")
				for _, vol := range pod.Spec.Volumes {
					if vol.EmptyDir != nil {
						Expect(vol.Name).ToNot(HavePrefix("cache-"), "cache volumes should not use emptyDir when PVC is configured")
					}
				}

				By("mounting cache paths with subPath on the PVC")
				mainContainer := pod.Spec.Containers[0]
				var cacheMounts []corev1.VolumeMount
				for _, m := range mainContainer.VolumeMounts {
					if m.SubPath != "" {
						cacheMounts = append(cacheMounts, m)
					}
				}
				Expect(cacheMounts).To(HaveLen(2))

				mountPaths := []string{cacheMounts[0].MountPath, cacheMounts[1].MountPath}
				Expect(mountPaths).To(ConsistOf("/tmp/build/workdir/.cache", "/tmp/build/workdir/.npm"))

				for _, m := range cacheMounts {
					Expect(m.SubPath).ToNot(BeEmpty(), "subPath should be set for cache mounts")
					Expect(m.Name).To(Equal("cache-pvc"), "cache mounts should reference the PVC volume")
				}
			})
		})

		Context("when CacheVolumeClaim is set with inputs and outputs", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "pvc-io-handle")

				cfgWithCachePVC := jetbridge.NewConfig("test-namespace", "")
				cfgWithCachePVC.CacheVolumeClaim = "concourse-cache"

				cachePVCWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfgWithCachePVC)

				var err error
				container, _, err = cachePVCWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("pvc-io-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{DestinationPath: "/tmp/build/workdir/my-input"},
						},
						Outputs: runtime.OutputPaths{
							"result": "/tmp/build/workdir/result",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("inputs and outputs remain emptyDir even when PVC is configured", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				emptyDirCount := 0
				for _, vol := range pod.Spec.Volumes {
					if vol.EmptyDir != nil {
						emptyDirCount++
					}
				}
				Expect(emptyDirCount).To(Equal(3), "dir, inputs, and outputs should still use emptyDir")
			})
		})

		Context("when CacheVolumeClaim is not set", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "no-cache-pvc-handle")

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("no-cache-pvc-handle"),
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

			It("does not include a PVC volume in the pod spec", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				pod := pods.Items[0]
				for _, vol := range pod.Spec.Volumes {
					Expect(vol.PersistentVolumeClaim).To(BeNil(), "no PVC volumes expected when CacheVolumeClaim is empty")
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

	Describe("Input streaming before exec", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorkerIS  *jetbridge.Worker
		)

		BeforeEach(func() {
			execExecutor = &fakeExecExecutor{}
			execWorkerIS = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
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
				setupFakeDBContainer(fakeDBWorker, "stream-input-handle")

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
				setupFakeDBContainer(fakeDBWorker, "nil-artifact-handle")

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
				setupFakeDBContainer(fakeDBWorker, "multi-input-handle")

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

var _ = Describe("Container with artifact store", func() {
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

	Describe("Run with ArtifactStoreClaim configured", func() {
		var (
			worker    *jetbridge.Worker
			container runtime.Container
		)

		BeforeEach(func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		})

		Context("with no inputs (basic pod)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "artifact-basic-handle")

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-basic-handle"),
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

			It("includes the artifact PVC volume and sidecar but no init containers", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))
				pod := pods.Items[0]

				By("adding the artifact store PVC volume")
				var artifactVol *corev1.Volume
				for i := range pod.Spec.Volumes {
					if pod.Spec.Volumes[i].PersistentVolumeClaim != nil &&
						pod.Spec.Volumes[i].PersistentVolumeClaim.ClaimName == "concourse-artifacts" {
						artifactVol = &pod.Spec.Volumes[i]
						break
					}
				}
				Expect(artifactVol).ToNot(BeNil(), "expected artifact store PVC volume")
				Expect(artifactVol.Name).To(Equal("artifact-store"))

				By("adding the artifact-helper sidecar")
				Expect(pod.Spec.Containers).To(HaveLen(2))
				sidecar := pod.Spec.Containers[1]
				Expect(sidecar.Name).To(Equal("artifact-helper"))
				Expect(sidecar.Image).To(Equal("alpine:latest"))

				By("sidecar mounts the artifact PVC")
				var hasPVCMount bool
				for _, m := range sidecar.VolumeMounts {
					if m.MountPath == jetbridge.ArtifactMountPath {
						hasPVCMount = true
						break
					}
				}
				Expect(hasPVCMount).To(BeTrue(), "sidecar should mount artifact PVC")

				By("main container does NOT mount the artifact PVC")
				mainContainer := pod.Spec.Containers[0]
				for _, m := range mainContainer.VolumeMounts {
					Expect(m.MountPath).ToNot(Equal(jetbridge.ArtifactMountPath),
						"main container should NOT mount artifact PVC")
				}

				By("no init containers when no artifact-store inputs")
				Expect(pod.Spec.InitContainers).To(BeEmpty())
			})
		})

		Context("with ArtifactStoreVolume inputs", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "artifact-init-handle")

				asv := jetbridge.NewArtifactStoreVolume(
					"caches/123.tar", "cache-vol-123", "k8s-worker-1", nil,
				)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-init-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        asv,
								DestinationPath: "/tmp/build/workdir/my-input",
							},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates init containers to extract artifacts from the PVC", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "ls /tmp/build/workdir/my-input"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("having an init container for the artifact-store input")
				Expect(pod.Spec.InitContainers).To(HaveLen(1))
				initC := pod.Spec.InitContainers[0]
				Expect(initC.Name).To(Equal("fetch-input-0"))
				Expect(initC.Image).To(Equal("alpine:latest"))

				By("init container command extracts tar from PVC to emptyDir without || true")
				Expect(initC.Command).To(Equal([]string{
					"sh", "-c",
					"tar xf /artifacts/artifacts/cache-vol-123.tar -C /tmp/build/workdir/my-input",
				}))

				By("init container mounts both artifact PVC and the input emptyDir")
				Expect(initC.VolumeMounts).To(HaveLen(2))
				var mountPaths []string
				for _, m := range initC.VolumeMounts {
					mountPaths = append(mountPaths, m.MountPath)
				}
				Expect(mountPaths).To(ConsistOf(
					jetbridge.ArtifactMountPath,
					"/tmp/build/workdir/my-input",
				))

				By("init container has SecurityContext with AllowPrivilegeEscalation=false")
				Expect(initC.SecurityContext).ToNot(BeNil())
				Expect(initC.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
				Expect(*initC.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

				By("init container command does NOT contain || true")
				Expect(initC.Command[2]).ToNot(ContainSubstring("|| true"))
			})
		})

		Context("with mixed inputs (ArtifactStoreVolume and regular)", func() {
			BeforeEach(func() {
				setupFakeDBContainer(fakeDBWorker, "artifact-mixed-handle")

				asv := jetbridge.NewArtifactStoreVolume(
					"caches/42.tar", "cache-vol-42", "k8s-worker-1", nil,
				)
				regular := &fakeArtifact{
					handle:    "regular-art",
					source:    "k8s-worker-1",
					streamOut: []byte("regular-data"),
				}

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-mixed-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        asv,
								DestinationPath: "/tmp/build/workdir/cached-input",
							},
							{
								Artifact:        regular,
								DestinationPath: "/tmp/build/workdir/streamed-input",
							},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates init containers for both artifact-store and regular inputs", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo done"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				By("init containers for both inputs when artifact store is configured")
				Expect(pod.Spec.InitContainers).To(HaveLen(2))
				Expect(pod.Spec.InitContainers[0].Name).To(Equal("fetch-input-0"))
				Expect(pod.Spec.InitContainers[0].Command[2]).To(ContainSubstring("cache-vol-42.tar"))
				Expect(pod.Spec.InitContainers[1].Name).To(Equal("fetch-input-1"))
				Expect(pod.Spec.InitContainers[1].Command[2]).To(ContainSubstring("regular-art.tar"))
			})
		})

		Context("with custom ArtifactHelperImage", func() {
			BeforeEach(func() {
				cfg := jetbridge.NewConfig("test-namespace", "")
				cfg.ArtifactStoreClaim = "concourse-artifacts"
				cfg.ArtifactHelperImage = "my-registry/helper:v1"
				worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

				setupFakeDBContainer(fakeDBWorker, "artifact-custom-img")

				asv := jetbridge.NewArtifactStoreVolume(
					"caches/1.tar", "cv-1", "k8s-worker-1", nil,
				)

				var err error
				container, _, err = worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-custom-img"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{Artifact: asv, DestinationPath: "/workdir/input"},
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uses the custom image for init containers and sidecar", func() {
				_, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh", Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod := pods.Items[0]

				Expect(pod.Spec.InitContainers).To(HaveLen(1))
				Expect(pod.Spec.InitContainers[0].Image).To(Equal("my-registry/helper:v1"))

				sidecar := pod.Spec.Containers[1]
				Expect(sidecar.Image).To(Equal("my-registry/helper:v1"))
			})
		})
	})

	Describe("GCS Fuse pod annotation", func() {
		It("adds gke-gcsfuse/volumes annotation when ArtifactStoreGCSFuse is true", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			cfg.ArtifactStoreGCSFuse = true
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "gcsfuse-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("gcsfuse-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"))
		})

		It("does not add gke-gcsfuse/volumes annotation when ArtifactStoreGCSFuse is false", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			cfg.ArtifactStoreGCSFuse = false
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "no-gcsfuse-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("no-gcsfuse-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"))
		})

		It("does not add gke-gcsfuse/volumes annotation when no artifact store claim", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreGCSFuse = true // flag set but no claim
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "no-claim-gcsfuse-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("no-claim-gcsfuse-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"))
		})
	})

	Describe("Slim check pods — skip artifact-helper and GCS FUSE for check steps", func() {
		It("does NOT include artifact-helper sidecar for check step containers", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "check-no-sidecar-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("check-no-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeCheck},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/registry-image-resource"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/check",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			pod := pods.Items[0]

			By("check pod has only the main container (no artifact-helper)")
			Expect(pod.Spec.Containers).To(HaveLen(1))
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
		})

		It("still includes artifact-helper sidecar for task step containers", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "task-with-sidecar-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-with-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(2))
			Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
		})

		It("still includes artifact-helper sidecar for get step containers", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "get-with-sidecar-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("get-with-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/git-resource"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in", Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Spec.Containers).To(HaveLen(2))
			Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
		})

		It("does NOT add gke-gcsfuse/volumes annotation for check step pods", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			cfg.ArtifactStoreGCSFuse = true
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "check-no-gcsfuse-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("check-no-gcsfuse-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeCheck},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/registry-image-resource"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/check",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"))
		})

		It("still adds gke-gcsfuse/volumes annotation for task step pods", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			cfg.ArtifactStoreGCSFuse = true
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "task-gcsfuse-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-gcsfuse-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"))
		})
	})

	Describe("Run without ArtifactStoreClaim", func() {
		It("does not include artifact PVC, init containers, or sidecar", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "no-artifact-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("no-artifact-handle"),
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
				Path: "/bin/sh", Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("no artifact PVC volume")
			for _, vol := range pod.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil {
					Expect(vol.PersistentVolumeClaim.ClaimName).ToNot(Equal("concourse-artifacts"))
				}
			}

			By("only main container, no sidecar")
			Expect(pod.Spec.Containers).To(HaveLen(1))
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))

			By("no init containers")
			Expect(pod.Spec.InitContainers).To(BeEmpty())
		})
	})

	Describe("streamInputs skips ALL inputs when artifact store configured", func() {
		It("skips all inputs (both regular and artifact-store) when ArtifactStoreClaim is set", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"

			fakeExecutor := &fakeExecExecutor{}
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			worker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "skip-stream-handle")

			asv := jetbridge.NewArtifactStoreVolume(
				"caches/99.tar", "cache-vol-99", "k8s-worker-1", nil,
			)
			regular := &fakeArtifact{
				handle:    "regular-art",
				source:    "k8s-worker-1",
				streamOut: []byte("regular-data"),
			}

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("skip-stream-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{
							Artifact:        asv,
							DestinationPath: "/tmp/build/workdir/cached-input",
						},
						{
							Artifact:        regular,
							DestinationPath: "/tmp/build/workdir/streamed-input",
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

			// Simulate pod running
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "skip-stream-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("no inputs are streamed via SPDY — ALL are handled by init containers")
			var streamInCalls []execCall
			for _, c := range fakeExecutor.execCalls {
				if len(c.command) > 0 && c.command[0] == "tar" && len(c.command) > 1 && c.command[1] == "xf" {
					streamInCalls = append(streamInCalls, c)
				}
			}
			Expect(streamInCalls).To(BeEmpty(), "no inputs should be streamed when artifact store is configured")
		})
	})

	Describe("uploadOutputsToArtifactStore", func() {
		It("execs tar commands in the artifact-helper sidecar for each output volume", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"

			fakeExecutor := &fakeExecExecutor{}
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			worker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "upload-output-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("upload-output-handle"),
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

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod running
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "upload-output-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("finding tar cf calls targeting the artifact-helper sidecar")
			var uploadCalls []execCall
			for _, c := range fakeExecutor.execCalls {
				if c.containerName == "artifact-helper" {
					uploadCalls = append(uploadCalls, c)
				}
			}
			Expect(uploadCalls).ToNot(BeEmpty(), "should have upload calls to artifact-helper sidecar")

			By("upload commands create tars on the artifact PVC")
			for _, c := range uploadCalls {
				Expect(c.command[2]).To(ContainSubstring("/artifacts/artifacts/"))
				Expect(c.command[2]).To(ContainSubstring(".tar"))
			}
		})

		It("fails the build when artifact upload fails", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"

			fakeExecutor := &fakeExecExecutor{}
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			worker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "upload-fail-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("upload-fail-handle"),
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

			// Make artifact upload fail
			callCount := 0
			fakeExecutor.execFunc = func() error {
				callCount++
				// First call is the command exec, subsequent calls are artifact uploads
				if callCount > 1 {
					return fmt.Errorf("disk full")
				}
				return nil
			}

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod running
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "upload-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("uploading artifacts"))
		})
	})

	Describe("artifact-helper sidecar hardening", func() {
		It("has resource limits and security context on the sidecar", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "sidecar-hardening-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-hardening-handle"),
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
			pod := pods.Items[0]

			By("finding the artifact-helper sidecar")
			Expect(pod.Spec.Containers).To(HaveLen(2))
			sidecar := pod.Spec.Containers[1]
			Expect(sidecar.Name).To(Equal("artifact-helper"))

			By("sidecar has SecurityContext with AllowPrivilegeEscalation=false")
			Expect(sidecar.SecurityContext).ToNot(BeNil())
			Expect(sidecar.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
			Expect(*sidecar.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

			By("sidecar has resource requests")
			Expect(sidecar.Resources.Requests).ToNot(BeNil())
			Expect(sidecar.Resources.Requests.Cpu().Cmp(resource.MustParse("50m"))).To(Equal(0))
			Expect(sidecar.Resources.Requests.Memory().Cmp(resource.MustParse("64Mi"))).To(Equal(0))

			By("sidecar has resource limits")
			Expect(sidecar.Resources.Limits).ToNot(BeNil())
			Expect(sidecar.Resources.Limits.Cpu().Cmp(resource.MustParse("200m"))).To(Equal(0))
			Expect(sidecar.Resources.Limits.Memory().Cmp(resource.MustParse("256Mi"))).To(Equal(0))
		})
	})

	Describe("init containers for regular Volume inputs", func() {
		It("creates init containers for regular artifact inputs when artifact store is configured", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactStoreClaim = "concourse-artifacts"
			worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

			setupFakeDBContainer(fakeDBWorker, "regular-init-handle")

			regular := &fakeArtifact{
				handle:    "source-vol-abc",
				source:    "k8s-worker-1",
				streamOut: []byte("some-data"),
			}

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("regular-init-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs: []runtime.Input{
						{
							Artifact:        regular,
							DestinationPath: "/tmp/build/workdir/my-input",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo done"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("creating an init container for the regular artifact input")
			Expect(pod.Spec.InitContainers).To(HaveLen(1))
			initC := pod.Spec.InitContainers[0]
			Expect(initC.Name).To(Equal("fetch-input-0"))

			By("using ArtifactKey derived from artifact Handle()")
			Expect(initC.Command[2]).To(ContainSubstring("artifacts/source-vol-abc.tar"))
			Expect(initC.Command[2]).ToNot(ContainSubstring("|| true"))
		})
	})
})

// failingArtifact is a test double for runtime.Artifact that returns an
// error on StreamOut, simulating a broken upstream artifact.
type failingArtifact struct {
	handle    string
	source    string
	streamErr error
}

func (a *failingArtifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return nil, a.streamErr
}
func (a *failingArtifact) Handle() string { return a.handle }
func (a *failingArtifact) Source() string { return a.source }

var _ = Describe("streamInputs failure paths (non-artifact-store)", func() {
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

	It("fails the build when StreamOut returns an error on an input artifact", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")
		// No ArtifactStoreClaim — uses SPDY streaming path

		fakeExecutor := &fakeExecExecutor{}
		worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)

		setupFakeDBContainer(fakeDBWorker, "streamout-fail-handle")

		brokenArtifact := &failingArtifact{
			handle:    "broken-vol",
			source:    "k8s-worker-1",
			streamErr: errors.New("upstream pod terminated"),
		}

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("streamout-fail-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Inputs: []runtime.Input{
					{
						Artifact:        brokenArtifact,
						DestinationPath: "/tmp/build/workdir/my-input",
					},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo should-not-run"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		// Simulate pod running so waitForRunning succeeds
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "streamout-fail-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("streaming inputs"))
		Expect(err.Error()).To(ContainSubstring("stream out artifact"))
		Expect(err.Error()).To(ContainSubstring("upstream pod terminated"))

		By("the main command exec should NOT have been called")
		for _, c := range fakeExecutor.execCalls {
			Expect(c.containerName).ToNot(Equal("main"),
				"main command should not exec when input streaming fails")
		}
	})

	It("fails the build when StreamIn (tar extract) returns an error", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")
		// No ArtifactStoreClaim — uses SPDY streaming path

		fakeExecutor := &fakeExecExecutor{
			execErr: errors.New("container not running: tar extract failed"),
		}
		worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)

		setupFakeDBContainer(fakeDBWorker, "streamin-fail-handle")

		goodArtifact := &fakeArtifact{
			handle:    "good-vol",
			source:    "k8s-worker-1",
			streamOut: []byte("valid-tar-data"),
		}

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("streamin-fail-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Inputs: []runtime.Input{
					{
						Artifact:        goodArtifact,
						DestinationPath: "/tmp/build/workdir/my-input",
					},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo should-not-run"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		// Simulate pod running
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "streamin-fail-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("streaming inputs"))
		Expect(err.Error()).To(ContainSubstring("stream in to"))
		Expect(err.Error()).To(ContainSubstring("tar extract failed"))
	})

	It("processes multiple inputs and fails on the first broken one", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")

		fakeExecutor := &fakeExecExecutor{}
		worker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)

		setupFakeDBContainer(fakeDBWorker, "multi-input-fail-handle")

		goodArtifact := &fakeArtifact{
			handle:    "good-vol-1",
			source:    "k8s-worker-1",
			streamOut: []byte("good-data"),
		}
		brokenArtifact := &failingArtifact{
			handle:    "broken-vol-2",
			source:    "k8s-worker-1",
			streamErr: errors.New("connection reset"),
		}

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("multi-input-fail-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Inputs: []runtime.Input{
					{
						Artifact:        goodArtifact,
						DestinationPath: "/tmp/build/workdir/input-a",
					},
					{
						Artifact:        brokenArtifact,
						DestinationPath: "/tmp/build/workdir/input-b",
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

		// Simulate pod running
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "multi-input-fail-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("broken-vol-2"))
		Expect(err.Error()).To(ContainSubstring("connection reset"))
	})
})

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
			cfgWithArtifact.ArtifactStoreClaim = "concourse-artifacts"
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

		It("includes main, artifact-helper, and user sidecar containers", func() {
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
			Expect(containerNames).To(Equal([]string{"main", "artifact-helper", "redis"}))

			By("user sidecar gets the same volume mounts as main (not the artifact PVC)")
			mainMounts := pod.Spec.Containers[0].VolumeMounts
			redisMounts := pod.Spec.Containers[2].VolumeMounts
			Expect(redisMounts).To(Equal(mainMounts))
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
})

// ---------------------------------------------------------------
// End-to-end pipeline integration scenarios
// ---------------------------------------------------------------
// These tests simulate realistic Concourse pipeline pod shapes
// across all step types, custom resource types, operator image
// overrides, artifact store configurations, and GCS FUSE settings.
// ---------------------------------------------------------------

var _ = Describe("Pipeline integration scenarios", func() {
	var (
		fakeDBWorker   *dbfakes.FakeWorker
		fakeClientset  *fake.Clientset
		ctx            context.Context
		delegate       runtime.BuildStepDelegate
		pipelineWorker *jetbridge.Worker
		pipelineCfg    jetbridge.Config
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		delegate = &noopDelegate{}

		// Realistic production config: artifact store, GCS FUSE, custom
		// artifact-helper image, and image pull secrets.
		pipelineCfg = jetbridge.NewConfig("pipeline-ns", "")
		pipelineCfg.ArtifactStoreClaim = "concourse-artifacts"
		pipelineCfg.ArtifactStoreGCSFuse = true
		pipelineCfg.ArtifactHelperImage = "gcr.io/my-project/artifact-helper:v2"
		pipelineCfg.ImagePullSecrets = []string{"registry-creds"}
		pipelineCfg.ServiceAccount = "concourse-worker"
		pipelineWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, pipelineCfg)
	})

	// helper: create container, run, return the pod.
	// Pod names are generated by GeneratePodName, so we find the pod
	// via the concourse.ci/handle label which always matches the handle.
	runPod := func(handle string, meta db.ContainerMetadata, spec runtime.ContainerSpec, proc runtime.ProcessSpec) corev1.Pod {
		setupFakeDBContainer(fakeDBWorker, handle)
		container, _, err := pipelineWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			meta,
			spec,
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
		_, err = container.Run(ctx, proc, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("pipeline-ns").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Find our pod by the handle label (pod name is generated)
		for _, p := range pods.Items {
			if p.Labels["concourse.ci/handle"] == handle {
				return p
			}
		}
		Fail("pod with handle " + handle + " not found")
		return corev1.Pod{}
	}

		Describe("check step pods are slim", func() {
			It("has only 1 container, no PVC, no GCS FUSE annotation", func() {
				pod := runPod(
					"chk-registry-image-001",
					db.ContainerMetadata{
						Type:         db.ContainerTypeCheck,
						StepName:     "registry-image",
						PipelineName: "main",
						JobName:      "",
						BuildName:    "",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/check",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/registry-image-resource"},
					},
					runtime.ProcessSpec{Path: "/opt/resource/check"},
				)

				By("exactly 1 container — no artifact-helper, no GCS FUSE sidecar")
				Expect(pod.Spec.Containers).To(HaveLen(1))
				Expect(pod.Spec.Containers[0].Name).To(Equal("main"))

				By("no artifact store PVC volume")
				for _, v := range pod.Spec.Volumes {
					Expect(v.PersistentVolumeClaim).To(BeNil(),
						"check pod should not have PVC volume %s", v.Name)
				}

				By("no GCS FUSE annotation")
				Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"))

				By("no init containers")
				Expect(pod.Spec.InitContainers).To(BeEmpty())

				By("image pull secrets and service account are still set")
				Expect(pod.Spec.ImagePullSecrets).To(ContainElement(
					corev1.LocalObjectReference{Name: "registry-creds"},
				))
				Expect(pod.Spec.ServiceAccountName).To(Equal("concourse-worker"))
			})

			It("resolves base resource type image via ResourceTypeImages mapping", func() {
				pod := runPod(
					"chk-git-resource-002",
					db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-repo"},
					runtime.ContainerSpec{
						TeamID:    1,
						ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					},
					runtime.ProcessSpec{Path: "/opt/resource/check"},
				)

				Expect(pod.Spec.Containers[0].Image).To(Equal("concourse/git-resource"))
				Expect(pod.Spec.Containers).To(HaveLen(1))
			})
		})

		Describe("get step pods have artifact-helper", func() {
			It("has main + artifact-helper, PVC volume, and GCS FUSE annotation", func() {
				pod := runPod(
					"get-my-repo-003",
					db.ContainerMetadata{
						Type:         db.ContainerTypeGet,
						StepName:     "my-repo",
						PipelineName: "main",
						JobName:      "build",
						BuildName:    "42",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/get",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/git-resource"},
						Outputs:   runtime.OutputPaths{"my-repo": "/tmp/build/get/my-repo"},
					},
					runtime.ProcessSpec{
						Path: "/opt/resource/in",
						Args: []string{"/tmp/build/get/my-repo"},
					},
				)

				By("2 containers: main + artifact-helper")
				Expect(pod.Spec.Containers).To(HaveLen(2))
				Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))

				By("artifact-helper uses custom image")
				Expect(pod.Spec.Containers[1].Image).To(Equal("gcr.io/my-project/artifact-helper:v2"))

				By("artifact store PVC volume is present")
				var foundPVC bool
				for _, v := range pod.Spec.Volumes {
					if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "concourse-artifacts" {
						foundPVC = true
					}
				}
				Expect(foundPVC).To(BeTrue(), "get pod should have artifact PVC volume")

				By("artifact-helper mounts the PVC")
				var sidecarHasPVC bool
				for _, m := range pod.Spec.Containers[1].VolumeMounts {
					if m.MountPath == jetbridge.ArtifactMountPath {
						sidecarHasPVC = true
					}
				}
				Expect(sidecarHasPVC).To(BeTrue())

				By("GCS FUSE annotation is present")
				Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"))

				By("output emptyDir volume is present")
				var foundOutput bool
				for _, m := range pod.Spec.Containers[0].VolumeMounts {
					if m.MountPath == "/tmp/build/get/my-repo" {
						foundOutput = true
					}
				}
				Expect(foundOutput).To(BeTrue(), "get pod should have output volume mount")
			})
		})

		Describe("put step pods have artifact-helper", func() {
			It("has main + artifact-helper, PVC volume, and GCS FUSE annotation", func() {
				pod := runPod(
					"put-deploy-004",
					db.ContainerMetadata{
						Type:         db.ContainerTypePut,
						StepName:     "deploy",
						PipelineName: "main",
						JobName:      "ship-it",
						BuildName:    "7",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/put",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/s3-resource"},
					},
					runtime.ProcessSpec{
						Path: "/opt/resource/out",
						Args: []string{"/tmp/build/put"},
					},
				)

				Expect(pod.Spec.Containers).To(HaveLen(2))
				Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
				Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"))
			})
		})

		Describe("task step pods have artifact-helper", func() {
			It("has main + artifact-helper with outputs and correct working directory", func() {
				pod := runPod(
					"task-build-005",
					db.ContainerMetadata{
						Type:         db.ContainerTypeTask,
						StepName:     "build",
						PipelineName: "main",
						JobName:      "build-and-test",
						BuildName:    "99",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///golang:1.22"},
						Outputs:   runtime.OutputPaths{"binary": "/tmp/build/workdir/binary"},
						Env:       []string{"GOOS=linux", "GOARCH=amd64"},
					},
					runtime.ProcessSpec{
						Path: "/bin/sh",
						Args: []string{"-c", "go build -o binary/ ./..."},
					},
				)

				By("2 containers: main + artifact-helper")
				Expect(pod.Spec.Containers).To(HaveLen(2))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))

				By("main container has correct image, workdir, and env")
				main := pod.Spec.Containers[0]
				Expect(main.Image).To(Equal("golang:1.22"))
				Expect(main.WorkingDir).To(Equal("/tmp/build/workdir"))
				Expect(main.Env).To(ContainElements(
					corev1.EnvVar{Name: "GOOS", Value: "linux"},
					corev1.EnvVar{Name: "GOARCH", Value: "amd64"},
				))

				By("output emptyDir volume is mounted in main")
				var mainHasOutput bool
				for _, m := range main.VolumeMounts {
					if m.MountPath == "/tmp/build/workdir/binary" {
						mainHasOutput = true
					}
				}
				Expect(mainHasOutput).To(BeTrue())

				By("artifact-helper also mounts the output volume for tar upload")
				sidecar := pod.Spec.Containers[1]
				var sidecarHasOutput bool
				for _, m := range sidecar.VolumeMounts {
					if m.MountPath == "/tmp/build/workdir/binary" {
						sidecarHasOutput = true
					}
				}
				Expect(sidecarHasOutput).To(BeTrue())

				By("GCS FUSE annotation is present")
				Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"))
			})

			It("supports user-defined sidecars alongside artifact-helper", func() {
				pod := runPod(
					"task-with-sidecar-006",
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "integration-test"},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///node:20"},
						Sidecars: []atc.SidecarConfig{
							{Name: "postgres", Image: "postgres:16", Env: []atc.SidecarEnvVar{{Name: "POSTGRES_PASSWORD", Value: "test"}}},
							{Name: "redis", Image: "redis:7"},
						},
					},
					runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "npm test"}},
				)

				By("4 containers: main + artifact-helper + 2 user sidecars")
				Expect(pod.Spec.Containers).To(HaveLen(4))
				Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
				Expect(pod.Spec.Containers[2].Name).To(Equal("postgres"))
				Expect(pod.Spec.Containers[3].Name).To(Equal("redis"))
			})
		})

		Describe("custom resource type image resolution", func() {
			It("resolves default resource type names to Docker images", func() {
				types := map[string]struct {
					resourceType string
					expectedImg  string
				}{
					"git":            {resourceType: "git", expectedImg: "concourse/git-resource"},
					"s3":             {resourceType: "s3", expectedImg: "concourse/s3-resource"},
					"registry-image": {resourceType: "registry-image", expectedImg: "concourse/registry-image-resource"},
					"time":           {resourceType: "time", expectedImg: "concourse/time-resource"},
					"semver":         {resourceType: "semver", expectedImg: "concourse/semver-resource"},
					"mock":           {resourceType: "mock", expectedImg: "concourse/mock-resource"},
				}

				for name, tc := range types {
					handle := fmt.Sprintf("chk-resolve-%s", name)
					pod := runPod(
						handle,
						db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: name},
						runtime.ContainerSpec{
							TeamID:    1,
							ImageSpec: runtime.ImageSpec{ResourceType: tc.resourceType},
						},
						runtime.ProcessSpec{Path: "/opt/resource/check"},
					)

					Expect(pod.Spec.Containers[0].Image).To(Equal(tc.expectedImg),
						"resource type %q should resolve to %q", name, tc.expectedImg)
				}
			})

			It("uses operator-overridden resource type images", func() {
				customCfg := jetbridge.NewConfig("pipeline-ns", "")
				customCfg.ArtifactStoreClaim = "concourse-artifacts"
				customCfg.ResourceTypeImages = map[string]string{
					"git":            "my-registry.io/custom-git:v3",
					"registry-image": "my-registry.io/custom-registry-image:v2",
					"time":           "concourse/time-resource", // keep default
				}
				customWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, customCfg)

				// Check with custom git image
				setupFakeDBContainer(fakeDBWorker, "chk-custom-git-007")
				container, _, err := customWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("chk-custom-git-007"),
					db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-repo"},
					runtime.ContainerSpec{
						TeamID:    1,
						ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
				_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/opt/resource/check"}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("pipeline-ns").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())

				var gitPod *corev1.Pod
				for i := range pods.Items {
					if pods.Items[i].Labels["concourse.ci/handle"] == "chk-custom-git-007" {
						gitPod = &pods.Items[i]
					}
				}
				Expect(gitPod).ToNot(BeNil(), "expected pod with handle chk-custom-git-007")
				Expect(gitPod.Spec.Containers[0].Image).To(Equal("my-registry.io/custom-git:v3"))
			})

			It("resolves custom pipeline type name via ResourceTypeImages mapping", func() {
				customCfg := jetbridge.NewConfig("pipeline-ns", "")
				customCfg.ArtifactStoreClaim = "concourse-artifacts"
				customCfg.ResourceTypeImages = map[string]string{
					"git":          "concourse/git-resource",
					"git-with-ado": "registry.home/git-with-ado-resource:latest",
				}
				customWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, customCfg)

				setupFakeDBContainer(fakeDBWorker, "chk-custom-pipeline-type-010")
				container, _, err := customWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("chk-custom-pipeline-type-010"),
					db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "git-with-ado"},
					runtime.ContainerSpec{
						TeamID: 1,
						ImageSpec: runtime.ImageSpec{
							ResourceType: "git-with-ado",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
				_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/opt/resource/check"}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("pipeline-ns").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())

				var customPod *corev1.Pod
				for i := range pods.Items {
					if pods.Items[i].Labels["concourse.ci/handle"] == "chk-custom-pipeline-type-010" {
						customPod = &pods.Items[i]
					}
				}
				Expect(customPod).ToNot(BeNil(), "expected pod with handle chk-custom-pipeline-type-010")
				Expect(customPod.Spec.Containers[0].Image).To(Equal("registry.home/git-with-ado-resource:latest"))
			})

			It("resolves docker:// prefixed URLs for custom type images", func() {
				pod := runPod(
					"get-custom-type-008",
					db.ContainerMetadata{Type: db.ContainerTypeGet, StepName: "custom-resource"},
					runtime.ContainerSpec{
						TeamID:    1,
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///my-org/custom-resource-type:latest"},
						Outputs:   runtime.OutputPaths{"custom-resource": "/tmp/build/get/custom-resource"},
					},
					runtime.ProcessSpec{Path: "/opt/resource/in", Args: []string{"/tmp/build/get/custom-resource"}},
				)

				By("strips docker:/// prefix")
				Expect(pod.Spec.Containers[0].Image).To(Equal("my-org/custom-resource-type:latest"))

				By("get step still gets artifact-helper for custom types")
				Expect(pod.Spec.Containers).To(HaveLen(2))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
			})
		})

		Describe("artifact store init containers for input volumes", func() {
			It("creates init containers to extract artifacts from PVC for get→task flow", func() {
				// Simulate a task step that receives an artifact from a
				// previous get step via the artifact store PVC.
				asv := jetbridge.NewArtifactStoreVolume(
					"artifacts/build-42/my-repo.tar",
					"get-output-vol-abc",
					"k8s-worker-1",
					nil,
				)

				setupFakeDBContainer(fakeDBWorker, "task-with-input-009")
				container, _, err := pipelineWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("task-with-input-009"),
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "build"},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///golang:1.22"},
						Inputs: []runtime.Input{
							{
								Artifact:        asv,
								DestinationPath: "/tmp/build/workdir/my-repo",
							},
						},
						Outputs: runtime.OutputPaths{"binary": "/tmp/build/workdir/binary"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				_, err = container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh", Args: []string{"-c", "go build -o binary/ ./..."},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err := fakeClientset.CoreV1().Pods("pipeline-ns").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				var pod *corev1.Pod
				for i := range pods.Items {
					if pods.Items[i].Name == "task-with-input-009" {
						pod = &pods.Items[i]
					}
				}
				Expect(pod).ToNot(BeNil())

				By("has init container to extract artifact from PVC")
				Expect(pod.Spec.InitContainers).To(HaveLen(1))
				init := pod.Spec.InitContainers[0]
				Expect(init.Image).To(Equal("gcr.io/my-project/artifact-helper:v2"))

				By("init container mounts PVC read-only")
				var initHasPVC bool
				for _, m := range init.VolumeMounts {
					if m.MountPath == jetbridge.ArtifactMountPath && m.ReadOnly {
						initHasPVC = true
					}
				}
				Expect(initHasPVC).To(BeTrue())

				By("main + artifact-helper containers")
				Expect(pod.Spec.Containers).To(HaveLen(2))
				Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
			})
		})

		Describe("pod labels carry pipeline metadata", func() {
			It("includes pipeline, job, build, step, and handle labels", func() {
				pod := runPod(
					"task-labeled-010",
					db.ContainerMetadata{
						Type:         db.ContainerTypeTask,
						StepName:     "unit-tests",
						PipelineName: "my-pipeline",
						JobName:      "test-job",
						BuildName:    "123",
					},
					runtime.ContainerSpec{
						TeamID:    1,
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "echo ok"}},
				)

				Expect(pod.Labels).To(HaveKeyWithValue("concourse.ci/pipeline", "my-pipeline"))
				Expect(pod.Labels).To(HaveKeyWithValue("concourse.ci/job", "test-job"))
				Expect(pod.Labels).To(HaveKeyWithValue("concourse.ci/build", "123"))
				Expect(pod.Labels).To(HaveKeyWithValue("concourse.ci/step", "unit-tests"))
			})
		})

		Describe("step type comparison across all types", func() {
			// Validates that the sidecar/annotation behavior is correct
			// for every ContainerType in a single table-driven test.
			type stepCase struct {
				stepType       db.ContainerType
				expectSidecar  bool
				expectGCSFuse  bool
				expectPVC      bool
			}

			cases := []stepCase{
				{db.ContainerTypeCheck, false, false, false},
				{db.ContainerTypeGet, true, true, true},
				{db.ContainerTypePut, true, true, true},
				{db.ContainerTypeTask, true, true, true},
				{db.ContainerTypeRun, true, true, true},
			}

			for _, tc := range cases {
				tc := tc // capture range variable
				It(fmt.Sprintf("type=%s: sidecar=%v, gcsFuse=%v, pvc=%v", tc.stepType, tc.expectSidecar, tc.expectGCSFuse, tc.expectPVC), func() {
					handle := fmt.Sprintf("step-type-%s-011", tc.stepType)
					pod := runPod(
						handle,
						db.ContainerMetadata{Type: tc.stepType},
						runtime.ContainerSpec{
							TeamID:    1,
							ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						},
						runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "echo ok"}},
					)

					if tc.expectSidecar {
						Expect(pod.Spec.Containers).To(HaveLen(2),
							"type %s should have artifact-helper", tc.stepType)
						Expect(pod.Spec.Containers[1].Name).To(Equal("artifact-helper"))
					} else {
						Expect(pod.Spec.Containers).To(HaveLen(1),
							"type %s should NOT have artifact-helper", tc.stepType)
					}

					if tc.expectGCSFuse {
						Expect(pod.Annotations).To(HaveKeyWithValue("gke-gcsfuse/volumes", "true"),
							"type %s should have GCS FUSE annotation", tc.stepType)
					} else {
						Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"),
							"type %s should NOT have GCS FUSE annotation", tc.stepType)
					}

					var hasPVC bool
					for _, v := range pod.Spec.Volumes {
						if v.PersistentVolumeClaim != nil {
							hasPVC = true
						}
					}
					if tc.expectPVC {
						Expect(hasPVC).To(BeTrue(),
							"type %s should have PVC volume", tc.stepType)
					} else {
						Expect(hasPVC).To(BeFalse(),
							"type %s should NOT have PVC volume", tc.stepType)
					}
				})
			}
		})

		Describe("without artifact store claim", func() {
			BeforeEach(func() {
				noClaim := jetbridge.NewConfig("pipeline-ns", "")
				noClaim.ArtifactStoreGCSFuse = true // flag set but no claim
				pipelineWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, noClaim)
			})

			It("no step type gets sidecar or GCS FUSE annotation", func() {
				for _, stepType := range []db.ContainerType{
					db.ContainerTypeCheck, db.ContainerTypeGet,
					db.ContainerTypePut, db.ContainerTypeTask,
				} {
					handle := fmt.Sprintf("no-claim-%s-012", stepType)
					pod := runPod(
						handle,
						db.ContainerMetadata{Type: stepType},
						runtime.ContainerSpec{
							TeamID:    1,
							ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						},
						runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "echo ok"}},
					)

					Expect(pod.Spec.Containers).To(HaveLen(1),
						"type %s without claim should have 1 container", stepType)
					Expect(pod.Annotations).ToNot(HaveKey("gke-gcsfuse/volumes"),
						"type %s without claim should not have GCS FUSE", stepType)
				}
			})
		})
	})
