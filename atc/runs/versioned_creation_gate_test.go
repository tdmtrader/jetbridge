package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the operator's hold on versioned run creation", func() {
	BeforeEach(func() {
		// The suite restores the process setting after each spec.
		atc.EnablePipelineRunCreation = false
	})

	It("answers the hold before invocation validation or a transaction is needed", func() {
		for _, admission := range []runs.Admission{
			{},
			{ContractKey: "valid-key"},
			{Template: templateRef, Principal: memberPrincipal, ContractKey: "valid-key"},
		} {
			run, replayed, err := admitter.AdmitVersionedRun(context.Background(), nil, admission, 1)
			Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
			Expect(run).To(BeZero())
			Expect(replayed).To(BeFalse())
		}
	})

	It("writes no run, invocation, payload or run number even if the caller commits", func() {
		ctx := context.Background()
		var beforeNumber, beforeBuilds int
		Expect(dbConn.QueryRow("SELECT last_run_number FROM pipelines WHERE id = $1", templatePipeline.ID()).Scan(&beforeNumber)).To(Succeed())
		Expect(dbConn.QueryRow("SELECT count(*) FROM builds").Scan(&beforeBuilds)).To(Succeed())
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		_, replayed, err := admitter.AdmitVersionedRun(ctx, tx, runs.Admission{
			Template: templateRef, Principal: memberPrincipal, ContractKey: "held-versioned-run",
		}, 1)
		Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
		Expect(replayed).To(BeFalse())
		Expect(tx.Commit()).To(Succeed())

		for _, query := range []string{
			"SELECT count(*) FROM pipeline_runs",
			"SELECT count(*) FROM pipeline_run_invocations",
			"SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL",
		} {
			var count int
			Expect(dbConn.QueryRow(query).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero(), query)
		}
		var number, builds int
		Expect(dbConn.QueryRow("SELECT last_run_number FROM pipelines WHERE id = $1", templatePipeline.ID()).Scan(&number)).To(Succeed())
		Expect(number).To(Equal(beforeNumber))
		Expect(dbConn.QueryRow("SELECT count(*) FROM builds").Scan(&builds)).To(Succeed())
		Expect(builds).To(Equal(beforeBuilds))
	})

	It("resumes ordinary invocation validation once the operator opens the gate", func() {
		atc.EnablePipelineRunCreation = true
		_, _, err := admitter.AdmitVersionedRun(context.Background(), nil, runs.Admission{}, 1)
		Expect(err).To(MatchError(runs.ErrInvalidInvocationKey))
	})
})
