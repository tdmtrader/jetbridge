package db_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The Run contract's own activation marker is moved by exactly one supported
// path: each web node reconciling it from --pipeline-run-activation-epoch at
// startup. It is independent of the Hangar output epoch.
var _ = Describe("Run activation reconciliation", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	marker := func() (int64, bool) {
		GinkgoHelper()
		var epoch int64
		var enabled bool
		Expect(dbConn.QueryRow(`SELECT epoch, admission_enabled FROM pipeline_run_activation WHERE singleton`).Scan(&epoch, &enabled)).To(Succeed())
		return epoch, enabled
	}

	It("admits at the configured epoch, and stops admitting at zero without moving the epoch", func() {
		state, err := db.ReconcilePipelineRunActivation(ctx, dbConn, 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(db.RunActivation{Epoch: 3, AdmissionEnabled: true}))
		epoch, enabled := marker()
		Expect(epoch).To(BeEquivalentTo(3))
		Expect(enabled).To(BeTrue())

		state, err = db.ReconcilePipelineRunActivation(ctx, dbConn, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(state.AdmissionEnabled).To(BeFalse())
		epoch, enabled = marker()
		Expect(epoch).To(BeEquivalentTo(3), "turning admission off keeps the recorded epoch")
		Expect(enabled).To(BeFalse())
	})

	It("does not need any Hangar epoch to admit", func() {
		var hangarEpochs int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs`).Scan(&hangarEpochs)).To(Succeed())
		Expect(hangarEpochs).To(BeZero())

		_, err := db.ReconcilePipelineRunActivation(ctx, dbConn, 1)
		Expect(err).NotTo(HaveOccurred())
		_, enabled := marker()
		Expect(enabled).To(BeTrue())
	})

	It("refuses an older epoch and leaves the marker as it was", func() {
		_, err := db.ReconcilePipelineRunActivation(ctx, dbConn, 5)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.ReconcilePipelineRunActivation(ctx, dbConn, 4)
		Expect(err).To(MatchError(db.RunActivationEpochRegressedError{Recorded: 5, Configured: 4}))

		epoch, enabled := marker()
		Expect(epoch).To(BeEquivalentTo(5))
		Expect(enabled).To(BeTrue())

		_, err = db.ReconcilePipelineRunActivation(ctx, dbConn, 6)
		Expect(err).NotTo(HaveOccurred())
		epoch, _ = marker()
		Expect(epoch).To(BeEquivalentTo(6))
	})
})
