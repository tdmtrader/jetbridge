package composition_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/agent/composition"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("admitting a child run", func() {
	var (
		ctx context.Context
		req composition.Request
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
	})

	// A1. Every run id read here comes from composition_iterations, never from
	// composition_calls: the call row deliberately has no run id column.
	Describe("idempotence under (build_id, plan_id)", func() {
		It("re-attaches to the run it already admitted", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Replayed).To(BeFalse())
			Expect(first.RunID).To(BeNumerically(">", 0))

			second, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.RunID).To(Equal(first.RunID))
			Expect(second.Replayed).To(BeTrue())

			Expect(countOf("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
			Expect(countOf("SELECT count(*) FROM composition_calls")).To(Equal(1))
			Expect(countOf("SELECT count(*) FROM composition_iterations")).To(Equal(1))

			// One payload pipeline for the run, and the iteration row names
			// exactly the run both calls reported.
			Expect(countOf("SELECT count(*) FROM pipelines WHERE pipeline_run_id = $1", first.RunID)).
				To(Equal(1))
			Expect(iterationRunID(buildID, "1/2")).To(Equal(first.RunID))
		})
	})

	// A3.
	Describe("a moved sealed-input digest", func() {
		It("is a typed conflict naming both digests, and admits nothing", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			moved := req
			moved.InputDigest = "sha256:bbb"

			_, err = service.Admit(ctx, moved)

			var conflict composition.DigestConflictError
			Expect(errorsAs(err, &conflict)).To(BeTrue())
			Expect(conflict.Recorded).To(Equal("sha256:aaa"))
			Expect(conflict.Presented).To(Equal("sha256:bbb"))

			// The variant the criterion asks for: an implementation that
			// admitted on a moved digest would have keyed on the digest, and
			// the run count would have moved with it. It did not.
			Expect(countOf("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
			Expect(countOf("SELECT count(*) FROM composition_calls")).To(Equal(1))
			Expect(countOf("SELECT count(*) FROM composition_iterations")).To(Equal(1))
			Expect(iterationRunID(buildID, "1/2")).To(Equal(first.RunID))

			// The recorded digest is not overwritten by the presented one.
			Expect(callDigest(buildID, "1/2")).To(Equal("sha256:aaa"))
		})
	})

	// A4.
	Describe("distinctness", func() {
		It("treats a new build_id as a new invocation", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Replayed).To(BeFalse())

			rerun := req
			rerun.BuildID = otherBuildID

			second, err := service.Admit(ctx, rerun)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Replayed).To(BeFalse())
			Expect(second.RunID).NotTo(Equal(first.RunID))

			Expect(countOf("SELECT count(*) FROM composition_calls")).To(Equal(2))
			Expect(countOf("SELECT count(*) FROM composition_iterations")).To(Equal(2))
			Expect(iterationRunID(buildID, "1/2")).To(Equal(first.RunID))
			Expect(iterationRunID(otherBuildID, "1/2")).To(Equal(second.RunID))

			// The earlier run is untouched and still reachable.
			Expect(countOf("SELECT count(*) FROM pipeline_runs WHERE id = $1", first.RunID)).
				To(Equal(1))
		})

		It("treats a second plan id in the same build as a new invocation", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Replayed).To(BeFalse())

			sibling := req
			sibling.PlanID = atc.PlanID("1/3")

			second, err := service.Admit(ctx, sibling)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Replayed).To(BeFalse())
			Expect(second.RunID).NotTo(Equal(first.RunID))

			Expect(countOf("SELECT count(*) FROM composition_calls")).To(Equal(2))
			Expect(countOf("SELECT count(*) FROM composition_iterations")).To(Equal(2))
			Expect(iterationRunID(buildID, "1/2")).To(Equal(first.RunID))
			Expect(iterationRunID(buildID, "1/3")).To(Equal(second.RunID))
			Expect(countOf("SELECT count(*) FROM pipeline_runs WHERE id = $1", first.RunID)).
				To(Equal(1))
		})
	})

	// The number is what a person is shown -- fly and the web address a run by
	// it, never by its id -- so the caller reports it, and it has to survive
	// the replay path as well as the admitting one. The two paths get it from
	// different places: the first off the run the port hands the before-commit
	// hook, the second by asking the port to read the run back. Neither of
	// them selects from pipeline_runs here, which is the point.
	Describe("the run's number", func() {
		It("reports the admitted run's own number", func() {
			result, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Replayed).To(BeFalse())

			// Against the core table, not against a constant: a service that
			// reported a plausible number belonging to some other run would
			// satisfy "equals 1" on a database with one run in it.
			Expect(result.Number).To(Equal(runNumber(result.RunID)))
			Expect(result.Number).To(Equal(1))
		})

		It("reports the same number when it re-attaches", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			second, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Replayed).To(BeTrue())
			Expect(second.RunID).To(Equal(first.RunID))
			Expect(second.Number).To(Equal(first.Number))
			Expect(second.Number).To(Equal(runNumber(second.RunID)))
		})

		// Two calls, two runs of one template, so the numbers differ -- and
		// each call's replay reports its own. This is what makes the spec
		// above falsifiable: a replay that looked up the wrong run, or read
		// the template's latest number instead of the run's, agrees with a
		// database holding a single run and disagrees here.
		It("reports each call's own number, not the template's latest", func() {
			first, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			sibling := req
			sibling.BuildID = otherBuildID

			second, err := service.Admit(ctx, sibling)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Number).NotTo(Equal(first.Number))

			firstReplayed, err := service.Admit(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(firstReplayed.Replayed).To(BeTrue())
			Expect(firstReplayed.Number).To(Equal(first.Number))

			secondReplayed, err := service.Admit(ctx, sibling)
			Expect(err).NotTo(HaveOccurred())
			Expect(secondReplayed.Replayed).To(BeTrue())
			Expect(secondReplayed.Number).To(Equal(second.Number))
		})
	})

	Describe("the contract key it supplies to the port", func() {
		It("is derived from the call identity alone, with no round trip", func() {
			Expect(composition.ContractKey(buildID, atc.PlanID("1/2"))).
				To(Equal(composition.ContractKey(buildID, atc.PlanID("1/2"))))
			Expect(composition.ContractKey(buildID, atc.PlanID("1/2"))).
				NotTo(Equal(composition.ContractKey(buildID, atc.PlanID("1/3"))))
			Expect(composition.ContractKey(buildID, atc.PlanID("1/2"))).
				NotTo(Equal(composition.ContractKey(otherBuildID, atc.PlanID("1/2"))))
			Expect(composition.ContractKey(buildID, atc.PlanID("1/2"))).NotTo(BeEmpty())
		})
	})
})
