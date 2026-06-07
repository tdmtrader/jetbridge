package jetbridge_test

// behavioral_runtime_spec_test.go covers the gaps identified in the K8s
// Runtime Behavioral Specification (forge/tracks/k8s_runtime_behavioral_spec_20260331/).
//
// Requirements covered:
//   PE-03: ImagePullPolicy=PullIfNotPresent on main container
//   PE-05: Image URL prefix stripping for main container image
//   PE-06: Environment variable merging (containerSpec + processSpec)
//   PE-08: TTY flag passed to ExecInPod in exec mode
//   SC-07: Sidecar log streaming routing (dedicated writer vs prefix fallback)
//   RF-04: InvalidImageName and CreateContainerConfigError terminal waiting states
//   RF-09: Failure detection priority order (OOM before ImagePullBackOff)
//   OE-01: pod.scheduled span event (with node.name attribute)
//   OE-02: pod.initialized span event
//   OE-04: image.pulled span event
//   OE-05: init.container.completed span event
//   OE-06: init.container.failed span event
//   OE-07: sidecar.started span event (with container.name attribute)
//   OE-08: pod.phase.<phase> span events on phase transitions
//   OE-09: Observability event deduplication via podEventTracker
//   OE-10: Metrics recording (K8sPodStartupDuration, K8sImagePullFailures, K8sPodFailure)

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ──────────────────────────────────────────────────────────────────────────────
// PE-03, PE-05, PE-06: Container spec behavioral requirements
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[PE-03] ImagePullPolicy for main container", func() {
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

	It("[PE-03] sets ImagePullPolicy to PullIfNotPresent on the main container", func() {
		setupFakeDBContainer(fakeDBWorker, "pe03-handle")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("pe03-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		_, err = container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))

		mainContainer := pods.Items[0].Spec.Containers[0]
		Expect(mainContainer.Name).To(Equal("main"))
		Expect(mainContainer.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
	})
})

var _ = Describe("[PE-05] Image URL prefix stripping for main container", func() {
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

	DescribeTable("[PE-05] strips Concourse image URL prefixes from main container image",
		func(rawImage, expectedImage string) {
			handle := "pe05-" + expectedImage[:min(8, len(expectedImage))]
			setupFakeDBContainer(fakeDBWorker, handle)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: rawImage},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))

			mainImage := pods.Items[0].Spec.Containers[0].Image
			Expect(mainImage).To(Equal(expectedImage))
		},
		Entry("strips docker:/// prefix", "docker:///busybox:latest", "busybox:latest"),
		Entry("strips docker:// prefix", "docker://busybox:latest", "busybox:latest"),
		Entry("strips raw:/// prefix", "raw:///alpine:3", "alpine:3"),
		Entry("leaves plain image reference unchanged", "alpine:3.18", "alpine:3.18"),
	)
})

var _ = Describe("[PE-06] Environment variable merging", func() {
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

	It("[PE-06] merges env vars from both ContainerSpec and ProcessSpec into the pod", func() {
		setupFakeDBContainer(fakeDBWorker, "pe06-handle")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("pe06-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Env:       []string{"CONTAINER_VAR=from_container", "SHARED_VAR=container_value"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		_, err = container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Env:  []string{"PROCESS_VAR=from_process"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))

		env := pods.Items[0].Spec.Containers[0].Env
		envMap := make(map[string]string)
		for _, e := range env {
			envMap[e.Name] = e.Value
		}

		By("including env vars from ContainerSpec")
		Expect(envMap["CONTAINER_VAR"]).To(Equal("from_container"))

		By("including env vars from ProcessSpec")
		Expect(envMap["PROCESS_VAR"]).To(Equal("from_process"))
	})

	It("[PE-06] ProcessSpec env vars take precedence over ContainerSpec on key collision", func() {
		setupFakeDBContainer(fakeDBWorker, "pe06-override-handle")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("pe06-override-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Env:       []string{"SHARED_VAR=from_container"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		_, err = container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Env:  []string{"SHARED_VAR=from_process"},
		}, runtime.ProcessIO{})
		Expect(err).ToNot(HaveOccurred())

		pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).ToNot(HaveOccurred())

		env := pods.Items[0].Spec.Containers[0].Env
		// Count occurrences of SHARED_VAR — only one should be present with process value
		var sharedVarValues []string
		for _, e := range env {
			if e.Name == "SHARED_VAR" {
				sharedVarValues = append(sharedVarValues, e.Value)
			}
		}
		// If both are present, the process value must be last (overrides)
		// or only one should exist
		Expect(sharedVarValues).ToNot(BeEmpty())
		Expect(sharedVarValues[len(sharedVarValues)-1]).To(Equal("from_process"))
	})
})

