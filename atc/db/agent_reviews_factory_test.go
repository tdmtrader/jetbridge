package db_test

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentReviewsFactory", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	rec := func(buildID int, team, pipeline, repo, commit string, score float64) *reviews.StoredReview {
		return &reviews.StoredReview{
			BuildID: buildID, BuildName: "1", TeamName: team,
			PipelineName: pipeline, JobName: "agent-review",
			Repo: repo, CommitSha: commit, Branch: "jetbridge",
			Score: score, MaxScore: 10, Pass: score >= 7,
			ProvenCount: 1, ObservationCount: 2, Summary: "s",
			AgentModel: "claude-sonnet-5", DurationSeconds: 60,
			SubmittedBy: "itest-reviewer",
			Review:      json.RawMessage(`{"schema_version":"1.0.0"}`),
		}
	}

	It("round-trips a review by build", func() {
		Expect(factory.Upsert(rec(101, "main", "p", "r", "c1", 7.5))).To(Succeed())

		got, err := factory.GetByBuild(101)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].TeamName).To(Equal("main"))
		Expect(got[0].Score).To(Equal(7.5))
		Expect(got[0].CreatedAt).To(BeNumerically(">", 0))
		Expect(got[0].Review).To(MatchJSON(`{"schema_version":"1.0.0"}`))
		Expect(got[0].SubmittedBy).To(Equal("itest-reviewer"))
	})

	It("returns multiple reviews for one build oldest-first", func() {
		Expect(factory.Upsert(rec(107, "main", "p", "r-first", "c-first", 6))).To(Succeed())
		Expect(factory.Upsert(rec(107, "main", "p", "r-second", "c-second", 8))).To(Succeed())

		got, err := factory.GetByBuild(107)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].Repo).To(Equal("r-first")) // oldest first
		Expect(got[1].Repo).To(Equal("r-second"))
	})

	It("upserts on (build_id, repo, commit_sha)", func() {
		Expect(factory.Upsert(rec(102, "main", "p", "r", "c2", 5.0))).To(Succeed())
		Expect(factory.Upsert(rec(102, "main", "p", "r", "c2", 9.0))).To(Succeed())

		got, err := factory.GetByBuild(102)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Score).To(Equal(9.0))
	})

	It("lists by team newest first with filters", func() {
		Expect(factory.Upsert(rec(103, "main", "pa", "r1", "c3", 8))).To(Succeed())
		Expect(factory.Upsert(rec(104, "main", "pb", "r2", "c4", 6))).To(Succeed())
		Expect(factory.Upsert(rec(105, "side", "pa", "r1", "c5", 7))).To(Succeed())

		got, err := factory.ListByTeam("main", reviews.ListFilter{Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].BuildID).To(Equal(104)) // newest first
		Expect(got[0].Review).To(BeNil())     // listings exclude the payload

		filtered, err := factory.ListByTeam("main", reviews.ListFilter{Pipeline: "pa", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(HaveLen(1))
		Expect(filtered[0].BuildID).To(Equal(103))
	})

	It("applies Limit, and Limit 0 returns all rows", func() {
		Expect(factory.Upsert(rec(108, "main", "p", "r1", "c8", 8))).To(Succeed())
		Expect(factory.Upsert(rec(109, "main", "p", "r2", "c9", 6))).To(Succeed())

		limited, err := factory.ListByTeam("main", reviews.ListFilter{Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(HaveLen(1))
		Expect(limited[0].BuildID).To(Equal(109)) // truncation keeps the newest

		all, err := factory.ListByTeam("main", reviews.ListFilter{Limit: 0})
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0].BuildID).To(Equal(109))
		Expect(all[1].BuildID).To(Equal(108))
	})

	It("counts evaluated findings from agent_feedback in listings", func() {
		Expect(factory.Upsert(rec(106, "main", "p", "fbrepo", "fbc", 8))).To(Succeed())

		fbFactory := db.NewAgentFeedbackFactory(dbConn)
		fbRec := feedbackRecord("fbrepo", "fbc", "PI-1", "accurate", "tdm")
		Expect(fbFactory.Save(&fbRec)).To(Succeed())

		got, err := factory.ListByTeam("main", reviews.ListFilter{Repo: "fbrepo", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].EvaluatedCount).To(Equal(1))
	})

	It("counts distinct finding_ids, not feedback rows", func() {
		Expect(factory.Upsert(rec(110, "main", "p", "distrepo", "distc", 8))).To(Succeed())

		fbFactory := db.NewAgentFeedbackFactory(dbConn)
		// Two reviewers weigh in on the SAME finding — one evaluated finding.
		fbA := feedbackRecord("distrepo", "distc", "PI-1", "accurate", "tdm")
		Expect(fbFactory.Save(&fbA)).To(Succeed())
		fbB := feedbackRecord("distrepo", "distc", "PI-1", "noisy", "bob")
		Expect(fbFactory.Save(&fbB)).To(Succeed())

		got, err := factory.ListByTeam("main", reviews.ListFilter{Repo: "distrepo", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].EvaluatedCount).To(Equal(1))
	})
})

func feedbackRecord(repo, commit, findingID, verdict, reviewer string) feedback.StoredFeedback {
	return feedback.StoredFeedback{
		ReviewRef: feedback.ReviewRef{Repo: repo, Commit: commit},
		FindingID: findingID, Verdict: verdict, Reviewer: reviewer,
	}
}
