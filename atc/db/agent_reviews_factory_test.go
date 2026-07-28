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

func insertReviewSnapshot(hexDigit string) snapshot.SnapshotID {
	var snapshotID snapshot.SnapshotID
	err := dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
		VALUES ($1, 'review', 1, $2, 1024, 1, 'filesystem-tree-v1', 'available')
		RETURNING id
	`, defaultTeam.ID(), "sha256:"+strings.Repeat(hexDigit, 64)).Scan(&snapshotID)
	Expect(err).ToNot(HaveOccurred())
	return snapshotID
}

// insertReviewSnapshotProjectionInput seals a review snapshot and records one
// upload occurrence for it — the shape a projected review that never ran in a
// build has.
func insertReviewSnapshotProjectionInput(hexDigit string) (snapshot.SnapshotID, snapshot.DatabaseID) {
	snapshotID := insertReviewSnapshot(hexDigit)
	var productionID snapshot.DatabaseID
	err := dbConn.QueryRow(`
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, team_id, team_name, created_by, upload_idempotency_key)
		VALUES ($1, 'upload', $2, $3, 'projection-test', $4)
		RETURNING id
	`, int64(snapshotID), defaultTeam.ID(), defaultTeam.Name(),
		fmt.Sprintf("review-projection-%s-%d", hexDigit, time.Now().UnixNano())).Scan(&productionID)
	Expect(err).ToNot(HaveOccurred())
	return snapshotID, productionID
}

// insertBuildProduction records a build occurrence of an already-sealed review
// snapshot. The build linkage lives here, not on the review row: one review can
// be produced by several builds.
func insertBuildProduction(snapshotID snapshot.SnapshotID, buildID int, planID string) snapshot.DatabaseID {
	var productionID snapshot.DatabaseID
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshot_productions
			(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
			 step_kind, step_name, output_port, occurrence_kind)
		VALUES ($1, $2, $3, $4, 'projection-test', $5, '1', 'task', 'review', 'review', 'build')
		RETURNING id
	`, int64(snapshotID), buildID, defaultTeam.ID(), defaultTeam.Name(), planID).Scan(&productionID)).To(Succeed())
	return productionID
}

func projectedReview(id snapshot.SnapshotID, production snapshot.DatabaseID, conclusion string, counts map[string]int) *reviews.StoredReview {
	return &reviews.StoredReview{
		SnapshotID: id, ProductionID: &production, TeamName: defaultTeam.Name(),
		Conclusion: conclusion, Summary: "s", SeverityCounts: counts,
		Review:      json.RawMessage(`{"record_version":"1.0.0"}`),
		SubmittedBy: "projector",
	}
}

