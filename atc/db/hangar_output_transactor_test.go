package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The one field between a deferred refusal and an infinite retry loop.
//
// Two of this plane's constraint triggers are DEFERRED, so their refusals
// arrive at COMMIT and nowhere earlier, as a bare driver error carrying a
// SQLSTATE. A controller that read one of those as an ambiguous commit would
// retry a DENIAL forever -- the policy gate is the obvious one: it does not
// clear, so the retry does not end. db.HangarOutputTx exists to map them: it is
// the transaction the web hands the capture coordinator and the reclaim pass.
//
// This is a real pool, a real deferred trigger and a real commit, because the
// classifier is only interesting against the error a real driver actually
// returns: a substring on the message could not tell a denial from a lost
// answer, which is the whole reason the SQLSTATE is what gets read.
var _ = Describe("the output-plane transaction", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		ref        hangar.TreeRef
	)

	BeforeEach(func() {
		ctx = context.Background()

		consumer, err := db.HangarConsumerPrefixHeld("transactor-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
		_, ref = hangarPublish(ctx, repository, hangarDigest(71), 1725830823000071)

		// The epoch's lifetime policy goes to at-risk, which is what the
		// deferred hangar_policy_admits_new_protection refuses new protection
		// under. It fires at the COMMIT of the claim below, not at its INSERT.
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(repository.RecordRuntimeAtRisk(ctx, tx, output.IntegrityFindingRecord{Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected object loss"})).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
	})

	claim := func(tx output.Tx) error {
		return repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           output.ClaimID(uuid.NewString()),
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID("binding-transactor"),
			RequestedAt:       output.NewTimestamp(time.Now()),
		})
	}

	It("types the deferred refusal a raw commit reports as a bare driver error", func() {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		transaction := db.HangarOutputTx{Tx: tx}
		defer func() { _ = transaction.Rollback() }()

		Expect(claim(transaction)).To(Succeed(),
			"the INSERT itself was refused, so this spec is no longer about a DEFERRED refusal")
		Expect(transaction.Commit()).To(MatchError(output.ErrAtRisk))
	})

	// THE CONTROL. The same commit without the classifier is a bare driver
	// error carrying a SQLSTATE and nothing a caller can branch on, which is
	// the state a fourth controller would be in one forgotten field away.
	It("is what makes the difference: a raw commit says nothing typed", func() {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)

		Expect(claim(tx)).To(Succeed())
		err = tx.Commit()
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(output.ErrAtRisk),
			"the raw driver commit came back already typed, so the classifier is not what "+
				"types it and this whole spec proves nothing")
	})
})
