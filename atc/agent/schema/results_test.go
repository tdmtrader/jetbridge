package schema_test

import (
	"encoding/json"

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

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals a full Results", func() {
			original := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    0.85,
				Summary:       "Review complete",
				Artifacts: []schema.Artifact{
					{
						Name:      "review-comments",
						Path:      "artifacts/comments.json",
						MediaType: "application/json",
						Metadata: map[string]interface{}{
							"count": float64(5),
						},
					},
				},
				Metadata: map[string]interface{}{
					"files_reviewed": float64(10),
				},
			}

			data, err := json.Marshal(original)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Results
			err = json.Unmarshal(data, &decoded)
			Expect(err).NotTo(HaveOccurred())

			Expect(decoded.SchemaVersion).To(Equal(original.SchemaVersion))
			Expect(decoded.Status).To(Equal(original.Status))
			Expect(decoded.Confidence).To(Equal(original.Confidence))
			Expect(decoded.Summary).To(Equal(original.Summary))
			Expect(decoded.Artifacts).To(HaveLen(1))
			Expect(decoded.Artifacts[0].Name).To(Equal("review-comments"))
			Expect(decoded.Artifacts[0].Path).To(Equal("artifacts/comments.json"))
			Expect(decoded.Artifacts[0].MediaType).To(Equal("application/json"))
			Expect(decoded.Metadata).To(HaveKeyWithValue("files_reviewed", float64(10)))
		})

		It("uses correct JSON field names", func() {
			r := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusFail,
				Confidence:    0.3,
				Summary:       "Issues found",
				Artifacts: []schema.Artifact{
					{
						Name:      "report",
						Path:      "out/report.md",
						MediaType: "text/markdown",
					},
				},
			}

			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			Expect(raw).To(HaveKey("schema_version"))
			Expect(raw).To(HaveKey("status"))
			Expect(raw).To(HaveKey("confidence"))
			Expect(raw).To(HaveKey("summary"))
			Expect(raw).To(HaveKey("artifacts"))
		})

		It("serializes nil artifacts as empty array", func() {
			r := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    1.0,
				Summary:       "ok",
			}

			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			artifacts, ok := raw["artifacts"].([]interface{})
			Expect(ok).To(BeTrue(), "artifacts should be an array")
			Expect(artifacts).To(BeEmpty())
		})

		It("omits metadata when nil", func() {
			r := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    1.0,
				Summary:       "ok",
				Artifacts:     []schema.Artifact{},
			}

			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			Expect(raw).NotTo(HaveKey("metadata"))
		})

		It("includes metadata when present", func() {
			r := schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    1.0,
				Summary:       "ok",
				Artifacts:     []schema.Artifact{},
				Metadata: map[string]interface{}{
					"custom_key": "custom_value",
				},
			}

			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			Expect(raw).To(HaveKey("metadata"))
		})

		It("round-trips artifact metadata", func() {
			original := schema.Artifact{
				Name:      "test",
				Path:      "out/test.json",
				MediaType: "application/json",
				Metadata: map[string]interface{}{
					"size_bytes": float64(1024),
				},
			}

			data, err := json.Marshal(original)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Artifact
			err = json.Unmarshal(data, &decoded)
			Expect(err).NotTo(HaveOccurred())

			Expect(decoded.Name).To(Equal(original.Name))
			Expect(decoded.Path).To(Equal(original.Path))
			Expect(decoded.MediaType).To(Equal(original.MediaType))
			Expect(decoded.Metadata).To(HaveKeyWithValue("size_bytes", float64(1024)))
		})
	})
})
