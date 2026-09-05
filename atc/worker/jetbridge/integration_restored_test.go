package jetbridge_test

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/integration_test.go (merge-base aef2244a63, via the
// compile-adapted copy the port stage produced for this branch).
//
// Three of that file's fourteen Its -- rows JB-integration-000, -011 and -012
// of DISPOSITION-jetbridge.md -- were recorded DELETED on FILE-level evidence
// only, and the rebase re-verification REFUTED all three. Restoring is always
// acceptable; deleting on inference is not. The other eleven Its stay deleted,
// their evidence intact; the Describes that held only those are gone with them.

import (
	"bytes"
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Integration tests exercise full workflows through the jetbridge package,
// simulating realistic pipeline step execution using the fake K8s clientset.
// All task types now use exec-mode (pause pod + SPDY exec).
var _ = Describe("Integration", func() {
	var (
		database      jetbridgeDB
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		fakeExecutor  *fakeExecExecutor
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
		// PRUNE-ADAPT: the team handle is unused by the three restored Its;
		// the team itself is still created because they run as team "main".
		_, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("ci-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		worker.SetExecutor(fakeExecutor)
	})

	// createContainer persists a container with the given handle and returns the
	// runtime container the worker builds for it.
	createContainer := func(handle string, containerType db.ContainerType, spec runtime.ContainerSpec) runtime.Container {
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

			By("verifying the real command was exec'd under the task supervisor")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			expectSupervisedExec(fakeExecutor.execCalls[0].command, `'/bin/sh' '-c' 'echo hello world && exit 0'`)

			By("verifying exit status is stored in container properties")
			props, err := container.Properties()
			Expect(err).ToNot(HaveOccurred())
			Expect(props).To(HaveKeyWithValue("concourse:exit-status", "0"))
		})
	})

	Describe("input/output passing between steps", func() {
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

	Describe("task with sidecar containers", func() {
		It("creates a pod with sidecars that share volume mounts and runs the task via exec", func() {
			By("creating a container with a sidecar")
			container := createContainer("task-sidecar", db.ContainerTypeTask, runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/tmp/build/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///node:18",
				},
				Inputs: []runtime.Input{
					{DestinationPath: "/tmp/build/workdir/my-app"},
				},
				Sidecars: []atc.SidecarConfig{
					{
						Name:  "postgres",
						Image: "postgres:15",
						Env: []atc.SidecarEnvVar{
							{Name: "POSTGRES_PASSWORD", Value: "test"},
							{Name: "POSTGRES_DB", Value: "testdb"},
						},
						Ports: []atc.SidecarPort{
							{ContainerPort: 5432},
						},
					},
				},
			})

			By("running the task")
			stdout := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "npm test"},
				Dir:  "/tmp/build/workdir/my-app",
			}, runtime.ProcessIO{
				Stdout: stdout,
			})
			Expect(err).ToNot(HaveOccurred())

			By("verifying the pod has both main and sidecar containers")
			pods, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			pod := pods.Items[0]
			containerNames := []string{}
			for _, c := range pod.Spec.Containers {
				containerNames = append(containerNames, c.Name)
			}
			Expect(containerNames).To(ContainElements("main", "postgres"))

			By("verifying the sidecar has correct env, ports, and shared volume mounts")
			var sidecar corev1.Container
			for _, c := range pod.Spec.Containers {
				if c.Name == "postgres" {
					sidecar = c
					break
				}
			}
			Expect(sidecar.Image).To(Equal("postgres:15"))
			Expect(sidecar.Env).To(ContainElements(
				corev1.EnvVar{Name: "POSTGRES_PASSWORD", Value: "test"},
				corev1.EnvVar{Name: "POSTGRES_DB", Value: "testdb"},
			))
			Expect(sidecar.Ports).To(ContainElement(
				corev1.ContainerPort{ContainerPort: 5432, Protocol: corev1.ProtocolTCP},
			))

			mainMounts := pod.Spec.Containers[0].VolumeMounts
			Expect(sidecar.VolumeMounts).To(Equal(mainMounts))

			By("simulating Pod running and waiting for exec result")
			simulatePodRunning("task-sidecar")
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the real command was exec'd in the main container")
			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			expectSupervisedExec(fakeExecutor.execCalls[0].command, `'/bin/sh' '-c' 'npm test'`)
			Expect(fakeExecutor.execCalls[0].containerName).To(Equal("main"))
		})
	})
})
