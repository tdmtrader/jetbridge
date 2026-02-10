package implement_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("BuildResults", func() {
	It("builds valid results from tracker and confidence", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "Task A", Phase: "P"},
			},
		}})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")

		conf := &implement.ConfidenceResult{Score: 0.9, Status: "pass"}

		results := implement.BuildResults(tracker, conf, implement.ResultsOpts{
			RepoDir:  "/repo",
			AgentCLI: "claude",
		})

		Expect(results.Status).To(Equal(schema.StatusPass))
		Expect(results.Confidence).To(BeNumerically("==", 0.9))
		Expect(results.Summary).To(ContainSubstring("1"))
		Expect(results.SchemaVersion).To(Equal("1.0"))
		Expect(results.Validate()).To(Succeed())
	})

	It("sets fail status from confidence", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{{
			Name: "P",
			Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "A", Phase: "P"},
			},
		}})
		tracker.Fail("1.1", "broken")

		conf := &implement.ConfidenceResult{Score: 0.0, Status: "fail"}

		results := implement.BuildResults(tracker, conf, implement.ResultsOpts{})
		Expect(results.Status).To(Equal(schema.StatusFail))
	})

	It("sets abstain status", func() {
		tracker := implement.NewTaskTracker(nil)
		conf := &implement.ConfidenceResult{Score: 0.0, Status: "abstain"}

		results := implement.BuildResults(tracker, conf, implement.ResultsOpts{})
		Expect(results.Status).To(Equal(schema.StatusAbstain))
	})
})
