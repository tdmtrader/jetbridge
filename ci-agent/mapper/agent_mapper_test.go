package mapper_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/mapper"
	"github.com/concourse/ci-agent/specparser"
)

type fakeMapperAgent struct {
	output string
	err    error
}

func (f *fakeMapperAgent) Run(_ context.Context, _ string) (string, error) {
	return f.output, f.err
}

var _ = Describe("RefineMapping", func() {
	var (
		spec     *specparser.Spec
		index    *mapper.TestIndex
		initial  []mapper.RequirementMapping
	)

	BeforeEach(func() {
		spec = &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "System authenticates users"},
				{ID: "R2", Text: "System logs audit events"},
			},
		}
		index = &mapper.TestIndex{
			Tests: []mapper.TestEntry{
				{File: "auth_test.go", Function: "TestAuth", Description: "authenticates users"},
			},
		}
		initial = []mapper.RequirementMapping{
			{SpecItem: specparser.SpecItem{ID: "R1", Text: "System authenticates users"}, Status: "covered"},
			{SpecItem: specparser.SpecItem{ID: "R2", Text: "System logs audit events"}, Status: "uncovered"},
		}
	})

	It("returns initial mapping when agent is nil", func() {
		result, err := mapper.RefineMapping(context.Background(), nil, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveLen(2))
		Expect(result[0].Status).To(Equal("covered"))
		Expect(result[1].Status).To(Equal("uncovered"))
	})

	It("refines mappings using agent response", func() {
		agent := &fakeMapperAgent{
			output: `[{"id":"R1","status":"partial","reason":"test only covers basic auth"},{"id":"R2","status":"covered","reason":"audit log test found"}]`,
		}
		result, err := mapper.RefineMapping(context.Background(), agent, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveLen(2))
		Expect(result[0].Status).To(Equal("partial"))
		Expect(result[1].Status).To(Equal("covered"))
	})

	It("falls back to initial on agent error", func() {
		agent := &fakeMapperAgent{err: fmt.Errorf("agent unavailable")}
		result, err := mapper.RefineMapping(context.Background(), agent, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Status).To(Equal("covered"))
		Expect(result[1].Status).To(Equal("uncovered"))
	})

	It("falls back to initial on invalid JSON response", func() {
		agent := &fakeMapperAgent{output: "not valid json"}
		result, err := mapper.RefineMapping(context.Background(), agent, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Status).To(Equal("covered"))
		Expect(result[1].Status).To(Equal("uncovered"))
	})

	It("ignores unknown IDs in agent response", func() {
		agent := &fakeMapperAgent{
			output: `[{"id":"R99","status":"covered","reason":"unknown"}]`,
		}
		result, err := mapper.RefineMapping(context.Background(), agent, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		// Original statuses unchanged since R99 doesn't match R1 or R2.
		Expect(result[0].Status).To(Equal("covered"))
		Expect(result[1].Status).To(Equal("uncovered"))
	})

	It("rejects invalid status values from agent", func() {
		agent := &fakeMapperAgent{
			output: `[{"id":"R1","status":"invalid_status","reason":"bad"}]`,
		}
		result, err := mapper.RefineMapping(context.Background(), agent, spec, index, initial)
		Expect(err).NotTo(HaveOccurred())
		// "invalid_status" is not in {covered, partial, uncovered} so R1 keeps original.
		Expect(result[0].Status).To(Equal("covered"))
	})
})
