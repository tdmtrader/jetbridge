package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"

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
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
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

	It("replays durable identity after a team rename and input expiry", func() {
		first, created, err := factory.CreateWithInputs(ctx, request("historical-replay"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		originalTeamName := defaultTeam.Name()
		renamedTeam := fmt.Sprintf("renamed-%d", time.Now().UnixNano())
		_, err = dbConn.Exec(`UPDATE teams SET name = $1 WHERE id = $2`, renamedTeam, defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_snapshots SET content_state = 'expired' WHERE id = $1`, int64(input.ID))
		Expect(err).NotTo(HaveOccurred())

		replay := request("historical-replay")
		replay.TeamName = renamedTeam
		second, created, err := factory.CreateWithInputs(ctx, replay)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(second.ID).To(Equal(first.ID))
		Expect(second.TeamName).To(Equal(originalTeamName), "the copied historical team name must not be rewritten")
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
			ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusSucceeded, "",
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

		conflict = request("conflict-key")
		conflict.CreatedBy = "mallory"
		_, _, err = factory.CreateWithInputs(ctx, conflict)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))

		var runs int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_runs WHERE idempotency_key = 'conflict-key'`).Scan(&runs)).To(Succeed())
		Expect(runs).To(Equal(1))
	})

	It("rejects forged copied schema and signature metadata before creating any durable state", func() {
		for _, mutate := range []func(*db.AgentWorkflowRunCreateRequest){
			func(request *db.AgentWorkflowRunCreateRequest) { request.SchemaVersion = 4 },
			func(request *db.AgentWorkflowRunCreateRequest) { request.SignatureVersion = 2 },
		} {
			candidate := request(fmt.Sprintf("forged-metadata-%d", time.Now().UnixNano()))
			mutate(&candidate)
			_, _, err := factory.CreateWithInputs(ctx, candidate)
			Expect(err).To(MatchError(ContainSubstring("copied definition identity")))
		}

		var runs, bindings, claims int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_runs WHERE idempotency_key LIKE 'forged-metadata-%'`).Scan(&runs)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_run_snapshots`).Scan(&bindings)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_retention_claims WHERE class = 'workflow'`).Scan(&claims)).To(Succeed())
		Expect(runs).To(Equal(0))
		Expect(bindings).To(Equal(0))
		Expect(claims).To(Equal(0))
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

	It("serializes input availability checks and workflow claims with digest GC", func() {
		// The suite defaults to one connection to expose accidental pool use;
		// this race intentionally needs independent locker, writer, and observer sessions.
		dbConn.SetMaxOpenConns(8)
		lockManager := db.NewAgentSnapshotDigestLocker(dbConn)
		digestLease, err := lockManager.AcquireMany(ctx, []snapshot.Digest{input.Digest})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(digestLease.Close()).To(Succeed()) })

		result := make(chan error, 1)
		go func() {
			_, _, createErr := factory.CreateWithInputs(ctx, request("input-gc-race"))
			result <- createErr
		}()

		Eventually(func() (int, error) {
			select {
			case createErr := <-result:
				return 0, fmt.Errorf("workflow-run creation completed before the digest barrier: %v", createErr)
			default:
			}
			var waiters int
			err := dbConn.QueryRow(`
				SELECT count(*)
				FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'
			`).Scan(&waiters)
			return waiters, err
		}).WithTimeout(5 * time.Second).Should(BeNumerically(">", 0))

		snapshots := db.NewAgentSnapshotsFactory(dbConn)
		expired, err := snapshots.MarkDigestExpired(ctx, digestLease, input.Digest, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(expired).To(BeTrue())
		Expect(digestLease.Close()).To(Succeed())

		var createErr error
		Eventually(result).Should(Receive(&createErr))
		Expect(createErr).To(MatchError(ContainSubstring("unavailable")))
		var runs, claims int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_workflow_runs WHERE idempotency_key = 'input-gc-race'`).Scan(&runs)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_snapshot_retention_claims WHERE snapshot_id = $1`, int64(input.ID)).Scan(&claims)).To(Succeed())
		Expect(runs).To(Equal(0))
		Expect(claims).To(Equal(0))
	})

	It("enforces the workflow-run transition graph and keeps terminal timestamps immutable", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("transition-graph"))
		Expect(err).NotTo(HaveOccurred())

		transitioned, err := factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusSucceeded, "",
		)
		Expect(err).To(MatchError(ContainSubstring("transition")))
		Expect(transitioned).To(BeFalse())

		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusSucceeded, "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())

		terminal, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(terminal.StartedAt).NotTo(BeNil())
		Expect(terminal.CompletedAt).NotTo(BeNil())
		Expect(*terminal.CompletedAt).To(BeTemporally(">=", *terminal.StartedAt))

		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusRunning, "reopen",
		)
		Expect(err).To(MatchError(ContainSubstring("transition")))
		Expect(transitioned).To(BeFalse())
		after, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(after.Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))
		Expect(after.StartedAt).To(Equal(terminal.StartedAt))
		Expect(after.CompletedAt).To(Equal(terminal.CompletedAt))
	})

	It("rejects execution links whose pipeline run, pipelines, or team do not agree", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("execution-associations"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, otherTemplateID, instanceID, pipelineRunID int
		suffix := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("assoc-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("assoc-other-template-%d", suffix), defaultTeam.ID()).Scan(&otherTemplateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("assoc-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())

		link := db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: otherTemplateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(MatchError(ContainSubstring("association")))

		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("workflow-link-other-%d", suffix)))
		Expect(err).NotTo(HaveOccurred())
		var otherTemplate, otherInstance, otherPipelineRun int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("other-team-template-%d", suffix), otherTeam.ID()).Scan(&otherTemplate)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("other-team-instance-%d", suffix), otherTeam.ID()).Scan(&otherInstance)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, otherTemplate, otherInstance).Scan(&otherPipelineRun)).To(Succeed())
		link = db.AgentWorkflowRunExecutionLink{
			PipelineRunID: otherPipelineRun, TemplatePipelineID: otherTemplate, InstancePipelineID: otherInstance,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(MatchError(ContainSubstring("team")))
	})

	It("rejects plans for builds outside the linked workflow instance", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("planned-build-ownership"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, instanceID, pipelineRunID, wrongBuildID int
		suffix := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("plan-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("plan-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(factory.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("d", 64),
		})).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id) VALUES ($1, 'pending', $2) RETURNING id`, fmt.Sprintf("wrong-plan-%d", suffix), defaultTeam.ID()).Scan(&wrongBuildID)).To(Succeed())

		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: wrongBuildID, ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash: strings.Repeat("e", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(MatchError(ContainSubstring("instance")))
	})

	It("persists planned build identifiers above the signed 32-bit range", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("bigint-planned-build"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, instanceID, pipelineRunID int
		suffix := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("bigint-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("bigint-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(factory.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("d", 64),
		})).To(Succeed())

		largeBuildID := int64(1 << 31)
		_, err = dbConn.Exec(`
			INSERT INTO builds (id, name, status, team_id, pipeline_id)
			VALUES ($1, $2, 'pending', $3, $4)
		`, largeBuildID, fmt.Sprintf("large-build-%d", suffix), defaultTeam.ID(), instanceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: int(largeBuildID), ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash: strings.Repeat("e", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(Succeed())

		stored, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.PlannedBuildID).To(Equal(func() *int { value := int(largeBuildID); return &value }()))
	})

	It("enforces the finite workflow-run status vocabulary in PostgreSQL", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("status-constraint"))
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET status = 'future-typo' WHERE id = $1`, int64(run.ID))
		Expect(err).To(HaveOccurred())
	})

	It("preserves infrastructure errors instead of masking them as semantic conflicts", func() {
		injected := errors.New("injected retry lookup failure")
		fakeTx := new(dbfakes.FakeTx)
		fakeTx.ExecContextStub = func(context.Context, string, ...any) (sql.Result, error) {
			return nil, nil
		}
		fakeTx.QueryRowContextStub = func(_ context.Context, query string, _ ...any) squirrel.RowScanner {
			switch {
			case strings.Contains(query, "idempotency_key"):
				return rowScannerFunc(func(...any) error { return sql.ErrNoRows })
			case strings.Contains(query, "SELECT name FROM teams"):
				return rowScannerFunc(func(destinations ...any) error {
					*destinations[0].(*string) = defaultTeam.Name()
					return nil
				})
			case strings.Contains(query, "FROM agent_workflow_definitions"):
				return rowScannerFunc(func(destinations ...any) error {
					*destinations[0].(*string) = definitionName
					*destinations[1].(*int) = 1
					*destinations[2].(*int) = 3
					*destinations[3].(*int) = 1
					*destinations[4].(*string) = strings.Repeat("a", 64)
					return nil
				})
			case strings.Contains(query, "SELECT team_id FROM agent_workflow_runs"):
				return rowScannerFunc(func(...any) error { return injected })
			default:
				return rowScannerFunc(func(...any) error {
					return fmt.Errorf("unexpected test query: %s", query)
				})
			}
		}
		fakeConn := new(dbfakes.FakeDbConn)
		fakeConn.BeginTxReturns(fakeTx, nil)
		faultFactory := db.NewAgentWorkflowRunsFactory(fakeConn)

		retryID := snapshot.WorkflowRunID(999)
		faultRequest := request("preserve-db-error")
		faultRequest.RetryOfWorkflowRunID = &retryID
		_, _, err := faultFactory.CreateWithInputs(ctx, faultRequest)
		Expect(err).To(MatchError(injected))
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

type rowScannerFunc func(...any) error

func (scan rowScannerFunc) Scan(destinations ...any) error { return scan(destinations...) }
