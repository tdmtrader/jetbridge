package db_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/encryption"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type agentWorkflowRunEncryptedConn struct {
	db.DbConn
	strategy encryption.Strategy
}

func (conn agentWorkflowRunEncryptedConn) EncryptionStrategy() encryption.Strategy {
	return conn.strategy
}

func (conn agentWorkflowRunEncryptedConn) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.Tx, error) {
	tx, err := conn.DbConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return agentWorkflowRunEncryptedTx{Tx: tx, strategy: conn.strategy}, nil
}

type agentWorkflowRunEncryptedTx struct {
	db.Tx
	strategy encryption.Strategy
}

func (tx agentWorkflowRunEncryptedTx) EncryptionStrategy() encryption.Strategy {
	return tx.strategy
}

func agentWorkflowRunEncryptionKey() encryption.Strategy {
	block, err := aes.NewCipher([]byte("AES256Key-32Characters1234567890"))
	Expect(err).NotTo(HaveOccurred())
	aesgcm, err := cipher.NewGCM(block)
	Expect(err).NotTo(HaveOccurred())
	return encryption.NewKey(aesgcm)
}

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
		definitionSource := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: test
`, definitionName)
		err := dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, $3, 'alice', 3, 1)
			RETURNING id
		`, definitionName, strings.Repeat("a", 64), definitionSource).Scan(&definitionID)
		Expect(err).NotTo(HaveOccurred())

		var snapshotID int64
		digest := "sha256:" + strings.Repeat("b", 64)
		err = dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'repository', 1, $2, 10, 1, 'application/vnd.jetbridge.snapshot.tar.v1')
			RETURNING id
		`, defaultTeam.ID(), digest).Scan(&snapshotID)
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

	terminalize := func(run db.AgentWorkflowRun) {
		_, err := dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET status = 'failed', completed_at = now(), updated_at = now()
			WHERE id = $1
		`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())
	}

	retryRequest := func(key string, source db.AgentWorkflowRun) db.AgentWorkflowRunCreateRequest {
		candidate := request(key)
		candidate.RetryOfWorkflowRunID = &source.ID
		candidate.OriginKind = "retry"
		candidate.OriginReference = source.ID.String()
		return candidate
	}

	createExecution := func(run db.AgentWorkflowRun, suffix string, buildStatus db.BuildStatus) (db.AgentWorkflowRunExecutionLink, int64) {
		var templateID, instanceID, pipelineRunID int
		var buildID int64
		unique := fmt.Sprintf("%s-%d", suffix, time.Now().UnixNano())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, unique+"-template", defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, unique+"-instance", defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ($1, $2, $3, $4) RETURNING id`, unique+"-build", buildStatus, defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())
		link := db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"jobs":[{"name":"run"}]}`), ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(Succeed())
		_, err := dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		return link, buildID
	}

	It("creates a durable run, input binding, and exact active-run claim atomically", func() {
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
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'run'
			  AND workflow_run_id = $3 AND expires_at IS NULL
		`, int64(input.ID), defaultTeam.ID(), int64(run.ID)).Scan(&claims)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(Equal(1))
	})

	It("accepts only admitting as the initial durable status", func() {
		for _, status := range []db.AgentWorkflowRunStatus{
			db.AgentWorkflowRunStatusRunning,
			db.AgentWorkflowRunStatusSucceeded,
			db.AgentWorkflowRunStatusFailed,
			db.AgentWorkflowRunStatusErrored,
			db.AgentWorkflowRunStatusCanceling,
			db.AgentWorkflowRunStatusAborted,
		} {
			candidate := request(fmt.Sprintf("invalid-initial-status-%s", status))
			candidate.Status = status

			_, _, err := factory.CreateWithInputs(ctx, candidate)
			Expect(err).To(MatchError(ContainSubstring("initial status must be admitting")))
		}
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

	It("scopes workflow idempotency and retry targets away from node runs", func() {
		nodeName := definitionName + "-node"
		nodeHash := strings.Repeat("d", 64)
		var nodeDefinitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('node', $1, 1, $2, 'schema_version: 1', 'alice', 3, 1)
			RETURNING id
		`, nodeName, nodeHash).Scan(&nodeDefinitionID)).To(Succeed())

		var nodeRunID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id,
				 workflow_name, workflow_version, schema_version, signature_version,
				 definition_content_hash, idempotency_key, parameterized_config,
				 parameterized_config_hash, origin_kind, origin_reference, created_by, status,
				 completed_at)
			VALUES
				('node', $1, $2, $3, $4, 1, 3, 1, $5, 'kind-shared', '{}',
				 $6, 'direct-node-test', '', 'alice', 'failed', now())
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), nodeDefinitionID, nodeName, nodeHash,
			strings.Repeat("e", 64)).Scan(&nodeRunID)).To(Succeed())

		workflowRun, created, err := factory.CreateWithInputs(ctx, request("kind-shared"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(int64(workflowRun.ID)).NotTo(Equal(nodeRunID))
		foundRun, found, err := factory.FindByIdempotencyKey(ctx, defaultTeam.ID(), "kind-shared")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(foundRun.ID).To(Equal(workflowRun.ID))
		_, found, err = factory.Get(ctx, defaultTeam.ID(), snapshot.WorkflowRunID(nodeRunID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		listed, err := factory.List(ctx, db.AgentWorkflowRunListFilter{
			TeamID: defaultTeam.ID(),
			Limit:  100,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).NotTo(ContainElement(HaveField("ID", snapshot.WorkflowRunID(nodeRunID))))
		counts, err := factory.CountByStatus(ctx, db.AgentWorkflowRunCountFilter{
			TeamID:       defaultTeam.ID(),
			WorkflowName: nodeName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(counts).To(BeEmpty())

		nodeRetryID := snapshot.WorkflowRunID(nodeRunID)
		retry := request("cross-kind-retry")
		retry.RetryOfWorkflowRunID = &nodeRetryID
		retry.OriginKind = "retry"
		retry.OriginReference = nodeRetryID.String()
		_, _, err = factory.CreateWithInputs(ctx, retry)
		Expect(err).To(MatchError(ContainSubstring("retry target is absent")))

		nodeTarget := request("node-definition-target")
		nodeTarget.WorkflowDefinitionID = nodeDefinitionID
		nodeTarget.WorkflowName = nodeName
		nodeTarget.WorkflowVersion = 1
		nodeTarget.SchemaVersion = 3
		nodeTarget.SignatureVersion = 1
		nodeTarget.DefinitionContentHash = nodeHash
		_, _, err = factory.CreateWithInputs(ctx, nodeTarget)
		Expect(err).To(MatchError(ContainSubstring(
			fmt.Sprintf("workflow-run definition %d does not exist", nodeDefinitionID),
		)))
	})

	It("creates and reads exact node runs in a separate kind-scoped identity", func() {
		nodeName := definitionName + "-direct-node"
		nodeHash := strings.Repeat("d", 64)
		var nodeDefinitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('node', $1, 1, $2, 'schema_version: 1', 'alice', 3, 1)
			RETURNING id
		`, nodeName, nodeHash).Scan(&nodeDefinitionID)).To(Succeed())

		nodeRequest := request("kind-shared-direct")
		nodeRequest.DefinitionKind = workflow.DefinitionKindNode
		nodeRequest.WorkflowDefinitionID = nodeDefinitionID
		nodeRequest.WorkflowName = nodeName
		nodeRequest.DefinitionContentHash = nodeHash
		nodeRequest.Inputs = map[string]snapshot.SnapshotRef{}
		node, created, err := factory.CreateWithInputs(ctx, nodeRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(node.DefinitionKind).To(Equal(workflow.DefinitionKindNode))

		workflowRequest := request("kind-shared-direct")
		workflowRun, created, err := factory.CreateWithInputs(ctx, workflowRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(workflowRun.ID).NotTo(Equal(node.ID))

		_, found, err := factory.Get(ctx, defaultTeam.ID(), node.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		stored, found, err := factory.GetKind(ctx, defaultTeam.ID(), workflow.DefinitionKindNode, node.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.WorkflowDefinitionID).To(Equal(nodeDefinitionID))

		resourceAdmissionID := int64(1)
		withSourceAdmission := nodeRequest
		withSourceAdmission.IdempotencyKey = "node-cannot-use-sources"
		withSourceAdmission.ResourceSourceAdmissionID = &resourceAdmissionID
		_, _, err = factory.CreateWithInputs(ctx, withSourceAdmission)
		Expect(err).To(MatchError(ContainSubstring("reusable node runs cannot use a resource source admission")))

		version := 1
		listed, err := factory.ListKind(ctx, workflow.DefinitionKindNode, db.AgentWorkflowRunListFilter{
			TeamID: defaultTeam.ID(), WorkflowName: nodeName, WorkflowVersion: &version, Limit: 10,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(ConsistOf(node))
	})

	It("persists validation provenance and treats it as idempotency identity", func() {
		firstRequest := request("validation-provenance")
		firstRequest.DevValidationProvenanceHash = strings.Repeat("d", 64)
		first, created, err := factory.CreateWithInputs(ctx, firstRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(first.DevValidationProvenanceHash).To(Equal(strings.Repeat("d", 64)))

		replay, created, err := factory.CreateWithInputs(ctx, firstRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replay.DevValidationProvenanceHash).To(Equal(strings.Repeat("d", 64)))

		conflicting := firstRequest
		conflicting.DevValidationProvenanceHash = strings.Repeat("e", 64)
		_, _, err = factory.CreateWithInputs(ctx, conflicting)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))
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

	It("supports team-scoped lookup, filtered listing, and fair bounded reconciliation claims", func() {
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

		now := time.Now().UTC().Truncate(time.Microsecond)
		originalUpdatedAt := now.Add(-time.Hour)
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET reconcile_after = $2, updated_at = $3
			WHERE id = $1
		`, int64(run.ID), now.Add(-time.Minute), originalUpdatedAt)
		Expect(err).NotTo(HaveOccurred())

		reconcilable, err := factory.ClaimForReconciliation(ctx, now, 30*time.Second, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilable).To(ContainElement(run.ID))
		var claimedReconcileAfter, claimedUpdatedAt time.Time
		Expect(dbConn.QueryRow(`
			SELECT reconcile_after, updated_at
			FROM agent_workflow_runs
			WHERE id = $1
		`, int64(run.ID)).Scan(&claimedReconcileAfter, &claimedUpdatedAt)).To(Succeed())
		Expect(claimedReconcileAfter).To(BeTemporally("==", now.Add(30*time.Second)))
		Expect(claimedUpdatedAt).To(BeTemporally("==", originalUpdatedAt), "claiming must not rewrite admission age")

		reconcilable, err = factory.ClaimForReconciliation(ctx, now, 30*time.Second, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilable).NotTo(ContainElement(run.ID), "a claimed row must rotate behind its new due time")
		_, err = factory.ClaimForReconciliation(ctx, time.Time{}, 30*time.Second, 10)
		Expect(err).To(HaveOccurred())
		_, err = factory.ClaimForReconciliation(ctx, now, 0, 10)
		Expect(err).To(HaveOccurred())
		_, err = factory.ClaimForReconciliation(ctx, now, time.Second, 1001)
		Expect(err).To(HaveOccurred())

		transitioned, err := factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusSucceeded, "bypass evidence",
		)
		Expect(err).To(MatchError(ContainSubstring("Finalize")))
		Expect(transitioned).To(BeFalse())
		_, finalized, err := factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			TerminalStatus: db.AgentWorkflowRunStatusErrored, ErrorMessage: "test terminalization",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		reconcilable, err = factory.ClaimForReconciliation(ctx, now.Add(time.Hour), 30*time.Second, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilable).NotTo(ContainElement(run.ID))
	})

	It("keyset-pages equal-timestamp history without gaps or duplicates", func() {
		createdAt := time.Date(2026, time.July, 22, 12, 34, 56, 123456000, time.UTC)
		var want []snapshot.WorkflowRunID
		for index := 0; index < 5; index++ {
			run, created, err := factory.CreateWithInputs(ctx, request(fmt.Sprintf("page-%d", index)))
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			want = append(want, run.ID)
			_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET created_at = $2 WHERE id = $1`, int64(run.ID), createdAt)
			Expect(err).NotTo(HaveOccurred())
		}
		sort.Slice(want, func(i, j int) bool { return want[i] > want[j] })

		filter := db.AgentWorkflowRunListFilter{
			TeamID: defaultTeam.ID(), WorkflowName: definitionName,
			Status:     db.AgentWorkflowRunStatusAdmitting,
			OriginKind: "ticket", OriginReference: "JIRA-123", Limit: 2,
		}
		var got []snapshot.WorkflowRunID
		for {
			page, err := factory.List(ctx, filter)
			Expect(err).NotTo(HaveOccurred())
			if len(page) == 0 {
				break
			}
			for _, run := range page {
				Expect(run.CreatedAt).To(BeTemporally("==", createdAt))
				got = append(got, run.ID)
			}
			last := page[len(page)-1]
			filter.Before = &pagination.Cursor{CreatedAt: last.CreatedAt, ID: int64(last.ID)}
		}
		Expect(got).To(Equal(want))
	})

	It("counts every operational run by status without a list limit or cross-team leakage", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("exact-status-count-base"))
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version,
				 definition_content_hash, function_id, idempotency_key,
				 parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			SELECT team_id, team_name, workflow_definition_id, workflow_name,
			       workflow_version, schema_version, signature_version,
			       definition_content_hash, function_id,
			       idempotency_key || '-bulk-' || series::text,
			       parameterized_config, parameterized_config_hash,
			       origin_kind, origin_reference, created_by, 'running'
			FROM agent_workflow_runs
			CROSS JOIN generate_series(1, 1005) AS series
			WHERE id = $1
		`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())

		otherTeam, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("workflow-count-other-%d", time.Now().UnixNano())))
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version,
				 definition_content_hash, function_id, idempotency_key,
				 parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			SELECT team_id, team_name, workflow_definition_id, workflow_name,
			       workflow_version, schema_version, signature_version,
			       definition_content_hash, function_id,
			       idempotency_key || '-experiment', parameterized_config,
			       parameterized_config_hash, 'experiment', origin_reference,
			       created_by, 'failed'
			FROM agent_workflow_runs WHERE id = $1
		`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version,
				 definition_content_hash, function_id, idempotency_key,
				 parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			SELECT $2, $3, workflow_definition_id, workflow_name,
			       workflow_version, schema_version, signature_version,
			       definition_content_hash, function_id,
			       idempotency_key || '-other-team', parameterized_config,
			       parameterized_config_hash, origin_kind, origin_reference,
			       created_by, 'errored'
			FROM agent_workflow_runs WHERE id = $1
		`, int64(run.ID), otherTeam.ID(), otherTeam.Name())
		Expect(err).NotTo(HaveOccurred())

		counts, err := factory.CountByStatus(ctx, db.AgentWorkflowRunCountFilter{
			TeamID: defaultTeam.ID(), WorkflowName: definitionName,
			ExcludeOriginKind: "experiment",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(counts).To(Equal(map[db.AgentWorkflowRunStatus]int64{
			db.AgentWorkflowRunStatusAdmitting: 1,
			db.AgentWorkflowRunStatusRunning:   1005,
		}))
	})

	It("skips a locked due row instead of letting one run block the reconciliation batch", func() {
		dbConn.SetMaxOpenConns(4)
		first, _, err := factory.CreateWithInputs(ctx, request("skip-locked-first"))
		Expect(err).NotTo(HaveOccurred())
		second, _, err := factory.CreateWithInputs(ctx, request("skip-locked-second"))
		Expect(err).NotTo(HaveOccurred())
		now := time.Now().UTC().Truncate(time.Microsecond)
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET reconcile_after = CASE id WHEN $1 THEN $3::timestamptz ELSE $4::timestamptz END
			WHERE id IN ($1, $2)
		`, int64(first.ID), int64(second.ID), now.Add(-2*time.Minute), now.Add(-time.Minute))
		Expect(err).NotTo(HaveOccurred())

		blocker, err := dbConn.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(blocker)
		var locked int64
		Expect(blocker.QueryRowContext(ctx, `
			SELECT id FROM agent_workflow_runs WHERE id = $1 FOR UPDATE
		`, int64(first.ID)).Scan(&locked)).To(Succeed())

		claimed, err := factory.ClaimForReconciliation(ctx, now, time.Minute, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(ConsistOf(second.ID))
	})

	It("claims bounded identifiers without decrypting large plans and isolates an unknown-key row", func() {
		correctKey := agentWorkflowRunEncryptionKey()
		correctFactory := db.NewAgentWorkflowRunsFactory(agentWorkflowRunEncryptedConn{
			DbConn: dbConn, strategy: correctKey,
		})
		factory = correctFactory

		poison, _, err := correctFactory.CreateWithInputs(ctx, request("claim-poison-plan"))
		Expect(err).NotTo(HaveOccurred())
		_, poisonBuildID := createExecution(poison, "claim-poison-plan", db.BuildStatusStarted)
		largePlan := json.RawMessage(`{"payload":"` + strings.Repeat("x", 2<<20) + `"}`)
		Expect(correctFactory.RecordPlan(ctx, poison.ID, db.AgentWorkflowRunPlan{
			BuildID:              poisonBuildID,
			ActualPlan:           largePlan,
			ActualPlanHash:       strings.Repeat("e", 64),
			ResolvedDependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`),
		})).To(Succeed())

		healthy, _, err := correctFactory.CreateWithInputs(ctx, request("claim-healthy-plan"))
		Expect(err).NotTo(HaveOccurred())
		createExecution(healthy, "claim-healthy-plan", db.BuildStatusStarted)

		block, err := aes.NewCipher([]byte("DifferentAES256KeyMaterial123456"))
		Expect(err).NotTo(HaveOccurred())
		wrongGCM, err := cipher.NewGCM(block)
		Expect(err).NotTo(HaveOccurred())
		wrongKeyFactory := db.NewAgentWorkflowRunsFactory(agentWorkflowRunEncryptedConn{
			DbConn: dbConn, strategy: encryption.NewKey(wrongGCM),
		})

		now := time.Now().UTC().Truncate(time.Microsecond)
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET reconcile_after = $3
			WHERE id IN ($1, $2)
		`, int64(poison.ID), int64(healthy.ID), now.Add(-time.Minute))
		Expect(err).NotTo(HaveOccurred())

		claimed, err := wrongKeyFactory.ClaimForReconciliation(ctx, now, time.Minute, 10)
		Expect(err).NotTo(HaveOccurred(), "claiming identifiers must not load or decrypt plan payloads")
		Expect(claimed).To(ConsistOf(poison.ID, healthy.ID), "claim memory must be independent of plan size")

		_, found, err := wrongKeyFactory.InspectForReconciliation(ctx, poison.ID)
		Expect(err).To(HaveOccurred())
		Expect(found).To(BeFalse())
		_, found, err = wrongKeyFactory.InspectForReconciliation(ctx, healthy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET reconcile_after = $3
			WHERE id IN ($1, $2)
		`, int64(poison.ID), int64(healthy.ID), now.Add(-time.Minute))
		Expect(err).NotTo(HaveOccurred())
		reconciler, err := workflowrun.NewReconciler(wrongKeyFactory, lager.NewLogger("claim-isolation"), 15*time.Minute, time.Minute,
			workflowrun.WithReconcilerClock(func() time.Time { return now }),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(ctx)).To(Succeed(), "a poison detail row must not starve later claimed identifiers")

		storedHealthy, found, err := correctFactory.Get(ctx, defaultTeam.ID(), healthy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(storedHealthy.Status).To(Equal(db.AgentWorkflowRunStatusRunning))
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

	It("includes retry provenance in idempotency identity", func() {
		source, _, err := factory.CreateWithInputs(ctx, request("retry-source"))
		Expect(err).NotTo(HaveOccurred())
		terminalize(source)
		firstRequest := retryRequest("retry-key", source)
		first, created, err := factory.CreateWithInputs(ctx, firstRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(first.RetryOfWorkflowRunID).To(Equal(&source.ID))

		withoutRetry := request("retry-key")
		_, _, err = factory.CreateWithInputs(ctx, withoutRetry)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))

		otherSource, _, err := factory.CreateWithInputs(ctx, request("retry-other-source"))
		Expect(err).NotTo(HaveOccurred())
		terminalize(otherSource)
		differentRetry := retryRequest("retry-key", otherSource)
		_, _, err = factory.CreateWithInputs(ctx, differentRetry)
		Expect(err).To(MatchError(ContainSubstring("idempotency")))
	})

	It("requires retries to preserve the source validation provenance identity", func() {
		nonemptySourceRequest := request("retry-provenance-source")
		nonemptySourceRequest.DevValidationProvenanceHash = strings.Repeat("a", 64)
		nonemptySource, _, err := factory.CreateWithInputs(ctx, nonemptySourceRequest)
		Expect(err).NotTo(HaveOccurred())
		terminalize(nonemptySource)

		sameHash := retryRequest("retry-provenance-same", nonemptySource)
		sameHash.DevValidationProvenanceHash = nonemptySourceRequest.DevValidationProvenanceHash
		_, created, err := factory.CreateWithInputs(ctx, sameHash)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		differentHash := retryRequest("retry-provenance-different", nonemptySource)
		differentHash.DevValidationProvenanceHash = strings.Repeat("b", 64)
		_, _, err = factory.CreateWithInputs(ctx, differentHash)
		Expect(err).To(MatchError(ContainSubstring("retry target is incompatible")))

		nonemptyToLegacy := retryRequest("retry-provenance-to-legacy", nonemptySource)
		_, _, err = factory.CreateWithInputs(ctx, nonemptyToLegacy)
		Expect(err).To(MatchError(ContainSubstring("retry target is incompatible")))

		legacySource, _, err := factory.CreateWithInputs(ctx, request("retry-provenance-legacy-source"))
		Expect(err).NotTo(HaveOccurred())
		terminalize(legacySource)
		legacyRetry := retryRequest("retry-provenance-legacy", legacySource)
		_, created, err = factory.CreateWithInputs(ctx, legacyRetry)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		legacyToNonempty := retryRequest("retry-provenance-from-legacy", legacySource)
		legacyToNonempty.DevValidationProvenanceHash = strings.Repeat("c", 64)
		_, _, err = factory.CreateWithInputs(ctx, legacyToNonempty)
		Expect(err).To(MatchError(ContainSubstring("retry target is incompatible")))
	})

	It("requires retries to reference an exact terminal run and its input bindings", func() {
		active, _, err := factory.CreateWithInputs(ctx, request("retry-active-source"))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factory.CreateWithInputs(ctx, retryRequest("retry-active", active))
		Expect(err).To(MatchError(ContainSubstring("retry target is not terminal")))

		terminalize(active)
		wrongInputs := retryRequest("retry-wrong-inputs", active)
		wrongInputs.Inputs = map[string]snapshot.SnapshotRef{}
		_, _, err = factory.CreateWithInputs(ctx, wrongInputs)
		Expect(err).To(MatchError(ContainSubstring("retry input bindings")))

		wrongOrigin := retryRequest("retry-wrong-origin", active)
		wrongOrigin.OriginKind = "manual"
		wrongOrigin.OriginReference = ""
		_, _, err = factory.CreateWithInputs(ctx, wrongOrigin)
		Expect(err).To(MatchError(ContainSubstring("retry origin")))
	})

	It("rejects retry substitution across workflow, function, or signature identity", func() {
		source, _, err := factory.CreateWithInputs(ctx, request("retry-compatible-source"))
		Expect(err).NotTo(HaveOccurred())
		terminalize(source)

		insertDefinition := func(name string, version, signatureVersion int, hash string) int {
			var id int
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, created_by, schema_version, signature_version)
				VALUES ($1, $2, $3, 'schema_version: 3', 'alice', 3, $4)
				RETURNING id
			`, name, version, hash, signatureVersion).Scan(&id)).To(Succeed())
			return id
		}

		otherWorkflow := request("retry-other-workflow")
		otherWorkflow.WorkflowName = definitionName + "-other"
		otherWorkflow.DefinitionContentHash = strings.Repeat("d", 64)
		otherWorkflow.WorkflowDefinitionID = insertDefinition(
			otherWorkflow.WorkflowName, 1, 1, otherWorkflow.DefinitionContentHash,
		)
		otherWorkflow.RetryOfWorkflowRunID = &source.ID
		otherWorkflow.OriginKind = "retry"
		otherWorkflow.OriginReference = source.ID.String()

		otherSignature := request("retry-other-signature")
		otherSignature.WorkflowVersion = 2
		otherSignature.SignatureVersion = 2
		otherSignature.DefinitionContentHash = strings.Repeat("e", 64)
		otherSignature.WorkflowDefinitionID = insertDefinition(
			definitionName, 2, 2, otherSignature.DefinitionContentHash,
		)
		otherSignature.RetryOfWorkflowRunID = &source.ID
		otherSignature.OriginKind = "retry"
		otherSignature.OriginReference = source.ID.String()

		functionID := "review-node"
		otherFunction := request("retry-other-function")
		otherFunction.FunctionID = &functionID
		otherFunction.RetryOfWorkflowRunID = &source.ID
		otherFunction.OriginKind = "retry"
		otherFunction.OriginReference = source.ID.String()

		otherVersion := request("retry-other-compatible-version")
		otherVersion.WorkflowVersion = 3
		otherVersion.DefinitionContentHash = strings.Repeat("f", 64)
		otherVersion.WorkflowDefinitionID = insertDefinition(
			definitionName, 3, 1, otherVersion.DefinitionContentHash,
		)
		otherVersion.RetryOfWorkflowRunID = &source.ID
		otherVersion.OriginKind = "retry"
		otherVersion.OriginReference = source.ID.String()

		for _, candidate := range []db.AgentWorkflowRunCreateRequest{otherWorkflow, otherSignature, otherFunction, otherVersion} {
			_, _, err := factory.CreateWithInputs(ctx, candidate)
			Expect(err).To(MatchError(ContainSubstring("retry target is incompatible")))
		}

		var substituted int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_workflow_runs
			WHERE idempotency_key LIKE 'retry-other-%'
		`).Scan(&substituted)).To(Succeed())
		Expect(substituted).To(Equal(0))
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

	It("serializes input availability checks and active-run claims with digest GC", func() {
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
		expiresAt := time.Now().Add(7 * 24 * time.Hour)
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, expires_at, actor, reason)
			VALUES ($1, $2, 'binding', $3, 'transition-graph-binding', 'post-production grace')
		`, int64(input.ID), defaultTeam.ID(), expiresAt)
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
		Expect(err).To(MatchError(ContainSubstring("Finalize")))
		Expect(transitioned).To(BeFalse())
		_, finalized, err := factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			TerminalStatus: db.AgentWorkflowRunStatusErrored, ErrorMessage: "test terminalization",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())

		terminal, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(terminal.StartedAt).NotTo(BeNil())
		Expect(terminal.CompletedAt).NotTo(BeNil())
		Expect(*terminal.CompletedAt).To(BeTemporally(">=", *terminal.StartedAt))
		var runClaims, bindingClaims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE class = 'run' AND workflow_run_id = $1
		`, int64(run.ID)).Scan(&runClaims)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'binding' AND actor = 'transition-graph-binding'
		`, int64(input.ID)).Scan(&bindingClaims)).To(Succeed())
		Expect(runClaims).To(Equal(0), "terminalization must atomically release active-run retention")
		Expect(bindingClaims).To(Equal(1), "ordinary post-production retention remains independent")
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, workflow_run_id, actor, reason)
			VALUES ($1, $2, 'run', $3, 'stale-terminal-claim', 'repair test')
		`, int64(input.ID), defaultTeam.ID(), int64(run.ID))
		Expect(err).NotTo(HaveOccurred())
		_, finalized, err = factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			TerminalStatus: db.AgentWorkflowRunStatusErrored, ErrorMessage: "test terminalization",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeFalse())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE class = 'run' AND workflow_run_id = $1
		`, int64(run.ID)).Scan(&runClaims)).To(Succeed())
		Expect(runClaims).To(Equal(0), "idempotent finalization repairs stale active-run retention")

		transitioned, err = factory.Transition(
			ctx, run.ID, db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusRunning, "reopen",
		)
		Expect(err).To(MatchError(ContainSubstring("transition")))
		Expect(transitioned).To(BeFalse())
		after, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(after.Status).To(Equal(db.AgentWorkflowRunStatusErrored))
		Expect(after.StartedAt).To(Equal(terminal.StartedAt))
		Expect(after.CompletedAt).To(Equal(terminal.CompletedAt))
	})

	It("reserves direct terminal transitions for an empty failed admission", func() {
		empty, _, err := factory.CreateWithInputs(ctx, request("empty-admission-failure"))
		Expect(err).NotTo(HaveOccurred())
		transitioned, err := factory.Transition(
			ctx, empty.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusErrored, "allocation failed",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		var runClaims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE class = 'run' AND workflow_run_id = $1
		`, int64(empty.ID)).Scan(&runClaims)).To(Succeed())
		Expect(runClaims).To(Equal(0))

		allocated, _, err := factory.CreateWithInputs(ctx, request("allocated-admission-failure"))
		Expect(err).NotTo(HaveOccurred())
		createExecution(allocated, "allocated-admission-failure", db.BuildStatusPending)
		transitioned, err = factory.Transition(
			ctx, allocated.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusErrored, "bypass selected build",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeFalse())
		stored, found, err := factory.Get(ctx, defaultTeam.ID(), allocated.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusAdmitting))
	})

	DescribeTable("records truthful copied outcomes when cancellation loses the race", func(
		buildStatus db.BuildStatus,
		executionStatus db.AgentWorkflowRunExecutionStatus,
		terminal db.AgentWorkflowRunStatus,
	) {
		run, _, err := factory.CreateWithInputs(ctx, request("canceling-"+string(terminal)))
		Expect(err).NotTo(HaveOccurred())
		_, buildID := createExecution(run, "canceling-"+string(terminal), db.BuildStatusStarted)
		planHash := strings.Repeat("9", 64)
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: buildID, ActualPlan: json.RawMessage(`{"task":"review"}`), ActualPlanHash: planHash,
			ResolvedDependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`),
		})).To(Succeed())
		transitioned, err := factory.AdvanceAdmission(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		transitioned, err = factory.Transition(ctx, run.ID, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusCanceling, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		_, err = dbConn.Exec(`UPDATE builds SET status = $2 WHERE id = $1`, buildID, buildStatus)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CaptureExecutionStatus(ctx, buildID, buildStatus)
		Expect(err).NotTo(HaveOccurred())
		result, finalized, err := factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusCanceling,
			ExpectedExecutionStatus: &executionStatus, ExpectedActualPlanHash: &planHash,
			TerminalStatus: terminal,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		Expect(result.Status).To(Equal(terminal))
	},
		Entry("succeeded", db.BuildStatusSucceeded, db.AgentWorkflowRunExecutionStatusSucceeded, db.AgentWorkflowRunStatusSucceeded),
		Entry("failed", db.BuildStatusFailed, db.AgentWorkflowRunExecutionStatusFailed, db.AgentWorkflowRunStatusFailed),
		Entry("errored", db.BuildStatusErrored, db.AgentWorkflowRunExecutionStatusErrored, db.AgentWorkflowRunStatusErrored),
		Entry("aborted", db.BuildStatusAborted, db.AgentWorkflowRunExecutionStatusAborted, db.AgentWorkflowRunStatusAborted),
	)

	It("inspects the selected build and advances only a complete server-owned admission chain", func() {
		incomplete, _, err := factory.CreateWithInputs(ctx, request("advance-incomplete"))
		Expect(err).NotTo(HaveOccurred())
		advanced, err := factory.AdvanceAdmission(ctx, incomplete.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeFalse())

		run, _, err := factory.CreateWithInputs(ctx, request("advance-complete"))
		Expect(err).NotTo(HaveOccurred())
		_, buildID := createExecution(run, "advance-complete", db.BuildStatusPending)
		view, found, err := factory.InspectForReconciliation(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(view.Run.ID).To(Equal(run.ID))
		Expect(view.SelectedBuildExists).To(BeTrue())
		Expect(view.SelectedBuildStatus).To(Equal(db.BuildStatusPending))
		Expect(view.Run.PlannedBuildID).To(Equal(&buildID))

		advanced, err = factory.AdvanceAdmission(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeTrue())
		view, found, err = factory.InspectForReconciliation(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(view.Run.Status).To(Equal(db.AgentWorkflowRunStatusRunning))

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		view, found, err = factory.InspectForReconciliation(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(view.SelectedBuildExists).To(BeFalse())
		Expect(view.Run.PlannedBuildID).To(Equal(&buildID), "copied scalar identity must survive build deletion")
	})

	It("advances a durably linked admission after its ephemeral selected build is deleted", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("advance-after-build-deletion"))
		Expect(err).NotTo(HaveOccurred())
		_, buildID := createExecution(run, "advance-after-build-deletion", db.BuildStatusPending)

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		advanced, err := factory.AdvanceAdmission(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeTrue(), "the transactionally copied execution link is the durable admission evidence")

		stored, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusRunning))
		Expect(stored.PlannedBuildID).To(Equal(&buildID))
	})

	It("copies the selected build outcome once, deduplicates later-build anomalies, and finalizes by locked CAS", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("copied-outcome"))
		Expect(err).NotTo(HaveOccurred())
		link, buildID := createExecution(run, "copied-outcome", db.BuildStatusStarted)
		planHash := strings.Repeat("e", 64)
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: buildID, ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash:       planHash,
			ResolvedDependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`),
		})).To(Succeed())
		advanced, err := factory.AdvanceAdmission(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeTrue())

		_, err = dbConn.Exec(`UPDATE builds SET status = 'failed' WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		capture, err := factory.CaptureExecutionStatus(ctx, buildID, db.BuildStatusFailed)
		Expect(err).NotTo(HaveOccurred())
		Expect(capture.WorkflowRunID).To(Equal(run.ID))
		Expect(capture.Disposition).To(Equal(db.AgentWorkflowRunBuildDispositionSelected))
		capture, err = factory.CaptureExecutionStatus(ctx, buildID, db.BuildStatusFailed)
		Expect(err).NotTo(HaveOccurred())
		Expect(capture.Disposition).To(Equal(db.AgentWorkflowRunBuildDispositionSelected))

		stored, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		executionFailed := db.AgentWorkflowRunExecutionStatusFailed
		Expect(stored.ExecutionStatus).To(Equal(&executionFailed))
		Expect(stored.PlannedBuildID).To(Equal(&buildID))

		var laterBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id)
			VALUES ($1, 'succeeded', $2, $3) RETURNING id
		`, fmt.Sprintf("later-%d", time.Now().UnixNano()), defaultTeam.ID(), link.InstancePipelineID).Scan(&laterBuildID)).To(Succeed())
		capture, err = factory.CaptureExecutionStatus(ctx, laterBuildID, db.BuildStatusSucceeded)
		Expect(err).NotTo(HaveOccurred())
		Expect(capture.Disposition).To(Equal(db.AgentWorkflowRunBuildDispositionAnomalous))
		_, err = factory.CaptureExecutionStatus(ctx, laterBuildID, db.BuildStatusSucceeded)
		Expect(err).NotTo(HaveOccurred())
		var anomalies int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_anomalies
			WHERE workflow_run_id = $1 AND kind = 'later_build_completed' AND build_id = $2
		`, int64(run.ID), laterBuildID).Scan(&anomalies)).To(Succeed())
		Expect(anomalies).To(Equal(1))
		_, err = dbConn.Exec(`UPDATE builds SET status = 'failed' WHERE id = $1`, laterBuildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CaptureExecutionStatus(ctx, laterBuildID, db.BuildStatusFailed)
		Expect(err).To(MatchError(ContainSubstring("anomaly conflicts")))

		finalized, changed, err := factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			ExpectedExecutionStatus: &executionFailed, ExpectedActualPlanHash: &planHash,
			TerminalStatus: db.AgentWorkflowRunStatusFailed, ErrorMessage: "selected build failed",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(finalized.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(finalized.CompletedAt).NotTo(BeZero())
		completedAt := finalized.CompletedAt

		finalized, changed, err = factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			ExpectedExecutionStatus: &executionFailed, ExpectedActualPlanHash: &planHash,
			TerminalStatus: db.AgentWorkflowRunStatusFailed, ErrorMessage: "selected build failed",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(finalized.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(finalized.CompletedAt).To(BeTemporally("==", completedAt))

		_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded' WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CaptureExecutionStatus(ctx, buildID, db.BuildStatusSucceeded)
		Expect(err).To(MatchError(ContainSubstring("conflicts with immutable history")))
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_anomalies
			WHERE workflow_run_id = $1 AND kind = 'later_build_completed' AND build_id = $2
		`, int64(run.ID), buildID).Scan(&anomalies)).To(Succeed())
		Expect(anomalies).To(Equal(0), "the selected build must never be reclassified as a later-build anomaly")
	})

	It("finalizes successful execution from exact fresh output evidence and classifies contract versus platform failures", func() {
		executionSucceeded := db.AgentWorkflowRunExecutionStatusSucceeded
		planHash := strings.Repeat("f", 64)
		prepare := func(key string) (db.AgentWorkflowRun, int64) {
			run, _, err := factory.CreateWithInputs(ctx, request(key))
			Expect(err).NotTo(HaveOccurred())
			_, buildID := createExecution(run, key, db.BuildStatusStarted)
			Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
				BuildID: buildID, ActualPlan: json.RawMessage(`{"task":"review"}`), ActualPlanHash: planHash,
				ResolvedDependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`),
			})).To(Succeed())
			advanced, err := factory.AdvanceAdmission(ctx, run.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(BeTrue())
			_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded' WHERE id = $1`, buildID)
			Expect(err).NotTo(HaveOccurred())
			_, err = factory.CaptureExecutionStatus(ctx, buildID, db.BuildStatusSucceeded)
			Expect(err).NotTo(HaveOccurred())
			return run, buildID
		}
		expected := func(run db.AgentWorkflowRun) []db.AgentWorkflowRunExpectedOutput {
			return []db.AgentWorkflowRunExpectedOutput{{
				Port: "result", Type: input.Type, WorkflowDefinitionID: definitionID, WorkflowRunID: run.ID,
				Producers: []db.AgentWorkflowRunExpectedProducer{{
					PlanID: "plan-result", StepKind: "task", StepName: "review", LocalOutputPort: "result",
				}},
			}}
		}
		bindEvidence := func(run db.AgentWorkflowRun, buildID int64, location bool) {
			_, err := dbConn.Exec(`
				INSERT INTO agent_workflow_run_snapshots
					(workflow_run_id, direction, port_name, snapshot_id)
				VALUES ($1, 'output', 'result', $2)
			`, int64(run.ID), int64(input.ID))
			Expect(err).NotTo(HaveOccurred())
			actor := fmt.Sprintf("workflow-run:%d:output:result", int64(run.ID))
			_, err = dbConn.Exec(`
				INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, expires_at, actor, reason)
				VALUES ($1, $2, 'workflow', NULL, $3, 'durable workflow output')
			`, int64(input.ID), defaultTeam.ID(), actor)
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`
				INSERT INTO agent_snapshot_productions
					(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
					 plan_id, attempt, step_kind, step_name, output_port,
					 workflow_definition_id, workflow_run_id)
				VALUES ($1, 'build', $2, $3, $4, 'alice',
				        'plan-result', '1', 'task', 'review', 'result', $5, $6)
			`, int64(input.ID), buildID, defaultTeam.ID(), defaultTeam.Name(), definitionID, int64(run.ID))
			Expect(err).NotTo(HaveOccurred())
			if location {
				_, err = dbConn.Exec(`
					INSERT INTO agent_snapshot_locations (digest, driver, key, node)
					VALUES ($1, 'migration-test', 'durable/result', 'worker-1')
					ON CONFLICT DO NOTHING
				`, input.Digest.String())
				Expect(err).NotTo(HaveOccurred())
			}
		}
		finalize := func(run db.AgentWorkflowRun, outputs []db.AgentWorkflowRunExpectedOutput) db.AgentWorkflowRunFinalizationResult {
			result, changed, err := factory.Finalize(ctx, db.AgentWorkflowRunFinalization{
				WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
				ExpectedExecutionStatus: &executionSucceeded, ExpectedActualPlanHash: &planHash,
				TerminalStatus: db.AgentWorkflowRunStatusSucceeded, ExpectedOutputs: outputs,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeTrue())
			return result
		}

		success, successBuild := prepare("output-success")
		bindEvidence(success, successBuild, true)
		Expect(finalize(success, expected(success)).Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))
		var promotedAt sql.NullTime
		Expect(dbConn.QueryRow(`
			SELECT promoted_at FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = 'result'
		`, int64(success.ID)).Scan(&promotedAt)).To(Succeed())
		Expect(promotedAt.Valid).To(BeTrue())
		visible, err := factory.Snapshots(ctx, success.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(visible).To(ContainElement(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: success.ID, Direction: db.AgentWorkflowRunSnapshotOutput,
			PortName: "result", Snapshot: snapshot.SnapshotRef{ID: input.ID, Type: input.Type, Digest: input.Digest},
		}))

		optionalMissing, _ := prepare("output-optional-missing")
		optionalContract := expected(optionalMissing)
		optionalContract[0].Optional = true
		Expect(finalize(optionalMissing, optionalContract).Status).To(Equal(db.AgentWorkflowRunStatusSucceeded))

		missingRequired, _ := prepare("output-missing-required")
		missingResult := finalize(missingRequired, expected(missingRequired))
		Expect(missingResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(missingResult.ErrorMessage).To(ContainSubstring("required output"))

		partial, partialBuild := prepare("output-partial-required-set")
		bindEvidence(partial, partialBuild, true)
		partialContract := expected(partial)
		partialContract = append(partialContract, db.AgentWorkflowRunExpectedOutput{
			Port: "second", Type: input.Type, WorkflowDefinitionID: definitionID, WorkflowRunID: partial.ID,
			Producers: []db.AgentWorkflowRunExpectedProducer{{
				PlanID: "plan-second", StepKind: "task", StepName: "second", LocalOutputPort: "second",
			}},
		})
		partialResult := finalize(partial, partialContract)
		Expect(partialResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		partialVisible, err := factory.Snapshots(ctx, partial.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(partialVisible).NotTo(ContainElement(And(
			HaveField("Direction", db.AgentWorkflowRunSnapshotOutput),
		)))

		wrongIdentity, wrongIdentityBuild := prepare("output-wrong-identity")
		bindEvidence(wrongIdentity, wrongIdentityBuild, true)
		wrongIdentityContract := expected(wrongIdentity)
		wrongIdentityContract[0].WorkflowRunID = success.ID
		wrongIdentityResult := finalize(wrongIdentity, wrongIdentityContract)
		Expect(wrongIdentityResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(wrongIdentityResult.ErrorMessage).To(ContainSubstring("workflow identity"))

		ambiguous, ambiguousBuild := prepare("output-ambiguous-producer")
		bindEvidence(ambiguous, ambiguousBuild, true)
		ambiguousContract := expected(ambiguous)
		ambiguousContract[0].Producers = append(ambiguousContract[0].Producers, db.AgentWorkflowRunExpectedProducer{
			PlanID: "other-plan", StepKind: "agent", StepName: "other-step", LocalOutputPort: "other-output",
		})
		ambiguousResult := finalize(ambiguous, ambiguousContract)
		Expect(ambiguousResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(ambiguousResult.ErrorMessage).To(ContainSubstring("ambiguous"))

		extra, extraBuild := prepare("output-extra")
		bindEvidence(extra, extraBuild, true)
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id)
			VALUES ($1, 'output', 'undeclared', $2)
		`, int64(extra.ID), int64(input.ID))
		Expect(err).NotTo(HaveOccurred())
		extraResult := finalize(extra, expected(extra))
		Expect(extraResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(extraResult.ErrorMessage).To(ContainSubstring("unexpected workflow output"))

		missingClaim, missingClaimBuild := prepare("output-missing-claim")
		bindEvidence(missingClaim, missingClaimBuild, true)
		_, err = dbConn.Exec(`
			DELETE FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'workflow'
			  AND actor = $3
		`, int64(input.ID), defaultTeam.ID(), fmt.Sprintf("workflow-run:%d:output:result", int64(missingClaim.ID)))
		Expect(err).NotTo(HaveOccurred())
		claimResult := finalize(missingClaim, expected(missingClaim))
		Expect(claimResult.Status).To(Equal(db.AgentWorkflowRunStatusErrored))
		Expect(claimResult.ErrorMessage).To(ContainSubstring("permanent workflow claim"))

		wrongProducer, wrongProducerBuild := prepare("output-wrong-producer-and-missing-claim")
		bindEvidence(wrongProducer, wrongProducerBuild, true)
		_, err = dbConn.Exec(`
			DELETE FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND team_id = $2 AND class = 'workflow'
			  AND actor = $3
		`, int64(input.ID), defaultTeam.ID(), fmt.Sprintf("workflow-run:%d:output:result", int64(wrongProducer.ID)))
		Expect(err).NotTo(HaveOccurred())
		wrongProducerContract := expected(wrongProducer)
		wrongProducerContract[0].Producers[0].PlanID = "not-the-producing-plan"
		wrongProducerResult := finalize(wrongProducer, wrongProducerContract)
		Expect(wrongProducerResult.Status).To(Equal(db.AgentWorkflowRunStatusFailed))
		Expect(wrongProducerResult.ErrorMessage).To(ContainSubstring("matching producer evidence"))

		_, err = dbConn.Exec(`DELETE FROM agent_snapshot_locations WHERE digest = $1`, input.Digest.String())
		Expect(err).NotTo(HaveOccurred())
		missingLocation, missingLocationBuild := prepare("output-missing-location")
		bindEvidence(missingLocation, missingLocationBuild, false)
		locationResult := finalize(missingLocation, expected(missingLocation))
		Expect(locationResult.Status).To(Equal(db.AgentWorkflowRunStatusErrored))
		Expect(locationResult.ErrorMessage).To(ContainSubstring("location"))
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

	It("rejects pipeline-run and instance ownership shared by different workflow runs", func() {
		first, _, err := factory.CreateWithInputs(ctx, request("execution-owner-first"))
		Expect(err).NotTo(HaveOccurred())
		second, _, err := factory.CreateWithInputs(ctx, request("execution-owner-second"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, instanceID, pipelineRunID int
		suffix := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("owner-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("owner-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())

		link := db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"instance":true}`), ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, first.ID, link)).To(Succeed())
		Expect(factory.LinkExecution(ctx, second.ID, link)).To(MatchError(ContainSubstring("already owned")))
	})

	It("rejects plans for builds outside the linked workflow instance", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("planned-build-ownership"))
		Expect(err).NotTo(HaveOccurred())

		var templateID, instanceID, pipelineRunID int
		var wrongBuildID int64
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
		})).To(MatchError(ContainSubstring("absent")))
	})

	It("captures plan provenance only for the preselected build with exact replay semantics", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("preselected-plan"))
		Expect(err).NotTo(HaveOccurred())
		_, buildID := createExecution(run, "preselected-plan", db.BuildStatusStarted)
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = NULL WHERE id = $1`, int64(run.ID))
		Expect(err).NotTo(HaveOccurred())
		plan := db.AgentWorkflowRunPlan{
			BuildID: buildID, ActualPlan: json.RawMessage(`{"task":"review","id":"plan"}`),
			ActualPlanHash:       strings.Repeat("e", 64),
			ResolvedDependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`),
		}
		Expect(factory.RecordPlan(ctx, run.ID, plan)).To(MatchError(ContainSubstring("selected")))

		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordPlan(ctx, run.ID, plan)).To(Succeed())
		replay := plan
		replay.ActualPlan = json.RawMessage(`{"id":"plan","task":"review"}`)
		replay.ResolvedDependencies = json.RawMessage(`{"images":[],"platform_resource_types":[],"resources":[],"version":1}`)
		Expect(factory.RecordPlan(ctx, run.ID, replay)).To(Succeed())
		conflict := plan
		conflict.ActualPlanHash = strings.Repeat("f", 64)
		Expect(factory.RecordPlan(ctx, run.ID, conflict)).To(MatchError(ContainSubstring("conflicts")))
	})

	It("encrypts the complete plan provenance at rest and round-trips its exact bytes", func() {
		factory = db.NewAgentWorkflowRunsFactory(agentWorkflowRunEncryptedConn{
			DbConn: dbConn, strategy: agentWorkflowRunEncryptionKey(),
		})
		run, _, err := factory.CreateWithInputs(ctx, request("encrypted-actual-plan"))
		Expect(err).NotTo(HaveOccurred())
		_, buildID := createExecution(run, "encrypted-actual-plan", db.BuildStatusStarted)

		canonical := json.RawMessage(`{"id":"secret-plan","private":"top-secret-api-token","task":"review"}`)
		planHash := strings.Repeat("e", 64)
		sourceHash := strings.Repeat("f", 64)
		dependencies := json.RawMessage(`{"version":1,"resources":[{"plan_id":"source-plan","name":"source","resource":"source","type":"git","version":{"ref":"abc123"},"source_identity_hash":"` + sourceHash + `"}],"images":[],"platform_resource_types":[]}`)
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID:              buildID,
			ActualPlan:           canonical,
			ActualPlanHash:       planHash,
			ResolvedDependencies: dependencies,
		})).To(Succeed())

		var storedPlan, storedHash, storedDependencies string
		var planNonce, hashNonce, dependenciesNonce sql.NullString
		Expect(dbConn.QueryRow(`
			SELECT actual_plan, actual_plan_nonce,
			       actual_plan_hash, actual_plan_hash_nonce,
			       resolved_dependencies, resolved_dependencies_nonce
			FROM agent_workflow_runs
			WHERE id = $1
		`, int64(run.ID)).Scan(
			&storedPlan, &planNonce, &storedHash, &hashNonce, &storedDependencies, &dependenciesNonce,
		)).To(Succeed())
		Expect(storedPlan).NotTo(ContainSubstring("top-secret-api-token"))
		Expect(storedHash).NotTo(Equal(planHash), "the deterministic plan fingerprint must not be guessable at rest")
		Expect(storedDependencies).NotTo(ContainSubstring(sourceHash), "dependency source fingerprints must not be guessable at rest")
		Expect(planNonce.Valid && hashNonce.Valid && dependenciesNonce.Valid).To(BeTrue())
		Expect(hashNonce.String).NotTo(Equal(planNonce.String), "each independently encrypted provenance field requires a distinct nonce")
		Expect(dependenciesNonce.String).NotTo(Equal(planNonce.String))
		Expect(dependenciesNonce.String).NotTo(Equal(hashNonce.String))
		originalPlan, originalPlanNonce := storedPlan, planNonce.String
		originalHash, originalHashNonce := storedHash, hashNonce.String
		originalDependencies, originalDependenciesNonce := storedDependencies, dependenciesNonce.String

		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID:              buildID,
			ActualPlan:           json.RawMessage(`{"task":"review","private":"top-secret-api-token","id":"secret-plan"}`),
			ActualPlanHash:       planHash,
			ResolvedDependencies: json.RawMessage(`{"platform_resource_types":[],"images":[],"resources":[{"source_identity_hash":"` + sourceHash + `","version":{"ref":"abc123"},"type":"git","resource":"source","name":"source","plan_id":"source-plan"}],"version":1}`),
		})).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT actual_plan, actual_plan_nonce,
			       actual_plan_hash, actual_plan_hash_nonce,
			       resolved_dependencies, resolved_dependencies_nonce
			FROM agent_workflow_runs
			WHERE id = $1
		`, int64(run.ID)).Scan(
			&storedPlan, &planNonce, &storedHash, &hashNonce, &storedDependencies, &dependenciesNonce,
		)).To(Succeed())
		Expect(storedPlan).To(Equal(originalPlan), "idempotent replay must not replace randomized plan ciphertext")
		Expect(planNonce.String).To(Equal(originalPlanNonce))
		Expect(storedHash).To(Equal(originalHash), "idempotent replay must not replace randomized hash ciphertext")
		Expect(hashNonce.String).To(Equal(originalHashNonce))
		Expect(storedDependencies).To(Equal(originalDependencies), "idempotent replay must not replace randomized dependency ciphertext")
		Expect(dependenciesNonce.String).To(Equal(originalDependenciesNonce))

		roundTripped, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(roundTripped.ActualPlan).To(Equal(canonical))
		Expect(roundTripped.ActualPlanHash).To(Equal(&planHash))
		Expect(roundTripped.ResolvedDependencies).To(Equal(dependencies))
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
			VALUES ($1, $2, 'started', $3, $4)
		`, largeBuildID, fmt.Sprintf("large-build-%d", suffix), defaultTeam.ID(), instanceID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), largeBuildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID: largeBuildID, ActualPlan: json.RawMessage(`{"task":"review"}`),
			ActualPlanHash: strings.Repeat("e", 64), ResolvedDependencies: json.RawMessage(`{}`),
		})).To(Succeed())

		stored, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.PlannedBuildID).To(Equal(&largeBuildID))
	})

	It("enforces the finite workflow-run status vocabulary in PostgreSQL", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("status-constraint"))
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET status = 'future-typo' WHERE id = $1`, int64(run.ID))
		Expect(err).To(HaveOccurred())
	})

	It("validates cancellation through the durable instance pipeline and entry job", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("cancellation-target"))
		Expect(err).NotTo(HaveOccurred())

		suffix := time.Now().UnixNano()
		var templateID, instanceID, pipelineRunID, jobID int
		var buildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, fmt.Sprintf("cancel-template-%d", suffix), defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, fmt.Sprintf("cancel-instance-%d", suffix), defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $2, 1) RETURNING id
		`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO jobs (name, pipeline_id, config, active)
			VALUES ($1, $2, '{}', true) RETURNING id
		`, fmt.Sprintf("cancel-entry-%d", suffix), instanceID).Scan(&jobID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id, job_id)
			VALUES ($1, 'started', $2, $3, $4) RETURNING id
		`, fmt.Sprintf("cancel-build-%d", suffix), defaultTeam.ID(), instanceID, jobID).Scan(&buildID)).To(Succeed())

		Expect(factory.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig:     json.RawMessage(`{"jobs":[{"name":"run"}]}`),
			ConcreteConfigHash: strings.Repeat("d", 64),
		})).To(Succeed())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		transitioned, err := factory.Transition(ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusCanceling, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())

		linked, err := factory.ValidateCancellationTarget(ctx, defaultTeam.ID(), run.ID, buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(linked).To(BeTrue())

		_, err = dbConn.Exec(`UPDATE jobs SET pipeline_id = $2 WHERE id = $1`, jobID, templateID)
		Expect(err).NotTo(HaveOccurred())
		linked, err = factory.ValidateCancellationTarget(ctx, defaultTeam.ID(), run.ID, buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(linked).To(BeFalse())

		_, err = dbConn.Exec(`UPDATE jobs SET pipeline_id = $2 WHERE id = $1`, jobID, instanceID)
		Expect(err).NotTo(HaveOccurred())
		linked, err = factory.ValidateCancellationTarget(ctx, defaultTeam.ID()+1, run.ID, buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(linked).To(BeFalse())
		linked, err = factory.ValidateCancellationTarget(ctx, defaultTeam.ID(), run.ID, buildID+1)
		Expect(err).NotTo(HaveOccurred())
		Expect(linked).To(BeFalse())
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
			case strings.Contains(query, "SELECT EXISTS") &&
				strings.Contains(query, "FROM agent_workflow_definitions"):
				return rowScannerFunc(func(destinations ...any) error {
					*destinations[0].(*bool) = true
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
			case strings.Contains(query, "SELECT team_id") && strings.Contains(query, "FROM agent_workflow_runs"):
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
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ('1', 'started', $1, $2) RETURNING id`, defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())

		link := db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID,
			InstancePipelineID: instanceID,
			ConcreteConfig:     json.RawMessage(`{"instance":true}`),
			ConcreteConfigHash: strings.Repeat("d", 64),
		}
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(Succeed())
		Expect(factory.LinkExecution(ctx, run.ID, link)).To(Succeed())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordPlan(ctx, run.ID, db.AgentWorkflowRunPlan{
			BuildID:              int64(buildID),
			ActualPlan:           json.RawMessage(`{"task":"review"}`),
			ActualPlanHash:       strings.Repeat("e", 64),
			ResolvedDependencies: json.RawMessage(`{"images":{"review":"sha256:abc"}}`),
		})).To(Succeed())

		matches, err := factory.InputBindingMatches(ctx, defaultTeam.ID(), buildID, run.ID, "source", &input)
		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(BeTrue())

		for _, mismatch := range []struct {
			teamID  int
			buildID int
			runID   snapshot.WorkflowRunID
			port    string
			ref     *snapshot.SnapshotRef
		}{
			{defaultTeam.ID() + 1, buildID, run.ID, "source", &input},
			{defaultTeam.ID(), buildID + 1, run.ID, "source", &input},
			{defaultTeam.ID(), buildID, run.ID + 1, "source", &input},
			{defaultTeam.ID(), buildID, run.ID, "other", &input},
			{defaultTeam.ID(), buildID, run.ID, "source", &snapshot.SnapshotRef{ID: input.ID, Type: "other/v1", Digest: input.Digest}},
			{defaultTeam.ID(), buildID, run.ID, "source", nil},
		} {
			matches, err = factory.InputBindingMatches(ctx, mismatch.teamID, mismatch.buildID, mismatch.runID, mismatch.port, mismatch.ref)
			Expect(err).NotTo(HaveOccurred())
			Expect(matches).To(BeFalse())
		}

		matches, err = factory.InputBindingMatches(ctx, defaultTeam.ID(), buildID, run.ID, "optional-unbound", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(BeTrue())
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
		expectedBuildID := int64(buildID)
		Expect(stored.PlannedBuildID).To(Equal(&expectedBuildID))
		Expect(stored.ActualPlan).To(MatchJSON(`{"task":"review"}`))
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusRunning))

		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM pipelines WHERE id IN ($1, $2)`, instanceID, templateID)
		Expect(err).NotTo(HaveOccurred())
		stored, found, err = factory.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.PipelineRunID).To(Equal(&pipelineRunID))
		Expect(stored.TemplatePipelineID).To(Equal(&templateID))
		Expect(stored.InstancePipelineID).To(Equal(&instanceID))
		Expect(stored.PlannedBuildID).To(Equal(&expectedBuildID))
		Expect(stored.ActualPlan).To(MatchJSON(`{"task":"review"}`))
		bindings, err := factory.Snapshots(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bindings).To(ConsistOf(db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: run.ID, Direction: db.AgentWorkflowRunSnapshotInput,
			PortName: "source", Snapshot: input,
		}))
	})

	It("preserves snapshot production history when its workflow-run link is deleted", func() {
		run, _, err := factory.CreateWithInputs(ctx, request("deletion-survival"))
		Expect(err).NotTo(HaveOccurred())

		var productionID int64
		err = dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by, plan_id,
				 attempt, step_kind, step_name, output_port, workflow_definition_id,
				 workflow_run_id, source_metadata)
			VALUES ($1, 'build', 922337, $2, $3, 'alice', 'plan-1', '1', 'task',
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
