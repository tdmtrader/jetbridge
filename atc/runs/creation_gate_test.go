package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The operator's hold, at the port.
//
// atc/integration/pipeline_run_gate_test.go proves it for the HTTP route
// against a booted server. This file proves the other path: an in-process
// caller -- the run_pipeline step's admitter, today the only one -- is refused
// by the same sentinel and leaves the same nothing behind.
//
// The rows are counted against real Postgres rather than asserted about a
// double, because the claim is that no row was written. A double would only
// show that the factory was not called, which is a different and weaker
// statement: the payload pipeline, the entry builds and the notification are
// all written by things the port calls in turn.
var _ = Describe("the operator's hold on run creation", func() {
	var ctx context.Context

	const contractKey = "creation-gate-test/some-call"

	BeforeEach(func() {
		ctx = context.Background()

		// The suite's BeforeEach opened the gate and registered its restore,
		// so closing it here lasts exactly as long as this spec.
		atc.EnablePipelineRunCreation = false
	})

	admit := func(adm runs.Admission) (runs.Run, error) {
		GinkgoHelper()

		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		if adm.ContractKey == "" {
			adm.ContractKey = contractKey
		}

		run, err := admitter.AdmitRun(ctx, tx, adm)
		if err != nil {
			return runs.Run{}, err
		}
		Expect(tx.Commit()).To(Succeed())

		return run, nil
	}

	countRows := func(query string) int {
		GinkgoHelper()

		var count int
		Expect(dbConn.QueryRow(query).Scan(&count)).To(Succeed())

		return count
	}

	assertNothingWasWritten := func() {
		GinkgoHelper()

		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
		Expect(countRows("SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL")).To(Equal(0))
	}

	// Both principal forms, because the hold is a property of the server and
	// not of who is asking. A gate that only caught the build would be a gate
	// the next consumer walks around.
	DescribeTable("refusing every principal while the hold is on",
		func(principal func() runs.Principal) {
			_, err := admit(runs.Admission{Template: templateRef, Principal: principal()})
			Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
			assertNothingWasWritten()
		},
		Entry("a build acting for itself", func() runs.Principal { return buildPrincipal }),
		Entry("a member's verified claims", func() runs.Principal { return memberPrincipal }),
		Entry("an admin's verified claims", func() runs.Principal { return adminPrincipal }),
	)

	// A refusal and not a fault: the step prints it on the build's stderr and
	// fails, rather than erroring and being retried against a server that is
	// never going to say yes on its own.
	It("is a refusal, so the step reports it rather than retrying it", func() {
		_, err := admit(runs.Admission{Template: templateRef, Principal: buildPrincipal})
		Expect(runs.IsRefusal(err)).To(BeTrue())
	})

	// Ahead of the contract key, ahead of the principal, ahead of the
	// reference. Each of these admissions is malformed in a second way that
	// the port would otherwise report first, and the hold is what comes back:
	// a server that is not creating runs weighs nothing about the call.
	DescribeTable("answering the hold before anything about the call",
		func(adm runs.Admission) {
			_, err := admit(adm)
			Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
		},
		Entry("an admission with no contract key", runs.Admission{
			Template:    templateRef,
			Principal:   runs.Principal{},
			ContractKey: " ",
		}),
		Entry("a principal presenting neither form", runs.Admission{
			Template: templateRef,
		}),
		Entry("a template on a team the principal has no standing on", runs.Admission{
			Template: otherTeamRef,
		}),
	)

	Context("when the operator has opened it again", func() {
		BeforeEach(func() {
			atc.EnablePipelineRunCreation = true
		})

		// The same admission, admitted. Without this the specs above would
		// hold on a port that refused everything for any reason at all.
		It("admits the identical admission and writes the run", func() {
			run, err := admit(runs.Admission{Template: templateRef, Principal: buildPrincipal})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.Number).To(Equal(1))

			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
			Expect(countRows("SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL")).To(Equal(1))
		})
	})
})
