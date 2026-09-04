package runs_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A11 parts 1 and 2. Unlike db_free_consumer_test.go this file may name atc/db
// types; it does not need to, because everything it asserts is either a port
// value or a row read with SQL. Reading created_by out of the column rather
// than off a model is deliberate: the claim is about what was persisted.
var _ = Describe("the port's own checks", func() {
	var ctx context.Context

	const contractKey = "admitter-test/some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	countRunRows := func() int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipeline_runs").Scan(&count)).To(Succeed())

		return count
	}

	admitWith := func(port runs.Admitter, adm runs.Admission) (runs.Run, error) {
		GinkgoHelper()
		tx, err := port.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		run, err := port.AdmitRun(ctx, tx, adm)
		if err != nil {
			return runs.Run{}, err
		}
		Expect(tx.Commit()).To(Succeed())

		return run, nil
	}

	Describe("authorization", func() {
		It("admits a principal the team's own auth makes a member, and records their display identity", func() {
			before := countRunRows()

			run, err := admitWith(admitter, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: contractKey,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(countRunRows()).To(Equal(before + 1))

			// The column, not the model: the claim is about what was written.
			var createdBy string
			Expect(dbConn.QueryRow("SELECT created_by FROM pipeline_runs WHERE id = $1", run.ID).
				Scan(&createdBy)).To(Succeed())
			Expect(createdBy).To(Equal("member-id"))
			Expect(createdBy).To(Equal(run.CreatedBy))
		})

		It("refuses a principal the team's auth makes only a viewer, and creates no row", func() {
			before := countRunRows()

			_, err := admitWith(admitter, runs.Admission{
				Template:    templateRef,
				Principal:   viewerPrincipal,
				ContractKey: contractKey,
			})
			Expect(err).To(MatchError(runs.ErrUnauthorized))
			Expect(countRunRows()).To(Equal(before))
		})

		// The predicate has one declaration, so the bound accessor puts on
		// custom roles binds here too: a mapping that made creating a run a
		// weaker capability than setting a config would be a privilege
		// escalation, because a run parameter is interpolated verbatim into
		// the materialized payload config.
		It("refuses a custom role mapping that makes run creation weaker than saving a config", func() {
			displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
			Expect(err).NotTo(HaveOccurred())

			weakened := runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds,
				map[string]string{atc.CreatePipelineRun: "viewer"})

			before := countRunRows()

			// The principal is a member and the template is fine: only the
			// operator's mapping is wrong, and it is refused rather than
			// honoured -- it is not quietly treated as "viewer is enough".
			_, err = admitWith(weakened, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: contractKey,
			})

			var invalid runs.CustomRolesInvalidError
			Expect(errors.As(err, &invalid)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring(atc.CreatePipelineRun))
			Expect(countRunRows()).To(Equal(before))
		})
	})

	Describe("the contract key", func() {
		It("refuses an admission with no key and creates no row", func() {
			before := countRunRows()

			_, err := admitWith(admitter, runs.Admission{
				Template:  templateRef,
				Principal: memberPrincipal,
			})
			Expect(err).To(MatchError(runs.ErrMissingContractKey))
			Expect(countRunRows()).To(Equal(before))
		})

		It("refuses an admission with an empty key and creates no row", func() {
			before := countRunRows()

			_, err := admitWith(admitter, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: "",
			})
			Expect(err).To(MatchError(runs.ErrMissingContractKey))
			Expect(countRunRows()).To(Equal(before))
		})

		// The key is opaque and unrecorded: the port stores nothing and reads
		// no consumer table to decide. A value no consumer would ever produce
		// is admitted exactly like a real one.
		It("accepts any non-empty key, because presence is the whole check", func() {
			_, err := admitWith(admitter, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: "not-a-key-any-consumer-would-mint",
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("the transaction handle", func() {
		It("refuses a transaction it did not open rather than panicking", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitter.AdmitRun(ctx, foreignTx{tx}, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: contractKey,
			})
			Expect(err).To(MatchError(runs.ForeignTransactionError{}))
		})
	})
})

// foreignTx is a Tx the port did not open: it satisfies the published
// interface and nothing more, which is exactly the case the bridge back to the
// concrete transaction type has to survive.
type foreignTx struct{ runs.Tx }
