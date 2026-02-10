package fix_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ParseReviewOutput", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "fix-parser-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("parses a valid review.json from disk", func() {
		review := schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			Score:         schema.Score{Value: 7.0, Max: 10.0},
			ProvenIssues: []schema.ProvenIssue{
				{ID: "ISS-001", Severity: schema.SeverityHigh, Title: "nil ptr", File: "a.go", Line: 10, TestFile: "a_test.go", TestName: "TestA"},
			},
			Summary: "Review done",
		}
		writeReviewJSON(tmpDir, review)

		output, err := fix.ParseReviewOutput(filepath.Join(tmpDir, "review.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(output.SchemaVersion).To(Equal("1.0.0"))
		Expect(output.ProvenIssues).To(HaveLen(1))
		Expect(output.ProvenIssues[0].ID).To(Equal("ISS-001"))
	})

	It("returns error for missing file", func() {
		_, err := fix.ParseReviewOutput("/nonexistent/review.json")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reading review"))
	})

	It("returns error for invalid JSON", func() {
		Expect(os.WriteFile(filepath.Join(tmpDir, "review.json"), []byte("{bad json}"), 0644)).To(Succeed())
		_, err := fix.ParseReviewOutput(filepath.Join(tmpDir, "review.json"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("parsing review"))
	})

	It("returns error for schema version mismatch", func() {
		review := schema.ReviewOutput{
			SchemaVersion: "2.0.0",
			Summary:       "future",
		}
		writeReviewJSON(tmpDir, review)

		_, err := fix.ParseReviewOutput(filepath.Join(tmpDir, "review.json"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("schema version"))
	})
})

var _ = Describe("SortIssuesBySeverity", func() {
	It("sorts critical before high before medium before low", func() {
		issues := []schema.ProvenIssue{
			{ID: "3", Severity: schema.SeverityLow, File: "c.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
			{ID: "1", Severity: schema.SeverityCritical, File: "a.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
			{ID: "4", Severity: schema.SeverityMedium, File: "d.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
			{ID: "2", Severity: schema.SeverityHigh, File: "b.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
		}

		sorted := fix.SortIssuesBySeverity(issues)
		Expect(sorted[0].Severity).To(Equal(schema.SeverityCritical))
		Expect(sorted[1].Severity).To(Equal(schema.SeverityHigh))
		Expect(sorted[2].Severity).To(Equal(schema.SeverityMedium))
		Expect(sorted[3].Severity).To(Equal(schema.SeverityLow))
	})

	It("sorts by file path within same severity", func() {
		issues := []schema.ProvenIssue{
			{ID: "2", Severity: schema.SeverityHigh, File: "z.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
			{ID: "1", Severity: schema.SeverityHigh, File: "a.go", Line: 1, Title: "t", TestFile: "t", TestName: "t"},
		}

		sorted := fix.SortIssuesBySeverity(issues)
		Expect(sorted[0].File).To(Equal("a.go"))
		Expect(sorted[1].File).To(Equal("z.go"))
	})

	It("returns empty slice for nil input", func() {
		sorted := fix.SortIssuesBySeverity(nil)
		Expect(sorted).To(BeEmpty())
	})
})

func writeReviewJSON(dir string, review schema.ReviewOutput) {
	data, _ := json.MarshalIndent(review, "", "  ")
	os.WriteFile(filepath.Join(dir, "review.json"), data, 0644)
}
