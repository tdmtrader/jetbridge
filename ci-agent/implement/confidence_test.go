package implement_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("ScoreConfidence", func() {
	It("returns 1.0 when all tasks committed", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
				{ID: "1.2", Description: "B", Phase: "P"},
			},
		}})
		// Advance both to committed.
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.2")
		tracker.Advance("1.2")
		tracker.Advance("1.2")

		result := implement.ScoreConfidence(tracker, true)
		Expect(result.Score).To(BeNumerically("==", 1.0))
		Expect(result.Status).To(Equal("pass"))
	})

	It("returns 0.9 when half skipped (pre-satisfied)", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
				{ID: "1.2", Description: "B", Phase: "P"},
			},
		}})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Skip("1.2", "already satisfied")

		result := implement.ScoreConfidence(tracker, true)
		Expect(result.Score).To(BeNumerically(">=", 0.8))
	})

	It("returns 0.5 when half committed half failed", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
				{ID: "1.2", Description: "B", Phase: "P"},
			},
		}})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Fail("1.2", "broken")

		result := implement.ScoreConfidence(tracker, true)
		Expect(result.Score).To(BeNumerically("~", 0.5, 0.15))
	})

	It("returns 0.0 when all failed", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
			},
		}})
		tracker.Fail("1.1", "broken")

		result := implement.ScoreConfidence(tracker, false)
		Expect(result.Score).To(BeNumerically("==", 0.0))
	})

	It("overrides to fail when suite fails", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
			},
		}})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")

		result := implement.ScoreConfidence(tracker, false)
		Expect(result.Score).To(BeNumerically("==", 0.0))
		Expect(result.Status).To(Equal("fail"))
	})

	It("adds bonus when suite passes", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
				{ID: "1.2", Description: "B", Phase: "P"},
			},
		}})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Fail("1.2", "broken")

		withSuite := implement.ScoreConfidence(tracker, true)
		withoutSuite := implement.ScoreConfidence(tracker, false)
		// With suite pass, score should be higher (but not by much since half failed).
		Expect(withSuite.Score).To(BeNumerically(">", withoutSuite.Score))
	})
})
