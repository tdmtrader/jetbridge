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
