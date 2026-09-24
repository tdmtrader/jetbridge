package jetbridge_test

// RESTORED 2026-09-18, from the file commit 3822b69a56 deleted whole.
//
// Round 1 brought back two of its specs. Round 2 (2026-09-18) brought back
// seven more -- rows JB-container-030, -040, -041, -042, -044, -045 and -046 --
// each retired against a `@live-kubernetes` scenario under `features/live/`
// (the live task-completion, mounted-application, no-daemon task-output and
// three-row task-hijack outlines). Those run only against a real cluster and
// never under `make test-unit`, so a live-tier scenario cannot retire a Go
// test that ran on every unit run. Their bodies are the originals, unweakened.
//
// The round-1 pair, each because the ledger's own newest row still asks for
// them:
//
//   - JB-container-055 (`Attach` exec-mode, pod without an exit annotation).
//     Its latest disposition entry is the 2026-09-08 twenty-second-pass
//     **RETAIN Go**; the 2026-09-18 deletion offered no newer evidence and no
//     brine scenario in its place.
//   - JB-kept-000 (a hijack session's context ending must not take the pod
//     with it). It postdates the campaign's merge base, so no both-red pairing
//     was ever recorded for it; its 2026-09-14 retirement note names a live
//     hijack-cancellation case, which is `@live-kubernetes` and so does not run
//     under `make test-unit`.
//
// Everything else the deleted file held stays deleted, its evidence intact.

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// restoredTask holds only the defaults shared by these restored fixtures.
// It preserves the production result and error; assertions stay in the specs.
func restoredTask(worker *jetbridge.Worker, ctx context.Context, handle string, spec runtime.ContainerSpec, delegate runtime.BuildStepDelegate) (runtime.Container, []runtime.VolumeMount, error) {
	spec.TeamID = 1
	if spec.ImageSpec.ImageURL == "" {
		spec.ImageSpec.ImageURL = "docker:///busybox"
	}
	return worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, spec, delegate)
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
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{})
	})

	// Successful task setup shares context and delegate, keeping the worker
	// explicit at alternate-worker call sites.
	createTaskOn := func(taskWorker *jetbridge.Worker, handle string, spec runtime.ContainerSpec) (runtime.Container, []runtime.VolumeMount) {
		GinkgoHelper()
		container, mounts, err := restoredTask(taskWorker, ctx, handle, spec, delegate)
		Expect(err).ToNot(HaveOccurred())
		return container, mounts
	}

	It("Run uses exec-mode for all tasks (universal pause pod) creates a pause pod even when stdin is nil", func() {
		var (
			execContainer runtime.Container
			execExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
		)

		execExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{
			Executor: execExecutor,
		})

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

	It("Input streaming is a no-op (handled by init containers) does not exec any streaming commands for inputs", func() {
		execExecutor := &fakeExecExecutor{}
		execWorkerIS := jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{
			Executor: execExecutor,
		})

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
		// The pause container is RUNNING, not terminated: it sleeps for the
		// life of the step and the command is exec'd into it. A terminated
		// one would mean the pod was destroyed while the exec was in flight.
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
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
			execWorkerOE = jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{
				Executor: execExecutor,
			})

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
			hijackWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{
				Executor: hijackExecutor,
			})

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

		// KEPT FROM CORE 2a9355e1e6 (added to container_test.go after this
		// branch's merge-base). No both-red evidence exists for it.
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

	Describe("Attach", func() {
		BeforeEach(func() {
			_, _, err := worker.FindOrCreateContainer(
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
				execWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg, jetbridge.WorkerDeps{
					Executor: &fakeExecExecutor{},
				})

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
})

// fakeArtifact is the original fixture from the deleted file, restored with
// the input-streaming and put-input specs that need it.
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
