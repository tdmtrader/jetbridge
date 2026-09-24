package jetbridge_test

// RESTORED 2026-09-18, from the file commit 3822b69a56 deleted whole.
//
// Round 1 brought back one Entry: row JB-behavioral_runtime_spec-010, the
// SC-07 prefix fallback path. Its newest ledger entry before the deletion was
// the 2026-09-08 twenty-ninth-pass **RETAIN Go** on this DescribeTable, and
// the 2026-09-18 retirement replaced it with a live sidecar-logs case, which
// is `@live-kubernetes` and does not run under `make test-unit`.
//
// Round 2 (2026-09-18) applies the same rule to its siblings and brings back
// three more:
//
//   - JB-behavioral_runtime_spec-009, the SC-07 dedicated-writer Entry. It was
//     retired against the same `features/live/sidecar-logs.feature`, so the
//     round-1 note that it "stays deleted" was wrong on the pass's own rule.
//   - JB-behavioral_runtime_spec-007 and -008, the PE-08 TTY-true/TTY-false
//     Entries, retired 2026-09-15 against live terminal rows that execute a
//     real resource process and a real supervised task on a cluster.
//
// PE-02 (row -031) stays deleted: its replacement is the direct-mode outline in
// `features/container-run.feature`, which is not a live-tier feature.
//
// These are retained Go contracts, not evidence that the historical
// GAP/REFUTED rows have closed. Tables share execution, not weaker assertions.

import (
	"bytes"
	"context"
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

var _ = Describe("Restored runtime contracts", func() {
	var (
		ctx       context.Context
		clientset *fake.Clientset
		dbWorker  db.Worker
		worker    *jetbridge.Worker
		delegate  runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dbWorker, err = persistNamedWorker(useJetbridgeDB(), "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		clientset = fake.NewSimpleClientset()
		worker = jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""), jetbridge.WorkerDeps{})
		delegate = &noopDelegate{}
	})

	createContainer := func(handle string, spec runtime.ContainerSpec) runtime.Container {
		GinkgoHelper()
		// All the originals use this exact image, not docker:///busybox.
		spec.ImageSpec = runtime.ImageSpec{ImageURL: "busybox"}
		container, _, err := restoredTask(worker, ctx, handle, spec, delegate)
		Expect(err).ToNot(HaveOccurred())
		return container
	}

	waitForPod := func(process runtime.Process, handle string, phase corev1.PodPhase) {
		GinkgoHelper()
		pod, err := clientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = phase
		if phase == corev1.PodSucceeded {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  "main",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}}
		}
		_, err = clientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
		result, err := process.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(0))
	}

	Describe("[PE-08] TTY flag in exec mode", func() {
		var executor *fakeExecExecutor
		BeforeEach(func() {
			executor = &fakeExecExecutor{}
			worker = jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""), jetbridge.WorkerDeps{Executor: executor})
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
})
