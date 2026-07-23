package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"

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

var _ = Describe("AgentFeedbackFactory snapshot review identity", func() {
	It("keys feedback by review_snapshot_id, finding_id, and reviewer", func() {
		reviewsFactory := db.NewAgentReviewsFactory(dbConn)
		feedbackFactory := db.NewAgentFeedbackFactory(dbConn)
		first, firstProduction := insertReviewSnapshotProjectionInput("d")
		second, secondProduction := insertReviewSnapshotProjectionInput("e")
		for _, input := range []struct {
			id         snapshot.SnapshotID
			production snapshot.DatabaseID
		}{{first, firstProduction}, {second, secondProduction}} {
			id, production := input.id, input.production
			Expect(reviewsFactory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
				SnapshotID: &id, ProductionID: &production, TeamName: defaultTeam.Name(),
				Repo: "org/repo", CommitSha: "same-commit", Review: json.RawMessage(`{}`),
				SubmittedBy: "projector",
			})).To(Succeed())
			Expect(feedbackFactory.Save(&feedback.StoredFeedback{
				ReviewSnapshotID: &id, ReviewTeamName: defaultTeam.Name(),
				FindingID: "ISS-1", Verdict: "accurate", Reviewer: "alice",
			})).To(Succeed())
		}

		for _, id := range []snapshot.SnapshotID{first, second} {
			got, err := feedbackFactory.GetByReviewSnapshot(id, defaultTeam.Name())
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].ReviewSnapshotID).ToNot(BeNil())
			Expect(*got[0].ReviewSnapshotID).To(Equal(id))
			Expect(got[0].ReviewRef.Repo).To(Equal("org/repo"), "legacy presentation fields come from the projection")
			Expect(got[0].ReviewRef.Commit).To(Equal("same-commit"))
		}

		Expect(feedbackFactory.Save(&feedback.StoredFeedback{
			ReviewSnapshotID: &first, ReviewTeamName: defaultTeam.Name(),
			FindingID: "ISS-1", Verdict: "noisy", Reviewer: "alice",
		})).To(Succeed())
		got, err := feedbackFactory.GetByReviewSnapshot(first, defaultTeam.Name())
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Verdict).To(Equal("noisy"))
		all, err := feedbackFactory.GetByReview("org/repo", "same-commit")
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(2), "same-commit reviews must not collide")
	})

	It("isolates content-addressed review feedback by authorized team", func() {
		reviewsFactory := db.NewAgentReviewsFactory(dbConn)
		feedbackFactory := db.NewAgentFeedbackFactory(dbConn)
		id, productionID := insertReviewSnapshotProjectionInput("5")
		Expect(reviewsFactory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			SnapshotID: &id, ProductionID: &productionID, TeamName: defaultTeam.Name(),
			Repo: "org/repo", CommitSha: "shared-value", Review: json.RawMessage(`{}`),
			SubmittedBy: "projector",
		})).To(Succeed())
		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("feedback-other-%d", time.Now().UnixNano())))
		Expect(err).ToNot(HaveOccurred())

		unauthorized := &feedback.StoredFeedback{
			ReviewSnapshotID: &id, ReviewTeamName: otherTeam.Name(),
			FindingID: "ISS-1", Verdict: "noisy", Reviewer: "alice",
		}
		Expect(feedbackFactory.Save(unauthorized)).To(MatchError(MatchRegexp("review projection not found.*")))
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'projection-test', 'shared review')
		`, int64(id), otherTeam.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(feedbackFactory.Save(&feedback.StoredFeedback{
			ReviewSnapshotID: &id, ReviewTeamName: defaultTeam.Name(),
			FindingID: "ISS-1", Verdict: "accurate", Reviewer: "alice",
		})).To(Succeed())
		Expect(feedbackFactory.Save(unauthorized)).To(Succeed())

		mainRows, err := feedbackFactory.GetByReviewSnapshot(id, defaultTeam.Name())
		Expect(err).ToNot(HaveOccurred())
		Expect(mainRows).To(HaveLen(1))
		Expect(mainRows[0].Verdict).To(Equal("accurate"))
		otherRows, err := feedbackFactory.GetByReviewSnapshot(id, otherTeam.Name())
		Expect(err).ToNot(HaveOccurred())
		Expect(otherRows).To(HaveLen(1))
		Expect(otherRows[0].Verdict).To(Equal("noisy"))
	})
})

var _ = Describe("AgentFeedbackFactory ticket backfill", func() {
	var factory db.AgentFeedbackFactory

	BeforeEach(func() {
		factory = db.NewAgentFeedbackFactory(dbConn)
	})

	It("backfills ticket_id from the linked review on Save", func() {
		tid := 42
		Expect(db.NewAgentReviewsFactory(dbConn).Upsert(&reviews.StoredReview{
			BuildID: 301, Repo: "o/r", CommitSha: "ddd", TeamName: "main",
			Review: json.RawMessage(`{}`), TicketID: &tid,
		})).To(Succeed())

		rec := feedbackRecord("o/r", "ddd", "judge-correctness-1", "accurate", "human")
		rec.FindingType = "judge"
		Expect(factory.Save(&rec)).To(Succeed())

		var got sql.NullInt64
		err := dbConn.QueryRow(
			`SELECT ticket_id FROM agent_feedback WHERE repo = 'o/r' AND commit_sha = 'ddd' AND finding_id = 'judge-correctness-1'`,
		).Scan(&got)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Valid).To(BeTrue())
		Expect(got.Int64).To(Equal(int64(42)))
	})
})
