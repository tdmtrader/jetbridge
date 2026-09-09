package jetbridge_test

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/container_test.go (merge-base aef2244a63, via the
// compile-adapted copy the port stage produced for this branch; that copy
// carries core commit 0d336e062b's own TaskCacheIdentity fixture edits, marked
// // PORT-ADAPT:).
//
// Forty of that file's seventy-three specs -- rows JB-container-000, -002,
// -004..-011, -015, -016, -018, -022, -024..-026, -028, -030, -034, -035, -038,
// -040..-042, -044..-046, -052, -055, -056, -061..-063, -065..-068 and
// -071..-072 of DISPOSITION-jetbridge.md -- were recorded DELETED on FILE-level
// evidence only, and the rebase re-verification did not sustain them (28
// REFUTED, 10 GAP, 2 INERT). Restoring is always acceptable; deleting on
// inference is not.
//
// The thirty-three specs whose evidence held stay deleted, and with them the
// Describes and Contexts that held only those. Helpers those specs alone used
// are marked // PRUNE-ADAPT: where they had to go too.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
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

// restoredTask holds only the defaults shared by these restored fixtures.
// It preserves the production result and error, including in concurrent and
// intentionally failing callers; assertions stay in the individual specs.
func restoredTask(worker *jetbridge.Worker, ctx context.Context, handle string, spec runtime.ContainerSpec, delegate runtime.BuildStepDelegate) (runtime.Container, []runtime.VolumeMount, error) {
	spec.TeamID = 1
	if spec.ImageSpec.ImageURL == "" {
		spec.ImageSpec.ImageURL = "docker:///busybox"
	}
	return worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, spec, delegate)
}

// restoredRunPod shares only successful pod-construction setup. The specs
// retain their own commands and assertions; this does not wait or simulate
// execution, and is not used by failure, concurrency, or process-lifecycle tests.
func restoredRunPod(ctx context.Context, clientset *fake.Clientset, container runtime.Container, script string) corev1.Pod {
	GinkgoHelper()
	_, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", script},
	}, runtime.ProcessIO{})
	Expect(err).ToNot(HaveOccurred())
	return restoredPod(ctx, clientset, "test-namespace")
}

func restoredPod(ctx context.Context, clientset *fake.Clientset, namespace string) corev1.Pod {
	GinkgoHelper()
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())
	Expect(pods.Items).To(HaveLen(1))
	return pods.Items[0]
}