var _ = Describe("AgentReviewsFactory", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	It("round-trips the sealed record's judgment by snapshot", func() {
		id, production := insertReviewSnapshotProjectionInput("a")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, production, "changes-required", map[string]int{"critical": 1, "observation": 2},
		))).To(Succeed())

		got, found, err := factory.GetBySnapshot(defaultTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.SnapshotID).To(Equal(id))
		Expect(got.Conclusion).To(Equal("changes-required"))
		Expect(got.SeverityCounts).To(Equal(map[string]int{"critical": 1, "observation": 2}))
		Expect(got.TeamName).To(Equal(defaultTeam.Name()))
		Expect(got.SubmittedBy).To(Equal("projection-test"), "the publisher is the production's, not the review row's")
		Expect(got.CreatedAt).To(BeNumerically(">", 0))
		Expect(got.Review).To(MatchJSON(`{"record_version":"1.0.0"}`))
	})

	It("re-projects the same snapshot in place and keeps distinct snapshots apart", func() {
		first, firstProduction := insertReviewSnapshotProjectionInput("b")
		second, secondProduction := insertReviewSnapshotProjectionInput("c")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			first, firstProduction, "inconclusive", map[string]int{}))).To(Succeed())
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			second, secondProduction, "accept", map[string]int{"observation": 1}))).To(Succeed())
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			first, firstProduction, "accept", map[string]int{"low": 1}))).To(Succeed())

		gotFirst, _, err := factory.GetBySnapshot(defaultTeam.Name(), first)
		Expect(err).ToNot(HaveOccurred())
		Expect(gotFirst.Conclusion).To(Equal("accept"))
		Expect(gotFirst.SeverityCounts).To(Equal(map[string]int{"low": 1}))
		gotSecond, _, err := factory.GetBySnapshot(defaultTeam.Name(), second)
		Expect(err).ToNot(HaveOccurred())
		Expect(gotSecond.SeverityCounts).To(Equal(map[string]int{"observation": 1}))

		var rows int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_reviews WHERE snapshot_id IN ($1, $2)`,
			int64(first), int64(second)).Scan(&rows)).To(Succeed())
		Expect(rows).To(Equal(2))
	})

	It("refuses a review with no snapshot identity", func() {
		production := snapshot.DatabaseID(1)
		Expect(factory.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
			ProductionID: &production, TeamName: defaultTeam.Name(),
		})).ToNot(Succeed())
	})

	It("reads the build occurrence, not a copy on the review row", func() {
		build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
		Expect(err).ToNot(HaveOccurred())
		id, upload := insertReviewSnapshotProjectionInput("7")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, upload, "accept", map[string]int{"low": 1}))).To(Succeed())
		insertBuildProduction(id, build.ID(), "review-plan")

		got, err := factory.GetByBuild(build.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].BuildID).To(Equal(build.ID()))
		Expect(got[0].BuildName).To(Equal(build.Name()))
		Expect(got[0].PipelineName).To(Equal(defaultPipeline.Name()))
		Expect(got[0].JobName).To(Equal(defaultJob.Name()))
		Expect(got[0].SnapshotID).To(Equal(id))
	})

	It("returns every review of one build, oldest occurrence first", func() {
		build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
		Expect(err).ToNot(HaveOccurred())
		firstID, firstUpload := insertReviewSnapshotProjectionInput("8")
		secondID, secondUpload := insertReviewSnapshotProjectionInput("0")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			firstID, firstUpload, "accept", map[string]int{}))).To(Succeed())
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			secondID, secondUpload, "changes-required", map[string]int{"high": 1}))).To(Succeed())
		insertBuildProduction(firstID, build.ID(), "first-plan")
		insertBuildProduction(secondID, build.ID(), "second-plan")

		got, err := factory.GetByBuild(build.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].SnapshotID).To(Equal(firstID))
		Expect(got[1].SnapshotID).To(Equal(secondID))
	})

	It("lists by team newest first, filtered by pipeline and truncated by limit", func() {
		build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
		Expect(err).ToNot(HaveOccurred())
		uploadOnly, uploadProduction := insertReviewSnapshotProjectionInput("1")
		inPipeline, pipelineUpload := insertReviewSnapshotProjectionInput("2")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			uploadOnly, uploadProduction, "accept", map[string]int{}))).To(Succeed())
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			inPipeline, pipelineUpload, "accept", map[string]int{}))).To(Succeed())
		insertBuildProduction(inPipeline, build.ID(), "pipeline-plan")

		filtered, err := factory.ListByTeam(defaultTeam.Name(), reviews.ListFilter{
			Pipeline: defaultPipeline.Name(), Limit: 10,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(HaveLen(1))
		Expect(filtered[0].SnapshotID).To(Equal(inPipeline))
		Expect(filtered[0].Review).To(BeNil(), "listings exclude the payload")

		all, err := factory.ListByTeam(defaultTeam.Name(), reviews.ListFilter{Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(1), "Limit truncates to the newest occurrence")
	})

	It("counts distinct evaluated findings from snapshot-keyed feedback", func() {
		id, production := insertReviewSnapshotProjectionInput("3")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, production, "changes-required", map[string]int{"high": 1}))).To(Succeed())

		fbFactory := db.NewAgentFeedbackFactory(dbConn)
		// Two reviewers weigh in on the SAME finding — one evaluated finding.
		for _, reviewer := range []string{"tdm", "bob"} {
			Expect(fbFactory.Save(&feedback.StoredFeedback{
				ReviewSnapshotID: id, ReviewTeamName: defaultTeam.Name(),
				FindingID: "PI-1", Verdict: "accurate", Reviewer: reviewer,
			})).To(Succeed())
		}

		got, found, err := factory.GetBySnapshot(defaultTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.EvaluatedCount).To(Equal(1))
	})
})

var _ = Describe("AgentReviewsFactory snapshot projections", func() {
	var factory db.AgentReviewsFactory

	BeforeEach(func() {
		factory = db.NewAgentReviewsFactory(dbConn)
	})

	It("discovers sealed-but-unprojected review snapshots and loads production context", func() {
		id, productionID := insertReviewSnapshotProjectionInput("4")
		candidates, err := factory.ListUnprojectedReviews(context.Background(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(candidates).To(ContainElement(snapshot.SnapshotRef{
			ID: id, Type: "review/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64)),
		}))

		input, found, err := factory.FindReviewInput(context.Background(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(input.Snapshot.ID).To(Equal(id))
		Expect(input.ProductionID).To(Equal(productionID))
		Expect(input.TeamName).To(Equal(defaultTeam.Name()))

		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, productionID, "accept", map[string]int{}))).To(Succeed())
		candidates, err = factory.ListUnprojectedReviews(context.Background(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(candidates).ToNot(ContainElement(HaveField("ID", id)))
	})

	It("keeps one value projection while listing every workflow production occurrence", func() {
		id, uploadProduction := insertReviewSnapshotProjectionInput("9")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, uploadProduction, "accept", map[string]int{"observation": 1}))).To(Succeed())

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
			Expect(got[0].SnapshotID).To(Equal(id))
			Expect(got[0].ProductionID).ToNot(BeNil())
			Expect(*got[0].ProductionID).To(Equal(occurrence.productionID))
			Expect(got[0].WorkflowRunID).ToNot(BeNil())
			Expect(*got[0].WorkflowRunID).To(Equal(occurrence.runID))
			// The durable run needs BOTH names to be linkable from a listing.
			Expect(got[0].WorkflowName).To(Equal(unique))
		}
		var projectionCount int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_reviews WHERE snapshot_id = $1`, int64(id)).Scan(&projectionCount)).To(Succeed())
		Expect(projectionCount).To(Equal(1))
	})

	It("does not authorize canonical review reads across snapshot owners", func() {
		id, productionID := insertReviewSnapshotProjectionInput("6")
		Expect(factory.UpsertReviewProjection(context.Background(), projectedReview(
			id, productionID, "accept", map[string]int{}))).To(Succeed())
		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("review-other-%d", time.Now().UnixNano())))
		Expect(err).ToNot(HaveOccurred())

		_, found, err := factory.GetBySnapshot(otherTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		_, found, err = factory.GetBySnapshot(otherTeam.Name(), id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
