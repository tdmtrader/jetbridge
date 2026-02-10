package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("FeedbackRecord", func() {
	validRecord := func() schema.FeedbackRecord {
		return schema.FeedbackRecord{
			ReviewRef: schema.ReviewRef{
				Repo:      "https://github.com/org/repo.git",
				Commit:    "abc123",
				ReviewTS:  "2026-02-09T17:00:00Z",
			},
			FindingID:       "ISS-001",
			FindingType:     "proven_issue",
			FindingSnapshot: json.RawMessage(`{"severity":"high","title":"nil deref"}`),
			Verdict:         schema.VerdictAccurate,
			Confidence:      0.95,
			Notes:           "Good catch",
			Conversation: []schema.ConversationMessage{
				{Role: "system", Content: "Finding ISS-001: nil deref"},
				{Role: "human", Content: "yes this is real"},
			},
			Reviewer: "tdm",
			Source:   schema.SourceInteractive,
		}
	}

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			rec := validRecord()
			data, err := json.Marshal(rec)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.FeedbackRecord
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())

			Expect(decoded.FindingID).To(Equal("ISS-001"))
			Expect(decoded.Verdict).To(Equal(schema.VerdictAccurate))
			Expect(decoded.Confidence).To(BeNumerically("~", 0.95, 0.01))
			Expect(decoded.ReviewRef.Repo).To(Equal("https://github.com/org/repo.git"))
			Expect(decoded.Conversation).To(HaveLen(2))
			Expect(decoded.Source).To(Equal(schema.SourceInteractive))
		})

		It("includes all JSON fields", func() {
			rec := validRecord()
			data, err := json.Marshal(rec)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			Expect(json.Unmarshal(data, &raw)).To(Succeed())

			Expect(raw).To(HaveKey("review_ref"))
			Expect(raw).To(HaveKey("finding_id"))
			Expect(raw).To(HaveKey("finding_type"))
			Expect(raw).To(HaveKey("finding_snapshot"))
			Expect(raw).To(HaveKey("verdict"))
			Expect(raw).To(HaveKey("confidence"))
			Expect(raw).To(HaveKey("notes"))
			Expect(raw).To(HaveKey("conversation"))
			Expect(raw).To(HaveKey("reviewer"))
			Expect(raw).To(HaveKey("source"))
		})
	})

	Describe("Validate", func() {
		It("passes for a valid record", func() {
			rec := validRecord()
			Expect(rec.Validate()).To(Succeed())
		})

		It("requires finding_id", func() {
			rec := validRecord()
			rec.FindingID = ""
			Expect(rec.Validate()).To(MatchError(ContainSubstring("finding_id")))
		})

		It("requires verdict", func() {
			rec := validRecord()
			rec.Verdict = ""
			Expect(rec.Validate()).To(MatchError(ContainSubstring("verdict")))
		})

		It("requires finding_snapshot", func() {
			rec := validRecord()
			rec.FindingSnapshot = nil
			Expect(rec.Validate()).To(MatchError(ContainSubstring("finding_snapshot")))
		})

		It("requires review_ref.repo", func() {
			rec := validRecord()
			rec.ReviewRef.Repo = ""
			Expect(rec.Validate()).To(MatchError(ContainSubstring("repo")))
		})

		It("requires review_ref.commit", func() {
			rec := validRecord()
			rec.ReviewRef.Commit = ""
			Expect(rec.Validate()).To(MatchError(ContainSubstring("commit")))
		})

		It("rejects invalid verdict", func() {
			rec := validRecord()
			rec.Verdict = "invalid_verdict"
			Expect(rec.Validate()).To(MatchError(ContainSubstring("invalid verdict")))
		})

		It("rejects invalid source", func() {
			rec := validRecord()
			rec.Source = "bad_source"
			Expect(rec.Validate()).To(MatchError(ContainSubstring("invalid source")))
		})

		It("accepts all valid verdicts", func() {
			for _, v := range []schema.Verdict{
				schema.VerdictAccurate,
				schema.VerdictFalsePositive,
				schema.VerdictNoisy,
				schema.VerdictOverlyStrict,
				schema.VerdictPartiallyCorrect,
				schema.VerdictMissedContext,
			} {
				rec := validRecord()
				rec.Verdict = v
				Expect(rec.Validate()).To(Succeed(), "verdict %s should be valid", v)
			}
		})

		It("accepts all valid sources", func() {
			for _, s := range []schema.FeedbackSource{
				schema.SourceInteractive,
				schema.SourceInferredConversation,
				schema.SourceInferredOutcome,
			} {
				rec := validRecord()
				rec.Source = s
				Expect(rec.Validate()).To(Succeed(), "source %s should be valid", s)
			}
		})
	})
})
