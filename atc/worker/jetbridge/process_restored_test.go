package jetbridge_test

// RESTORED 2026-09-18, from the file commit 3822b69a56 deleted whole.
//
// One It comes back: row JB-kept-002. It postdates the campaign's merge base
// (core 2a9355e1e6), so no both-red pairing was ever recorded for it, and its
// 2026-09-14 retirement note names live before-start/running cancellation
// cases, which are `@live-kubernetes` and do not run under `make test-unit`.
// The Unschedulable-timeout It (row JB-process-023) stays deleted: its newest
// entry is a 2026-09-11 real-API follow-up that names a non-live replacement.

import (
	"bytes"
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("Process (restored)", func() {
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

	// KEPT FROM CORE 2a9355e1e6 (added to process_test.go after this branch's
	// merge-base). No both-red evidence exists for it.
	It("supervised step teardown on context end deletes a supervised task's pause pod when context is cancelled", func() {
		fakeExecutor := &fakeExecExecutor{}
		execWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		execWorker.SetExecutor(fakeExecutor)

		// A get step's command runs on the exec stream and dies with it, so a
		// cancelled context leaves nothing running and the pod is worth
		// keeping for fly hijack. A supervised task step's command was started
		// with SIGHUP ignored and would otherwise keep running, so its pod is
		// torn down.

		taskContainer, _, err := execWorker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("exec-task-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())

		var deleteOptions []metav1.DeleteOptions
		fakeClientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
			deleteOptions = append(deleteOptions, action.(k8stesting.DeleteActionImpl).DeleteOptions)
			return false, nil, nil
		})

		// No Stdin: this is what makes the step supervised.
		process, err := taskContainer.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "trap '' TERM; sleep 600"},
		}, runtime.ProcessIO{Stdout: new(bytes.Buffer)})
		Expect(err).ToNot(HaveOccurred())

		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err = process.Wait(cancelCtx)
		Expect(err).To(HaveOccurred())

		By("verifying the abandoned pause pod was deleted, with no grace period")
		_, err = fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-task-handle", metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the task's pause pod to be gone")

		Expect(deleteOptions).To(HaveLen(1))
		Expect(deleteOptions[0].GracePeriodSeconds).ToNot(BeNil())
		Expect(*deleteOptions[0].GracePeriodSeconds).To(BeEquivalentTo(0))
	})
})
