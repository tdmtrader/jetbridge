package composition_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A run_pipeline step resumed after a web restart asks again for the child it
// already admitted. If the operator put admission on hold in the meantime, the
// restarted node speaks for no epoch -- and the child is still running. A hold
// stops new Runs, not running ones, so the step re-attaches rather than being
// refused and, on a rerun of the parent, admitting a second child.
var _ = Describe("re-attaching a resumed step under an admission hold", func() {
	var (
		ctx   context.Context
		req   composition.Request
		first composition.Result
	)

	BeforeEach(func() {
		ctx = context.Background()
		req = composition.Request{
			BuildID:     buildID,
			PlanID:      atc.PlanID("1/2"),
			Template:    templateRef,
			Principal:   principal,
			InputDigest: "sha256:aaa",
		}

		var err error
		first, err = service.Admit(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// The operator's hold, rolled out: every web node restarts with
		// web.pipelineRunActivationEpoch: 0 and the marker stops admitting.
		atc.PipelineRunActivationEpoch = 0
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET admission_enabled=false WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())
	})

	It("re-attaches to the child it already admitted", func() {
		restarted := composition.NewService(newAdmitter(dbConn), 0)

		again, err := restarted.Admit(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Replayed).To(BeTrue())
		Expect(again.RunID).To(Equal(first.RunID))
		Expect(again.Number).To(Equal(first.Number))
		Expect(countOf("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
	})

	It("still refuses a new call with the hold", func() {
		restarted := composition.NewService(newAdmitter(dbConn), 0)

		req.PlanID = atc.PlanID("1/3")
		_, err := restarted.Admit(ctx, req)
		Expect(err).To(MatchError(atc.ErrPipelineRunCreationDisabled))
		Expect(runs.IsRefusal(err)).To(BeTrue())
		Expect(countOf("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
	})
})
