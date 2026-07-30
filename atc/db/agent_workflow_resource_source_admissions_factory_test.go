package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
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

	It("keeps definition-owned resource source lookups isolated while exposing exact binding-owned instances to shared lifecycle", func() {
		publicationTarget := agentPRBindingPublicationTarget{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderGitHub,
				Repository: "example/source-binding",
				ExternalID: fmt.Sprint(time.Now().UnixNano()),
			},
			URL:                   "https://github.example/example/source-binding/pull/1",
			SourceRef:             "refs/heads/source",
			SourceSHA:             strings.Repeat("c", 40),
			TargetRef:             "refs/heads/main",
			TargetSHA:             strings.Repeat("d", 40),
			Destination:           "github.example/example/source-binding",
			ApprovalPolicyVersion: "engineering/v3",
		}
		originRunID, acceptedOccurrenceID, creationOccurrenceID, observationID :=
			insertAgentPRBindingEvidence(
				pipeline.TeamID, scenario.Team.Name(), definitionID,
				fmt.Sprintf("source-binding-%d", time.Now().UnixNano()),
				publicationTarget,
			)
		binding, created, err := db.NewAgentPRBindingsFactory(dbConn).Create(
			context.Background(),
			pullrequest.CreateBinding{
				TeamID:                           pipeline.TeamID,
				Locator:                          publicationTarget.Locator,
				URL:                              publicationTarget.URL,
				SourceRef:                        publicationTarget.SourceRef,
				TargetRef:                        publicationTarget.TargetRef,
				Destination:                      publicationTarget.Destination,
				ApprovalPolicyVersion:            publicationTarget.ApprovalPolicyVersion,
				OriginatingWorkflowRunID:         snapshot.WorkflowRunID(originRunID),
				OriginatingPublicationOccurrence: acceptedOccurrenceID,
				CreationPublicationOccurrenceID:  creationOccurrenceID,
				MonitorWorkflowDefinitionID:      definitionID,
				MonitorWorkflowVersion:           1,
				AcknowledgedCursor:               pullrequest.Cursor("opaque-binding-cursor"),
				LastObservationSnapshotID:        snapshot.SnapshotID(observationID),
				LastReconciledSourceSHA:          publicationTarget.SourceSHA,
				LastReconciledTargetSHA:          publicationTarget.TargetSHA,
				LastReconciledAt:                 time.Now().UTC(),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		var bindingPipelineID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering, paused, version)
			VALUES ($1, $2, 1, true, 1) RETURNING id
		`, fmt.Sprintf("binding-source-%d", binding.ID), pipeline.TeamID).Scan(&bindingPipelineID)).To(Succeed())
		declarations, err := json.Marshal(pipeline.SourceDeclarations)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_resource_source_pipelines
				(pipeline_id, team_id, workflow_definition_id, workflow_name,
				 workflow_version, pipeline_config_version, config_hash,
				 source_declarations, state, pr_binding_id)
			VALUES ($1,$2,$3,$4,1,1,$5,$6,'active',$7)
		`, bindingPipelineID, pipeline.TeamID, definitionID, pipeline.WorkflowName,
			strings.Repeat("e", 64), declarations, binding.ID)
		Expect(err).NotTo(HaveOccurred())

		registry := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		_, found, err := registry.Find(context.Background(), pipeline.TeamID, bindingPipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "legacy pipeline-ID lookup must not cross the instance seam")
		owned, found, err := registry.FindByBinding(context.Background(), pipeline.TeamID, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(owned.PipelineID).To(Equal(bindingPipelineID))
		Expect(owned.PRBindingID).NotTo(BeNil())
		Expect(*owned.PRBindingID).To(Equal(binding.ID))
		_, found, err = registry.FindByBinding(context.Background(), defaultTeam.ID(), binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		active, found, err := registry.FindActive(context.Background(), pipeline.TeamID, pipeline.WorkflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(active.PipelineID).To(Equal(pipeline.PipelineID))
		dbConn.SetMaxOpenConns(2)
		lifecycle, err := registry.ResourceSourcePipelineLifecycle(context.Background(), pipeline.TeamID)
		Expect(err).NotTo(HaveOccurred())
		Expect(lifecycle).To(HaveLen(2))
		Expect(lifecycle).To(ContainElements(
			HaveField("PipelineID", pipeline.PipelineID),
			HaveField("PipelineID", bindingPipelineID),
		))
		Expect(registry.Drain(context.Background(), pipeline.TeamID, bindingPipelineID)).To(HaveOccurred())
	})

	It("atomically owns and reconfigures one protected paused PR monitor source pipeline per binding", func() {
		publicationTarget := agentPRBindingPublicationTarget{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderGitHub,
				Repository: "example/monitor-pipeline",
				ExternalID: fmt.Sprint(time.Now().UnixNano()),
			},
			URL:                   "https://github.example/example/monitor-pipeline/pull/1",
			SourceRef:             "refs/heads/source",
			SourceSHA:             strings.Repeat("c", 40),
			TargetRef:             "refs/heads/main",
			TargetSHA:             strings.Repeat("d", 40),
			Destination:           "github.example/example/monitor-pipeline",
			ApprovalPolicyVersion: "engineering/v3",
		}
		originRunID, acceptedOccurrenceID, creationOccurrenceID, observationID :=
			insertAgentPRBindingEvidence(
				pipeline.TeamID, scenario.Team.Name(), definitionID,
				fmt.Sprintf("monitor-pipeline-%d", time.Now().UnixNano()),
				publicationTarget,
			)
		bindings := db.NewAgentPRBindingsFactory(dbConn)
		binding, created, err := bindings.Create(
			context.Background(),
			pullrequest.CreateBinding{
				TeamID:                           pipeline.TeamID,
				Locator:                          publicationTarget.Locator,
				URL:                              publicationTarget.URL,
				SourceRef:                        publicationTarget.SourceRef,
				TargetRef:                        publicationTarget.TargetRef,
				Destination:                      publicationTarget.Destination,
				ApprovalPolicyVersion:            publicationTarget.ApprovalPolicyVersion,
				OriginatingWorkflowRunID:         snapshot.WorkflowRunID(originRunID),
				OriginatingPublicationOccurrence: acceptedOccurrenceID,
				CreationPublicationOccurrenceID:  creationOccurrenceID,
				MonitorWorkflowDefinitionID:      definitionID,
				MonitorWorkflowVersion:           1,
				AcknowledgedCursor:               pullrequest.Cursor("monitor-cursor-1"),
				LastObservationSnapshotID:        snapshot.SnapshotID(observationID),
				LastReconciledSourceSHA:          publicationTarget.SourceSHA,
				LastReconciledTargetSHA:          publicationTarget.TargetSHA,
				LastReconciledAt:                 time.Now().UTC().Truncate(time.Microsecond),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		originalRevision := binding.Revision

		target, err := pullrequest.MonitorPipelineTargetForBinding(
			binding,
			dbTestMonitorPipelinePolicy(),
		)
		Expect(err).NotTo(HaveOccurred())
		rendered, err := pullrequest.RenderMonitorPipeline(target)
		Expect(err).NotTo(HaveOccurred())

		registry := db.NewWorkflowResourceSourcePipelinesFactory(dbConn)
		registered, changed, err := registry.ConvergeMonitorPipeline(
			context.Background(), binding, rendered,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(registered.PRBindingID).NotTo(BeNil())
		Expect(*registered.PRBindingID).To(Equal(binding.ID))
		Expect(registered.State).To(Equal(db.AgentWorkflowResourceSourcePipelineActive))
		Expect(registered.SourceDeclarations).To(Equal([]db.ResourceSourceDeclaration{{
			SourceName:   pullrequest.MonitorSourceName,
			ResourceName: pullrequest.MonitorResourceName,
			SnapshotType: snapshot.TypeRef("pull-request/v1"),
		}}))

		storedBinding, found, err := bindings.Get(
			context.Background(), binding.TeamID, binding.ID,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(storedBinding.Revision).To(Equal(originalRevision))
		Expect(storedBinding.PipelineID).NotTo(BeNil())
		Expect(*storedBinding.PipelineID).To(Equal(registered.PipelineID))

		physical, found, err := scenario.Team.Pipeline(
			atc.PipelineRef{Name: rendered.PipelineName},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(physical.Template()).To(BeFalse())
		Expect(physical.Paused()).To(BeTrue())
		config, err := physical.Config()
		Expect(err).NotTo(HaveOccurred())
		canonical, err := config.CanonicalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(canonical).To(Equal(rendered.CanonicalJSON))

		repeated, changed, err := registry.ConvergeMonitorPipeline(
			context.Background(), storedBinding, rendered,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(repeated).To(Equal(registered))

		_, _, err = scenario.Team.SavePipeline(
			atc.PipelineRef{Name: physical.Name()},
			config,
			physical.ConfigVersion(),
			false,
		)
		Expect(errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable)).To(BeTrue())
		Expect(errors.Is(
			physical.Pause("caller"),
			db.ErrAgentWorkflowResourceSourceImmutable,
		)).To(BeTrue())
		Expect(errors.Is(
			physical.Archive(),
			db.ErrAgentWorkflowResourceSourceImmutable,
		)).To(BeTrue())
		job, found, err := physical.Job(pullrequest.MonitorJobName)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = job.CreateBuild("caller")
		Expect(errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable)).To(BeTrue())

		_, err = registry.UnpauseActiveResourceSourcePipeline(
			context.Background(),
			binding.TeamID,
			registered,
		)
		Expect(err).To(HaveOccurred())
		activated, err := registry.UnpauseActiveBindingResourceSourcePipeline(
			context.Background(),
			binding.TeamID,
			registered,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(activated).To(BeTrue())
		physical, found, err = scenario.Team.Pipeline(
			atc.PipelineRef{Name: rendered.PipelineName},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(physical.Paused()).To(BeFalse())

		observed, err := bindings.RequestObservation(
			context.Background(),
			pullrequest.OperatorRequest{
				TeamID: binding.TeamID, BindingID: binding.ID,
				ExpectedRevision: storedBinding.Revision,
			},
		)
		Expect(err).NotTo(HaveOccurred())
		nextTarget, err := pullrequest.MonitorPipelineTargetForBinding(
			observed,
			dbTestMonitorPipelinePolicy(),
		)
		Expect(err).NotTo(HaveOccurred())
		nextRendered, err := pullrequest.RenderMonitorPipeline(nextTarget)
		Expect(err).NotTo(HaveOccurred())
		Expect(nextRendered.PipelineName).To(Equal(rendered.PipelineName))
		Expect(nextRendered.ConfigHash).NotTo(Equal(rendered.ConfigHash))

		reconfigured, changed, err := registry.ConvergeMonitorPipeline(
			context.Background(), observed, nextRendered,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(reconfigured.PipelineID).To(Equal(registered.PipelineID))
		Expect(reconfigured.PipelineConfigVersion).NotTo(Equal(
			registered.PipelineConfigVersion,
		))
		Expect(reconfigured.ConfigHash).To(Equal(nextRendered.ConfigHash))
		physical, found, err = scenario.Team.Pipeline(
			atc.PipelineRef{Name: nextRendered.PipelineName},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(physical.Paused()).To(BeFalse())
		config, err = physical.Config()
		Expect(err).NotTo(HaveOccurred())
		canonical, err = config.CanonicalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(canonical).To(Equal(nextRendered.CanonicalJSON))

		_, _, err = registry.ConvergeMonitorPipeline(
			context.Background(), storedBinding, rendered,
		)
		Expect(errors.Is(err, pullrequest.ErrStaleBindingRevision)).To(BeTrue())

		attention, err := bindings.MarkAttention(
			context.Background(), observed.TeamID, observed.ID,
			"provider result requires operator attention",
		)
		Expect(err).NotTo(HaveOccurred())
		paused, err := registry.PauseActiveBindingResourceSourcePipeline(
			context.Background(), observed.TeamID, reconfigured,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(paused).To(BeTrue())
		resumed, err := bindings.Resume(
			context.Background(),
			pullrequest.OperatorRequest{
				TeamID: attention.TeamID, BindingID: attention.ID,
				ExpectedRevision: attention.Revision,
			},
		)
		Expect(err).NotTo(HaveOccurred())
		unpaused, err := registry.UnpauseActiveBindingResourceSourcePipeline(
			context.Background(), resumed.TeamID, reconfigured,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(unpaused).To(BeTrue())
		terminated, err := bindings.Terminate(
			context.Background(),
			pullrequest.OperatorRequest{
				TeamID: resumed.TeamID, BindingID: resumed.ID,
				ExpectedRevision: resumed.Revision,
			},
		)
		Expect(err).NotTo(HaveOccurred())
		drained, err := registry.DrainTerminalBindingResourceSourcePipeline(
			context.Background(), terminated.TeamID, reconfigured,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(drained).To(BeTrue())
		stored, found, err := registry.FindByBinding(
			context.Background(), terminated.TeamID, terminated.ID,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.State).To(Equal(
			db.AgentWorkflowResourceSourcePipelineDraining,
		))
		drained, err = registry.DrainTerminalBindingResourceSourcePipeline(
			context.Background(), terminated.TeamID, reconfigured,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(drained).To(BeFalse())
		paused, err = registry.PauseDrainedResourceSourcePipeline(
			context.Background(), terminated.TeamID, stored,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(paused).To(BeTrue())
		archived, err := registry.ArchiveDrainedResourceSourcePipeline(
			context.Background(), terminated.TeamID, stored,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(archived).To(BeTrue())
		archived, err = registry.ArchiveDrainedResourceSourcePipeline(
			context.Background(), terminated.TeamID, stored,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(archived).To(BeFalse())
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

	It("rejects a caller selection that substitutes the frozen declared resource", func() {
		version := atc.Version{"ref": "immutable-input"}
		scenario.Run(builder.WithResourceVersions("repository", version))
		job := scenario.Job("admit")
		var buildID int
		Expect(dbConn.QueryRow(`INSERT INTO builds (name,status,team_id,pipeline_id,job_id) VALUES ($1,'pending',$2,$3,$4) RETURNING id`, fmt.Sprintf("source-select-substitution-%d", time.Now().UnixNano()), pipeline.TeamID, pipeline.PipelineID, job.ID()).Scan(&buildID)).To(Succeed())
		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		scenario.Run(builder.WithNextInputMapping("admit", dbtest.JobInputs{{Name: "repo-source", Version: version}}))
		_, ready, err := build.AdoptInputsAndPipes()
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

		store := db.NewWorkflowResourceSourceAdmissionStore(dbConn)
		admission, created, err := store.ClaimBuild(context.Background(), pipeline.TeamID, pipeline.PipelineID, int64(buildID), db.BuildClaim{
			WorkflowDefinitionID: definitionID,
			SourceConfigHash:     pipeline.ConfigHash,
			IdempotencyKey:       "substituted-input",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		var resourceID int
		var resourceConfigVersionID int64
		var digest string
		Expect(dbConn.QueryRow(`
			SELECT resource.id, version.id, input.version_digest
			FROM build_resource_config_version_inputs input
			JOIN resources resource ON resource.id = input.resource_id
			JOIN resource_config_versions version
			  ON version.resource_config_scope_id = resource.resource_config_scope_id
			 AND input.version_digest IN (version.version_md5, version.version_sha256)
			WHERE input.build_id = $1 AND input.name = 'repo-source'
		`, buildID).Scan(&resourceID, &resourceConfigVersionID, &digest)).To(Succeed())
		callerKey, err := db.WorkflowResourceSourceCaptureOperationKey(
			pipeline.TeamID, definitionID, pipeline.PipelineID,
			pipeline.PipelineConfigVersion, "repo-source", "substituted-resource",
			digest, snapshot.TypeRef("repository/v1"),
		)
		Expect(err).NotTo(HaveOccurred())

		_, err = store.BindSelection(context.Background(), pipeline.TeamID, admission.ID, int64(buildID), []db.SelectedSource{{
			SourceName:              "repo-source",
			ResourceName:            "substituted-resource",
			SelectingBuildID:        int64(buildID),
			ResourceID:              resourceID,
			ResourceConfigVersionID: resourceConfigVersionID,
			ResourceVersionID:       resourceConfigVersionID,
			VersionDigest:           digest,
			Version:                 version,
			SnapshotType:            snapshot.TypeRef("repository/v1"),
			CaptureOperationKey:     callerKey,
		}})
		Expect(err).To(MatchError(ContainSubstring("does not match frozen source declaration")))
	})

	It("accepts a repeated final capture when a concurrent worker already made the admission ready", func() {
		version := atc.Version{"ref": "concurrent-capture"}
		scenario.Run(builder.WithResourceVersions("repository", version))
		job := scenario.Job("admit")
		var buildID int
		Expect(dbConn.QueryRow(`INSERT INTO builds (name,status,team_id,pipeline_id,job_id) VALUES ($1,'pending',$2,$3,$4) RETURNING id`, fmt.Sprintf("source-select-capture-%d", time.Now().UnixNano()), pipeline.TeamID, pipeline.PipelineID, job.ID()).Scan(&buildID)).To(Succeed())
		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		scenario.Run(builder.WithNextInputMapping("admit", dbtest.JobInputs{{Name: "repo-source", Version: version}}))
		_, ready, err := build.AdoptInputsAndPipes()
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

		store := db.NewWorkflowResourceSourceAdmissionStore(dbConn)
		admission, _, err := store.ClaimBuild(context.Background(), pipeline.TeamID, pipeline.PipelineID, int64(buildID), db.BuildClaim{
			WorkflowDefinitionID: definitionID, SourceConfigHash: pipeline.ConfigHash, IdempotencyKey: "concurrent-final-capture",
		})
		Expect(err).NotTo(HaveOccurred())
		var resourceID int
		var resourceConfigVersionID int64
		var digest string
		Expect(dbConn.QueryRow(`
			SELECT resource.id, version.id, input.version_digest
			FROM build_resource_config_version_inputs input
			JOIN resources resource ON resource.id = input.resource_id
			JOIN resource_config_versions version ON version.resource_config_scope_id = resource.resource_config_scope_id
			  AND input.version_digest IN (version.version_md5, version.version_sha256)
			WHERE input.build_id = $1 AND input.name = 'repo-source'`, buildID).Scan(&resourceID, &resourceConfigVersionID, &digest)).To(Succeed())
		captureKey, err := db.WorkflowResourceSourceCaptureOperationKey(pipeline.TeamID, definitionID, pipeline.PipelineID, pipeline.PipelineConfigVersion, "repo-source", "repository", digest, snapshot.TypeRef("repository/v1"))
		Expect(err).NotTo(HaveOccurred())
		_, err = store.BindSelection(context.Background(), pipeline.TeamID, admission.ID, int64(buildID), []db.SelectedSource{{
			SourceName: "repo-source", ResourceName: "repository", SelectingBuildID: int64(buildID), ResourceID: resourceID,
			ResourceConfigVersionID: resourceConfigVersionID, ResourceVersionID: resourceConfigVersionID,
			VersionDigest: digest, Version: version, SnapshotType: snapshot.TypeRef("repository/v1"), CaptureOperationKey: captureKey,
		}})
		Expect(err).NotTo(HaveOccurred())
		var snapshotID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots (team_id,type_name,type_version,digest,byte_size,file_count,representation)
			VALUES ($1,'repository',1,$2,1,1,'application/vnd.jetbridge.snapshot.tar.v1') RETURNING id`, pipeline.TeamID, "sha256:"+strings.Repeat("c", 64)).Scan(&snapshotID)).To(Succeed())
		bound, err := store.BindCapture(context.Background(), pipeline.TeamID, admission.ID, "repo-source", snapshot.SnapshotID(snapshotID))
		Expect(err).NotTo(HaveOccurred())
		Expect(bound).To(BeTrue())
		bound, err = store.BindCapture(context.Background(), pipeline.TeamID, admission.ID, "repo-source", snapshot.SnapshotID(snapshotID))
		Expect(err).NotTo(HaveOccurred())
		Expect(bound).To(BeFalse())
	})

	It("attaches exactly one ordinary manually-triggered admit build to a manual admission", func() {
		admission, created, err := db.NewWorkflowResourceSourceAdmissionStore(dbConn).CreateManual(context.Background(), pipeline.TeamID, db.ManualAdmissionIdentity{
			WorkflowDefinitionID: definitionID, SourcePipelineID: pipeline.PipelineID,
			SourceConfigHash: pipeline.ConfigHash, IdempotencyKey: "manual-normal-build",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		builds := db.NewWorkflowResourceSourceBuildStore(dbConn, lockFactory, checkFactory)
		first, firstCreated, err := builds.EnsureManualBuild(context.Background(), pipeline.TeamID, admission.ID, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstCreated).To(BeTrue())
		second, secondCreated, err := builds.EnsureManualBuild(context.Background(), pipeline.TeamID, admission.ID, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondCreated).To(BeFalse())
		Expect(second).To(Equal(first))
		var manuallyTriggered bool
		var jobName string
		Expect(dbConn.QueryRow(`SELECT build.manually_triggered, job.name FROM builds build JOIN jobs job ON job.id=build.job_id WHERE build.id=$1`, first).Scan(&manuallyTriggered, &jobName)).To(Succeed())
		Expect(manuallyTriggered).To(BeTrue())
		Expect(jobName).To(Equal("admit"))
	})

	It("keeps an automatic ready admission eligible when allocation stopped at an empty admitting run", func() {
		version := atc.Version{"ref": "retry-empty-allocation"}
		scenario.Run(builder.WithResourceVersions("repository", version))
		job := scenario.Job("admit")
		var buildID int
		Expect(dbConn.QueryRow(`INSERT INTO builds (name,status,team_id,pipeline_id,job_id) VALUES ($1,'pending',$2,$3,$4) RETURNING id`, fmt.Sprintf("source-select-empty-run-%d", time.Now().UnixNano()), pipeline.TeamID, pipeline.PipelineID, job.ID()).Scan(&buildID)).To(Succeed())
		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		scenario.Run(builder.WithNextInputMapping("admit", dbtest.JobInputs{{Name: "repo-source", Version: version}}))
		_, ready, err := build.AdoptInputsAndPipes()
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		admissions := db.NewWorkflowResourceSourceAdmissionStore(dbConn)
		admission, _, err := admissions.ClaimBuild(context.Background(), pipeline.TeamID, pipeline.PipelineID, int64(buildID), db.BuildClaim{
			WorkflowDefinitionID: definitionID, SourceConfigHash: pipeline.ConfigHash, IdempotencyKey: "empty-admitting-retry",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_resource_source_admissions SET status='ready' WHERE id=$1`, admission.ID)
		Expect(err).NotTo(HaveOccurred())
		builds := db.NewWorkflowResourceSourceBuildStore(dbConn, lockFactory, checkFactory)
		candidates, err := builds.SuccessfulUnclaimedBuilds(context.Background(), pipeline.TeamID, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(HaveLen(1))
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id,team_name,workflow_definition_id,workflow_name,workflow_version,schema_version,signature_version,definition_content_hash,idempotency_key,parameterized_config,parameterized_config_hash,dev_validation_provenance_hash,origin_kind,origin_reference,created_by,status,resource_source_admission_id)
			SELECT $1,$2,definition.id,definition.name,definition.version,definition.schema_version,definition.signature_version,definition.content_hash,'empty-admitting-retry','{}',$3,'','resource-source-build','pipeline:build','workflow-resource-source-reconciler','admitting',$4
			FROM agent_workflow_definitions definition WHERE definition.id=$5`, pipeline.TeamID, scenario.Team.Name(), strings.Repeat("d", 64), admission.ID, definitionID)
		Expect(err).NotTo(HaveOccurred())
		candidates, err = builds.SuccessfulUnclaimedBuilds(context.Background(), pipeline.TeamID, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(HaveLen(1), "empty admitting allocation must remain retryable")
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET status='errored',completed_at=now(),updated_at=now() WHERE resource_source_admission_id=$1`, admission.ID)
		Expect(err).NotTo(HaveOccurred())
		candidates, err = builds.SuccessfulUnclaimedBuilds(context.Background(), pipeline.TeamID, pipeline.PipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(BeEmpty(), "a nonempty terminal allocation closes the automatic retry")
	})
})

func dbTestMonitorPipelinePolicy() pullrequest.MonitorPipelinePolicy {
	return pullrequest.MonitorPipelinePolicy{
		APIBaseURL:        "https://api.github.example",
		RepositoryURL:     "https://github.example/example/monitor-pipeline.git",
		ReadCredential:    "engineering-github-read",
		PollInterval:      5 * time.Minute,
		FreshnessInterval: 6 * time.Hour,
		ResourceType: atc.ResourceType{
			Name: pullrequest.MonitorResourceTypeName,
			Image: "registry.example/forge-pr@sha256:" +
				strings.Repeat("a", 64),
		},
	}
}