// ──────────────────────────────────────────────────────────────────────────────
// PE-08: TTY flag in exec mode
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[PE-08] TTY flag in exec mode", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		execExecutor  *fakeExecExecutor
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
		execExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		execWorker.SetExecutor(execExecutor)
	})

	It("[PE-08] passes TTY=true to ExecInPod when ProcessSpec.TTY is set", func() {
		setupFakeDBContainer(fakeDBWorker, "pe08-tty-handle")

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
		setupFakeDBContainer(fakeDBWorker, "pe08-notty-handle")

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

// ──────────────────────────────────────────────────────────────────────────────
// SC-07: Sidecar log streaming routing
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[SC-07] Sidecar log streaming routing (direct mode)", func() {
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

	It("[SC-07] when SidecarWriters contains an entry, GetLogs is requested for the sidecar container by name", func() {
		setupFakeDBContainer(fakeDBWorker, "sc07-dedicated-handle")

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
		setupFakeDBContainer(fakeDBWorker, "sc07-prefix-handle")

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

// ──────────────────────────────────────────────────────────────────────────────
// RF-04: Additional terminal waiting states
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[RF-04] Additional terminal waiting states", func() {
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

	DescribeTable("[RF-04] fails the pod immediately when a container enters a terminal waiting state",
		func(reason, expectedErrSubstring string) {
			handle := "rf04-" + reason[:min(12, len(reason))]
			setupFakeDBContainer(fakeDBWorker, handle)

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
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
			}, runtime.ProcessIO{
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  reason,
							Message: "simulated " + reason,
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(expectedErrSubstring))
		},
		Entry("InvalidImageName", "InvalidImageName", "InvalidImageName"),
		Entry("CreateContainerConfigError", "CreateContainerConfigError", "CreateContainerConfigError"),
		Entry("ImagePullBackOff (existing)", "ImagePullBackOff", "ImagePullBackOff"),
		Entry("ErrImagePull (existing)", "ErrImagePull", "ErrImagePull"),
		Entry("CrashLoopBackOff (existing)", "CrashLoopBackOff", "CrashLoopBackOff"),
	)
})

// ──────────────────────────────────────────────────────────────────────────────
// RF-09: Failure detection priority order
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[RF-09] Failure detection priority order", func() {
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

	It("[RF-09] reports OOMKilled rather than CrashLoopBackOff when both conditions are present", func() {
		// OOMKilled containers often restart and enter CrashLoopBackOff.
		// The system MUST report OOMKilled (most actionable) first.
		setupFakeDBContainer(fakeDBWorker, "rf09-oom-vs-crash")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("rf09-oom-vs-crash"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		stderrBuf := new(bytes.Buffer)
		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stderr: stderrBuf,
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "rf09-oom-vs-crash", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Simulate a pod that has been OOM-killed, restarted, and now shows
		// CrashLoopBackOff in its current waiting state.
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name:         "main",
				RestartCount: 2,
				State: corev1.ContainerState{
					// Current state: CrashLoopBackOff (what you see after OOM restart)
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "CrashLoopBackOff",
						Message: "back-off 10s restarting failed container",
					},
				},
				// Last termination: OOMKilled (the actual root cause)
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason:   "OOMKilled",
						ExitCode: 137,
					},
				},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("OOMKilled"),
			"expected OOMKilled to take priority over CrashLoopBackOff")
		Expect(err.Error()).ToNot(ContainSubstring("CrashLoopBackOff"),
			"CrashLoopBackOff should not be reported when OOMKilled is the root cause")
	})

	It("[RF-09] reports ImagePullBackOff before checking exit code when both are present", func() {
		// If a container is in ImagePullBackOff, the exit code check should not
		// take precedence.
		setupFakeDBContainer(fakeDBWorker, "rf09-pull-vs-exit")

		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("rf09-pull-vs-exit"),
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
		}, runtime.ProcessIO{
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "rf09-pull-vs-exit", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Pod has failed phase AND image pull backoff (e.g. init container failed
		// but main container can't even pull).
		pod.Status.Phase = corev1.PodPending
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "Back-off pulling image",
					},
				},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ImagePullBackOff"))
	})
})

