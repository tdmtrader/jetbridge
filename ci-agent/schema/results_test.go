package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Results", func() {
	validResults := func() schema.Results {
		return schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusPass,
			Confidence:    0.85,
			Summary:       "Planning completed successfully",
			Artifacts: []schema.Artifact{
				{Name: "spec", Path: "spec.md", MediaType: "text/markdown"},
				{Name: "plan", Path: "plan.md", MediaType: "text/markdown"},
			},
			Metadata: map[string]string{"agent": "claude"},
		}
	}

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			r := validResults()
			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Results
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())

			Expect(decoded.SchemaVersion).To(Equal("1.0"))
			Expect(decoded.Status).To(Equal(schema.StatusPass))
			Expect(decoded.Confidence).To(BeNumerically("~", 0.85, 0.01))
			Expect(decoded.Summary).To(Equal("Planning completed successfully"))
			Expect(decoded.Artifacts).To(HaveLen(2))
			Expect(decoded.Metadata["agent"]).To(Equal("claude"))
		})

		It("uses correct JSON field names", func() {
			r := validResults()
			data, _ := json.Marshal(r)
			var raw map[string]interface{}
			json.Unmarshal(data, &raw)

			Expect(raw).To(HaveKey("schema_version"))
			Expect(raw).To(HaveKey("status"))
			Expect(raw).To(HaveKey("confidence"))
			Expect(raw).To(HaveKey("summary"))
			Expect(raw).To(HaveKey("artifacts"))
			Expect(raw).To(HaveKey("metadata"))
		})
	})

	Describe("Validate", func() {
		It("passes for valid results", func() {
			r := validResults()
			Expect(r.Validate()).To(Succeed())
		})

		It("requires status", func() {
			r := validResults()
			r.Status = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("status")))
		})

		It("rejects invalid status", func() {
			r := validResults()
			r.Status = "unknown"
			Expect(r.Validate()).To(MatchError(ContainSubstring("invalid status")))
		})

		It("requires summary", func() {
			r := validResults()
			r.Summary = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("summary")))
		})

		It("requires at least one artifact", func() {
			r := validResults()
			r.Artifacts = nil
			Expect(r.Validate()).To(MatchError(ContainSubstring("artifact")))
		})

		It("rejects confidence below 0", func() {
			r := validResults()
			r.Confidence = -0.1
			Expect(r.Validate()).To(MatchError(ContainSubstring("confidence")))
		})

		It("rejects confidence above 1", func() {
			r := validResults()
			r.Confidence = 1.1
			Expect(r.Validate()).To(MatchError(ContainSubstring("confidence")))
		})

		It("requires artifact name", func() {
			r := validResults()
			r.Artifacts[0].Name = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("name")))
		})

		It("requires artifact path", func() {
			r := validResults()
			r.Artifacts[0].Path = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("path")))
		})

		It("requires artifact media_type", func() {
			r := validResults()
			r.Artifacts[0].MediaType = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("media_type")))
		})

		It("accepts all valid statuses", func() {
			for _, s := range []schema.Status{
				schema.StatusPass, schema.StatusFail, schema.StatusError, schema.StatusAbstain,
			} {
				r := validResults()
				r.Status = s
				Expect(r.Validate()).To(Succeed())
			}
		})

		It("defaults schema_version to 1.0", func() {
			r := validResults()
			r.SchemaVersion = ""
			Expect(r.Validate()).To(Succeed())
			Expect(r.SchemaVersion).To(Equal("1.0"))
		})
	})
})
