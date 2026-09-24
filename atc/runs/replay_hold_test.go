package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A hold stops new Runs, not running ones. Replaying an invocation re-attaches
// to a Run that already exists -- a caller retrying after a lost response, a
// run_pipeline step resumed after a web restart -- so it succeeds at the port
// under either form of hold, while a new key is refused with the hold.
var _ = Describe("replaying an invocation under an admission hold", func() {
	var ctx context.Context

	const key = "replay-hold.admitted"

	BeforeEach(func() { ctx = context.Background() })

	admit := func(adm runs.Admission, epoch int64) (runs.Run, bool, error) {
		GinkgoHelper()
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		run, replayed, err := admitter.AdmitVersionedRun(ctx, tx, adm, epoch)
		if err != nil {
			return runs.Run{}, false, err
		}
		Expect(tx.Commit()).To(Succeed())
		return run, replayed, nil
	}

	countRuns := func() int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs`).Scan(&count)).To(Succeed())
		return count
	}

	var admitted runs.Run

	BeforeEach(func() {
		var err error
		admitted, _, err = admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: key}, testEpoch)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET admission_enabled=false WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("on a node configured with admission off", func() {
		BeforeEach(func() { atc.PipelineRunActivationEpoch = 0 })

		It("replays the Run the key admitted", func() {
			run, replayed, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: key}, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(replayed).To(BeTrue())
			Expect(run.ID).To(Equal(admitted.ID))
			Expect(countRuns()).To(Equal(1))
		})

		It("refuses a new key with the hold and writes nothing", func() {
			_, _, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: "replay-hold.new"}, 0)
			Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
			Expect(runs.IsRefusal(err)).To(BeTrue())
			Expect(countRuns()).To(Equal(1))
		})
	})

	Context("on a node still at the epoch while another holds the marker", func() {
		It("replays the Run the key admitted", func() {
			run, replayed, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: key}, testEpoch)
			Expect(err).NotTo(HaveOccurred())
			Expect(replayed).To(BeTrue())
			Expect(run.ID).To(Equal(admitted.ID))
		})

		It("refuses a new key and writes nothing", func() {
			_, _, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: "replay-hold.new"}, testEpoch)
			Expect(err).To(MatchError(runs.ErrVersionedAdmissionUnavailable))
			Expect(countRuns()).To(Equal(1))
		})
	})

	It("replays from a node on a newer epoch, and from one left on the older", func() {
		_, err := dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, testEpoch+1)
		Expect(err).NotTo(HaveOccurred())
		for _, nodeEpoch := range []int64{testEpoch + 1, testEpoch} {
			atc.PipelineRunActivationEpoch = nodeEpoch
			run, replayed, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal, ContractKey: key}, nodeEpoch)
			Expect(err).NotTo(HaveOccurred(), "node epoch %d", nodeEpoch)
			Expect(replayed).To(BeTrue())
			Expect(run.ID).To(Equal(admitted.ID))
		}
	})
})