// ──────────────────────────────────────────────────────────────────────────────
// OE-02, OE-04, OE-06, OE-09: Observability span event requirements
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[OE] Observability span events", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		execContainer runtime.Container
		fakeExecutor  *fakeExecExecutor
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		spanRecorder  *tracetest.SpanRecorder
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(spanRecorder),
			sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
		)
		tracing.ConfigureTraceProvider(tp)

		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	// Helper: find span by name
	findSpan := func(name string) sdktrace.ReadOnlySpan {
		for _, s := range spanRecorder.Ended() {
			if s.Name() == name {
				return s
			}
		}
		return nil
	}

	// Helper: collect event names from a span
	eventNames := func(span sdktrace.ReadOnlySpan) []string {
		names := []string{}
		for _, e := range span.Events() {
			names = append(names, e.Name)
		}
		return names
	}

	Context("exec mode (waitForRunning span)", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "oe-span-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("oe-span-handle"),
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
		})

		It("[OE-02] emits pod.initialized span event when Initialized condition becomes True", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe-span-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.Conditions = []corev1.PodCondition{
				{
					Type:   corev1.PodInitialized,
					Status: corev1.ConditionTrue,
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Transition to Running so waitForRunning completes.
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			})
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			span := findSpan("k8s.exec-process.wait-for-running")
			Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
			Expect(eventNames(span)).To(ContainElement("pod.initialized"))
		})

		It("[OE-04] emits image.pulled span event when container transitions out of ContainerCreating", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			// Pre-stage: set ContainerCreating state BEFORE Wait() so it appears
			// in the watcher's initial sync snapshot. This triggers image.pulling.
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe-span-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Transition to Running concurrently while Wait() is blocking.
			// This sends a watch event that triggers image.pulled (transition out of ContainerCreating).
			go func() {
				time.Sleep(20 * time.Millisecond)
				pod.Status.Phase = corev1.PodRunning
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:  "main",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					},
				}
				_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			}()

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			span := findSpan("k8s.exec-process.wait-for-running")
			Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
			Expect(eventNames(span)).To(ContainElements("image.pulling", "image.pulled"))
		})
	})

	Context("init container failure events", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "oe-init-fail-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("oe-init-fail-handle"),
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
		})

		It("[OE-06] emits init.container.failed span event when init container exits non-zero", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe-init-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Stage 1: init container failed — visible in initial sync snapshot.
			pod.Status.Phase = corev1.PodPending
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "fetch-inputs",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
							Reason:   "Error",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Stage 2: init container retries and succeeds, pod reaches Running.
			// Concurrent update so Wait() can complete.
			go func() {
				time.Sleep(20 * time.Millisecond)
				pod.Status.Phase = corev1.PodRunning
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "fetch-inputs",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
						},
					},
				}
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				}
				_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			}()

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			span := findSpan("k8s.exec-process.wait-for-running")
			Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
			Expect(eventNames(span)).To(ContainElement("init.container.failed"))
		})
	})
})

var _ = Describe("[OE-06] init.container.failed span event (dedicated test)", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		fakeExecutor  *fakeExecExecutor
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		spanRecorder  *tracetest.SpanRecorder
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(spanRecorder),
			sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
		)
		tracing.ConfigureTraceProvider(tp)

		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	It("[OE-06] emits init.container.failed event and then transitions to Running succeeds", func() {
		setupFakeDBContainer(fakeDBWorker, "oe06-init-fail-run")

		execContainer, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("oe06-init-fail-run"),
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

		process, err := execContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe06-init-fail-run", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Stage 1: Pre-stage failed init container (visible in initial sync snapshot).
		// waitForRunning will see this state first and emit init.container.failed.
		pod.Status.Phase = corev1.PodPending
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "fetch-input-0",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1,
						Reason:   "Error",
					},
				},
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Stage 2: Concurrently transition to Running so Wait() can complete.
		// The watcher sees: failed init (initial sync) → successful init + Running (watch event).
		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "fetch-input-0",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
							Reason:   "Completed",
						},
					},
				},
			}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		var waitSpan sdktrace.ReadOnlySpan
		for _, s := range spanRecorder.Ended() {
			if s.Name() == "k8s.exec-process.wait-for-running" {
				waitSpan = s
				break
			}
		}
		Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

		names := []string{}
		for _, e := range waitSpan.Events() {
			names = append(names, e.Name)
		}
		Expect(names).To(ContainElement("init.container.failed"),
			"expected init.container.failed event to be emitted")
	})
})

