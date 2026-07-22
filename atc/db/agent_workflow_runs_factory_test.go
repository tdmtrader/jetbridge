package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentWorkflowRunsFactory", func() {
	var (
		ctx            context.Context
		factory        db.AgentWorkflowRunsFactory
		definitionName string
		definitionID   int
		input          snapshot.SnapshotRef
	)

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentWorkflowRunsFactory(dbConn)

		definitionName = fmt.Sprintf("durable-run-%d", time.Now().UnixNano())
		err := dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice')
			RETURNING id
		`, definitionName, strings.Repeat("a", 64)).Scan(&definitionID)
		Expect(err).NotTo(HaveOccurred())

		var snapshotID int64
		digest := "sha256:" + strings.Repeat("b", 64)
		err = dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository', 1, $1, 10, 1, 'application/vnd.jetbridge.snapshot.tar.v1')
			RETURNING id
		`, digest).Scan(&snapshotID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'alice', 'workflow input')
		`, snapshotID, defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		input = snapshot.SnapshotRef{
			ID: snapshot.SnapshotID(snapshotID), Type: "repository/v1", Digest: snapshot.Digest(digest),
		}
	})

	request := func(key string) db.AgentWorkflowRunCreateRequest {
		return db.AgentWorkflowRunCreateRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
			WorkflowDefinitionID: definitionID,
			WorkflowName:         definitionName, WorkflowVersion: 1,
			SchemaVersion: 3, SignatureVersion: 1,
			DefinitionContentHash:   strings.Repeat("a", 64),
			IdempotencyKey:          key,
			ParameterizedConfig:     json.RawMessage(`{"jobs":[{"name":"run"}]}`),
			ParameterizedConfigHash: strings.Repeat("c", 64),
			OriginKind:              "ticket", OriginReference: "JIRA-123",
			CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
			Inputs: map[string]snapshot.SnapshotRef{"source": input},
		}
	}

	It("creates a durable run, input binding, and nonexpiring workflow claim atomically", func() {
		run, created, err := factory.CreateWithInputs(ctx, request("create-one"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(run.ID).To(BeNumerically(">", 0))
		Expect(run.ParameterizedConfig).To(MatchJSON(`{"jobs":[{"name":"run"}]}`))
		Expect(run.Status).To(Equal(db.AgentWorkflowRunStatusAdmitting))

		bindings, err := factory.Snapshots(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput,
			PortName: "source", Snapshot: input,
		}))

		var claims int
		err = dbConn.QueryRow(`
			SELECT count(*)
			FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'workflow'
			  AND expires_at IS NULL
		`, int64(input.ID), defaultTeam.ID()).Scan(&claims)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(Equal(1))
	})

	It("returns the exact existing run for an identical idempotency key", func() {
		first, created, err := factory.CreateWithInputs(ctx, request("same-key"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		second, created, err := factory.CreateWithInputs(ctx, request("same-key"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(second.ID).To(Equal(first.ID))
	})

	It("supports team-scoped lookup, filtered listing, and bounded reconciliation", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("queryable"))
		Expect(err).NotTo(HaveOccurred())

		foundRun, found, err := factory.FindByIdempotencyKey(ctx, defaultTeam.ID(), "queryable")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(foundRun.ID).To(Equal(run.ID))

		runs, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
			TeamID: defaultTeam.ID(), WorkflowName: definitionName,
			Status:     db.AgentWorkflowRunStatusAdmitting,
			OriginKind: "ticket", OriginReference: "JIRA-123", Limit: 10,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(ConsistOf(HaveField("ID", run.ID)))

		reconcilable, err := factory.ListForReconciliation(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilable).To(ContainElement(HaveField("ID", run.ID)))
		transitioned, err := factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusSucceeded, "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		reconcilable, err = factory.ListForReconciliation(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilable).NotTo(ContainElement(HaveField("ID", run.ID)))
	})

	It("rejects idempotency reuse when immutable target, origin, or inputs differ", func() {
		_, _, err := factory.CreateWithInputs(ctx, request("conflict-key"))
		Expect(err).NotTo(HaveOccurred())

		conflict := request("conflict-key")
		conflict.OriginReference = "JIRA-999"
		_, _, err = factory.CreateWithInputs(ctx, conflict)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))

		conflict = request("conflict-key")
		conflict.SchemaVersion++
		_, _, err = factory.CreateWithInputs(ctx, conflict)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))

		conflict = request("conflict-key")
		conflict.SignatureVersion++
		_, _, err = factory.CreateWithInputs(ctx, conflict)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))

		var runs int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_runs WHERE idempotency_key = 'conflict-key'`).Scan(&runs)).To(Succeed())
		Expect(runs).To(Equal(1))
	})

	It("converges concurrent creation to one durable identity", func() {
		results := make(chan snapshot.WorkflowRunID, 2)
		errors := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				run, _, err := factory.CreateWithInputs(ctx, request("concurrent"))
				if err != nil {
					errors <- err
					return
				}
				results <- run.ID
			}()
		}
		wg.Wait()
		close(errors)
		close(results)
		Expect(errors).To(BeEmpty())
		ids := []snapshot.WorkflowRunID{}
		for id := range results {
			ids = append(ids, id)
		}
		Expect(ids).To(HaveLen(2))
		Expect(ids[0]).To(Equal(ids[1]))
	})

	It("records immutable execution and plan provenance and performs guarded transitions", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("provenance"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, instanceID, pipelineRunID, buildID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ('workflow-template', $1, 1) RETURNING id`, defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ('workflow-instance', $1, 1) RETURNING id`, defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ('1', 'pending', $1, $2) RETURNING id`, defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())

		link := db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID,
			InstancePipelineID: instanceID,
			ConcreteConfig:     json.RawMessage(`{"instance":true}`),
			ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(Succeed())
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(Succeed())
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID:              buildID,
			ActualPlan:           json.RawMessage(`{"task":"review"}`),
			ActualPlanHash:       strings.Repeat("e", 64),
			ResolvedDependencies: json.RawMessage(`{"images":{"review":"sha256:abc"}}`),
		})).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id)
			VALUES ($1, 'output', 'result', $2)
		`, int64(run.ID), int64(input.ID))
		Expect(err).NotTo(HaveOccurred())

		transitioned, err := factory.Transition(ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		transitioned, err = factory.Transition(ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusErrored, "late")
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeFalse())

		stored, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.PipelineRunID).To(Equal(&pipelineRunID))
		Expect(stored.PlannedBuildID).To(Equal(&buildID))
		Expect(stored.ActualPlan).To(MatchJSON(`{"task":"review"}`))
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusRunning))

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM pipelines WHERE id IN ($1, $2)`, instanceID, templateID)
		Expect(err).NotTo(HaveOccurred())
		stored, found, err = factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.PipelineRunID).To(BeNil())
		Expect(stored.TemplatePipelineID).To(BeNil())
		Expect(stored.InstancePipelineID).To(BeNil())
		Expect(stored.PlannedBuildID).To(BeNil())
		Expect(stored.ActualPlan).To(MatchJSON(`{"task":"review"}`))
		bindings, err := factory.Snapshots(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput,
				PortName: "source", Snapshot: input,
			},
			db.AgentWorkflowRunSnapshotBinding{
				WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotOutput,
				PortName: "result", Snapshot: input,
			},
		))
	})

	It("preserves snapshot production history when its workflow-run link is deleted", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("deletion-survival"))
		Expect(err).NotTo(HaveOccurred())

		var productionID int64
		err = dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, build_id, team_id, team_name, created_by, plan_id,
				 attempt, step_kind, step_name, output_port, workflow_definition_id,
				 workflow_run_id, source_metadata)
			VALUES ($1, 922337, $2, $3, 'alice', 'plan-1', '1', 'task',
			        'review', 'result', $4, $5, '{"adapter":"test"}')
			RETURNING id
		`, int64(input.ID), defaultTeam.ID(), defaultTeam.Name(), definitionID, int64(run.ID)).Scan(&productionID)
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())

		var linkedRunID *int64
		var source []byte
		err = dbConn.QueryRow(`
			SELECT workflow_run_id, source_metadata
			FROM agent_snapshot_productions
			WHERE id = $1
		`, productionID).Scan(&linkedRunID, &source)
		Expect(err).NotTo(HaveOccurred())
		Expect(linkedRunID).To(BeNil())
		Expect(source).To(MatchJSON(`{"adapter":"test"}`))
	})
})
