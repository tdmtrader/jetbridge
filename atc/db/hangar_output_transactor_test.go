package db_test

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The one field between a deferred refusal and an infinite retry loop.
//
// Two of this plane's constraint triggers are DEFERRED, so their refusals
// arrive at COMMIT and nowhere earlier, as a bare driver error carrying a
// SQLSTATE. A controller that read one of those as an ambiguous commit would
// retry a DENIAL forever -- the policy gate is the obvious one: it does not
// clear, so the retry does not end. controller.SQLTransactor exists to map
// them, and the mapping lives in a struct field.
//
// This is a real pool, a real deferred trigger and a real commit, because the
// classifier is only interesting against the error a real driver actually
// returns: a substring on the message could not tell a denial from a lost
// answer, which is the whole reason the SQLSTATE is what gets read.
var _ = Describe("the controller transactor", func() {
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
		Expect(repository.RecordRuntimeAtRisk(ctx, tx, 1, output.PolicyFinding{Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected object loss"})).To(Succeed())
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

	// A real, reachable pool of its own, so that "it refused" cannot be
	// confused with "it could not connect".
	pool := func() *sql.DB {
		GinkgoHelper()
		conn := postgresRunner.OpenSingleton()
		DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
		Expect(conn.Ping()).To(Succeed())

		return conn
	}

	// The field is nil-able and its absence used to be silent: Commit fell back
	// to returning the raw error. A fourth controller is one forgotten field
	// away from the exact bug Phase 6 found, and a wiring mistake is a
	// programming error rather than a runtime condition, so it is refused where
	// it is made rather than surfacing as untyped commits much later.
	It("refuses to begin at all when it was wired without a commit classifier", func() {
		transactor := controller.SQLTransactor{DB: pool()}

		transaction, err := transactor.Begin()
		Expect(transaction).To(BeNil())
		Expect(err).To(MatchError(output.ErrIncomplete))
		Expect(err.Error()).To(ContainSubstring("commit"))

		// And the pool really was usable, so the refusal is the classifier's
		// absence and not an unreachable database.
		working := controller.SQLTransactor{DB: transactor.DB, CommitError: db.HangarCommitError}
		usable, err := working.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(usable.Rollback()).To(Succeed())
	})

	It("types the deferred refusal a raw commit reports as a bare driver error", func() {
		transactor := controller.SQLTransactor{
			DB:          pool(),
			CommitError: db.HangarCommitError,
		}

		transaction, err := transactor.Begin()
		Expect(err).NotTo(HaveOccurred())
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
