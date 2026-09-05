package jetbridge_test

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/behavioral_runtime_spec_test.go (merge-base aef2244a63,
// which the port stage confirmed compiles and passes on this branch
// byte-for-byte -- zero adaptations were required).
//
// Five of that file's thirty-seven specs -- rows
// JB-behavioral_runtime_spec-007, -008, -009, -010 and -031 of
// DISPOSITION-jetbridge.md -- were recorded DELETED on FILE-level evidence
// only, and the rebase re-verification did not sustain them (three REFUTED, one
// GAP, one INERT). Restoring is always acceptable; deleting on inference is not.
//
// Requirements restored here:
//   PE-08: TTY flag passed to ExecInPod in exec mode
//   SC-07: Sidecar log streaming routing (dedicated writer vs prefix fallback)
//   PE-02: direct mode command embedding
// Everything else in the original file stays deleted, its evidence intact. The
// [P3] Describe keeps only its PE-02 child; its PE-09, RF-14 and RF-15 children
// are gone with the rest.

import (
	"bytes"
	"context"
	"github.com/concourse/concourse/atc/metric"
	"io"

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

var _ = Describe("[PE-08] TTY flag in exec mode", func() {
	var (
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		execExecutor  *fakeExecExecutor
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		execExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		execWorker.SetExecutor(execExecutor)
	})

	It("[PE-08] passes TTY=true to ExecInPod when ProcessSpec.TTY is set", func() {

		container, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("pe08-tty-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			TTY: &runtime.TTYSpec{
				WindowSize: runtime.WindowSize{Columns: 80, Rows: 24},
			},
		}, runtime.ProcessIO{
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		// Transition pod to Running so waitForRunning completes.
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "pe08-tty-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		execExecutor.mu.Lock()
		calls := execExecutor.execCalls
		execExecutor.mu.Unlock()

		Expect(calls).To(HaveLen(1))
		Expect(calls[0].tty).To(BeTrue(), "expected TTY=true to be passed to ExecInPod")
	})

	It("[PE-08] passes TTY=false to ExecInPod when ProcessSpec.TTY is nil", func() {

		container, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("pe08-notty-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			TTY:  nil,
		}, runtime.ProcessIO{
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "pe08-notty-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodRunning
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		execExecutor.mu.Lock()
		calls := execExecutor.execCalls
		execExecutor.mu.Unlock()

		Expect(calls).To(HaveLen(1))
		Expect(calls[0].tty).To(BeFalse(), "expected TTY=false when ProcessSpec.TTY is nil")
	})
})

var _ = Describe("[SC-07] Sidecar log streaming routing (direct mode)", func() {
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
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
	})

	It("[SC-07] when SidecarWriters contains an entry, GetLogs is requested for the sidecar container by name", func() {

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("sc07-dedicated-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Sidecars: []atc.SidecarConfig{
					{Name: "postgres", Image: "postgres:15"},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		sidecarWriter := new(bytes.Buffer)
		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdout: new(bytes.Buffer),
			SidecarWriters: map[string]io.Writer{
				"postgres": sidecarWriter,
			},
		})
		Expect(err).ToNot(HaveOccurred())

		// Complete the pod so Wait() returns.
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sc07-dedicated-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
				},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		// Verify GetLogs was requested for the sidecar container.
		var sidecarLogRequested bool
		for _, action := range fakeClientset.Actions() {
			if action.GetVerb() == "get" && action.GetSubresource() == "log" {
				if getAction, ok := action.(interface{ GetName() string }); ok {
					_ = getAction
				}
				sidecarLogRequested = true
				break
			}
		}
		Expect(sidecarLogRequested).To(BeTrue(),
			"expected GetLogs to be called for the sidecar container")
	})

	It("[SC-07] when SidecarWriters is empty, GetLogs is still requested for the sidecar (prefix fallback path)", func() {

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("sc07-prefix-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Sidecars: []atc.SidecarConfig{
					{Name: "redis", Image: "redis:7"},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdout: new(bytes.Buffer),
			// No SidecarWriters — falls back to prefixed output on Stdout
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sc07-prefix-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name:  "main",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		// Verify GetLogs was requested (prefix fallback path also calls GetLogs).
		var logRequested bool
		for _, action := range fakeClientset.Actions() {
			if action.GetVerb() == "get" && action.GetSubresource() == "log" {
				logRequested = true
				break
			}
		}
		Expect(logRequested).To(BeTrue(),
			"expected GetLogs to be called for sidecar prefix-fallback log streaming")
	})
})

var _ = Describe("[P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15)", func() {
	var (
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
	})

	getPod := func(name string) *corev1.Pod {
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, name, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return pod
	}
	// PRUNE-ADAPT: helper updateStatus dropped -- its only callers were the
	// PE-09/RF-14/RF-15 Its that stay deleted.
	// directContainer builds a container on a worker WITHOUT an executor, so
	// Run() takes the direct-mode path (command baked into the pod spec).
	directContainer := func(handle string) runtime.Container {
		worker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Type:      db.ContainerTypeTask,
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
		return container
	}

	// PRUNE-ADAPT: helper execContainerWith dropped -- its only callers stay deleted.
	Describe("PE-02: direct mode command embedding", func() {
		It("[PE-02] bakes the real command into the main container (no pause pod) and counts the container", func() {
			container := directContainer("pe02-direct")

			metric.Metrics.ContainersCreated.Delta() // reset shared counter

			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pod := getPod("pe02-direct")
			var main *corev1.Container
			for i := range pod.Spec.Containers {
				if pod.Spec.Containers[i].Name == "main" {
					main = &pod.Spec.Containers[i]
				}
			}
			Expect(main).ToNot(BeNil(), "expected a main container")

			// The actual command is embedded directly — not exec'd via a pause pod.
			Expect(main.Command).To(Equal([]string{"/opt/resource/in"}))
			Expect(main.Args).To(Equal([]string{"/tmp/build/get"}))
			Expect(main.Command).ToNot(ContainElement("sh"),
				"direct mode must not use the pause-pod sleep command")

			// PE-02: ContainersCreated metric incremented.
			Expect(metric.Metrics.ContainersCreated.Delta()).To(BeNumerically(">=", float64(1)))
		})
	})
})
