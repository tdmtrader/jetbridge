package composition_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Observe and Cancel are the port's, passed through: the port authorizes and
// runs its own transaction, and the service adds nothing to either.
var _ = Describe("observing and cancelling a child run", func() {
	var (
		ctx   context.Context
		child composition.Result
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		child, err = service.Admit(ctx, composition.Request{
			BuildID:     buildID,
			PlanID:      atc.PlanID("4/1"),
			Template:    templateRef,
			Principal:   principal,
			InputDigest: "sha256:aaa",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("observes the run the call admitted", func() {
		observed, err := service.Observe(ctx, child.RunID, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(observed.Run.ID).To(Equal(child.RunID))
		Expect(observed.Run.Number).To(Equal(child.Number))
		Expect(observed.Status).To(Equal(atc.RunStatusRunning))
		Expect(observed.CancellationRequested).To(BeFalse())
	})

	It("cancels it, and a second cancel is not an error", func() {
		Expect(service.Cancel(ctx, child.RunID, principal, "parent aborted")).To(Succeed())
		Expect(service.Cancel(ctx, child.RunID, principal, "again")).To(Succeed())

		observed, err := service.Observe(ctx, child.RunID, principal)
		Expect(err).NotTo(HaveOccurred())
		Expect(observed.CancellationRequested).To(BeTrue())

		var by, reason string
		Expect(dbConn.QueryRow(`SELECT cancel_requested_by, cancel_reason FROM pipeline_runs WHERE id = $1`, child.RunID).
			Scan(&by, &reason)).To(Succeed())
		Expect(by).To(Equal("local:composer"))
		Expect(reason).To(Equal("parent aborted"))
	})

	It("passes the port's refusals through unchanged", func() {
		stranger := runs.Principal{Claims: map[string]any{
			"sub":              "local:stranger",
			"name":             "stranger",
			"federated_claims": map[string]any{"connector_id": "local", "user_id": "stranger-id"},
		}}

		_, err := service.Observe(ctx, child.RunID, stranger)
		Expect(err).To(BeIdenticalTo(runs.ErrUnauthorized))
		Expect(service.Cancel(ctx, child.RunID, stranger, "")).To(BeIdenticalTo(runs.ErrUnauthorized))
	})
})
