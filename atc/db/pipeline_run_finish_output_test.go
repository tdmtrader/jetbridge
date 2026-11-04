package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A build's finish waits on its captures only while waiting can change the
// answer. The engine holds a pending finish and asks again, so a disposition
// that can never accept the status must be said once, not left pending.
var _ = Describe("Run build finish over its captures", func() {
	var f *closureCaptures

	BeforeEach(func() {
		f = newClosureCaptures()
	})

	settle := func(a db.RunExecutionAdmission, failed bool) {
		GinkgoHelper()
		consumer, err := db.HangarConsumerPrefixHeld("finish-output-test")
		Expect(err).NotTo(HaveOccurred())
		repository := db.NewHangarOutputRepository(consumer)
		Expect(f.inTx(func(tx db.Tx) error {
			if failed {
				_, err := repository.MarkFailed(f.ctx, tx, a.Capture, "seal refused")
				return err
			}
			_, err := repository.CASPendingToDiscarded(f.ctx, tx, a.Capture, "producer failed")
			return err
		})).To(Succeed())
	}

	It("keeps a succeeded finish pending while its capture is in flight", func() {
		f.start(f.review, f.reviewPlan)
		err := f.review.Finish(db.BuildStatusSucceeded)
		Expect(err).To(MatchError(atc.ErrRunOutputPending))
		Expect(err).NotTo(MatchError(atc.ErrRunExecutionOpen), "a capture in flight is worth waiting for")
	})

	// Once the captures settle, an execution with no closure is closed only by
	// a step that reattaches to it. The engine runs the plan again for that
	// answer instead of waiting on it.
	It("says an errored finish over settled captures has an open execution", func() {
		settle(f.start(f.review, f.reviewPlan), true)
		err := f.review.Finish(db.BuildStatusErrored)
		Expect(err).To(MatchError(atc.ErrRunExecutionOpen))
		Expect(err).To(MatchError(atc.ErrRunOutputPending))
		completed, _ := f.buildState(f.review)
		Expect(completed).To(BeFalse())
	})

	It("says a succeeded finish lost its output once the capture failed", func() {
		settle(f.start(f.review, f.reviewPlan), true)
		Expect(f.review.Finish(db.BuildStatusSucceeded)).To(MatchError(atc.ErrRunOutputLost))
		completed, _ := f.buildState(f.review)
		Expect(completed).To(BeFalse(), "a refused finish completed the build")
	})

	It("says a succeeded finish lost its output once the capture was discarded", func() {
		settle(f.start(f.review, f.reviewPlan), false)
		Expect(f.review.Finish(db.BuildStatusSucceeded)).To(MatchError(atc.ErrRunOutputLost))
	})

	It("does not call an errored finish lost over a failed capture", func() {
		settle(f.start(f.review, f.reviewPlan), true)
		Expect(f.review.Finish(db.BuildStatusErrored)).NotTo(MatchError(atc.ErrRunOutputLost))
	})
})
