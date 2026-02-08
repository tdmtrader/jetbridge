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
		fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
		fakeCreatingContainer.HandleReturns(handle)
		fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
		fakeCreatedContainer.HandleReturns(handle)
		fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
		fakeDBWorker.FindContainerReturns(nil, nil, nil)
		fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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

	simulatePodSucceeded := func(podName string, exitCode int32) {
		pod, err := fakeClientset.CoreV1().Pods("ci-namespace").Get(ctx, podName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		phase := corev1.PodSucceeded
		if exitCode != 0 {
			phase = corev1.PodFailed
		}
		pod.Status.Phase = phase
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode},
				},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("ci-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
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

			By("verifying the Pod was created with the correct spec")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			pod := pods.Items[0]
			Expect(pod.Spec.Containers[0].Image).To(Equal("docker:///ubuntu:22.04"))
			Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"/bin/sh"}))
			Expect(pod.Spec.Containers[0].Args).To(Equal([]string{"-c", "echo hello world && exit 0"}))
			Expect(pod.Labels["concourse.ci/worker"]).To(Equal("k8s-worker-1"))

			By("simulating Pod completion and waiting for result")
			simulatePodSucceeded("task-abc123", 0)
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying exit status is stored in container properties")
			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveKeyWithValue("concourse:exit-status", "0"))
		})

		It("handles task failure with non-zero exit code", func() {
			container := createContainer("task-fail", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			})

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "exit 2"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			simulatePodSucceeded("task-fail", 2)
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
		It("cleans up the Pod when the context is cancelled during a direct-mode task", func() {
			container := createContainer("cancel-task", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			})

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sleep",
				Args: []string{"3600"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("cancelling the context before the Pod completes")
			cancelCtx, cancel := context.WithCancel(ctx)
			cancel()

			_, err = process.Wait(cancelCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("context canceled"))

			By("verifying the Pod was deleted")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(BeEmpty(), "Pod should have been deleted on cancellation")
		})

		It("cleans up the Pod when the context is cancelled during an exec-mode resource step", func() {
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

			By("waiting - the exec returns an error, and the Pod is cleaned up")
			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			By("verifying the pause Pod was deleted")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(BeEmpty(), "pause Pod should have been deleted after exec failure")
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

			By("simulating successful completion")
			simulatePodSucceeded("task-with-io", 0)
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
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
