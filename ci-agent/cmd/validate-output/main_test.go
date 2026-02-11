package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/concourse/ci-agent/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestValidateOutput(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ValidateOutput Suite")
}

var binaryPath string

var _ = BeforeSuite(func() {
	var err error
	binaryPath, err = filepath.Abs("validate-output")
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir, _ = os.Getwd()
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "build failed: %s", string(out))

	DeferCleanup(func() { os.Remove(binaryPath) })
})

func runValidate(outputDir, outputType string) (string, int) {
	cmd := exec.Command(binaryPath, "--output-dir", outputDir, "--type", outputType)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return string(out), exitCode
}

func writeJSON(dir, filename string, v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(dir, filename), data, 0644)).To(Succeed())
}

var _ = Describe("validate-output", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "validate-output-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Context("review type", func() {
		It("passes for valid review.json", func() {
			review := schema.ReviewOutput{
				SchemaVersion: "1.0.0",
				Summary:       "All good",
				Score:         schema.Score{Value: 9.0, Max: 10.0, Pass: true, Threshold: 7.0},
			}
			writeJSON(tmpDir, "review.json", review)

			out, code := runValidate(tmpDir, "review")
			Expect(code).To(Equal(0), "expected exit 0, output: %s", out)
			Expect(out).To(ContainSubstring("PASS"))
		})

		It("fails when review.json is missing", func() {
			out, code := runValidate(tmpDir, "review")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("review.json"))
		})

		It("fails when review.json has no schema_version", func() {
			review := schema.ReviewOutput{
				Summary: "All good",
			}
			writeJSON(tmpDir, "review.json", review)

			out, code := runValidate(tmpDir, "review")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("schema_version"))
		})

		It("fails for malformed JSON", func() {
			Expect(os.WriteFile(filepath.Join(tmpDir, "review.json"), []byte("{invalid"), 0644)).To(Succeed())

			out, code := runValidate(tmpDir, "review")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
		})
	})

	Context("fix type", func() {
		It("passes for valid fix-report.json", func() {
			report := schema.FixReport{
				SchemaVersion: "1.0.0",
				Metadata: schema.FixMetadata{
					Repo:       "test/repo",
					BaseCommit: "abc123",
				},
				Summary: schema.FixSummary{TotalIssues: 1, Fixed: 1, RegressionFree: true},
			}
			writeJSON(tmpDir, "fix-report.json", report)

			out, code := runValidate(tmpDir, "fix")
			Expect(code).To(Equal(0), "expected exit 0, output: %s", out)
			Expect(out).To(ContainSubstring("PASS"))
		})

		It("fails when fix-report.json is missing", func() {
			out, code := runValidate(tmpDir, "fix")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("fix-report.json"))
		})

		It("fails when metadata.repo is empty", func() {
			report := schema.FixReport{
				SchemaVersion: "1.0.0",
				Metadata:      schema.FixMetadata{BaseCommit: "abc123"},
			}
			writeJSON(tmpDir, "fix-report.json", report)

			out, code := runValidate(tmpDir, "fix")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("metadata.repo"))
		})
	})

	Context("plan type (results.json)", func() {
		It("passes for valid results.json", func() {
			results := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    0.9,
				Summary:       "Plan complete",
				Artifacts:     []schema.Artifact{{Name: "plan.md", Path: "plan.md", MediaType: "text/markdown"}},
			}
			writeJSON(tmpDir, "results.json", results)

			out, code := runValidate(tmpDir, "plan")
			Expect(code).To(Equal(0), "expected exit 0, output: %s", out)
			Expect(out).To(ContainSubstring("PASS"))
		})

		It("fails when results.json is missing", func() {
			out, code := runValidate(tmpDir, "plan")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("results.json"))
		})

		It("fails when status is invalid", func() {
			results := schema.Results{
				Status:     "bogus",
				Confidence: 0.5,
				Summary:    "Plan",
				Artifacts:  []schema.Artifact{{Name: "a", Path: "a", MediaType: "text/plain"}},
			}
			writeJSON(tmpDir, "results.json", results)

			out, code := runValidate(tmpDir, "plan")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("invalid status"))
		})
	})

	Context("qa type", func() {
		It("passes for valid qa.json", func() {
			qa := schema.QAOutput{
				SchemaVersion: "1.0.0",
				Results: []schema.RequirementResult{
					{ID: "R1", Text: "Requirement one", Status: schema.CoverageCovered, CoveragePoints: 1.0},
				},
				Score: schema.QAScore{Value: 9.0, Max: 10.0, Pass: true, Threshold: 7.0},
			}
			writeJSON(tmpDir, "qa.json", qa)

			out, code := runValidate(tmpDir, "qa")
			Expect(code).To(Equal(0), "expected exit 0, output: %s", out)
			Expect(out).To(ContainSubstring("PASS"))
		})

		It("fails when qa.json is missing", func() {
			out, code := runValidate(tmpDir, "qa")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("qa.json"))
		})

		It("fails when score.max is zero", func() {
			qa := schema.QAOutput{
				SchemaVersion: "1.0.0",
				Results: []schema.RequirementResult{
					{ID: "R1", Text: "Req", Status: schema.CoverageCovered},
				},
				Score: schema.QAScore{Value: 0, Max: 0},
			}
			writeJSON(tmpDir, "qa.json", qa)

			out, code := runValidate(tmpDir, "qa")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("score.max"))
		})
	})

	Context("implement type (results.json)", func() {
		It("passes for valid results.json", func() {
			results := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    0.85,
				Summary:       "Implementation complete",
				Artifacts:     []schema.Artifact{{Name: "diff", Path: "changes.patch", MediaType: "text/x-patch"}},
			}
			writeJSON(tmpDir, "results.json", results)

			out, code := runValidate(tmpDir, "implement")
			Expect(code).To(Equal(0), "expected exit 0, output: %s", out)
			Expect(out).To(ContainSubstring("PASS"))
		})
	})

	Context("unknown type", func() {
		It("fails with unknown type", func() {
			out, code := runValidate(tmpDir, "bogus")
			Expect(code).To(Equal(1), "expected exit 1, output: %s", out)
			Expect(out).To(ContainSubstring("unknown"))
		})
	})
})
