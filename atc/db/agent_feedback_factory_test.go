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

	// The legacy repo/commit READ path is gone with GET /api/v1/agent/feedback;
	// Save still has a legacy branch (a request without review_snapshot_id), so
	// its write is asserted directly against the row it produces.
	Describe("Save on the legacy repo/commit branch", func() {
		readRow := func(findingID string) (string, float64, []byte) {
			var verdict string
			var confidence float64
			var findingSnapshot []byte
			ExpectWithOffset(1, dbConn.QueryRow(`
				SELECT verdict, confidence, finding_snapshot
				FROM agent_feedback
				WHERE repo = $1 AND commit_sha = $2 AND finding_id = $3 AND reviewer = $4
			`, "org/repo", "abc123", findingID, "alice").Scan(&verdict, &confidence, &findingSnapshot),
			).To(Succeed())
			return verdict, confidence, findingSnapshot
		}

		It("round-trips a feedback record", func() {
			findingSnapshot := json.RawMessage(`{"severity":"high","title":"Null deref"}`)
			Expect(factory.Save(&feedback.StoredFeedback{
				ReviewRef:       feedback.ReviewRef{Repo: "org/repo", Commit: "abc123"},
				FindingID:       "ISS-001",
				FindingType:     "proven_issue",
				FindingSnapshot: findingSnapshot,
				Verdict:         "accurate",
				Confidence:      0.9,
				Notes:           "real bug",
				Reviewer:        "alice",
				Source:          "interactive",
			})).To(Succeed())

			verdict, confidence, stored := readRow("ISS-001")
			Expect(verdict).To(Equal("accurate"))
			Expect(confidence).To(Equal(0.9))
			var raw json.RawMessage
			Expect(json.Unmarshal(stored, &raw)).To(Succeed())
			var snap map[string]string
			Expect(json.Unmarshal(raw, &snap)).To(Succeed())
			Expect(snap["severity"]).To(Equal("high"))
		})

		It("updates the existing record on conflict", func() {
			rec := &feedback.StoredFeedback{
				ReviewRef: feedback.ReviewRef{Repo: "org/repo", Commit: "abc123"},
				FindingID: "ISS-002",
				Verdict:   "accurate",
				Reviewer:  "alice",
			}
			Expect(factory.Save(rec)).To(Succeed())

			rec.Verdict = "false_positive"
			rec.Confidence = 0.85
			Expect(factory.Save(rec)).To(Succeed())

			var rows int
			Expect(dbConn.QueryRow(`
				SELECT COUNT(*) FROM agent_feedback
				WHERE repo = $1 AND commit_sha = $2 AND finding_id = $3 AND reviewer = $4
			`, "org/repo", "abc123", "ISS-002", "alice").Scan(&rows)).To(Succeed())
			Expect(rows).To(Equal(1))

			verdict, confidence, _ := readRow("ISS-002")
			Expect(verdict).To(Equal("false_positive"))
			Expect(confidence).To(Equal(0.85))
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
		var sameCommitRows int
		Expect(dbConn.QueryRow(`
			SELECT COUNT(*) FROM agent_feedback WHERE repo = $1 AND commit_sha = $2
		`, "org/repo", "same-commit").Scan(&sameCommitRows)).To(Succeed())
		Expect(sameCommitRows).To(Equal(2), "same-commit reviews must not collide")
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