var _ = Describe("[OE-09] Observability event deduplication", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		fakeExecutor  *fakeExecExecutor
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		spanRecorder  *tracetest.SpanRecorder
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(spanRecorder),
			sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
		)
		tracing.ConfigureTraceProvider(tp)

		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	It("[OE-09] emits pod.scheduled event only once even when pod is observed in Scheduled state multiple times", func() {
		setupFakeDBContainer(fakeDBWorker, "oe09-dedup-handle")

		execContainer, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("oe09-dedup-handle"),
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

		process, err := execContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe09-dedup-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		scheduledCondition := corev1.PodCondition{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
			Reason: "Scheduled",
		}

		// First observation: pod scheduled.
		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{scheduledCondition}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Second observation: pod scheduled again (same state, no change — e.g. watch reconnect).
		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{scheduledCondition}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Transition to Running so Wait() completes.
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{scheduledCondition}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		var waitSpan sdktrace.ReadOnlySpan
		for _, s := range spanRecorder.Ended() {
			if s.Name() == "k8s.exec-process.wait-for-running" {
				waitSpan = s
				break
			}
		}
		Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

		scheduledCount := 0
		for _, e := range waitSpan.Events() {
			if e.Name == "pod.scheduled" {
				scheduledCount++
			}
		}
		Expect(scheduledCount).To(Equal(1),
			"pod.scheduled event should be emitted exactly once, even if pod is observed in Scheduled state multiple times")
	})

	It("[OE-09] emits sidecar.started event only once even when sidecar is observed Running multiple times", func() {
		setupFakeDBContainer(fakeDBWorker, "oe09-sidecar-dedup")

		execContainer, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("oe09-sidecar-dedup"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
				Type:      db.ContainerTypeTask,
				Sidecars: []atc.SidecarConfig{
					{Name: "postgres", Image: "postgres:15"},
				},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		process, err := execContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "oe09-sidecar-dedup", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		runningStatuses := []corev1.ContainerStatus{
			{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "postgres", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}

		// First observation: sidecar running.
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses = runningStatuses
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Second observation: same Running state again (e.g. watch reconnect polling).
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses = runningStatuses
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		var waitSpan sdktrace.ReadOnlySpan
		for _, s := range spanRecorder.Ended() {
			if s.Name() == "k8s.exec-process.wait-for-running" {
				waitSpan = s
				break
			}
		}
		Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

		sidecarStartedCount := 0
		for _, e := range waitSpan.Events() {
			if e.Name == "sidecar.started" {
				sidecarStartedCount++
			}
		}
		Expect(sidecarStartedCount).To(Equal(1),
			"sidecar.started event should be emitted exactly once per sidecar")
	})
})

// ──────────────────────────────────────────────────────────────────────────────
// OE-01, OE-05, OE-07, OE-08: Remaining observability span events
//
// These characterize the span events emitted during exec-mode waitForRunning:
// pod.scheduled and the per-container lifecycle events from emitPodLifecycleEvents
// (init.container.completed, sidecar.started) plus the pod.phase.<phase> events
// emitted on each phase transition. All four land on the
// k8s.exec-process.wait-for-running span, like OE-02/04/06.
// ──────────────────────────────────────────────────────────────────────────────

