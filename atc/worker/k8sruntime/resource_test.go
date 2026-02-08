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

var _ = Describe("Resource Step Execution", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		fakeExecutor  *fakeExecExecutor
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
		fakeExecutor = &fakeExecExecutor{}

		worker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)
	})

	setupContainer := func(handle string, containerType db.ContainerType, spec runtime.ContainerSpec) runtime.Container {
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

	// simulatePodRunning updates the Pod status to Running so that
	// Process.Wait can proceed with the exec.
	simulatePodRunning := func(podName string) {
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, podName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	Describe("get step", func() {
		var container runtime.Container

		BeforeEach(func() {
			container = setupContainer("get-resource-handle", db.ContainerTypeGet, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/tmp/build/get",
				ImageSpec: runtime.ImageSpec{
					ResourceType: "git",
				},
				Type:           db.ContainerTypeGet,
				CertsBindMount: true,
			})
		})

		It("creates a pause Pod and execs /opt/resource/in with stdin/stdout", func() {
			stdinJSON := `{"source":{"uri":"https://github.com/concourse/concourse"},"version":{"ref":"abc123"}}`
			stdoutJSON := `{"version":{"ref":"abc123"},"metadata":[{"name":"commit","value":"abc123"}]}`
			fakeExecutor.execStdout = []byte(stdoutJSON)

			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)

			process, err := container.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(stdinJSON),
				Stdout: stdout,
				Stderr: stderr,
			})
			Expect(err).ToNot(HaveOccurred())

			By("creating a Pod with a pause command (exec mode)")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			pod := pods.Items[0]
			Expect(pod.Spec.Containers[0].Image).To(Equal("concourse/git-resource"))
			// In exec mode, the Pod command should be a pause/sleep, not the resource script
			Expect(pod.Spec.Containers[0].Command[0]).ToNot(Equal("/opt/resource/in"))

			By("waiting for the Pod to complete via exec")
			simulatePodRunning("get-resource-handle")

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("executing the resource script via PodExecutor")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"/opt/resource/in", "/tmp/build/get"}))
			Expect(call.podName).To(Equal("get-resource-handle"))
			Expect(call.namespace).To(Equal("test-namespace"))
			Expect(call.containerName).To(Equal("main"))

			By("piping stdin JSON to the resource script")
			stdinData, err := io.ReadAll(call.stdin)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(stdinData)).To(Equal(stdinJSON))

			By("capturing stdout JSON from the resource script")
			Expect(stdout.String()).To(Equal(stdoutJSON))
		})

		It("returns non-zero exit code on resource failure", func() {
			fakeExecutor.execErr = &k8sruntime.ExecExitError{ExitCode: 1}

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

			simulatePodRunning("get-resource-handle")

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(1))
		})
	})

	Describe("put step", func() {
		var container runtime.Container

		BeforeEach(func() {
			container = setupContainer("put-resource-handle", db.ContainerTypePut, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				ImageSpec: runtime.ImageSpec{
					ResourceType: "git",
				},
				Type: db.ContainerTypePut,
				Inputs: []runtime.Input{
					{DestinationPath: "/tmp/build/put/my-repo"},
				},
				CertsBindMount: true,
			})
		})

		It("creates a Pod with input volumes and execs /opt/resource/out", func() {
			stdinJSON := `{"source":{"uri":"https://github.com/concourse/concourse"},"params":{"repository":"my-repo"}}`
			stdoutJSON := `{"version":{"ref":"def456"},"metadata":[{"name":"pushed","value":"true"}]}`
			fakeExecutor.execStdout = []byte(stdoutJSON)

			stdout := new(bytes.Buffer)

			process, err := container.Run(ctx, runtime.ProcessSpec{
				ID:   "resource",
				Path: "/opt/resource/out",
				Args: []string{"/tmp/build/put"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(stdinJSON),
				Stdout: stdout,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			By("including input volume mounts in the Pod")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]
			mainContainer := pod.Spec.Containers[0]
			hasMountAt := false
			for _, vm := range mainContainer.VolumeMounts {
				if vm.MountPath == "/tmp/build/put/my-repo" {
					hasMountAt = true
				}
			}
			Expect(hasMountAt).To(BeTrue(), "expected volume mount at /tmp/build/put/my-repo")

			By("executing /opt/resource/out via PodExecutor")
			simulatePodRunning("put-resource-handle")

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"/opt/resource/out", "/tmp/build/put"}))
			Expect(stdout.String()).To(Equal(stdoutJSON))
		})
	})

	Describe("check step", func() {
		var container runtime.Container

		BeforeEach(func() {
			container = setupContainer("check-resource-handle", db.ContainerTypeCheck, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				ImageSpec: runtime.ImageSpec{
					ResourceType: "git",
				},
				Type:           db.ContainerTypeCheck,
				CertsBindMount: true,
			})
		})

		It("creates a Pod and execs /opt/resource/check with stdin/stdout", func() {
			stdinJSON := `{"source":{"uri":"https://github.com/concourse/concourse"}}`
			stdoutJSON := `[{"ref":"abc123"},{"ref":"def456"}]`
			fakeExecutor.execStdout = []byte(stdoutJSON)

			stdout := new(bytes.Buffer)

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/check",
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(stdinJSON),
				Stdout: stdout,
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			simulatePodRunning("check-resource-handle")

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("executing /opt/resource/check via PodExecutor")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			call := fakeExecutor.execCalls[0]
			Expect(call.command).To(Equal([]string{"/opt/resource/check"}))

			By("piping stdin and capturing stdout")
			stdinData, _ := io.ReadAll(call.stdin)
			Expect(string(stdinData)).To(Equal(stdinJSON))
			Expect(stdout.String()).To(Equal(stdoutJSON))
		})

		It("does not set a process ID for check steps", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/check",
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())
			// Check steps have no explicit ID - uses container handle
			Expect(process.ID()).To(Equal("check-resource-handle"))
		})
	})
})
