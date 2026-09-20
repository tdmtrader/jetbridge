package jetbridge_test

// RESTORED 2026-09-18 by the disposition-ledger resolution pass.
//
// The ledger pass restored fourteen specs here: every test retired by a
// DISPOSITION-jetbridge.md row whose newest entry named no scenario, and whose
// replacement — once the paraphrase was resolved to an actual `Scenario:`
// line — turned out to live under `brine/features/live/`. A `@live-kubernetes`
// scenario runs only against a real cluster and never under `make test-unit`,
// so on an ordinary unit run nothing carried these contracts.
//
// Thirteen of the fourteen were restored in the same round, independently, by
// the round-2 live-only pass — verbatim from core `b294dafc49` and into their
// original files (`behavioral_runtime_spec_restored_test.go`,
// `container_restored_test.go`, `integration_restored_test.go`, all `package
// jetbridge`). The integration merge kept those originals and dropped the
// copies that were here, so this file now carries only the one spec nobody
// else restored:
//
//   JB-process-023                  live/startup-failure.feature:60
//
// The DISPOSITION rows for the other thirteen record both passes' findings.
// The body is the one at core `b294dafc49`, taken verbatim except for the
// fixture helper below. Nothing is weakened: every original Expect is carried
// over.
//
// This is a coverage restoration, not a regression report — it passes against
// the code at this commit.

import (
	"bytes"
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ledgerTask keeps only the defaults these fixtures share; every assertion
// stays in the spec that makes it.
func ledgerTask(worker *jetbridge.Worker, ctx context.Context, handle string, kind db.ContainerType, spec runtime.ContainerSpec) (runtime.Container, []runtime.VolumeMount, error) {
	if spec.TeamID == 0 {
		spec.TeamID = 1
	}
	if spec.ImageSpec.ImageURL == "" && spec.ImageSpec.ResourceType == "" {
		spec.ImageSpec.ImageURL = "docker:///busybox"
	}
	return worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: kind}, spec, &noopDelegate{})
}

// JB-process-023. Its newest entry (2026-09-14) named no scenario; the only
// brine case that exercises an unschedulable pod is
// live/startup-failure.feature:60 "A pod nothing can schedule fails the step
// instead of waiting", whose feature is tagged @live-kubernetes and whose
// step definition (steps/process_gaps.go:17) calls diagnoseLiveScheduling
// against a real cluster.
var _ = Describe("Ledger-restored process contracts", func() {
	var (
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		ctx           context.Context
		cfg           jetbridge.Config
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dbWorker, err = persistNamedWorker(useJetbridgeDB(), "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
	})

	It("execProcess failure state detection waits for Unschedulable pod and times out", func() {
		// Short timeouts so the test completes quickly.
		// PodSchedulingTimeout must be >= PodStartupTimeout since
		// waitForRunning extends the startup context to the scheduling
		// deadline when Unschedulable is detected.
		shortCfg := cfg
		shortCfg.PodSchedulingTimeout = 3 * time.Second
		shortCfg.PodStartupTimeout = 2 * time.Second
		shortWorker := jetbridge.NewWorker(dbWorker, fakeClientset, shortCfg)
		shortWorker.SetExecutor(&fakeExecExecutor{})

		shortContainer, _, err := ledgerTask(shortWorker, ctx, "exec-sched-timeout-handle", db.ContainerTypeGet,
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ResourceType: "git"},
				Type:      db.ContainerTypeGet,
			})
		Expect(err).ToNot(HaveOccurred())

		stderrBuf := new(bytes.Buffer)
		process, err := shortContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/opt/resource/in",
			Args: []string{"/tmp/build/get"},
		}, runtime.ProcessIO{
			Stdin:  bytes.NewBufferString(`{}`),
			Stdout: new(bytes.Buffer),
			Stderr: stderrBuf,
		})
		Expect(err).ToNot(HaveOccurred())

		pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-timeout-handle", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: insufficient cpu.",
			},
		}
		_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())

		_, err = process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("pod scheduling timeout"))
		Expect(err.Error()).To(ContainSubstring("Unschedulable"))
		Expect(stderrBuf.String()).To(ContainSubstring("waiting up to"))
		Expect(stderrBuf.String()).To(ContainSubstring("cluster resources"))
	})
})
