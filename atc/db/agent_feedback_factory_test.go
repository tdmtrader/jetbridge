package db_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/atc/db"
)

var _ = Describe("AgentFeedbackFactory", func() {
	var factory db.AgentFeedbackFactory

	BeforeEach(func() {
		factory = db.NewAgentFeedbackFactory(dbConn)
	})

	Describe("Save and GetByReview", func() {
		It("round-trips a feedback record", func() {
			snapshot := json.RawMessage(`{"severity":"high","title":"Null deref"}`)
			rec := &feedback.StoredFeedback{
				ReviewRef: feedback.ReviewRef{
					Repo:   "org/repo",
					Commit: "abc123",
				},
				FindingID:       "ISS-001",
				FindingType:     "proven_issue",
				FindingSnapshot: snapshot,
				Verdict:         "accurate",
				Confidence:      0.9,
				Notes:           "real bug",
				Reviewer:        "alice",
				Source:          "interactive",
			}

			err := factory.Save(rec)
			Expect(err).NotTo(HaveOccurred())

			results, err := factory.GetByReview("org/repo", "abc123")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].FindingID).To(Equal("ISS-001"))
			Expect(results[0].Verdict).To(Equal("accurate"))
			Expect(results[0].Confidence).To(Equal(0.9))
			Expect(results[0].Notes).To(Equal("real bug"))
			Expect(results[0].Reviewer).To(Equal("alice"))
			Expect(results[0].ReviewRef.Repo).To(Equal("org/repo"))
			Expect(results[0].ReviewRef.Commit).To(Equal("abc123"))

			var snap map[string]string
			Expect(json.Unmarshal(results[0].FindingSnapshot, &snap)).To(Succeed())
			Expect(snap["severity"]).To(Equal("high"))
		})
	})

	Describe("upsert", func() {
		It("updates existing record on conflict", func() {
			rec := &feedback.StoredFeedback{
				ReviewRef: feedback.ReviewRef{Repo: "org/repo", Commit: "abc123"},
				FindingID: "ISS-002",
				Verdict:   "accurate",
				Reviewer:  "alice",
			}
			Expect(factory.Save(rec)).To(Succeed())

			// Save again with different verdict — should upsert.
			rec.Verdict = "false_positive"
			rec.Confidence = 0.85
			Expect(factory.Save(rec)).To(Succeed())

			results, err := factory.GetByReview("org/repo", "abc123")
			Expect(err).NotTo(HaveOccurred())

			// Filter to ISS-002.
			var found []feedback.StoredFeedback
			for _, r := range results {
				if r.FindingID == "ISS-002" {
					found = append(found, r)
				}
			}
			Expect(found).To(HaveLen(1))
			Expect(found[0].Verdict).To(Equal("false_positive"))
			Expect(found[0].Confidence).To(Equal(0.85))
		})
	})

	Describe("GetAll", func() {
		It("returns all records", func() {
			for _, id := range []string{"ISS-010", "ISS-011", "ISS-012"} {
				Expect(factory.Save(&feedback.StoredFeedback{
					ReviewRef: feedback.ReviewRef{Repo: "r", Commit: "c"},
					FindingID: id,
					Verdict:   "accurate",
					Reviewer:  "bob",
				})).To(Succeed())
			}

			results, err := factory.GetAll()
			Expect(err).NotTo(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 3))
		})
	})

	Describe("GetByReview with no matches", func() {
		It("returns empty slice", func() {
			results, err := factory.GetByReview("nonexistent", "nonexistent")
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(0))
			Expect(results).NotTo(BeNil())
		})
	})

	Describe("GetByReview with empty repo", func() {
		It("filters only by commit_sha", func() {
			Expect(factory.Save(&feedback.StoredFeedback{
				ReviewRef: feedback.ReviewRef{Repo: "repo-a", Commit: "shared-commit"},
				FindingID: "ISS-020",
				Verdict:   "accurate",
				Reviewer:  "alice",
			})).To(Succeed())
			Expect(factory.Save(&feedback.StoredFeedback{
				ReviewRef: feedback.ReviewRef{Repo: "repo-b", Commit: "shared-commit"},
				FindingID: "ISS-021",
				Verdict:   "noisy",
				Reviewer:  "bob",
			})).To(Succeed())

			// Empty repo should return findings from both repos.
			results, err := factory.GetByReview("", "shared-commit")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))
		})
	})
})
