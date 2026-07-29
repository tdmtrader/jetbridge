package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("workflow resource-source admission persistence", func() {
	var scenario *dbtest.Scenario
	var pipeline db.WorkflowResourceSourcePipeline
	var definitionID int

	BeforeEach(func() {
		suffix := time.Now().UnixNano()
		scenario = dbtest.Setup(builder.WithPipeline(atc.Config{
			Resources: atc.ResourceConfigs{{Name: "repository", Type: dbtest.BaseResourceType, Source: atc.Source{"uri": fmt.Sprintf("https://example.invalid/%d", suffix)}}},
			Jobs:      atc.JobConfigs{{Name: "admit", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "repo-source", Resource: "repository"}}}}},
		}))
		Expect(dbConn.QueryRow(`INSERT INTO agent_workflow_definitions (name,version,content_hash,definition,created_by,schema_version,signature_version) VALUES ($1,1,$2,'schema_version: 3','alice',3,1) RETURNING id`, fmt.Sprintf("source-admission-%d", suffix), strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		pipeline = db.WorkflowResourceSourcePipeline{PipelineID: scenario.Pipeline.ID(), TeamID: scenario.Team.ID(), WorkflowDefinitionID: definitionID, WorkflowName: fmt.Sprintf("source-admission-%d", suffix), WorkflowVersion: 1, PipelineConfigVersion: int(scenario.Pipeline.ConfigVersion()), ConfigHash: strings.Repeat("b", 64), SourceDeclarations: []db.ResourceSourceDeclaration{{SourceName: "repo-source", ResourceName: "repository", SnapshotType: snapshot.TypeRef("repository/v1")}}}
		Expect(db.NewWorkflowResourceSourcePipelinesFactory(dbConn).Activate(context.Background(), pipeline)).To(Succeed())
	})

	It("scopes lifecycle ownership and serializes archive before new capture", func() {
		factory := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		Expect(factory.Drain(context.Background(), defaultTeam.ID(), pipeline.PipelineID)).To(HaveOccurred())
		Expect(factory.Drain(context.Background(), pipeline.TeamID, pipeline.PipelineID)).To(Succeed())
		Expect(factory.Archive(context.Background(), pipeline.TeamID, pipeline.PipelineID)).To(Succeed())
		_, err := db.NewWorkflowResourceSourceAdmissionsFactory(dbConn).CreateCaptured(context.Background(), db.CreateWorkflowResourceSourceAdmissionRequest{TeamID: pipeline.TeamID, WorkflowDefinitionID: definitionID, SourcePipelineID: pipeline.PipelineID, SourceConfigHash: pipeline.ConfigHash, IdempotencyKey: "archived", Mode: "automatic", SelectingBuildID: 1})
		Expect(err).To(MatchError(ContainSubstring("pipeline authority")))
	})

	It("derives persisted version and type only from the selecting build", func() {
		version := atc.Version{"ref": "deadbeef"}
		scenario.Run(builder.WithResourceVersions("repository", version))
		job := scenario.Job("admit")
		var buildID int
		Expect(dbConn.QueryRow(`INSERT INTO builds (name,status,team_id,pipeline_id,job_id) VALUES ($1,'pending',$2,$3,$4) RETURNING id`, fmt.Sprintf("source-select-%d", time.Now().UnixNano()), pipeline.TeamID, pipeline.PipelineID, job.ID()).Scan(&buildID)).To(Succeed())
		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		scenario.Run(builder.WithNextInputMapping("admit", dbtest.JobInputs{{Name: "repo-source", Version: version}}))
		_, ready, err := build.AdoptInputsAndPipes()
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		request := db.CreateWorkflowResourceSourceAdmissionRequest{TeamID: pipeline.TeamID, WorkflowDefinitionID: definitionID, SourcePipelineID: pipeline.PipelineID, SourceConfigHash: pipeline.ConfigHash, IdempotencyKey: "derived", Mode: "automatic", SelectingBuildID: int64(buildID)}
		admissionID, err := db.NewWorkflowResourceSourceAdmissionsFactory(dbConn).CreateCaptured(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(admissionID).To(BeNumerically(">", 0))
		var resourceName, sourceName, typeName, digest string
		var typeVersion int
		Expect(dbConn.QueryRow(`SELECT resource_name,source_name,snapshot_type_name,snapshot_type_version,version_digest FROM agent_workflow_resource_source_bindings WHERE admission_id=$1`, admissionID).Scan(&resourceName, &sourceName, &typeName, &typeVersion, &digest)).To(Succeed())
		Expect(resourceName).To(Equal("repository"))
		Expect(sourceName).To(Equal("repo-source"))
		Expect(typeName).To(Equal("repository"))
		Expect(typeVersion).To(Equal(1))
		Expect(digest).NotTo(BeEmpty())
		again, err := db.NewWorkflowResourceSourceAdmissionsFactory(dbConn).CreateCaptured(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(admissionID))
		request.Mode = "manual"
		_, err = db.NewWorkflowResourceSourceAdmissionsFactory(dbConn).CreateCaptured(context.Background(), request)
		Expect(err).To(MatchError(ContainSubstring("idempotency conflict")))

		declarations, err := json.Marshal([]db.ResourceSourceDeclaration{{SourceName: "repo-source", ResourceName: "substituted-resource", SnapshotType: snapshot.TypeRef("repository/v1")}})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_resource_source_pipelines SET source_declarations=$1 WHERE pipeline_id=$2`, declarations, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		request.IdempotencyKey = "frozen-declaration-rejection"
		request.Mode = "automatic"
		_, err = db.NewWorkflowResourceSourceAdmissionsFactory(dbConn).CreateCaptured(context.Background(), request)
		Expect(err).To(MatchError(ContainSubstring("does not match frozen source declaration")))
	})
})
