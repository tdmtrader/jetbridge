package plan_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("InputParser", func() {
	Describe("ParseInput (from io.Reader)", func() {
		It("parses valid JSON into PlanningInput", func() {
			raw := `{
				"title": "Add auth",
				"description": "Implement JWT auth",
				"type": "feature",
				"priority": "high"
			}`
			input, err := plan.ParseInput(strings.NewReader(raw))
			Expect(err).NotTo(HaveOccurred())
			Expect(input.Title).To(Equal("Add auth"))
			Expect(input.Description).To(Equal("Implement JWT auth"))
			Expect(input.Type).To(Equal(schema.StoryFeature))
			Expect(input.Priority).To(Equal(schema.PriorityHigh))
		})

		It("parses minimal input (title + description only)", func() {
			raw := `{"title":"Task","description":"Do it"}`
			input, err := plan.ParseInput(strings.NewReader(raw))
			Expect(err).NotTo(HaveOccurred())
			Expect(input.Title).To(Equal("Task"))
			Expect(input.Description).To(Equal("Do it"))
			Expect(input.Context).To(BeNil())
		})

		It("returns error on invalid JSON", func() {
			_, err := plan.ParseInput(strings.NewReader("not json"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parse"))
		})

		It("returns error on validation failure", func() {
			raw := `{"title":"","description":"desc"}`
			_, err := plan.ParseInput(strings.NewReader(raw))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("title"))
		})

		It("parses input with full context", func() {
			raw := `{
				"title": "Feature X",
				"description": "Build feature X",
				"context": {
					"repo": "https://github.com/org/repo.git",
					"language": "go",
					"related_files": ["main.go", "handler.go"]
				}
			}`
			input, err := plan.ParseInput(strings.NewReader(raw))
			Expect(err).NotTo(HaveOccurred())
			Expect(input.Context).NotTo(BeNil())
			Expect(input.Context.Repo).To(Equal("https://github.com/org/repo.git"))
			Expect(input.Context.Language).To(Equal("go"))
			Expect(input.Context.RelatedFiles).To(HaveLen(2))
		})
	})

	Describe("ParseInputFile (from file path)", func() {
		var tmpDir string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "input-parser-test")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(tmpDir)
		})

		It("reads and parses input.json from a file", func() {
			content := `{"title":"From file","description":"Read from disk","type":"bug"}`
			inputPath := filepath.Join(tmpDir, "input.json")
			Expect(os.WriteFile(inputPath, []byte(content), 0644)).To(Succeed())

			input, err := plan.ParseInputFile(inputPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(input.Title).To(Equal("From file"))
			Expect(input.Type).To(Equal(schema.StoryBug))
		})

		It("returns error on missing file", func() {
			_, err := plan.ParseInputFile(filepath.Join(tmpDir, "missing.json"))
			Expect(err).To(HaveOccurred())
		})

		It("returns error on invalid JSON in file", func() {
			inputPath := filepath.Join(tmpDir, "bad.json")
			Expect(os.WriteFile(inputPath, []byte("}{"), 0644)).To(Succeed())

			_, err := plan.ParseInputFile(inputPath)
			Expect(err).To(HaveOccurred())
		})
	})
})
