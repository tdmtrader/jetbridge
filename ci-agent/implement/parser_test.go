package implement_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("ParsePlan", func() {
	It("parses a plan with phases and tasks", func() {
		input := `# Implementation Plan

## Phase 1: Setup

- [ ] Create project structure
- [ ] Add configuration file

## Phase 2: Core Logic

- [ ] Implement parser
- [ ] Implement validator
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases).To(HaveLen(2))

		Expect(phases[0].Name).To(Equal("Setup"))
		Expect(phases[0].Tasks).To(HaveLen(2))
		Expect(phases[0].Tasks[0].ID).To(Equal("1.1"))
		Expect(phases[0].Tasks[0].Description).To(Equal("Create project structure"))
		Expect(phases[0].Tasks[0].Phase).To(Equal("Setup"))
		Expect(phases[0].Tasks[1].ID).To(Equal("1.2"))
		Expect(phases[0].Tasks[1].Description).To(Equal("Add configuration file"))

		Expect(phases[1].Name).To(Equal("Core Logic"))
		Expect(phases[1].Tasks).To(HaveLen(2))
		Expect(phases[1].Tasks[0].ID).To(Equal("2.1"))
		Expect(phases[1].Tasks[0].Description).To(Equal("Implement parser"))
		Expect(phases[1].Tasks[1].ID).To(Equal("2.2"))
		Expect(phases[1].Tasks[1].Description).To(Equal("Implement validator"))
	})

	It("marks [x] tasks as committed", func() {
		input := `## Phase 1: Done

- [x] Already done task
- [ ] Pending task
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases[0].Tasks[0].Completed).To(BeTrue())
		Expect(phases[0].Tasks[1].Completed).To(BeFalse())
	})

	It("marks [~] tasks as in-progress", func() {
		input := `## Phase 1: Work

- [~] In progress task
- [ ] Pending task
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases[0].Tasks[0].InProgress).To(BeTrue())
		Expect(phases[0].Tasks[1].InProgress).To(BeFalse())
	})

	It("extracts file references from task text", func() {
		input := `## Phase 1: Files

- [ ] Create ` + "`ci-agent/implement/parser.go`" + ` and ` + "`ci-agent/implement/parser_test.go`" + `
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases[0].Tasks[0].Files).To(ConsistOf(
			"ci-agent/implement/parser.go",
			"ci-agent/implement/parser_test.go",
		))
	})

	It("ignores sub-bullets and keeps top-level task description", func() {
		input := `## Phase 1: Tasks

- [ ] Write tests for parser
  - Parses phases and tasks
  - Returns ordered list
  - Handles edge cases
- [ ] Implement parser
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases[0].Tasks).To(HaveLen(2))
		Expect(phases[0].Tasks[0].Description).To(Equal("Write tests for parser"))
	})

	It("skips non-task lines like prose and tables", func() {
		input := `## Phase 1: Setup

Some prose explaining the phase.

| File | Change |
|------|--------|
| foo.go | NEW |

- [ ] Actual task here

### Checkpoint info
`
		phases, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).NotTo(HaveOccurred())
		Expect(phases[0].Tasks).To(HaveLen(1))
		Expect(phases[0].Tasks[0].Description).To(Equal("Actual task here"))
	})

	It("returns error on empty plan", func() {
		_, err := implement.ParsePlan(strings.NewReader(""))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no phases"))
	})

	It("returns error on plan with no parseable tasks", func() {
		input := `## Phase 1: Empty

Just some prose.
`
		_, err := implement.ParsePlan(strings.NewReader(input))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no tasks"))
	})
})

var _ = Describe("ParsePlanFile", func() {
	It("reads a plan file from disk", func() {
		dir := GinkgoT().TempDir()
		planPath := dir + "/plan.md"
		err := writeFile(planPath, `## Phase 1: Test

- [ ] A task from file
`)
		Expect(err).NotTo(HaveOccurred())

		phases, err := implement.ParsePlanFile(planPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(phases).To(HaveLen(1))
		Expect(phases[0].Tasks[0].Description).To(Equal("A task from file"))
	})

	It("returns error on missing file", func() {
		_, err := implement.ParsePlanFile("/nonexistent/plan.md")
		Expect(err).To(HaveOccurred())
	})
})
