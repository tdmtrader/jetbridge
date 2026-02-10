package implement_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("RenderSummary", func() {
	It("renders Markdown with task details", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{
			{Name: "Setup", Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "Create project", Phase: "Setup"},
				{ID: "1.2", Description: "Add config", Phase: "Setup"},
			}},
		})
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.Advance("1.1")
		tracker.SetCommitInfo("1.1", "abc123def456", "project_test.go")
		tracker.Skip("1.2", "already satisfied")

		conf := &implement.ConfidenceResult{Score: 0.9, Status: "pass"}

		md := implement.RenderSummary(tracker, conf, 5*time.Minute)
		Expect(md).To(ContainSubstring("# Implementation Summary"))
		Expect(md).To(ContainSubstring("Create project"))
		Expect(md).To(ContainSubstring("abc123de"))
		Expect(md).To(ContainSubstring("Add config"))
		Expect(md).To(ContainSubstring("already satisfied"))
		Expect(md).To(ContainSubstring("0.90"))
	})

	It("includes failed task reasons", func() {
		tracker := implement.NewTaskTracker([]implement.Phase{
			{Name: "Fail", Tasks: []implement.PlanTask{
				{ID: "1.1", Description: "Broken task", Phase: "Fail"},
			}},
		})
		tracker.Fail("1.1", "compilation error")

		conf := &implement.ConfidenceResult{Score: 0.0, Status: "fail"}
		md := implement.RenderSummary(tracker, conf, time.Minute)
		Expect(md).To(ContainSubstring("Broken task"))
		Expect(md).To(ContainSubstring("compilation error"))
	})

	It("shows total duration", func() {
		tracker := implement.NewTaskTracker(nil)
		conf := &implement.ConfidenceResult{Score: 0.0, Status: "abstain"}
		md := implement.RenderSummary(tracker, conf, 2*time.Hour+30*time.Minute)
		Expect(md).To(ContainSubstring("2h30m"))
	})
})
