package storage_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/storage"
)

var _ = Describe("FeedbackStore", func() {
	var (
		ctx   context.Context
		store storage.FeedbackStore
	)

	BeforeEach(func() {
		ctx = context.Background()
		store = storage.NewMemoryFeedbackStore()
	})

	newRecord := func(findingID string, verdict schema.Verdict) schema.FeedbackRecord {
		return schema.FeedbackRecord{
			ReviewRef: schema.ReviewRef{
				Repo:   "https://github.com/org/repo.git",
				Commit: "abc123",
			},
			FindingID:       findingID,
			FindingType:     "proven_issue",
			FindingSnapshot: json.RawMessage(`{"severity":"high"}`),
			Verdict:         verdict,
			Confidence:      0.9,
			Reviewer:        "tdm",
			Source:          schema.SourceInteractive,
		}
	}

	Describe("SaveFeedback", func() {
		It("saves and retrieves a feedback record", func() {
			rec := newRecord("ISS-001", schema.VerdictAccurate)
			Expect(store.SaveFeedback(ctx, &rec)).To(Succeed())

			results, err := store.GetFeedbackByReview(ctx, "https://github.com/org/repo.git", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].FindingID).To(Equal("ISS-001"))
			Expect(results[0].Verdict).To(Equal(schema.VerdictAccurate))
		})

		It("upserts on same repo+commit+finding_id+reviewer", func() {
			rec := newRecord("ISS-001", schema.VerdictAccurate)
			Expect(store.SaveFeedback(ctx, &rec)).To(Succeed())

			rec.Verdict = schema.VerdictFalsePositive
			Expect(store.SaveFeedback(ctx, &rec)).To(Succeed())

			results, err := store.GetFeedbackByReview(ctx, "https://github.com/org/repo.git", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].Verdict).To(Equal(schema.VerdictFalsePositive))
		})

		It("stores multiple records for different findings", func() {
			rec1 := newRecord("ISS-001", schema.VerdictAccurate)
			rec2 := newRecord("ISS-002", schema.VerdictFalsePositive)
			Expect(store.SaveFeedback(ctx, &rec1)).To(Succeed())
			Expect(store.SaveFeedback(ctx, &rec2)).To(Succeed())

			results, err := store.GetFeedbackByReview(ctx, "https://github.com/org/repo.git", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(2))
		})
	})

	Describe("GetFeedbackByReview", func() {
		It("returns empty slice when no feedback exists", func() {
			results, err := store.GetFeedbackByReview(ctx, "no-repo", "no-commit")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(BeEmpty())
		})

		It("filters by repo and commit", func() {
			rec1 := newRecord("ISS-001", schema.VerdictAccurate)
			rec2 := newRecord("ISS-002", schema.VerdictNoisy)
			rec2.ReviewRef.Commit = "other-commit"

			Expect(store.SaveFeedback(ctx, &rec1)).To(Succeed())
			Expect(store.SaveFeedback(ctx, &rec2)).To(Succeed())

			results, err := store.GetFeedbackByReview(ctx, "https://github.com/org/repo.git", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].FindingID).To(Equal("ISS-001"))
		})
	})

	Describe("GetFeedbackSummary", func() {
		It("returns zero summary when no feedback", func() {
			summary, err := store.GetFeedbackSummary(ctx, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(summary.Total).To(Equal(0))
			Expect(summary.AccuracyRate).To(Equal(0.0))
			Expect(summary.FPRate).To(Equal(0.0))
		})

		It("computes accuracy and FP rates", func() {
			records := []struct {
				id      string
				verdict schema.Verdict
			}{
				{"ISS-001", schema.VerdictAccurate},
				{"ISS-002", schema.VerdictAccurate},
				{"ISS-003", schema.VerdictFalsePositive},
				{"ISS-004", schema.VerdictNoisy},
			}
			for _, r := range records {
				rec := newRecord(r.id, r.verdict)
				Expect(store.SaveFeedback(ctx, &rec)).To(Succeed())
			}

			summary, err := store.GetFeedbackSummary(ctx, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(summary.Total).To(Equal(4))
			Expect(summary.AccuracyRate).To(BeNumerically("~", 0.5, 0.01))  // 2/4
			Expect(summary.FPRate).To(BeNumerically("~", 0.25, 0.01))       // 1/4
			Expect(summary.ByVerdict["accurate"]).To(Equal(2))
			Expect(summary.ByVerdict["false_positive"]).To(Equal(1))
			Expect(summary.ByVerdict["noisy"]).To(Equal(1))
		})

		It("filters by repo when specified", func() {
			rec1 := newRecord("ISS-001", schema.VerdictAccurate)
			rec2 := newRecord("ISS-002", schema.VerdictFalsePositive)
			rec2.ReviewRef.Repo = "other-repo"

			Expect(store.SaveFeedback(ctx, &rec1)).To(Succeed())
			Expect(store.SaveFeedback(ctx, &rec2)).To(Succeed())

			summary, err := store.GetFeedbackSummary(ctx, "https://github.com/org/repo.git")
			Expect(err).NotTo(HaveOccurred())
			Expect(summary.Total).To(Equal(1))
			Expect(summary.AccuracyRate).To(BeNumerically("~", 1.0, 0.01))
		})
	})
})
