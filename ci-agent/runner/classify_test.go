package runner_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ClassifyResults", func() {
	It("promotes failing test to proven issue", func() {
		findings := []runner.AgentFinding{
			{
				Title:        "nil pointer in handler",
				Description:  "Handler does not check for nil",
				File:         "handler.go",
				Line:         42,
				SeverityHint: schema.SeverityHigh,
				Category:     schema.CategoryCorrectness,
				TestCode:     "package handler\n\nfunc TestNilHandler(t *testing.T) {}",
				TestFile:     "handler_test.go",
				TestName:     "TestNilHandler",
			},
		}

		results := map[string]*runner.TestResult{
			"handler_test.go": {Pass: false, Output: "panic: nil pointer", Duration: time.Second},
		}

		proven, observations := runner.ClassifyResults(findings, results)
		Expect(proven).To(HaveLen(1))
		Expect(observations).To(BeEmpty())
		Expect(proven[0].Title).To(Equal("nil pointer in handler"))
		Expect(proven[0].Severity).To(Equal(schema.SeverityHigh))
		Expect(proven[0].File).To(Equal("handler.go"))
		Expect(proven[0].Line).To(Equal(42))
		Expect(proven[0].TestFile).To(Equal("handler_test.go"))
		Expect(proven[0].TestName).To(Equal("TestNilHandler"))
	})

	It("discards finding when test passes", func() {
		findings := []runner.AgentFinding{
			{
				Title:        "possible race",
				File:         "worker.go",
				Line:         10,
				SeverityHint: schema.SeverityMedium,
				Category:     schema.CategorySecurity,
				TestCode:     "package worker\n\nfunc TestRace(t *testing.T) {}",
				TestFile:     "worker_test.go",
				TestName:     "TestRace",
			},
		}

		results := map[string]*runner.TestResult{
			"worker_test.go": {Pass: true, Output: "ok", Duration: time.Second},
		}

		proven, observations := runner.ClassifyResults(findings, results)
		Expect(proven).To(BeEmpty())
		Expect(observations).To(BeEmpty())
	})

	It("demotes compilation error to observation", func() {
		findings := []runner.AgentFinding{
			{
				Title:        "buffer overflow",
				File:         "buf.go",
				Line:         5,
				SeverityHint: schema.SeverityCritical,
				Category:     schema.CategorySecurity,
				TestCode:     "package buf\n\nfunc TestOverflow(t *testing.T) { undefined() }",
				TestFile:     "buf_test.go",
				TestName:     "TestOverflow",
			},
		}

		results := map[string]*runner.TestResult{
			"buf_test.go": {Error: true, Output: "undefined: undefined", Duration: time.Second},
		}

		proven, observations := runner.ClassifyResults(findings, results)
		Expect(proven).To(BeEmpty())
		Expect(observations).To(HaveLen(1))
		Expect(observations[0].Title).To(Equal("buffer overflow"))
		Expect(observations[0].File).To(Equal("buf.go"))
		Expect(observations[0].Line).To(Equal(5))
		Expect(observations[0].Category).To(Equal(schema.CategorySecurity))
	})

	It("classifies finding with no test as observation", func() {
		findings := []runner.AgentFinding{
			{
				Title:        "style concern",
				File:         "util.go",
				Line:         20,
				SeverityHint: schema.SeverityLow,
				Category:     schema.CategoryMaintainability,
				// No TestCode, TestFile, or TestName
			},
		}

		proven, observations := runner.ClassifyResults(findings, nil)
		Expect(proven).To(BeEmpty())
		Expect(observations).To(HaveLen(1))
		Expect(observations[0].Title).To(Equal("style concern"))
	})
})