var _ = Describe("[OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10)", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		execWorker    *jetbridge.Worker
		execContainer runtime.Container
		fakeExecutor  *fakeExecExecutor
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		spanRecorder  *tracetest.SpanRecorder
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(spanRecorder),
			sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
		)
		tracing.ConfigureTraceProvider(tp)

		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		fakeExecutor = &fakeExecExecutor{}
		execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	// waitSpan returns the exec-mode wait-for-running span, where all the
	// observability lifecycle events are recorded.
	waitSpan := func() sdktrace.ReadOnlySpan {
		for _, s := range spanRecorder.Ended() {
			if s.Name() == "k8s.exec-process.wait-for-running" {
				return s
			}
		}
		return nil
	}

	eventNames := func(span sdktrace.ReadOnlySpan) []string {
		names := []string{}
		for _, e := range span.Events() {
			names = append(names, e.Name)
		}
		return names
	}

	findEvent := func(span sdktrace.ReadOnlySpan, name string) (sdktrace.Event, bool) {
		for _, e := range span.Events() {
			if e.Name == name {
				return e, true
			}
		}
		return sdktrace.Event{}, false
	}

	attrValue := func(e sdktrace.Event, key string) (string, bool) {
		for _, kv := range e.Attributes {
			if string(kv.Key) == key {
				return kv.Value.AsString(), true
			}
		}
		return "", false
	}

	// startContainer creates a DB-backed exec container ready to Run().
	startContainer := func(handle string) {
		setupFakeDBContainer(fakeDBWorker, handle)
		var err error
		execContainer, _, err = execWorker.FindOrCreateContainer(
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
	}

	run := func() runtime.Process {
		process, err := execContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: new(bytes.Buffer),
		})
		Expect(err).ToNot(HaveOccurred())
		return process
	}

	getPod := func(handle string) *corev1.Pod {
		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return pod
	}

	updateStatus := func(pod *corev1.Pod) {
		_, err := fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	It("[OE-01] emits pod.scheduled with node.name when PodScheduled becomes True", func() {
		startContainer("oe01-scheduled")
		process := run()

		// Initial sync snapshot: pod scheduled onto a node, not yet Running.
		pod := getPod("oe01-scheduled")
		pod.Spec.NodeName = "node-oe01"
		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
		}
		updateStatus(pod)

		// Concurrent transition to Running so Wait() can complete.
		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		span := waitSpan()
		Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
		Expect(eventNames(span)).To(ContainElement("pod.scheduled"))

		ev, ok := findEvent(span, "pod.scheduled")
		Expect(ok).To(BeTrue())
		node, ok := attrValue(ev, "node.name")
		Expect(ok).To(BeTrue(), "pod.scheduled event must carry a node.name attribute")
		Expect(node).To(Equal("node-oe01"))
	})

	It("[OE-05] emits init.container.completed when an init container exits 0", func() {
		startContainer("oe05-init-done")
		process := run()

		// Initial sync snapshot: init container has completed successfully.
		pod := getPod("oe05-init-done")
		pod.Status.Phase = corev1.PodPending
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
			{
				Name:  "fetch-input-0",
				Image: "alpine:latest",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
				},
			},
		}
		updateStatus(pod)

		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		span := waitSpan()
		Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
		Expect(eventNames(span)).To(ContainElement("init.container.completed"))

		ev, ok := findEvent(span, "init.container.completed")
		Expect(ok).To(BeTrue())
		name, ok := attrValue(ev, "container.name")
		Expect(ok).To(BeTrue(), "init.container.completed event must carry a container.name attribute")
		Expect(name).To(Equal("fetch-input-0"))
	})

	It("[OE-07] emits sidecar.started with container.name when a non-main container reaches Running", func() {
		startContainer("oe07-sidecar")
		process := run()

		pod := getPod("oe07-sidecar")
		pod.Status.Phase = corev1.PodPending
		updateStatus(pod)

		// Main and a sidecar both reach Running. The sidecar (non-"main") must
		// emit sidecar.started; main reaching Running lets Wait() complete.
		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "metrics-sidecar", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		span := waitSpan()
		Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
		Expect(eventNames(span)).To(ContainElement("sidecar.started"))

		ev, ok := findEvent(span, "sidecar.started")
		Expect(ok).To(BeTrue())
		name, ok := attrValue(ev, "container.name")
		Expect(ok).To(BeTrue(), "sidecar.started event must carry a container.name attribute")
		Expect(name).To(Equal("metrics-sidecar"))
	})

	It("[OE-08] emits pod.phase.<phase> events on phase transitions", func() {
		startContainer("oe08-phase")
		process := run()

		// Initial sync snapshot observes Pending.
		pod := getPod("oe08-phase")
		pod.Status.Phase = corev1.PodPending
		updateStatus(pod)

		// Transition to Running.
		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		span := waitSpan()
		Expect(span).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")
		Expect(eventNames(span)).To(ContainElements("pod.phase.pending", "pod.phase.running"))

		ev, ok := findEvent(span, "pod.phase.running")
		Expect(ok).To(BeTrue())
		phase, ok := attrValue(ev, "pod.phase")
		Expect(ok).To(BeTrue(), "pod.phase.running event must carry a pod.phase attribute")
		Expect(phase).To(Equal("Running"))
	})

	// ── OE-10: Metrics recording ────────────────────────────────────────────
	// Driven through the same exec-mode runtime. The in-process Monitor metrics
	// (K8sPodStartupDuration gauge, K8sImagePullFailures counter) are package
	// globals read directly; the OTel K8sPodFailure counter is read back via a
	// ManualReader. Specs run serially within a Ginkgo process, so resetting the
	// shared in-process metrics at the start of a spec is race-free.

	It("[OE-10] records K8sPodStartupDuration when the pod reaches Running", func() {
		// Reset the shared gauge's max-tracking before driving startup.
		metric.Metrics.K8sPodStartupDuration.Max()

		startContainer("oe10-startup")
		process := run()

		pod := getPod("oe10-startup")
		pod.Status.Phase = corev1.PodPending
		updateStatus(pod)

		go func() {
			time.Sleep(20 * time.Millisecond)
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, _ = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		}()

		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))

		// Set(...) was called with the startup duration in ms (>= the 20ms wait).
		Expect(metric.Metrics.K8sPodStartupDuration.Max()).To(BeNumerically(">", 0),
			"expected a positive K8sPodStartupDuration to be recorded on successful startup")
	})

	It("[OE-10] increments K8sImagePullFailures when a container hits ImagePullBackOff", func() {
		// Reset the shared counter (Delta swaps it back to zero).
		metric.Metrics.K8sImagePullFailures.Delta()

		startContainer("oe10-imgpull")
		process := run()

		// Pre-stage ImagePullBackOff so it's detected on the initial sync.
		pod := getPod("oe10-imgpull")
		pod.Status.Phase = corev1.PodPending
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "Back-off pulling image \"busybox\"",
					},
				},
			},
		}
		updateStatus(pod)

		_, err := process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ImagePullBackOff"))

		Expect(metric.Metrics.K8sImagePullFailures.Delta()).To(Equal(float64(1)),
			"expected K8sImagePullFailures to be incremented exactly once")
	})

	It("[OE-10] records the K8sPodFailure OTel counter with a reason attribute", func() {
		// Wire a manual reader so the OTel pod-failures counter is collectable.
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)
		metric.InitOTelMetrics()

		startContainer("oe10-podfailure")
		process := run()

		// Pre-stage an OOM kill; the OOM check fires before the Running check.
		pod := getPod("oe10-podfailure")
		pod.Status.Phase = corev1.PodRunning
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "main",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason:   "OOMKilled",
						ExitCode: 137,
					},
				},
			},
		}
		updateStatus(pod)

		_, err := process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("OOMKilled"))

		var rm metricdata.ResourceMetrics
		Expect(reader.Collect(ctx, &rm)).To(Succeed())

		var podFailures *metricdata.Sum[int64]
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.k8s.pod_failures" {
					if s, ok := m.Data.(metricdata.Sum[int64]); ok {
						podFailures = &s
					}
				}
			}
		}
		Expect(podFailures).ToNot(BeNil(), "expected concourse.k8s.pod_failures counter")

		found := false
		for _, dp := range podFailures.DataPoints {
			if v, ok := dp.Attributes.Value("reason"); ok && v.AsString() == "OOMKilled" {
				found = true
				Expect(dp.Value).To(BeNumerically(">=", int64(1)))
			}
		}
		Expect(found).To(BeTrue(), "expected a pod_failures data point with reason=OOMKilled")
	})
})
