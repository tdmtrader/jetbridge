package db_test

import (
	"context"
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

	// The review snapshot is the only identity, so Save resolves it through the
	// review projection and the team's grant on it. A record naming a review the
	// team cannot see is a 404, not a stored row.
	Describe("Save", func() {
		It("round-trips a feedback record and updates it on conflict", func() {
			reviewsFactory := db.NewAgentReviewsFactory(dbConn)
			id, productionID := insertReviewSnapshotProjectionInput("f")
			Expect(reviewsFactory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
				SnapshotID: id, ProductionID: &productionID, TeamName: defaultTeam.Name(),
				Conclusion: "accept", Review: json.RawMessage(`{}`),
				SubmittedBy: "projector",
			})).To(Succeed())

			rec := &feedback.StoredFeedback{
				ReviewSnapshotID: id, ReviewTeamName: defaultTeam.Name(),
				FindingID:       "ISS-001",
				FindingType:     "proven_issue",
				FindingSnapshot: json.RawMessage(`{"severity":"high","title":"Null deref"}`),
				Verdict:         "accurate",
				Confidence:      0.9,
				Notes:           "real bug",
				Reviewer:        "alice",
				Source:          "interactive",
			}
			Expect(factory.Save(rec)).To(Succeed())

			got, err := factory.GetByReviewSnapshot(id, defaultTeam.Name())
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].Verdict).To(Equal("accurate"))
			Expect(got[0].Confidence).To(Equal(0.9))
			var raw json.RawMessage
			Expect(json.Unmarshal(got[0].FindingSnapshot, &raw)).To(Succeed())
			var snap map[string]string
			Expect(json.Unmarshal(raw, &snap)).To(Succeed())
			Expect(snap["severity"]).To(Equal("high"))

			rec.Verdict = "false_positive"
			rec.Confidence = 0.85
			Expect(factory.Save(rec)).To(Succeed())

			got, err = factory.GetByReviewSnapshot(id, defaultTeam.Name())
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].Verdict).To(Equal("false_positive"))
			Expect(got[0].Confidence).To(Equal(0.85))
		})

		It("rejects a record that names no review snapshot", func() {
			Expect(factory.Save(&feedback.StoredFeedback{
				ReviewTeamName: defaultTeam.Name(),
				FindingID:      "ISS-001", Verdict: "accurate", Reviewer: "alice",
			})).ToNot(Succeed())
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
				SnapshotID: id, ProductionID: &production, TeamName: defaultTeam.Name(),
				Conclusion: "accept", Review: json.RawMessage(`{}`),
				SubmittedBy: "projector",
			})).To(Succeed())
			Expect(feedbackFactory.Save(&feedback.StoredFeedback{
				ReviewSnapshotID: id, ReviewTeamName: defaultTeam.Name(),
				FindingID: "ISS-1", Verdict: "accurate", Reviewer: "alice",
			})).To(Succeed())
		}

		for _, id := range []snapshot.SnapshotID{first, second} {
			got, err := feedbackFactory.GetByReviewSnapshot(id, defaultTeam.Name())
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].ReviewSnapshotID).To(Equal(id))
			Expect(got[0].ReviewTeamName).To(Equal(defaultTeam.Name()))
		}

		Expect(feedbackFactory.Save(&feedback.StoredFeedback{
			ReviewSnapshotID: first, ReviewTeamName: defaultTeam.Name(),
			FindingID: "ISS-1", Verdict: "noisy", Reviewer: "alice",
		})).To(Succeed())
		got, err := feedbackFactory.GetByReviewSnapshot(first, defaultTeam.Name())
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Verdict).To(Equal("noisy"))
		var snapshotKeyedRows int
		Expect(dbConn.QueryRow(`
			SELECT COUNT(*) FROM agent_feedback WHERE review_snapshot_id IN ($1, $2)
		`, int64(first), int64(second)).Scan(&snapshotKeyedRows)).To(Succeed())
		Expect(snapshotKeyedRows).To(Equal(2), "two reviews of the same finding must not collide")
	})

	It("isolates content-addressed review feedback by authorized team", func() {
		reviewsFactory := db.NewAgentReviewsFactory(dbConn)
		feedbackFactory := db.NewAgentFeedbackFactory(dbConn)
		id, productionID := insertReviewSnapshotProjectionInput("5")
		Expect(reviewsFactory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			SnapshotID: id, ProductionID: &productionID, TeamName: defaultTeam.Name(),
			Conclusion: "accept", Review: json.RawMessage(`{}`),
			SubmittedBy: "projector",
		})).To(Succeed())
		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("feedback-other-%d", time.Now().UnixNano())))
		Expect(err).ToNot(HaveOccurred())

		unauthorized := &feedback.StoredFeedback{
			ReviewSnapshotID: id, ReviewTeamName: otherTeam.Name(),
			FindingID: "ISS-1", Verdict: "noisy", Reviewer: "alice",
		}
		Expect(feedbackFactory.Save(unauthorized)).To(MatchError(MatchRegexp("review projection not found.*")))
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'projection-test', 'shared review')
		`, int64(id), otherTeam.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(feedbackFactory.Save(&feedback.StoredFeedback{
			ReviewSnapshotID: id, ReviewTeamName: defaultTeam.Name(),
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
