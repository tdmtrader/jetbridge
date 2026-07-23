package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func insertReviewSnapshotProjectionInput(hexDigit string) (snapshot.SnapshotID, snapshot.DatabaseID) {
	var snapshotID snapshot.SnapshotID
	err := dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(type_name, type_version, digest, byte_size, file_count, representation, content_state)
		VALUES ('review', 1, $1, 1024, 1, 'filesystem-tree-v1', 'available')
		RETURNING id
	`, "sha256:"+strings.Repeat(hexDigit, 64)).Scan(&snapshotID)
	Expect(err).ToNot(HaveOccurred())

	var productionID snapshot.DatabaseID
	err = dbConn.QueryRow(`
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, team_id, team_name, created_by, upload_idempotency_key)
		VALUES ($1, 'upload', $2, $3, 'projection-test', $4)
		RETURNING id
	`, int64(snapshotID), defaultTeam.ID(), defaultTeam.Name(), fmt.Sprintf("review-projection-%s-%d", hexDigit, time.Now().UnixNano())).Scan(&productionID)
	Expect(err).ToNot(HaveOccurred())
	_, err = dbConn.Exec(`
		INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
		VALUES ($1, $2, 'projection-test', 'review projection test')
		ON CONFLICT (snapshot_id, team_id) DO NOTHING
	`, int64(snapshotID), defaultTeam.ID())
	Expect(err).ToNot(HaveOccurred())
	return snapshotID, productionID
}

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

		second := rec(102, "main", "p", "r", "c2", 9.0)
		second.Review = json.RawMessage(`{"schema_version":"1.0.0","resubmitted":true}`)
		second.SubmittedBy = "itest-reviewer-2"
		Expect(factory.Upsert(second)).To(Succeed())

		got, err := factory.GetByBuild(102)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Score).To(Equal(9.0))
		Expect(got[0].Review).To(MatchJSON(`{"schema_version":"1.0.0","resubmitted":true}`))
		Expect(got[0].SubmittedBy).To(Equal("itest-reviewer-2"))
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

var _ = Describe("AgentReviewsFactory ticket linkage", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	It("persists ticket_id/pipeline_run_id and lists by ticket oldest-first", func() {
		tid, prid := 42, 7
		err := factory.Upsert(&reviews.StoredReview{
			BuildID: 201, Repo: "o/r", CommitSha: "aaa", TeamName: "main",
			Review: json.RawMessage(`{}`), TicketID: &tid, PipelineRunID: &prid,
		})
		Expect(err).ToNot(HaveOccurred())
		err = factory.Upsert(&reviews.StoredReview{
			BuildID: 202, Repo: "o/r", CommitSha: "bbb", TeamName: "main",
			Review: json.RawMessage(`{}`), TicketID: &tid,
		})
		Expect(err).ToNot(HaveOccurred())

		got, err := factory.ListByTicket(42)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].BuildID).To(Equal(201))
		Expect(*got[0].TicketID).To(Equal(42))
		Expect(*got[0].PipelineRunID).To(Equal(7))
		Expect(got[1].PipelineRunID).To(BeNil())
	})

	It("preserves linkage when a NULL-linkage upsert hits the same key", func() {
		tid := 42
		Expect(factory.Upsert(&reviews.StoredReview{
			BuildID: 203, Repo: "o/r", CommitSha: "ccc", TeamName: "main",
			Review: json.RawMessage(`{}`), TicketID: &tid,
		})).To(Succeed())
		// same (build_id, repo, commit_sha) key, no linkage — the CI path
		Expect(factory.Upsert(&reviews.StoredReview{
			BuildID: 203, Repo: "o/r", CommitSha: "ccc", TeamName: "main",
			Review: json.RawMessage(`{}`),
		})).To(Succeed())

		got, err := factory.ListByTicket(42)
		Expect(err).ToNot(HaveOccurred())
		var builds []int
		for _, r := range got {
			builds = append(builds, r.BuildID)
		}
		Expect(builds).To(ContainElement(203), "COALESCE must preserve linkage past a NULL upsert")
	})
})

var _ = Describe("AgentReviewsFactory snapshot projections", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	It("idempotently upserts by snapshot_id without collapsing same-commit reviews", func() {
		first, firstProduction := insertReviewSnapshotProjectionInput("a")
		second, secondProduction := insertReviewSnapshotProjectionInput("b")
		for _, input := range []struct {
			id         snapshot.SnapshotID
			production snapshot.DatabaseID
			score      float64
		}{{first, firstProduction, 7}, {second, secondProduction, 8}} {
			id, production := input.id, input.production
			Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
				SnapshotID: &id, ProductionID: &production, TeamName: defaultTeam.Name(),
				Repo: "org/repo", CommitSha: "same-commit", Score: input.score,
				Review: json.RawMessage(`{"schema_version":"1.0.0"}`), SubmittedBy: "projector",
			})).To(Succeed())
		}

		firstAgain, found, err := factory.GetBySnapshot(defaultTeam.Name(), first)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		firstAgain.Score = 9
		Expect(factory.UpsertReviewProjection(context.Background(), &firstAgain)).To(Succeed())

		gotFirst, found, err := factory.GetBySnapshot(defaultTeam.Name(), first)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotFirst.Score).To(Equal(9.0))
		gotSecond, found, err := factory.GetBySnapshot(defaultTeam.Name(), second)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotSecond.Score).To(Equal(8.0))

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_reviews WHERE commit_sha = 'same-commit'`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(2))
	})

	It("keeps legacy and projected reviews distinct even for the same build and commit", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		buildID := build.ID()
		Expect(factory.Upsert(&reviews.StoredReview{
			BuildID: buildID, TeamName: defaultTeam.Name(), Repo: "org/repo", CommitSha: "shared",
			Review: json.RawMessage(`{}`), SubmittedBy: "legacy-publish",
		})).To(Succeed())

		id, _ := insertReviewSnapshotProjectionInput("f")
		var production snapshot.DatabaseID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
				 step_kind, step_name, output_port, occurrence_kind)
			VALUES ($1, $2, $3, $4, 'projection-test', 'review-plan', '1',
			        'task', 'review', 'review', 'build') RETURNING id
		`, int64(id), buildID, defaultTeam.ID(), defaultTeam.Name()).Scan(&production)).To(Succeed())
		Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			BuildID: buildID, SnapshotID: &id, ProductionID: &production,
			TeamName: defaultTeam.Name(), Repo: "org/repo", CommitSha: "shared",
			Review: json.RawMessage(`{}`), SubmittedBy: "projector",
		})).To(Succeed())

		got, err := factory.GetByBuild(buildID)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].SnapshotID).To(BeNil())
		Expect(got[1].SnapshotID).ToNot(BeNil())
		Expect(*got[1].SnapshotID).To(Equal(id))
	})

	It("discovers sealed-but-unprojected review snapshots and loads production context", func() {
		id, productionID := insertReviewSnapshotProjectionInput("c")
		candidates, err := factory.ListUnprojectedReviews(context.Background(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(candidates).To(ContainElement(snapshot.SnapshotRef{
			ID: id, Type: "review/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
		}))

		input, found, err := factory.FindReviewInput(context.Background(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(input.Snapshot.ID).To(Equal(id))
		Expect(input.ProductionID).To(Equal(productionID))
		Expect(input.TeamName).To(Equal(defaultTeam.Name()))
		Expect(input.SubmittedBy).To(Equal("projection-test"))

		projectionID, projectionProduction := id, productionID
		Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			SnapshotID: &projectionID, ProductionID: &projectionProduction,
			TeamName: defaultTeam.Name(), Repo: "r", CommitSha: "c",
			Review: json.RawMessage(`{}`), SubmittedBy: "projector",
		})).To(Succeed())
		candidates, err = factory.ListUnprojectedReviews(context.Background(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(candidates).ToNot(ContainElement(HaveField("ID", id)))
	})

	It("keeps one value projection while listing every workflow production occurrence", func() {
		id, uploadProduction := insertReviewSnapshotProjectionInput("9")
		Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			SnapshotID: &id, ProductionID: &uploadProduction, TeamName: defaultTeam.Name(),
			Repo: "org/repo", CommitSha: "value-sha", Review: json.RawMessage(`{}`),
			SubmittedBy: "projector",
		})).To(Succeed())

		unique := fmt.Sprintf("review-occurrences-%d", time.Now().UnixNano())
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'projection-test', 3, 1)
			RETURNING id
		`, unique, strings.Repeat("8", 64)).Scan(&definitionID)).To(Succeed())

		type occurrence struct {
			runID        snapshot.WorkflowRunID
			productionID snapshot.DatabaseID
		}
		occurrences := make([]occurrence, 0, 2)
		for index := 0; index < 2; index++ {
			var runID snapshot.WorkflowRunID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_runs
					(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
					 schema_version, signature_version, definition_content_hash, idempotency_key,
					 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
					 created_by, status)
				VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
				        'manual', '', 'projection-test', 'succeeded')
				RETURNING id
			`, defaultTeam.ID(), defaultTeam.Name(), definitionID, unique, strings.Repeat("8", 64),
				fmt.Sprintf("%s-run-%d", unique, index), strings.Repeat("7", 64)).Scan(&runID)).To(Succeed())
			var buildID int64
			Expect(dbConn.QueryRow(`
				INSERT INTO builds (name, status, team_id)
				VALUES ($1, 'succeeded', $2) RETURNING id
			`, fmt.Sprintf("occurrence-%d", index), defaultTeam.ID()).Scan(&buildID)).To(Succeed())
			var productionID snapshot.DatabaseID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshot_productions
					(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
					 step_kind, step_name, output_port, workflow_definition_id, workflow_run_id,
					 occurrence_kind)
				VALUES ($1, $2, $3, $4, 'projection-test', $5, '1', 'task', 'review',
				        'review', $6, $7, 'build') RETURNING id
			`, int64(id), buildID, defaultTeam.ID(), defaultTeam.Name(),
				fmt.Sprintf("plan-%d", index), definitionID, int64(runID)).Scan(&productionID)).To(Succeed())
			occurrences = append(occurrences, occurrence{runID: runID, productionID: productionID})
		}

		for _, occurrence := range occurrences {
			got, err := factory.ListByWorkflowRun(defaultTeam.Name(), unique, occurrence.runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].SnapshotID).ToNot(BeNil())
			Expect(*got[0].SnapshotID).To(Equal(id))
			Expect(got[0].ProductionID).ToNot(BeNil())
			Expect(*got[0].ProductionID).To(Equal(occurrence.productionID))
			Expect(got[0].WorkflowRunID).ToNot(BeNil())
			Expect(*got[0].WorkflowRunID).To(Equal(occurrence.runID))
		}
		var projectionCount int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_reviews WHERE snapshot_id = $1`, int64(id)).Scan(&projectionCount)).To(Succeed())
		Expect(projectionCount).To(Equal(1))
	})

	It("authorizes canonical review reads by the exact snapshot grant", func() {
		id, productionID := insertReviewSnapshotProjectionInput("6")
		Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			SnapshotID: &id, ProductionID: &productionID, TeamName: defaultTeam.Name(),
			Repo: "org/repo", CommitSha: "private", Review: json.RawMessage(`{}`),
			SubmittedBy: "projector",
		})).To(Succeed())
		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("review-other-%d", time.Now().UnixNano())))
		Expect(err).ToNot(HaveOccurred())

		_, found, err := factory.GetBySnapshot(otherTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'projection-test', 'shared review')
		`, int64(id), otherTeam.ID())
		Expect(err).ToNot(HaveOccurred())
		got, found, err := factory.GetBySnapshot(otherTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.TeamName).To(Equal(otherTeam.Name()))
		Expect(got.ProductionID).To(BeNil(), "a shared value has no invented production occurrence")
	})

	It("preserves legacy build-key reads alongside projections", func() {
		Expect(factory.Upsert(&reviews.StoredReview{
			BuildID: 909, BuildName: "1", TeamName: defaultTeam.Name(), PipelineName: "legacy",
			Repo: "org/repo", CommitSha: "legacy-sha", Score: 6,
			Review: json.RawMessage(`{}`), SubmittedBy: "legacy-publish",
		})).To(Succeed())
		got, err := factory.GetByBuild(909)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].SnapshotID).To(BeNil())
		Expect(got[0].CommitSha).To(Equal("legacy-sha"))
	})
})
