package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The port's one read operation.
//
// It exists because a consumer that recorded a run id and came back later
// holds an id and nothing else, and core will not have it read pipeline_runs
// for the rest -- that is the coupling the whole package exists to prevent, and
// it would be invisible to an import graph because SQL names no packages.
var _ = Describe("looking an admitted run up by id", func() {
	var ctx context.Context

	const contractKey = "lookup-run-test/some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("returns exactly what admission returned", func() {
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		admitted, err := admitter.AdmitRun(ctx, tx, runs.Admission{
			Template:    templateRef,
			Principal:   memberPrincipal,
			ContractKey: contractKey,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		read, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer read.Rollback()

		looked, err := admitter.LookupRun(ctx, read, admitted.ID)
		Expect(err).NotTo(HaveOccurred())

		// The whole struct, not a field of it. Run has five fields and a read
		// that filled four of them would be a read that quietly reports a zero
		// payload pipeline or a zero number, which is the shape of bug a
		// per-field assertion invites.
		Expect(looked).To(Equal(admitted))

		// And the values are real rather than jointly zero, which Equal alone
		// would also accept.
		Expect(looked.ID).To(BeNumerically(">", 0))
		Expect(looked.Number).To(Equal(1))
		Expect(looked.TemplatePipelineID).To(BeNumerically(">", 0))
		Expect(looked.PayloadPipelineID).To(BeNumerically(">", 0))
		Expect(looked.PayloadPipelineID).NotTo(Equal(looked.TemplatePipelineID))
		Expect(looked.CreatedBy).To(Equal("member-id"))
	})

	// The number is the reason this operation exists at all: it is what a
	// person is shown and what fly addresses a run by, and it is the one field
	// on Run that a consumer cannot reconstruct from an id it recorded.
	It("distinguishes the runs of one template by their number", func() {
		var admitted []runs.Run
		for range 3 {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())

			run, err := admitter.AdmitRun(ctx, tx, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: contractKey,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			admitted = append(admitted, run)
		}

		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		for i, run := range admitted {
			looked, err := admitter.LookupRun(ctx, tx, run.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(looked.Number).To(Equal(i + 1))
		}
	})

	// The read goes through the caller's transaction, and this is what says
	// so. A read that went to the pool instead would not see this row at all
	// -- it is uncommitted -- and on the one-connection pool this suite runs
	// against it would not even get that far: it would park waiting for a
	// second connection while this transaction holds the only one. Either way
	// the failure is here rather than in production.
	It("sees a run the caller has admitted but not yet committed", func() {
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		admitted, err := admitter.AdmitRun(ctx, tx, runs.Admission{
			Template:    templateRef,
			Principal:   memberPrincipal,
			ContractKey: contractKey,
		})
		Expect(err).NotTo(HaveOccurred())

		looked, err := admitter.LookupRun(ctx, tx, admitted.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(looked).To(Equal(admitted))
	})

	Describe("an id that names no run", func() {
		// BeIdenticalTo rather than MatchError, for the reason
		// db_free_consumer_test.go states at length: the port's sentinels are
		// asserted by identity so that an untranslated error of equal text
		// cannot pass for one.
		It("is the port's own refusal", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitter.LookupRun(ctx, tx, 0)
			Expect(err).To(BeIdenticalTo(runs.ErrRunNotFound))

			_, err = admitter.LookupRun(ctx, tx, 999999)
			Expect(err).To(BeIdenticalTo(runs.ErrRunNotFound))
		})

		// The case the refusal actually reports in production: a run that
		// existed when the id was recorded and does not now. A rolled-back
		// admission produces the same state as a deleted run from a later
		// reader's point of view, and it is the only way to produce it here
		// without deleting a row behind the port's back.
		It("covers a run that was admitted and never committed", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())

			admitted, err := admitter.AdmitRun(ctx, tx, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: contractKey,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Rollback()).To(Succeed())

			read, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer read.Rollback()

			_, err = admitter.LookupRun(ctx, read, admitted.ID)
			Expect(err).To(BeIdenticalTo(runs.ErrRunNotFound))
		})
	})

	// A lookup is a read of a row the caller already holds the id of, so there
	// is no name to guess at and nothing for an existence oracle to leak. It
	// therefore decides no authorization, and this says so out loud rather
	// than leaving it to be inferred from the absence of a principal in the
	// signature.
	It("takes no principal and decides no authorization", func() {
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		// Admitted by a member of runs-team, read back by a caller presenting
		// no identity at all -- there is nowhere in the signature to put one.
		admitted, err := admitter.AdmitRun(ctx, tx, runs.Admission{
			Template:    templateRef,
			Principal:   memberPrincipal,
			ContractKey: contractKey,
		})
		Expect(err).NotTo(HaveOccurred())

		looked, err := admitter.LookupRun(ctx, tx, admitted.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(looked.ID).To(Equal(admitted.ID))
	})

	// Unlike AdmitRun, which has to hand its Tx back to the run factory and so
	// bridges to the concrete transaction type, this reads through the
	// published interface and nothing else. A Tx the port did not open is a
	// transaction its owner opened somewhere else, and a perfectly good place
	// to read from.
	It("reads through a transaction handle it did not open", func() {
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		admitted, err := admitter.AdmitRun(ctx, tx, runs.Admission{
			Template:    templateRef,
			Principal:   memberPrincipal,
			ContractKey: contractKey,
		})
		Expect(err).NotTo(HaveOccurred())

		looked, err := admitter.LookupRun(ctx, foreignTx{tx}, admitted.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(looked).To(Equal(admitted))
	})
})
