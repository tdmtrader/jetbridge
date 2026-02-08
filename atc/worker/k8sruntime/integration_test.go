package k8sruntime_test

import (
	"bytes"
	"context"
	"io"

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

// Integration tests exercise full workflows through the k8sruntime package,
// simulating realistic pipeline step execution using the fake K8s clientset.
// All task types now use exec-mode (pause pod + SPDY exec).
var _ = Describe("Integration", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		fakeExecutor  *fakeExecExecutor
		worker        *k8sruntime.Worker
		ctx           context.Context
		cfg           k8sruntime.Config
		delegate      runtime.BuildStepDelegate
		containerSeq  int
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = k8sruntime.NewConfig("ci-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}
		containerSeq = 0

		worker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)
	})

	// createContainer is a helper that sets up DB fakes and calls
	// FindOrCreateContainer with a unique handle.
	createContainer := func(handle string, containerType db.ContainerType, spec runtime.ContainerSpec) runtime.Container {
		containerSeq++
		setupFakeDBContainer(fakeDBWorker, handle)

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: containerType},
			spec,
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
		return container
	}

	simulatePodRunning := func(podName string) {
		pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, podName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	Describe("simple task pipeline", func() {
		It("runs a task step end-to-end: create container → run → wait → exit", func() {
			By("creating a container for the task step")
			container := createContainer("task-abc123", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///ubuntu:22.04",
				},
			})

			By("running the task script")
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello world && exit 0"},
				Dir:  "/tmp/build/workdir",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process.ID()).To(Equal("task-abc123"))

			By("verifying the Pod was created as a pause pod")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			pod := pods.Items[0]
			Expect(pod.Spec.Containers[0].Image).To(Equal("ubuntu:22.04"))
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"}))
			Expect(pod.Labels["concourse.ci/worker"]).To(Equal("k8s-worker-1"))

			By("simulating Pod reaching Running state and waiting for exec result")
			simulatePodRunning("task-abc123")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the real command was exec'd via the executor")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			Expect(fakeExecutor.execCalls[0].command).To(Equal([]string{"/bin/sh", "-c", "echo hello world && exit 0"}))

			By("verifying exit status is stored in container properties")
			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveKeyWithValue("concourse:exit-status", "0"))
		})

		It("handles task failure with non-zero exit code", func() {
			fakeExecutor.execErr = &k8sruntime.ExecExitError{ExitCode: 2}

			container := createContainer("task-fail", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			})

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "exit 2"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("task-fail")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(2))
		})
	})

	Describe("pipeline with get/put resources", func() {
		It("runs a get step followed by a put step with the resource protocol", func() {
			By("running the get step")
			getContainer := createContainer("get-repo", db.ContainerTypeGet, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/tmp/build/get",
				ImageSpec: runtime.ImageSpec{
					ResourceType: "git",
				},
				Type:           db.ContainerTypeGet,
				CertsBindMount: true,
			})

			getStdin := `{"source":{"uri":"https://github.com/concourse/concourse","branch":"main"},"version":{"ref":"abc123"}}`
			getStdout := `{"version":{"ref":"abc123"},"metadata":[{"name":"commit","value":"abc123"}]}`
			fakeExecutor.execStdout = []byte(getStdout)

			getStdoutBuf := new(bytes.Buffer)
			getProcess, err := getContainer.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(getStdin),
				Stdout: getStdoutBuf,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("get-repo")
			getResult, err := getProcess.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(getResult.ExitStatus).To(Equal(0))
			Expect(getStdoutBuf.String()).To(Equal(getStdout))

			By("verifying get step stdin was piped correctly")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			stdinData, _ := io.ReadAll(fakeExecutor.execCalls[0].stdin)
			Expect(string(stdinData)).To(Equal(getStdin))

			By("running the put step with the fetched resource")
			// Reset executor for the put step
			fakeExecutor.execCalls = nil
			putStdout := `{"version":{"ref":"def456"},"metadata":[{"name":"pushed","value":"true"}]}`
			fakeExecutor.execStdout = []byte(putStdout)

			putContainer := createContainer("put-repo", db.ContainerTypePut, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				ImageSpec: runtime.ImageSpec{
					ResourceType: "git",
				},
				Type: db.ContainerTypePut,
				Inputs: []runtime.Input{
					{DestinationPath: "/tmp/build/put/repo"},
				},
				CertsBindMount: true,
			})

			putStdin := `{"source":{"uri":"https://github.com/concourse/concourse","branch":"main"},"params":{"repository":"repo"}}`
			putStdoutBuf := new(bytes.Buffer)
			putProcess, err := putContainer.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/out",
				Args: []string{"/tmp/build/put"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(putStdin),
				Stdout: putStdoutBuf,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("put-repo")
			putResult, err := putProcess.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(putResult.ExitStatus).To(Equal(0))
			Expect(putStdoutBuf.String()).To(Equal(putStdout))

			By("verifying the put exec was called correctly")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			Expect(fakeExecutor.execCalls[0].command).To(Equal([]string{"/opt/resource/out", "/tmp/build/put"}))
		})
	})

	Describe("build cancellation", func() {
		It("returns an error when the context is cancelled during exec-mode task", func() {
			// Make the executor return context error to simulate cancellation during exec
			fakeExecutor.execErr = context.Canceled

			container := createContainer("cancel-task", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			})

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sleep",
				Args: []string{"3600"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("simulating the Pod reaching Running state")
			simulatePodRunning("cancel-task")

			By("waiting for result — exec returns cancellation error")
			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("canceled"))

			By("verifying the Pod still exists (GC handles cleanup, not the process)")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1), "pause Pod should remain for GC to clean up")
		})

		It("returns an error when the context is cancelled during an exec-mode resource step", func() {
			container := createContainer("cancel-resource", db.ContainerTypeGet, runtime.ContainerSpec{
				TeamID:   1,
				ImageSpec: runtime.ImageSpec{ResourceType: "git"},
				Type:     db.ContainerTypeGet,
			})

			// Make the executor block by returning context error
			fakeExecutor.execErr = context.Canceled

			process, err := container.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			By("simulating the Pod reaching Running state")
			simulatePodRunning("cancel-resource")

			By("waiting - the exec returns an error")
			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			By("verifying the pause Pod still exists (GC handles cleanup)")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1), "pause Pod should remain for GC to clean up")
		})
	})

	Describe("pipeline failure modes", func() {
		It("detects ImagePullBackOff in a task step and returns a diagnostic error", func() {
			container := createContainer("task-bad-image", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///nonexistent-image:bad-tag"},
			})

			var stderr bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stderr: &stderr,
			})
			Expect(err).ToNot(HaveOccurred())

			By("simulating ImagePullBackOff on the pod")
			pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, "task-bad-image", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: `Back-off pulling image "nonexistent-image:bad-tag"`,
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ImagePullBackOff"))

			By("verifying diagnostics were written to stderr")
			Expect(stderr.String()).To(ContainSubstring("Pod Failure Diagnostics"))
			Expect(stderr.String()).To(ContainSubstring("ImagePullBackOff"))
		})

		It("detects pod eviction in a resource get step and returns a diagnostic error", func() {
			container := createContainer("get-evicted", db.ContainerTypeGet, runtime.ContainerSpec{
				TeamID:   1,
				ImageSpec: runtime.ImageSpec{ResourceType: "git"},
				Type:     db.ContainerTypeGet,
			})

			fakeExecutor.execStdout = []byte(`{}`)
			var stderr bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{"source":{}}`),
				Stdout: new(bytes.Buffer),
				Stderr: &stderr,
			})
			Expect(err).ToNot(HaveOccurred())

			By("simulating pod eviction before exec can run")
			pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, "get-evicted", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: memory."
			_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Evicted"))

			By("verifying diagnostics were written to stderr")
			Expect(stderr.String()).To(ContainSubstring("Pod Failure Diagnostics"))
			Expect(stderr.String()).To(ContainSubstring("Evicted"))
		})

		It("detects CrashLoopBackOff in a task step during waitForRunning", func() {
			container := createContainer("task-crashloop", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			})

			var stderr bytes.Buffer
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "exit 1"},
			}, runtime.ProcessIO{
				Stderr: &stderr,
			})
			Expect(err).ToNot(HaveOccurred())

			By("simulating CrashLoopBackOff")
			pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, "task-crashloop", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off 5m0s restarting failed container",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("CrashLoopBackOff"))

			By("verifying diagnostics were written to stderr")
			Expect(stderr.String()).To(ContainSubstring("Pod Failure Diagnostics"))
			Expect(stderr.String()).To(ContainSubstring("CrashLoopBackOff"))
		})
	})

	Describe("cache volume lifecycle", func() {
		var (
			cacheWorker      *k8sruntime.Worker
			cacheCfg         k8sruntime.Config
			fakeVolumeRepo   *dbfakes.FakeVolumeRepository
		)

		BeforeEach(func() {
			cacheCfg = k8sruntime.NewConfig("ci-namespace", "")
			cacheCfg.CacheVolumeClaim = "my-cache-pvc"
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)

			cacheWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cacheCfg)
			cacheWorker.SetExecutor(fakeExecutor)
			cacheWorker.SetVolumeRepo(fakeVolumeRepo)
		})

		It("uses PVC subPath for task caches and LookupVolume finds them", func() {
			By("creating a task container with caches")
			setupFakeDBContainer(fakeDBWorker, "task-cached")
			container, mounts, err := cacheWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-cached"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					TeamName: "main",
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{
						ImageURL: "docker:///golang:1.25",
					},
					Caches: []string{"/root/.cache/go-build", "/root/.cache/go-mod"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(container).ToNot(BeNil())

			By("verifying volume mounts include the cache paths")
			mountPaths := make([]string, len(mounts))
			for i, m := range mounts {
				mountPaths[i] = m.MountPath
			}
			Expect(mountPaths).To(ContainElements(
				"/root/.cache/go-build",
				"/root/.cache/go-mod",
			))

			By("running the task to create the pod")
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "go test ./..."},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("verifying the Pod has PVC volume with subPath mounts for caches")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			var hasPVC bool
			for _, vol := range pod.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == "my-cache-pvc" {
					hasPVC = true
				}
			}
			Expect(hasPVC).To(BeTrue(), "Pod should have a PVC volume for cache")

			var subPathMounts []string
			for _, vm := range pod.Spec.Containers[0].VolumeMounts {
				if vm.SubPath != "" {
					subPathMounts = append(subPathMounts, vm.SubPath)
				}
			}
			Expect(subPathMounts).To(HaveLen(2), "Should have 2 subPath mounts for caches")

			By("looking up a cache volume via DB")
			fakeDBVolume := new(dbfakes.FakeCreatedVolume)
			fakeDBVolume.HandleReturns("vol-cache-1")
			fakeDBVolume.WorkerNameReturns("k8s-worker-1")
			fakeVolumeRepo.FindVolumeReturns(fakeDBVolume, true, nil)

			vol, found, err := cacheWorker.LookupVolume(ctx, "vol-cache-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol.Handle()).To(Equal("vol-cache-1"))

			By("completing the task")
			simulatePodRunning("task-cached")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})

		It("uses emptyDir for caches when CacheVolumeClaim is not configured", func() {
			noCacheCfg := k8sruntime.NewConfig("ci-namespace", "")
			noCacheWorker := k8sruntime.NewWorker(fakeDBWorker, fakeClientset, noCacheCfg)
			noCacheWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "task-no-pvc")
			container, _, err := noCacheWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("task-no-pvc"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Caches:    []string{"/cache/data"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())

			By("verifying all volumes are emptyDir (no PVC)")
			for _, vol := range pods.Items[0].Spec.Volumes {
				Expect(vol.PersistentVolumeClaim).To(BeNil())
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			simulatePodRunning("task-no-pvc")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})

		It("resource cache initialization delegates to DB volume", func() {
			By("creating a cache-backed volume with a DB volume")
			fakeDBVolume := new(dbfakes.FakeCreatedVolume)
			fakeDBVolume.HandleReturns("resource-cache-vol")
			fakeDBVolume.WorkerNameReturns("k8s-worker-1")

			fakeUsedCache := &db.UsedWorkerResourceCache{ID: 42}
			fakeDBVolume.InitializeResourceCacheReturns(fakeUsedCache, nil)

			fakeVolumeRepo.FindVolumeReturns(fakeDBVolume, true, nil)

			vol, found, err := cacheWorker.LookupVolume(ctx, "resource-cache-vol")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			By("calling InitializeResourceCache which delegates to DB")
			usedCache, err := vol.InitializeResourceCache(ctx, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(usedCache).ToNot(BeNil())
			Expect(usedCache.ID).To(Equal(42))
			Expect(fakeDBVolume.InitializeResourceCacheCallCount()).To(Equal(1))
		})
	})

	Describe("input/output passing between steps", func() {
		It("mounts input volumes from a get step and output volumes for a task", func() {
			By("creating a task container with inputs from a previous get and outputs")
			container := createContainer("task-with-io", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///golang:1.25",
				},
				Inputs: []runtime.Input{
					{DestinationPath: "/tmp/build/workdir/source-code"},
					{DestinationPath: "/tmp/build/workdir/ci"},
				},
				Outputs: runtime.OutputPaths{
					"binary":   "/tmp/build/workdir/binary",
					"coverage": "/tmp/build/workdir/coverage",
				},
			})

			By("running the task")
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "go build -o /tmp/build/workdir/binary/app ./..."},
				Dir:  "/tmp/build/workdir/source-code",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("verifying the Pod has correct volume mounts")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			mainContainer := pods.Items[0].Spec.Containers[0]

			// 2 inputs + 2 outputs = 4 volume mounts
			Expect(mainContainer.VolumeMounts).To(HaveLen(4))

			mountPaths := make([]string, len(mainContainer.VolumeMounts))
			for i, vm := range mainContainer.VolumeMounts {
				mountPaths[i] = vm.MountPath
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/workdir/source-code",
				"/tmp/build/workdir/ci",
				"/tmp/build/workdir/binary",
				"/tmp/build/workdir/coverage",
			))

			By("verifying all volumes are emptyDir")
			for _, vol := range pods.Items[0].Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}

			By("simulating successful completion via exec")
			simulatePodRunning("task-with-io")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the command was exec'd with the correct working dir")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			Expect(fakeExecutor.execCalls[0].command).To(Equal([]string{"/bin/sh", "-c", "go build -o /tmp/build/workdir/binary/app ./..."}))
		})

		It("passes inputs from a get step to a put step via volume mounts", func() {
			By("creating a put container with multiple inputs")
			container := createContainer("put-multi-input", db.ContainerTypePut, runtime.ContainerSpec{
				TeamID: 1,
				ImageSpec: runtime.ImageSpec{
					ResourceType: "s3",
				},
				Type: db.ContainerTypePut,
				Inputs: []runtime.Input{
					{DestinationPath: "/tmp/build/put/compiled-binary"},
					{DestinationPath: "/tmp/build/put/release-notes"},
				},
			})

			putStdout := `{"version":{"path":"releases/v1.0.0/app.tar.gz"}}`
			fakeExecutor.execStdout = []byte(putStdout)

			stdout := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/out",
				Args: []string{"/tmp/build/put"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{"source":{"bucket":"releases"},"params":{"file":"app.tar.gz"}}`),
				Stdout: stdout,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			By("verifying input volumes are mounted in the pause Pod")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			mainContainer := pods.Items[0].Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(2))

			mountPaths := make([]string, len(mainContainer.VolumeMounts))
			for i, vm := range mainContainer.VolumeMounts {
				mountPaths[i] = vm.MountPath
			}
			Expect(mountPaths).To(ContainElements(
				"/tmp/build/put/compiled-binary",
				"/tmp/build/put/release-notes",
			))

			By("completing the put and verifying output")
			simulatePodRunning("put-multi-input")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
			Expect(stdout.String()).To(Equal(putStdout))
		})
	})
})