var _ = Describe("Container", func() {
	var (
		database      jetbridgeDB
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
	})

	// Successful task setup shares context and delegate, keeping the worker
	// explicit at alternate-worker call sites. Error-path and concurrent calls
	// still use restoredTask directly, with their own result/error assertions.
	createTaskOn := func(taskWorker *jetbridge.Worker, handle string, spec runtime.ContainerSpec) (runtime.Container, []runtime.VolumeMount) {
		GinkgoHelper()
		container, mounts, err := restoredTask(taskWorker, ctx, handle, spec, delegate)
		Expect(err).ToNot(HaveOccurred())
		return container, mounts
	}
	createTask := func(handle string, spec runtime.ContainerSpec) runtime.Container {
		GinkgoHelper()
		container, _ := createTaskOn(worker, handle, spec)
		return container
	}

	It("Run creates a Pod with the correct image, command, args, and env", func() {
		var container runtime.Container

		container = createTask("run-test-handle", runtime.ContainerSpec{
			TeamName: "main",
			Dir:      "/workdir",
			Env:      []string{"FOO=bar", "BAZ=qux"},
		})

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo hello"},
			Dir:  "/workdir",
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())
		Expect(process).ToNot(BeNil())

		pod := restoredPod(ctx, fakeClientset, "test-namespace")
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

		By("hardening: seccomp set")
		Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
		Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

	})

	It("Run with Dir volume creates a Pod with an emptyDir volume for spec.Dir when Dir is set", func() {

		container, _ := createTaskOn(worker, "dir-vol-handle", runtime.ContainerSpec{
			Dir: "/tmp/build/workdir",
		})

		pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

		By("adding an emptyDir volume for the Dir path")
		Expect(pod.Spec.Volumes).To(HaveLen(1))
		Expect(pod.Spec.Volumes[0].EmptyDir).ToNot(BeNil())

		By("mounting the Dir volume at the correct path")
		mainContainer := pod.Spec.Containers[0]
		Expect(mainContainer.VolumeMounts).To(HaveLen(1))
		Expect(mainContainer.VolumeMounts[0].MountPath).To(Equal("/tmp/build/workdir"))

	})

	// One mount contract, with independent fixture paths for each volume source.
	// The output row also checks emptyDir now; its original title promised this
	// but its body checked only cardinality and paths.
	DescribeTable("ephemeral working-set mounts",
		func(handle, script string, spec runtime.ContainerSpec, wantPaths []string) {
			container := createTask(handle, spec)
			pod := restoredRunPod(ctx, fakeClientset, container, script)
			Expect(wantPaths).NotTo(BeEmpty())
			Expect(pod.Spec.Volumes).To(HaveLen(len(wantPaths)))
			for _, vol := range pod.Spec.Volumes {
				Expect(vol.EmptyDir).ToNot(BeNil())
			}
			mainContainer := pod.Spec.Containers[0]
			Expect(mainContainer.VolumeMounts).To(HaveLen(len(wantPaths)))
			mountPaths := []string{}
			for _, vm := range mainContainer.VolumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElements(wantPaths))
		},
		Entry("creates a Pod with emptyDir volumes mounted at input paths",
			"input-vol-handle", "ls /tmp/build/workdir/input-a",
			runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Inputs: []runtime.Input{
					{
						Artifact:        &fakeArtifact{handle: "input-a"},
						DestinationPath: "/tmp/build/workdir/input-a",
					},
					{
						Artifact:        &fakeArtifact{handle: "input-b"},
						DestinationPath: "/tmp/build/workdir/input-b",
					},
				},
			},
			[]string{
				"/tmp/build/workdir",
				"/tmp/build/workdir/input-a",
				"/tmp/build/workdir/input-b",
			}),
		Entry("creates a Pod with emptyDir volumes mounted at output paths",
			"output-vol-handle", "echo done",
			runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Outputs: runtime.OutputPaths{
					"result":   "/tmp/build/workdir/result",
					"metadata": "/tmp/build/workdir/metadata",
				},
			},
			[]string{
				"/tmp/build/workdir",
				"/tmp/build/workdir/result",
				"/tmp/build/workdir/metadata",
			}),
		Entry("creates a Pod with emptyDir volumes mounted at cache paths",
			"cache-vol-handle", "echo done",
			runtime.ContainerSpec{
				Dir:    "/tmp/build/workdir",
				Caches: []string{"/tmp/build/workdir/.cache"},
			},
			[]string{
				"/tmp/build/workdir",
				"/tmp/build/workdir/.cache",
			}),
		Entry("creates a Pod with emptyDir volumes for scratch paths",
			"scratch-vol-handle", "echo done",
			runtime.ContainerSpec{
				Dir:          "/tmp/build/workdir",
				ScratchPaths: []string{"/scratch/buildkit"},
			},
			[]string{
				"/tmp/build/workdir",
				"/scratch/buildkit",
			}),
	)

	Describe("Run with same-name input and output", func() {
		var container runtime.Container

		BeforeEach(func() {
			container = createTask("shared-io-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Inputs: []runtime.Input{
					{Artifact: &fakeArtifact{handle: "repo"}, DestinationPath: "/tmp/build/workdir/repo"},
				},
				Outputs: runtime.OutputPaths{
					"repo": "/tmp/build/workdir/repo/",
				},
			})
		})

		It("shares a single volume when input and output paths overlap", func() {
			pod := restoredRunPod(ctx, fakeClientset, container, "ls /tmp/build/workdir/repo")

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
			pod := restoredRunPod(ctx, fakeClientset, container, "echo ok")
			mainContainer := pod.Spec.Containers[0]

			By("the shared mount being named input-*, not output-*")
			for _, vm := range mainContainer.VolumeMounts {
				if vm.MountPath == "/tmp/build/workdir/repo" || vm.MountPath == "/tmp/build/workdir/repo/" {
					Expect(vm.Name).To(HavePrefix("input-"))
				}
			}
		})
	})

	It("Run with non-overlapping inputs and outputs creates separate volumes for non-overlapping input and output", func() {
		var container runtime.Container

		container = createTask("nonoverlap-io-handle", runtime.ContainerSpec{
			Dir: "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{Artifact: &fakeArtifact{handle: "source"}, DestinationPath: "/tmp/build/workdir/source"},
			},
			Outputs: runtime.OutputPaths{
				"binary": "/tmp/build/workdir/binary/",
			},
		})

		pod := restoredRunPod(ctx, fakeClientset, container, "echo ok")

		By("creating 3 volumes (dir + input + output)")
		Expect(pod.Spec.Volumes).To(HaveLen(3))

		By("creating 3 mounts")
		mainContainer := pod.Spec.Containers[0]
		Expect(mainContainer.VolumeMounts).To(HaveLen(3))

	})

	It("Run with scratch path volumes does not create cache entries for scratch paths", func() {
		var container runtime.Container

		container = createTask("scratch-vol-handle", runtime.ContainerSpec{
			Dir:          "/tmp/build/workdir",
			ScratchPaths: []string{"/scratch/buildkit"},
		})

		pod := restoredRunPod(ctx, fakeClientset, container, "echo done")

		By("having no init containers for scratch restore")
		Expect(pod.Spec.InitContainers).To(BeEmpty())

	})

	Describe("Run with cache hostPath configured", func() {
		var container runtime.Container

		It("when CacheHostPath is set but JobID is 0 (one-off build) falls back to emptyDir for one-off builds", func() {
			{
				cfgWithHostPath := jetbridge.NewConfig("test-namespace", "")
				cfgWithHostPath.CacheHostPath = "/var/concourse/cache"
				// PORT-ADAPT: CacheStore added. Verbatim from core 0d336e062b. Without it the
				// "no hostPath volume" assertion below holds vacuously (the default cache store
				// is not hostPath); with it the spec proves the real behaviour, hostPath
				// requested + nil identity => emptyDir fallback. Setup only, assertion unchanged.
				cfgWithHostPath.CacheStore = jetbridge.CacheStoreHostPath

				hostPathWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfgWithHostPath)

				var err error
				container, _, err = hostPathWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("cache-oneoff-handle"),
					db.ContainerMetadata{
						Type:  db.ContainerTypeTask,
						JobID: 0,
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
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

			for _, vol := range pod.Spec.Volumes {
				Expect(vol.HostPath).To(BeNil(), "one-off builds should not use hostPath")
			}

		})
	})

	Describe("Run with explicit CacheStore selector", func() {
		var container runtime.Container

		It("when CacheStore=hostpath overrides artifact store uses hostPath even though artifact store is configured", func() {
			{
				cfgExplicit := jetbridge.NewConfig("test-namespace", "")
				cfgExplicit.CacheHostPath = "/var/concourse/cache"
				cfgExplicit.CacheStore = jetbridge.CacheStoreHostPath

				explicitWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfgExplicit)

				var err error
				container, _, err = explicitWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("explicit-hostpath-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    7,
						StepName: "compile",
					},
					// PORT-ADAPT: TaskCacheIdentity added. core removed runtime.ContainerSpec.JobID
					// (int) in 0d336e062b; the hostPath cache key now comes from the opaque
					// *atc.TaskCacheIdentity, and a nil identity silently downgrades hostPath to
					// emptyDir. JobID 7 reproduces the original /var/concourse/cache/job-7-compile-
					// key that db.ContainerMetadata{JobID: 7} used to supply. Verbatim from core.
					runtime.ContainerSpec{
						TeamID:            1,
						TaskCacheIdentity: &atc.TaskCacheIdentity{JobID: 7},
						Dir:               "/tmp/build/workdir",
						ImageSpec:         runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:            []string{"/tmp/build/workdir/.cache"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

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

		It("when CacheStore=emptydir is explicitly set uses emptyDir for caches", func() {
			{
				cfgExplicit := jetbridge.NewConfig("test-namespace", "")
				cfgExplicit.CacheStore = jetbridge.CacheStoreEmptyDir

				explicitWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfgExplicit)

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
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

			By("cache volumes should be emptyDir with no subPath")
			mainContainer := pod.Spec.Containers[0]
			for _, m := range mainContainer.VolumeMounts {
				Expect(m.SubPath).To(BeEmpty(), "emptyDir caches should not use subPath")
			}

		})
	})

	Describe("Run with resource limits", func() {
		var container runtime.Container

		It("when only requests are specified with no limits (Burstable no-cap QoS) sets requests with no limits", func() {
			{
				cpuReq := uint64(256)
				memReq := uint64(536870912) // 512MB

				container = createTask("requests-only-handle", runtime.ContainerSpec{
					Dir: "/workdir",
					Limits: runtime.ContainerLimits{
						CPURequest:    &cpuReq,
						MemoryRequest: &memReq,
					},
				})
			}

			listedPod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

			mainContainer := listedPod.Spec.Containers[0]

			By("not setting any limits")
			Expect(mainContainer.Resources.Limits).To(BeNil())

			By("setting only requests")
			Expect(mainContainer.Resources.Requests.Cpu().Cmp(*resource.NewMilliQuantity(256, resource.DecimalSI))).To(Equal(0))
			Expect(mainContainer.Resources.Requests.Memory().Cmp(*resource.NewQuantity(536870912, resource.BinarySI))).To(Equal(0))

		})
	})

	Describe("Run with security context", func() {
		var container runtime.Container

		It("when the container is not privileged sets AllowPrivilegeEscalation=false on non-privileged container", func() {

			container = createTask("secure-handle", runtime.ContainerSpec{
				Dir: "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL:   "docker:///busybox",
					Privileged: false,
				},
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")
			mainContainer := pod.Spec.Containers[0]

			By("not setting RunAsNonRoot (images may run as root)")
			Expect(pod.Spec.SecurityContext).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.RunAsNonRoot).To(BeNil())

			By("setting AllowPrivilegeEscalation=false on container security context")
			Expect(mainContainer.SecurityContext).ToNot(BeNil())
			Expect(mainContainer.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
			Expect(*mainContainer.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

			By("setting seccomp RuntimeDefault profile")
			Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

		})

		It("when the container is privileged sets Privileged=true on privileged container", func() {

			container = createTask("priv-handle", runtime.ContainerSpec{
				Dir: "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL:   "docker:///busybox",
					Privileged: true,
				},
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")
			mainContainer := pod.Spec.Containers[0]

			By("not setting RunAsNonRoot")
			Expect(pod.Spec.SecurityContext).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.RunAsNonRoot).To(BeNil())

			By("setting Privileged=true on container security context")
			Expect(mainContainer.SecurityContext).ToNot(BeNil())
			Expect(mainContainer.SecurityContext.Privileged).ToNot(BeNil())
			Expect(*mainContainer.SecurityContext.Privileged).To(BeTrue())

			By("setting seccomp RuntimeDefault profile even for privileged pods")
			Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

		})
	})

	Describe("Run with imagePullSecrets and serviceAccount", func() {
		var container runtime.Container

		It("when image pull secrets and service account are configured includes imagePullSecrets and serviceAccountName in the pod spec", func() {
			{
				cfgWithSecrets := jetbridge.NewConfig("test-namespace", "")
				cfgWithSecrets.ImagePullSecrets = []string{"registry-creds", "gcr-key"}
				cfgWithSecrets.ServiceAccount = "ci-runner"

				secretsWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfgWithSecrets)

				container, _ = createTaskOn(secretsWorker, "secrets-handle", runtime.ContainerSpec{
					Dir: "/workdir",
				})
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

			By("setting imagePullSecrets from config")
			Expect(pod.Spec.ImagePullSecrets).To(HaveLen(2))
			Expect(pod.Spec.ImagePullSecrets).To(ContainElements(
				corev1.LocalObjectReference{Name: "registry-creds"},
				corev1.LocalObjectReference{Name: "gcr-key"},
			))

			By("setting serviceAccountName from config")
			Expect(pod.Spec.ServiceAccountName).To(Equal("ci-runner"))

		})

		It("when ImageRegistry is configured with a SecretName auto-includes the registry secret in imagePullSecrets", func() {
			{
				cfgWithRegistry := jetbridge.NewConfig("test-namespace", "")
				cfgWithRegistry.ImagePullSecrets = []string{"existing-secret"}
				cfgWithRegistry.ImageRegistry = &jetbridge.ImageRegistryConfig{
					Prefix:     "gcr.io/my-project/concourse",
					SecretName: "gcr-auth",
				}

				registryWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfgWithRegistry)

				container, _ = createTaskOn(registryWorker, "registry-handle", runtime.ContainerSpec{
					Dir: "/workdir",
				})
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")
			Expect(pod.Spec.ImagePullSecrets).To(HaveLen(2))
			Expect(pod.Spec.ImagePullSecrets).To(ContainElements(
				corev1.LocalObjectReference{Name: "existing-secret"},
				corev1.LocalObjectReference{Name: "gcr-auth"},
			))

		})
	})

	It("Run uses exec-mode for all tasks (universal pause pod) creates a pause pod even when stdin is nil", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
		)

		execExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		execWorker.SetExecutor(execExecutor)

		execContainer, _ = createTaskOn(execWorker, "exec-task-handle", runtime.ContainerSpec{
			Dir: "/workdir",
		})

		process, err := execContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo hello"},
			Dir:  "/workdir",
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())
		Expect(process).ToNot(BeNil())

		pod := restoredPod(ctx, fakeClientset, "test-namespace")
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
		expectSupervisedExec(execExecutor.execCalls[0].command, `'/bin/sh' '-c' 'echo hello'`)

	})

	Describe("FindOrCreateContainer returns VolumeMounts", func() {
		var execWorker *jetbridge.Worker

		BeforeEach(func() {
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(&fakeExecExecutor{})
		})

		It("when container spec has inputs returns Volumes with an executor wired up for StreamIn/StreamOut", func() {
			var volumeMounts []runtime.VolumeMount

			_, volumeMounts = createTaskOn(execWorker, "vm-input-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Inputs: []runtime.Input{
					{Artifact: &fakeArtifact{handle: "my-input"}, DestinationPath: "/tmp/build/workdir/my-input"},
					{Artifact: &fakeArtifact{handle: "other-input"}, DestinationPath: "/tmp/build/workdir/other-input"},
				},
			})

			inputMounts := filterMountsByPaths(volumeMounts, []string{
				"/tmp/build/workdir/my-input",
			})
			Expect(inputMounts).To(HaveLen(1))

			vol, ok := inputMounts[0].Volume.(*jetbridge.Volume)
			Expect(ok).To(BeTrue(), "volume should be *jetbridge.Volume")
			Expect(vol).ToNot(BeNil())
			Expect(vol.HasExecutor()).To(BeTrue(), "volume should have an executor for StreamIn/StreamOut")

		})

		It("when container spec has outputs returns a VolumeMount for each output with correct MountPath", func() {
			var volumeMounts []runtime.VolumeMount

			_, volumeMounts = createTaskOn(execWorker, "vm-output-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Outputs: runtime.OutputPaths{
					"result":   "/tmp/build/workdir/result",
					"metadata": "/tmp/build/workdir/metadata",
				},
			})

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

		It("when container spec has caches returns cache Volumes with an executor wired up for StreamIn/StreamOut", func() {
			var volumeMounts []runtime.VolumeMount

			_, volumeMounts = createTaskOn(execWorker, "vm-cache-handle", runtime.ContainerSpec{
				Dir:    "/tmp/build/workdir",
				Caches: []string{"/tmp/build/workdir/.cache"},
			})

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

	It("Input streaming is a no-op (handled by init containers) does not exec any streaming commands for inputs", func() {

		execExecutor := &fakeExecExecutor{}
		execWorkerIS := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		execWorkerIS.SetExecutor(execExecutor)

		artifact := &fakeArtifact{
			handle:    "input-vol-1",
			source:    "k8s-worker-1",
			streamOut: []byte("tar-stream-data"),
		}

		container, _ := createTaskOn(execWorkerIS, "noop-stream-handle", runtime.ContainerSpec{
			Dir: "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        artifact,
					DestinationPath: "/tmp/build/workdir/my-input",
				},
			},
		})

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
		expectSupervisedExec(execExecutor.execCalls[0].command, `'/bin/sh' '-c' 'echo done'`)

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
			execWorkerOE = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorkerOE.SetExecutor(execExecutor)

			execContainer, volumeMounts = createTaskOn(execWorkerOE, "output-extract-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Outputs: runtime.OutputPaths{
					"result": "/tmp/build/workdir/result",
				},
			})
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
			restoredPod(ctx, fakeClientset, "test-namespace")
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
			hijackWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
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

			// Persist the container so LookupContainer finds it.
			creating, err := dbWorker.CreateContainer(
				db.NewFixedHandleContainerOwner("hijack-pod"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
			)
			Expect(err).NotTo(HaveOccurred())
			_, err = creating.Created()
			Expect(err).NotTo(HaveOccurred())

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
			listedPod := restoredPod(ctx, fakeClientset, "test-namespace")
			Expect(listedPod.Name).To(Equal("hijack-pod"))

			By("executing the hijack command via the executor")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(hijackExecutor.execCalls).To(HaveLen(1))
			expectSupervisedExec(hijackExecutor.execCalls[0].command, `'/bin/bash' '-l'`)
			Expect(hijackExecutor.execCalls[0].podName).To(Equal("hijack-pod"))
		})

		// KEPT FROM CORE 2a9355e1e6 (added to container_test.go after this
		// branch's merge-base). No brine evidence exists for it, so it is
		// carried here rather than deleted with the rest of the file.
		//
		// The hijack session's context ends when the operator closes the
		// window, and the pod is the step's, not the session's -- deleting it
		// would kill whatever was being debugged.
		It("leaves the pod alone when the hijack session's context ends", func() {
			hijackCtx, endSession := context.WithCancel(ctx)
			defer endSession()

			hijackExecutor.execFunc = func() error {
				<-hijackCtx.Done()
				return hijackCtx.Err()
			}

			process, err := hijackContainer.Run(hijackCtx, runtime.ProcessSpec{
				Path: "/bin/bash",
				Args: []string{"-l"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			waited := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, waitErr := process.Wait(hijackCtx)
				waited <- waitErr
			}()

			endSession()
			Eventually(waited, 10*time.Second).Should(Receive(HaveOccurred()))

			listedPod := restoredPod(ctx, fakeClientset, "test-namespace")
			Expect(listedPod.Name).To(Equal("hijack-pod"))
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
	})

	Describe("Run metrics", func() {
		var container runtime.Container

		It("when pod creation fails increments FailedContainers", func() {

			container = createTask("metric-fail-handle", runtime.ContainerSpec{
				Dir: "/workdir",
			})

			// Make pod creation fail by injecting a reactor.
			fakeClientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				return true, nil, fmt.Errorf("simulated pod creation failure")
			})

			metric.Metrics.ContainersCreated.Delta()
			metric.Metrics.FailedContainers.Delta()

			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())

			Expect(metric.Metrics.FailedContainers.Delta()).To(Equal(float64(1)))
			Expect(metric.Metrics.ContainersCreated.Delta()).To(Equal(float64(0)))

		})
	})

	Describe("Attach", func() {
		// PRUNE-ADAPT: the `container` handle this BeforeEach used to keep is
		// gone with the two Attach Its whose evidence held; the container is
		// still created because the surviving It shares the worker.
		BeforeEach(func() {
			var err error
			_, _, err = worker.FindOrCreateContainer(
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

		It("exec-mode: when executor is set and pod has no exit annotation returns error so engine falls through to Run", func() {
			var execContainer runtime.Container

			{
				execWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				execWorker.SetExecutor(&fakeExecExecutor{})

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
			}

			_, err := execContainer.Attach(ctx, "some-process", runtime.ProcessIO{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no completion status"))

		})
	})

	Describe("FindOrCreateContainer failure handling", func() {
		findOrCreate := func(w *jetbridge.Worker, handle string) error {
			_, _, err := restoredTask(w,
				ctx,
				handle,
				runtime.ContainerSpec{},
				delegate,
			)
			return err
		}

		stateOf := func(handle string) string {
			var state string
			Expect(database.Conn.QueryRow(
				`SELECT state FROM containers WHERE handle = $1`, handle,
			).Scan(&state)).To(Succeed())
			return state
		}

		It("marks the container as failed when Created() fails", func() {
			faultWorker := jetbridge.NewWorker(failCreatedTransition{dbWorker}, fakeClientset, cfg)

			err := findOrCreate(faultWorker, "fail-create-handle")
			Expect(err).To(MatchError(ContainSubstring("mark container as created")))

			By("marking the container as failed in the DB")
			Expect(stateOf("fail-create-handle")).To(Equal(string(atc.ContainerStateFailed)))
		})
	})
	Describe("Run with sidecar containers", func() {
		var container runtime.Container

		It("when one sidecar is configured creates a pod with the main container and the sidecar", func() {

			container = createTask("one-sidecar-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
				Inputs: []runtime.Input{
					{Artifact: &fakeArtifact{handle: "my-repo"}, DestinationPath: "/tmp/build/workdir/my-repo"},
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
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")
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
			Expect(pod.Spec.SecurityContext.SeccompProfile).ToNot(BeNil())
			Expect(pod.Spec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

		})

		It("when multiple sidecars are configured creates a pod with the main container and all sidecars", func() {

			container = createTask("multi-sidecar-handle", runtime.ContainerSpec{
				Dir: "/tmp/build/workdir",
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
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

			Expect(pod.Spec.Containers).To(HaveLen(3))
			Expect(pod.Spec.Containers[0].Name).To(Equal("main"))
			Expect(pod.Spec.Containers[1].Name).To(Equal("redis"))
			Expect(pod.Spec.Containers[2].Name).To(Equal("nginx"))

		})

		It("when a sidecar has resources, command, args, and workingDir maps all sidecar fields to the K8s container spec", func() {

			container = createTask("sidecar-full-handle", runtime.ContainerSpec{
				Dir: "/workdir",
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
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

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

		It("when sidecars are configured alongside the artifact store includes main and user sidecar containers (no artifact-helper sidecar)", func() {
			var (
				artifactWorker *jetbridge.Worker
			)

			{
				cfgWithArtifact := jetbridge.NewConfig("test-namespace", "")
				artifactWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfgWithArtifact)

				container, _ = createTaskOn(artifactWorker, "sidecar-artifact-handle", runtime.ContainerSpec{
					Dir: "/tmp/build/workdir",
					Inputs: []runtime.Input{
						{Artifact: &fakeArtifact{handle: "my-input"}, DestinationPath: "/tmp/build/workdir/my-input"},
					},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "redis",
							Image: "redis:7",
							Ports: []atc.SidecarPort{{ContainerPort: 6379}},
						},
					},
				})
			}

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

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

		It("when sidecars are configured in exec-mode (pause pod) creates a pause pod with sidecar containers", func() {
			var (
				execWorker   *jetbridge.Worker
				fakeExecutor *fakeExecExecutor
			)

			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			container, _ = createTaskOn(execWorker, "sidecar-exec-handle", runtime.ContainerSpec{
				Dir: "/workdir",
				Sidecars: []atc.SidecarConfig{
					{
						Name:  "postgres",
						Image: "postgres:15",
						Ports: []atc.SidecarPort{{ContainerPort: 5432}},
					},
				},
			})

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "npm test"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			Expect(process).ToNot(BeNil())

			pod := restoredPod(ctx, fakeClientset, "test-namespace")

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

		It("when a sidecar image has a docker:/// prefix (image_artifact handoff) strips Concourse URL prefixes from sidecar images in the pod spec", func() {

			container = createTask("sidecar-prefix-handle", runtime.ContainerSpec{
				Dir: "/workdir",
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
			})

			pod := restoredRunPod(ctx, fakeClientset, container, "echo hello")

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

// PORT-ADAPT: this file's copy of filterMountsByPaths was deleted here. When the
// suite was retired the branch rescued a byte-identical copy into
// jetbridge_suite_test.go:178, and keeping both is "filterMountsByPaths
// redeclared in this block". The suite copy is used unchanged. This was the ONLY
// duplicate-declaration collision in the restored file.

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
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		ctx           context.Context
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dbWorker, err = persistNamedWorker(useJetbridgeDB(), "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		delegate = &noopDelegate{}
	})

	It("handles concurrent SetProperty and Properties without races", func() {
		cfg := jetbridge.NewConfig("test-namespace", "")
		worker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)

		container, _, err := restoredTask(worker,
			ctx,
			"concurrent-props-handle",
			runtime.ContainerSpec{
				Dir: "/workdir",
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

				localWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)

				containers[n], _, errs[n] = restoredTask(localWorker,
					ctx,
					fmt.Sprintf("concurrent-handle-%d", n),
					runtime.ContainerSpec{
						Dir: "/workdir",
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

				handle := fmt.Sprintf("concurrent-run-%d", n)

				localWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				container, _, err := restoredTask(localWorker,
					ctx,
					handle,
					runtime.ContainerSpec{
						Dir: "/workdir",
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

// ---------------------------------------------------------------
// End-to-end pipeline integration scenarios
// ---------------------------------------------------------------
