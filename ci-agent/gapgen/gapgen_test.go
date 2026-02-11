package gapgen_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/gapgen"
	"github.com/concourse/ci-agent/mapper"
	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/specparser"
)

type fakeAgent struct {
	output string
	err    error
}

func (f *fakeAgent) Run(_ context.Context, _ string) (string, error) {
	return f.output, f.err
}

type countingAgent struct {
	responses []string
	callCount *int
}

func (c *countingAgent) Run(_ context.Context, _ string) (string, error) {
	idx := *c.callCount
	*c.callCount++
	if idx < len(c.responses) {
		return c.responses[idx], nil
	}
	return "", fmt.Errorf("no more responses")
}

var _ = Describe("GenerateGapTests", func() {
	It("generates tests for uncovered requirements", func() {
		agent := &fakeAgent{
			output: `{"test_name": "TestAuth", "test_code": "package foo\n\nfunc TestAuth(t *testing.T) {}"}`,
		}
		gaps := []mapper.RequirementMapping{
			{
				SpecItem: specparser.SpecItem{ID: "R1", Text: "Auth required"},
				Status:   "uncovered",
			},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(HaveLen(1))
		Expect(tests[0].RequirementID).To(Equal("R1"))
		Expect(tests[0].TestName).To(Equal("TestAuth"))
	})

	It("skips non-uncovered requirements", func() {
		agent := &fakeAgent{output: `{"test_name": "Test", "test_code": "code"}`}
		gaps := []mapper.RequirementMapping{
			{
				SpecItem: specparser.SpecItem{ID: "R1", Text: "covered"},
				Status:   "covered",
			},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(BeEmpty())
	})

	It("skips when agent returns empty test_code", func() {
		agent := &fakeAgent{output: `{"test_name": "Test", "test_code": ""}`}
		gaps := []mapper.RequirementMapping{
			{
				SpecItem: specparser.SpecItem{ID: "R1", Text: "uncovered"},
				Status:   "uncovered",
			},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(BeEmpty())
	})

	It("returns empty when agent is nil", func() {
		gaps := []mapper.RequirementMapping{
			{SpecItem: specparser.SpecItem{ID: "R1"}, Status: "uncovered"},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), nil, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(BeEmpty())
	})
})

var _ = Describe("GenerateGapTests error handling", func() {
	It("skips when agent returns invalid JSON", func() {
		agent := &fakeAgent{output: "not json at all"}
		gaps := []mapper.RequirementMapping{
			{SpecItem: specparser.SpecItem{ID: "R1", Text: "uncovered req"}, Status: "uncovered"},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(BeEmpty())
	})

	It("skips when agent returns error", func() {
		agent := &fakeAgent{err: fmt.Errorf("agent down")}
		gaps := []mapper.RequirementMapping{
			{SpecItem: specparser.SpecItem{ID: "R1", Text: "req"}, Status: "uncovered"},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		Expect(tests).To(BeEmpty())
	})

	It("processes multiple gaps independently", func() {
		callCount := 0
		agent := &countingAgent{
			responses: []string{
				`{"test_name": "TestA", "test_code": "package t\nfunc TestA(t *testing.T) {}"}`,
				"bad json",
				`{"test_name": "TestC", "test_code": "package t\nfunc TestC(t *testing.T) {}"}`,
			},
			callCount: &callCount,
		}
		gaps := []mapper.RequirementMapping{
			{SpecItem: specparser.SpecItem{ID: "R1", Text: "first"}, Status: "uncovered"},
			{SpecItem: specparser.SpecItem{ID: "R2", Text: "second"}, Status: "uncovered"},
			{SpecItem: specparser.SpecItem{ID: "R3", Text: "third"}, Status: "uncovered"},
		}
		tests, err := gapgen.GenerateGapTests(context.Background(), agent, "/tmp/repo", gaps)
		Expect(err).NotTo(HaveOccurred())
		// R1 succeeds, R2 fails (bad json), R3 succeeds
		Expect(tests).To(HaveLen(2))
		Expect(tests[0].RequirementID).To(Equal("R1"))
		Expect(tests[1].RequirementID).To(Equal("R3"))
	})
})

var _ = Describe("ClassifyGapResults", func() {
	It("classifies passing test as uncovered_implemented", func() {
		result := gapgen.ClassifyGapResults("R1", "Auth", &gapgen.TestResult{Passed: true})
		Expect(result.Status).To(Equal(schema.CoverageUncoveredImplemented))
		Expect(result.CoveragePoints).To(BeNumerically("~", 0.75, 0.01))
	})

	It("classifies failing test as uncovered_broken", func() {
		result := gapgen.ClassifyGapResults("R1", "Auth", &gapgen.TestResult{Passed: false})
		Expect(result.Status).To(Equal(schema.CoverageUncoveredBroken))
		Expect(result.CoveragePoints).To(BeNumerically("~", 0.0, 0.01))
	})

	It("classifies compilation error as uncovered_broken with note", func() {
		result := gapgen.ClassifyGapResults("R1", "Auth", &gapgen.TestResult{CompErr: true, Output: "syntax error"})
		Expect(result.Status).To(Equal(schema.CoverageUncoveredBroken))
		Expect(result.Notes).To(ContainSubstring("compilation error"))
	})

	It("classifies nil result as uncovered_broken", func() {
		result := gapgen.ClassifyGapResults("R1", "Auth", nil)
		Expect(result.Status).To(Equal(schema.CoverageUncoveredBroken))
	})
})
