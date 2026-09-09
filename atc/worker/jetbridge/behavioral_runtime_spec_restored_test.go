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
	"io"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// These are retained Go contracts, not evidence that the historical brine
// GAP/REFUTED rows have closed. Tables share execution, not weaker assertions.
var _ = Describe("Restored runtime contracts", func() {
	var (
		ctx       context.Context
		clientset *fake.Clientset
		worker    *jetbridge.Worker
		delegate  runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbWorker, err := persistNamedWorker(useJetbridgeDB(), "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		clientset = fake.NewSimpleClientset()
		worker = jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""))
		delegate = &noopDelegate{}
	})

	createContainer := func(handle string, spec runtime.ContainerSpec) runtime.Container {
		GinkgoHelper()
		// All five originals use this exact image, not docker:///busybox.
		spec.ImageSpec = runtime.ImageSpec{ImageURL: "busybox"}
		container, _, err := restoredTask(worker, ctx, handle, spec, delegate)
		Expect(err).ToNot(HaveOccurred())
		return container
	}

	getPod := func(handle string) *corev1.Pod {
		GinkgoHelper()
		pod, err := clientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return pod
	}

	waitForPod := func(process runtime.Process, handle string, phase corev1.PodPhase) {
		GinkgoHelper()
		pod := getPod(handle)
		pod.Status.Phase = phase
		if phase == corev1.PodSucceeded {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  "main",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}}
		}
		_, err := clientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))
	}

	Describe("[PE-08] TTY flag in exec mode", func() {
		var executor *fakeExecExecutor
		BeforeEach(func() {
			executor = &fakeExecExecutor{}
			worker.SetExecutor(executor)
		})

		DescribeTable("forwards the requested terminal mode",
			func(handle string, tty *runtime.TTYSpec, wantTTY bool) {
				container := createContainer(handle, runtime.ContainerSpec{})
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					TTY:  tty,
				}, runtime.ProcessIO{
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())
				waitForPod(process, handle, corev1.PodRunning)

				executor.mu.Lock()
				calls := executor.execCalls
				executor.mu.Unlock()
				Expect(calls).To(HaveLen(1))
				Expect(calls[0].tty).To(Equal(wantTTY), "expected TTY=%v to be passed to ExecInPod", wantTTY)
			},
			Entry("[PE-08] passes TTY=true to ExecInPod when ProcessSpec.TTY is set",
				"pe08-tty-handle", &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 80, Rows: 24}}, true),
			Entry("[PE-08] passes TTY=false to ExecInPod when ProcessSpec.TTY is nil",
				"pe08-notty-handle", (*runtime.TTYSpec)(nil), false),
		)
	})

	Describe("[SC-07] Sidecar log streaming routing (direct mode)", func() {
		DescribeTable("retains the log request on both writer paths",
			func(handle string, sidecar atc.SidecarConfig, dedicated bool) {
				container := createContainer(handle, runtime.ContainerSpec{
					Dir:      "/workdir",
					Sidecars: []atc.SidecarConfig{sidecar},
				})
				processIO := runtime.ProcessIO{Stdout: new(bytes.Buffer)}
				if dedicated {
					processIO.SidecarWriters = map[string]io.Writer{sidecar.Name: new(bytes.Buffer)}
				}
				process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh"}, processIO)
				Expect(err).ToNot(HaveOccurred())
				waitForPod(process, handle, corev1.PodSucceeded)

				// Preserve the original, weak observation: any GetLogs request.
				// This does not prove a particular sidecar or output destination.
				var logRequested bool
				for _, action := range clientset.Actions() {
					if action.GetVerb() == "get" && action.GetSubresource() == "log" {
						logRequested = true
						break
					}
				}
				Expect(logRequested).To(BeTrue(), "expected GetLogs on the sidecar writer path (dedicated=%v)", dedicated)
			},
			Entry("[SC-07] when SidecarWriters contains an entry, GetLogs is requested for the sidecar container by name",
				"sc07-dedicated-handle", atc.SidecarConfig{Name: "postgres", Image: "postgres:15"}, true),
			Entry("[SC-07] when SidecarWriters is empty, GetLogs is still requested for the sidecar (prefix fallback path)",
				"sc07-prefix-handle", atc.SidecarConfig{Name: "redis", Image: "redis:7"}, false),
		)
	})

	Describe("[P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15)", func() {
		It("PE-02: direct mode command embedding [PE-02] bakes the real command into the main container (no pause pod) and counts the container", func() {

			// No executor: this explicitly retains the direct-mode contract.
			container := createContainer("pe02-direct", runtime.ContainerSpec{
				Dir:  "/workdir",
				Type: db.ContainerTypeTask,
			})
			metric.Metrics.ContainersCreated.Delta()

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
			Expect(main.Command).To(Equal([]string{"/opt/resource/in"}))
			Expect(main.Args).To(Equal([]string{"/tmp/build/get"}))
			Expect(main.Command).ToNot(ContainElement("sh"), "direct mode must not use the pause-pod sleep command")
			Expect(metric.Metrics.ContainersCreated.Delta()).To(BeNumerically(">=", float64(1)))

		})
	})
})
