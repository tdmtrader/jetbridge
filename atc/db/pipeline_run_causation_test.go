package db_test

import (
	"context"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/runinput"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Requirements 16, 43 and 44 of the durable Run invocation contract: a v2 Run
// may carry one opaque correlation and one caused_by_run edge to an earlier
// Run of its team. Both are immutable caller intent, covered by the
// caller-intent digest, and refused without an existence leak.
var _ = Describe("Run causation and correlation", func() {
	var (
		ctx      context.Context
		factory  db.PipelineRunFactory
		template db.Pipeline
		owner    = runinput.PrincipalDigest("local:owner")
	)

	config := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}

	BeforeEach(func() {
		ctx = context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("run-causation-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "caused"}, config, 0, false)
		Expect(err).NotTo(HaveOccurred())
	})

	create := func(on db.Pipeline, key string, cause *int, correlation string) (db.RunCreation, error) {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := factory.CreateRunInTx(ctx, tx, on, db.RunParams{}, "owner", db.RunCreationOpts{
			ActivationEpoch: 1,
			HangarEpoch:     1,
			Invocation:      &db.RunInvocationIdentity{PrincipalDigest: owner, KeyDigest: strings.Repeat(key, 64)},
			CausedByRun:     cause,
			Correlation:     correlation,
		})
		if err != nil {
			return db.RunCreation{}, err
		}
		Expect(tx.Commit()).To(Succeed())
		return creation, nil
	}

	lastRunNumber := func() int {
		GinkgoHelper()
		var number int
		Expect(dbConn.QueryRow(`SELECT last_run_number FROM pipelines WHERE id=$1`, template.ID()).Scan(&number)).To(Succeed())
		return number
	}

	It("retains an earlier same-team cause and a correlation as immutable caller intent", func() {
		predecessor, err := create(template, "a", nil, "")
		Expect(err).NotTo(HaveOccurred())
		cause := predecessor.Run.ID()

		successor, err := create(template, "b", &cause, "review.batch-7~x")
		Expect(err).NotTo(HaveOccurred())
		Expect(successor.Replayed).To(BeFalse())
		Expect(successor.Run.CausedByRun()).To(Equal(&cause))
		Expect(successor.Run.Correlation()).To(Equal("review.batch-7~x"))

		read, found, err := factory.GetRunByID(successor.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(read.CausedByRun()).To(Equal(&cause))
		Expect(read.Correlation()).To(Equal("review.batch-7~x"))

		legacyRead, _, err := factory.GetRunByID(predecessor.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(legacyRead.CausedByRun()).To(BeNil())
		Expect(legacyRead.Correlation()).To(BeEmpty())

		By("replaying identical intent")
		replay, err := create(template, "b", &cause, "review.batch-7~x")
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.Replayed).To(BeTrue())
		Expect(replay.Run.ID()).To(Equal(successor.Run.ID()))

		By("refusing a changed, added or removed cause or correlation under the same key")
		other := predecessor.Run.ID()
		earlier, err := create(template, "c", nil, "")
		Expect(err).NotTo(HaveOccurred())
		moved := earlier.Run.ID()
		for _, changed := range []struct {
			cause       *int
			correlation string
		}{
			{&moved, "review.batch-7~x"},
			{nil, "review.batch-7~x"},
			{&cause, "review.batch-8"},
			{&cause, ""},
		} {
			_, err := create(template, "b", changed.cause, changed.correlation)
			Expect(err).To(MatchError(db.ErrRunInvocationConflict))
		}
		_, err = create(template, "a", &other, "")
		Expect(err).To(MatchError(db.ErrRunInvocationConflict), "a cause added on replay is changed intent")
		Expect(lastRunNumber()).To(Equal(3))

		By("refusing mutation of the retained edge and correlation")
		for _, query := range []string{
			`UPDATE pipeline_runs SET correlation='changed' WHERE id=$1`,
			`UPDATE pipeline_runs SET correlation=NULL WHERE id=$1`,
			`UPDATE pipeline_runs SET caused_by_run=NULL WHERE id=$1`,
		} {
			_, err := dbConn.Exec(query, successor.Run.ID())
			Expect(err).To(MatchError(ContainSubstring("immutable")), query)
		}
	})

	It("refuses a missing, later, or other-team cause alike and allocates nothing", func() {
		otherTeam, err := teamFactory.CreateTeam(atc.Team{Name: "cause-other-team"})
		Expect(err).NotTo(HaveOccurred())
		foreignTemplate, _, err := otherTeam.SavePipeline(atc.PipelineRef{Name: "foreign"}, config, 0, false)
		Expect(err).NotTo(HaveOccurred())
		foreign, err := create(foreignTemplate, "f", nil, "")
		Expect(err).NotTo(HaveOccurred())

		foreignID := foreign.Run.ID()
		missing := foreignID + 1_000_000
		zero := 0
		for _, cause := range []*int{&foreignID, &missing, &zero} {
			_, err := create(template, "d", cause, "")
			Expect(err).To(MatchError(db.ErrRunCauseUnavailable))
		}
		Expect(lastRunNumber()).To(BeZero())
		var runs int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id=$1`, template.ID()).Scan(&runs)).To(Succeed())
		Expect(runs).To(BeZero())
	})

	It("requires an invocation for causation and correlation, and bounds both in the schema", func() {
		// The suite's pool may hold one connection; finish this transaction
		// before create() asks for another.
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		one := 1
		_, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{CausedByRun: &one})
		Expect(err).To(MatchError(db.ErrInvalidRunInvocation))
		_, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{Correlation: "x"})
		Expect(err).To(MatchError(db.ErrInvalidRunInvocation))
		Expect(tx.Rollback()).To(Succeed())

		for _, bad := range []string{strings.Repeat("x", 129), "has space", "slash/ed", "é"} {
			_, err := create(template, "e", nil, bad)
			Expect(err).To(MatchError(db.ErrInvalidRunInvocation), bad)
		}
		_, err = create(template, "e", nil, strings.Repeat("x", 128))
		Expect(err).NotTo(HaveOccurred())

		plain, err := dbtest.CreateRun(dbConn, factory, ctx, template, db.RunParams{}, "owner")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER run_causation_immutable`)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			_, err := dbConn.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER run_causation_immutable`)
			Expect(err).NotTo(HaveOccurred())
		}()
		_, err = dbConn.Exec(`UPDATE pipeline_runs SET correlation='not/allowed' WHERE id=$1`, plain.Run.ID())
		Expect(err).To(MatchError(ContainSubstring("pipeline_run_correlation")), "a correlation outside the alphabet is refused by the schema")
		_, err = dbConn.Exec(`UPDATE pipeline_runs SET caused_by_run=id WHERE id=$1`, plain.Run.ID())
		Expect(err).To(MatchError(ContainSubstring("pipeline_run_causation")))
	})
})
