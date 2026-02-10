package schema_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/agent/schema"
)

var _ = Describe("Results", func() {
	var validResults func() schema.Results

	BeforeEach(func() {
		validResults = func() schema.Results {
			return schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    0.92,
				Summary:       "All checks passed",
				Artifacts:     []schema.Artifact{},
			}
		}
	})

	Describe("Validate", func() {
		It("accepts a valid Results with all required fields", func() {
			r := validResults()
			Expect(r.Validate()).To(Succeed())
		})

		It("rejects missing schema_version", func() {
			r := validResults()
			r.SchemaVersion = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("schema_version")))
		})

		It("rejects missing status", func() {
			r := validResults()
			r.Status = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("status")))
		})

		It("rejects invalid status value", func() {
			r := validResults()
			r.Status = "unknown"
			Expect(r.Validate()).To(MatchError(ContainSubstring("status")))
		})

		It("accepts all valid status values", func() {
			for _, s := range []schema.Status{
				schema.StatusPass,
				schema.StatusFail,
				schema.StatusError,
				schema.StatusAbstain,
			} {
				r := validResults()
				r.Status = s
				Expect(r.Validate()).To(Succeed(), "expected status %q to be valid", s)
			}
		})

		It("rejects missing summary", func() {
			r := validResults()
			r.Summary = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("summary")))
		})

		It("rejects confidence below 0.0", func() {
			r := validResults()
			r.Confidence = -0.1
			Expect(r.Validate()).To(MatchError(ContainSubstring("confidence")))
		})

		It("rejects confidence above 1.0", func() {
			r := validResults()
			r.Confidence = 1.1
			Expect(r.Validate()).To(MatchError(ContainSubstring("confidence")))
		})

		It("accepts confidence at 0.0", func() {
			r := validResults()
			r.Confidence = 0.0
			Expect(r.Validate()).To(Succeed())
		})

		It("accepts confidence at 1.0", func() {
			r := validResults()
			r.Confidence = 1.0
			Expect(r.Validate()).To(Succeed())
		})

		It("rejects nil artifacts (must be present, even if empty)", func() {
			r := validResults()
			r.Artifacts = nil
			Expect(r.Validate()).To(MatchError(ContainSubstring("artifacts")))
		})

		It("accepts an empty artifacts array", func() {
			r := validResults()
			r.Artifacts = []schema.Artifact{}
			Expect(r.Validate()).To(Succeed())
		})

		It("rejects an artifact with missing name", func() {
			r := validResults()
			r.Artifacts = []schema.Artifact{{
				Name:      "",
				Path:      "out/review.json",
				MediaType: "application/json",
			}}
			Expect(r.Validate()).To(MatchError(ContainSubstring("name")))
		})

		It("rejects an artifact with missing path", func() {
			r := validResults()
			r.Artifacts = []schema.Artifact{{
				Name:      "review",
				Path:      "",
				MediaType: "application/json",
			}}
			Expect(r.Validate()).To(MatchError(ContainSubstring("path")))
		})

		It("rejects an artifact with missing media_type", func() {
			r := validResults()
			r.Artifacts = []schema.Artifact{{
				Name:      "review",
				Path:      "out/review.json",
				MediaType: "",
			}}
			Expect(r.Validate()).To(MatchError(ContainSubstring("media_type")))
		})

		It("accepts valid artifacts", func() {
			r := validResults()
			r.Artifacts = []schema.Artifact{
				{
					Name:      "review-comments",
					Path:      "artifacts/comments.json",
					MediaType: "application/json",
				},
				{
					Name:      "summary",
					Path:      "artifacts/summary.md",
					MediaType: "text/markdown",
				},
			}
			Expect(r.Validate()).To(Succeed())
		})

		It("allows optional metadata", func() {
			r := validResults()
			r.Metadata = map[string]interface{}{
				"files_reviewed": 42,
				"issues_found":   3,
			}
			Expect(r.Validate()).To(Succeed())
		})
	})
})
