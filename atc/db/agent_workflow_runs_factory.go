package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db/encryption"
)

//counterfeiter:generate . AgentWorkflowRunsFactory
type AgentWorkflowRunsFactory interface {
	CreateWithInputs(context.Context, AgentWorkflowRunCreateRequest) (AgentWorkflowRun, bool, error)
	FindByIdempotencyKey(context.Context, int, string) (AgentWorkflowRun, bool, error)
	Get(context.Context, int, snapshot.WorkflowRunID) (AgentWorkflowRun, bool, error)
	List(context.Context, AgentWorkflowRunListFilter) ([]AgentWorkflowRun, error)
	LinkExecution(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunExecutionLink) error
	RecordPlan(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunPlan) error
	CaptureExecutionStatus(context.Context, int64, BuildStatus) (AgentWorkflowRunBuildCaptureResult, error)
	Transition(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunStatus, AgentWorkflowRunStatus, string) (bool, error)
	ClaimForReconciliation(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error)
	InspectForReconciliation(context.Context, snapshot.WorkflowRunID) (AgentWorkflowRunReconciliationView, bool, error)
	AdvanceAdmission(context.Context, snapshot.WorkflowRunID) (bool, error)
	Finalize(context.Context, AgentWorkflowRunFinalization) (AgentWorkflowRunFinalizationResult, bool, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]AgentWorkflowRunSnapshotBinding, error)
	InputBindingMatches(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error)
	ValidateCancellationTarget(context.Context, int, snapshot.WorkflowRunID, int64) (bool, error)
}

func NewAgentWorkflowRunsFactory(conn DbConn) AgentWorkflowRunsFactory {
	return &agentWorkflowRunsFactory{conn: conn}
}

type agentWorkflowRunsFactory struct {
	conn DbConn
}

func (factory *agentWorkflowRunsFactory) CreateWithInputs(
	ctx context.Context,
	request AgentWorkflowRunCreateRequest,
) (AgentWorkflowRun, bool, error) {
	if err := request.Validate(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	defer Rollback(tx)

	if err := lockWorkflowRunIdempotency(ctx, tx, request.TeamID, request.IdempotencyKey); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	existing, found, err := findWorkflowRunByIdempotencyKey(
		ctx, tx, tx.EncryptionStrategy(), request.TeamID, request.IdempotencyKey, true,
	)
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if found {
		if err := validateIdempotentWorkflowRun(existing, request); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		if err := validateWorkflowRunBindings(ctx, tx, existing.ID, request.Inputs); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		return existing, false, nil
	}

	if err := lockWorkflowRunInputDigests(ctx, tx, request.Inputs); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunTarget(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunInputs(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name,
			 workflow_version, schema_version, signature_version,
			 definition_content_hash, function_id, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 origin_kind, origin_reference, created_by, status,
			 retry_of_workflow_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (team_id, idempotency_key) DO NOTHING
	`, request.TeamID, request.TeamName, request.WorkflowDefinitionID, request.WorkflowName,
		request.WorkflowVersion, request.SchemaVersion, request.SignatureVersion,
		request.DefinitionContentHash, optionalString(request.FunctionID), request.IdempotencyKey,
		[]byte(request.ParameterizedConfig), request.ParameterizedConfigHash,
		request.OriginKind, request.OriginReference, request.CreatedBy, string(request.Status),
		optionalInt64(request.RetryOfWorkflowRunID))
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	created := affected == 1
	if !created {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run idempotency serialization did not reserve a unique key")
	}

	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE team_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, request.TeamID, request.IdempotencyKey), tx.EncryptionStrategy())
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateIdempotentWorkflowRun(run, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}

	ports := sortedSnapshotPorts(request.Inputs)
	for _, port := range ports {
		ref := request.Inputs[port]
		if err := bindWorkflowRunSnapshot(ctx, tx, run.ID, "input", port, ref.ID); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		actor := fmt.Sprintf("workflow-run:%d:input:%s", int64(run.ID), port)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, actor, reason)
			VALUES ($1, $2, 'workflow', $3, 'durable workflow-run input')
		`, int64(ref.ID), request.TeamID, actor)
		if err != nil {
			return AgentWorkflowRun{}, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	return run, created, nil
}

const workflowRunIdempotencyLockDomain = "agent-workflow-run-idempotency/v1\x00"

func lockWorkflowRunIdempotency(ctx context.Context, tx Tx, teamID int, key string) error {
	lockKey := snapshotAdvisoryLockKey(workflowRunIdempotencyLockDomain, fmt.Sprintf("%d\x00%s", teamID, key))
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey)
	return err
}

func lockWorkflowRunInputDigests(
	ctx context.Context,
	tx Tx,
	inputs map[string]snapshot.SnapshotRef,
) error {
	unique := make(map[snapshot.Digest]struct{}, len(inputs))
	for _, ref := range inputs {
		unique[ref.Digest] = struct{}{}
	}
	ordered := make([]snapshot.Digest, 0, len(unique))
	for digest := range unique {
		ordered = append(ordered, digest)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, digest := range ordered {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, snapshotDigestLockKey(digest)); err != nil {
			return fmt.Errorf("db: lock workflow-run input digest %s: %w", digest, err)
		}
	}
	return nil
}

func findWorkflowRunByIdempotencyKey(
	ctx context.Context,
	queryer snapshotQueryer,
	encryptionStrategy encryption.Strategy,
	teamID int,
	key string,
	lock bool,
) (AgentWorkflowRun, bool, error) {
	query := `SELECT ` + agentWorkflowRunColumns + `
		FROM agent_workflow_runs
		WHERE team_id = $1 AND idempotency_key = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	run, err := scanAgentWorkflowRun(queryer.QueryRowContext(ctx, query, teamID, key), encryptionStrategy)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRun{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	return run, true, nil
}

func validateWorkflowRunTarget(ctx context.Context, tx Tx, request AgentWorkflowRunCreateRequest) error {
	var teamName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE id = $1`, request.TeamID).Scan(&teamName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run team %d does not exist", request.TeamID)
		}
		return err
	}
	if teamName != request.TeamName {
		return fmt.Errorf("db: workflow-run team name does not match current team")
	}

	var name, hash string
	var version, schemaVersion, signatureVersion int
	err := tx.QueryRowContext(ctx, `
		SELECT name, version, schema_version, signature_version, content_hash
		FROM agent_workflow_definitions
		WHERE id = $1
	`, request.WorkflowDefinitionID).Scan(&name, &version, &schemaVersion, &signatureVersion, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run definition %d does not exist", request.WorkflowDefinitionID)
	}
	if err != nil {
		return err
	}
	if name != request.WorkflowName || version != request.WorkflowVersion ||
		schemaVersion != request.SchemaVersion || signatureVersion != request.SignatureVersion ||
		hash != request.DefinitionContentHash {
		return fmt.Errorf("db: workflow-run copied definition identity does not match the durable definition")
	}

	if request.RetryOfWorkflowRunID != nil {
		var (
			teamID                int
			workflowDefinitionID  int
			workflowName          string
			workflowVersion       int
			schemaVersion         int
			signatureVersion      int
			definitionContentHash string
			functionID            sql.NullString
			status                AgentWorkflowRunStatus
		)
		err := tx.QueryRowContext(ctx, `
			SELECT team_id, workflow_definition_id, workflow_name, workflow_version,
			       schema_version, signature_version, definition_content_hash,
			       function_id, status
			FROM agent_workflow_runs
			WHERE id = $1
			FOR KEY SHARE
		`, int64(*request.RetryOfWorkflowRunID)).Scan(
			&teamID, &workflowDefinitionID, &workflowName, &workflowVersion,
			&schemaVersion, &signatureVersion, &definitionContentHash,
			&functionID, &status,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
		if err != nil {
			return err
		}
		if teamID != request.TeamID {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
		var sourceFunctionID *string
		if functionID.Valid {
			sourceFunctionID = &functionID.String
		}
		if workflowDefinitionID != request.WorkflowDefinitionID ||
			workflowName != request.WorkflowName ||
			workflowVersion != request.WorkflowVersion ||
			schemaVersion != request.SchemaVersion ||
			signatureVersion != request.SignatureVersion ||
			definitionContentHash != request.DefinitionContentHash ||
			!equalOptionalString(sourceFunctionID, request.FunctionID) {
			return fmt.Errorf("db: workflow-run retry target is incompatible with the requested workflow target")
		}
		if !isTerminalWorkflowRunStatus(status) {
			return fmt.Errorf("db: workflow-run retry target is not terminal")
		}
		if request.OriginKind != "retry" || request.OriginReference != request.RetryOfWorkflowRunID.String() {
			return fmt.Errorf("db: workflow-run retry origin does not identify its retry target")
		}
		if err := validateWorkflowRunBindings(ctx, tx, *request.RetryOfWorkflowRunID, request.Inputs); err != nil {
			return fmt.Errorf("db: workflow-run retry input bindings do not match the retry target: %w", err)
		}
	}
	return nil
}

func isTerminalWorkflowRunStatus(status AgentWorkflowRunStatus) bool {
	switch status {
	case AgentWorkflowRunStatusSucceeded,
		AgentWorkflowRunStatusFailed,
		AgentWorkflowRunStatusErrored,
		AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

func validateWorkflowRunInputs(ctx context.Context, tx Tx, request AgentWorkflowRunCreateRequest) error {
	for port, ref := range request.Inputs {
		value, err := authorizedSnapshotByRef(ctx, tx, request.TeamID, ref, true)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run input %q is unavailable or unauthorized", port)
		}
		if err != nil {
			return err
		}
		if value.Type != ref.Type || value.Digest != ref.Digest {
			return fmt.Errorf("db: workflow-run input %q does not match its immutable identity", port)
		}
	}
	return nil
}

func validateIdempotentWorkflowRun(run AgentWorkflowRun, request AgentWorkflowRunCreateRequest) error {
	if run.TeamID != request.TeamID ||
		run.WorkflowDefinitionID != request.WorkflowDefinitionID ||
		run.WorkflowName != request.WorkflowName || run.WorkflowVersion != request.WorkflowVersion ||
		run.SchemaVersion != request.SchemaVersion || run.SignatureVersion != request.SignatureVersion ||
		run.DefinitionContentHash != request.DefinitionContentHash ||
		!equalOptionalString(run.FunctionID, request.FunctionID) ||
		run.IdempotencyKey != request.IdempotencyKey ||
		!semanticJSONEqual(run.ParameterizedConfig, request.ParameterizedConfig) ||
		run.ParameterizedConfigHash != request.ParameterizedConfigHash ||
		run.OriginKind != request.OriginKind || run.OriginReference != request.OriginReference ||
		run.CreatedBy != request.CreatedBy ||
		!equalWorkflowRunID(run.RetryOfWorkflowRunID, request.RetryOfWorkflowRunID) {
		return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable target, origin, or inputs")
	}
	return nil
}

func validateWorkflowRunBindings(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	expected map[string]snapshot.SnapshotRef,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT b.port_name, b.snapshot_id, s.type_name, s.type_version, s.digest
		FROM agent_workflow_run_snapshots b
		JOIN agent_snapshots s ON s.id = b.snapshot_id
		WHERE b.workflow_run_id = $1 AND b.direction = 'input'
	`, int64(runID))
	if err != nil {
		return err
	}
	defer Close(rows)
	found := make(map[string]snapshot.SnapshotRef)
	for rows.Next() {
		var port, typeName, digest string
		var id int64
		var typeVersion int
		if err := rows.Scan(&port, &id, &typeName, &typeVersion, &digest); err != nil {
			return err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			return err
		}
		found[port] = snapshot.SnapshotRef{
			ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest),
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) != len(expected) {
		return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable input bindings")
	}
	for port, ref := range expected {
		if found[port] != ref {
			return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable input bindings")
		}
	}
	return nil
}

func sortedSnapshotPorts(inputs map[string]snapshot.SnapshotRef) []string {
	ports := make([]string, 0, len(inputs))
	for port := range inputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalWorkflowRunID(left, right *snapshot.WorkflowRunID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (factory *agentWorkflowRunsFactory) FindByIdempotencyKey(
	ctx context.Context,
	teamID int,
	key string,
) (AgentWorkflowRun, bool, error) {
	if teamID <= 0 || strings.TrimSpace(key) == "" {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run team and idempotency key are required")
	}
	return findWorkflowRunByIdempotencyKey(
		ctx, factory.conn, factory.conn.EncryptionStrategy(), teamID, key, false,
	)
}

func (factory *agentWorkflowRunsFactory) Get(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (AgentWorkflowRun, bool, error) {
	if teamID <= 0 {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run team ID must be positive")
	}
	if err := id.Validate(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	run, err := scanAgentWorkflowRun(factory.conn.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1 AND team_id = $2
	`, int64(id), teamID), factory.conn.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRun{}, false, nil
	}
	return run, err == nil, err
}

// ValidateCancellationTarget verifies the selected build through the full
// durable workflow-run -> pipeline-run -> instance-pipeline -> job chain.
// Cancellation must not rely on a build ID and team alone: both are public
// integer identities and neither proves that the build is the entry job of
// the instance materialized for this workflow run.
func (factory *agentWorkflowRunsFactory) ValidateCancellationTarget(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
	buildID int64,
) (bool, error) {
	if teamID <= 0 || buildID <= 0 {
		return false, fmt.Errorf("db: workflow-run cancellation target requires positive team and build IDs")
	}
	if err := id.Validate(); err != nil {
		return false, err
	}
	var linked bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs run
			JOIN pipeline_runs execution
			  ON execution.id = run.pipeline_run_id
			 AND execution.template_pipeline_id = run.template_pipeline_id
			 AND execution.instance_pipeline_id = run.instance_pipeline_id
			JOIN pipelines template
			  ON template.id = run.template_pipeline_id
			 AND template.team_id = run.team_id
			JOIN pipelines instance
			  ON instance.id = run.instance_pipeline_id
			 AND instance.team_id = run.team_id
			JOIN builds selected
			  ON selected.id = run.planned_build_id
			 AND selected.pipeline_id = instance.id
			 AND selected.team_id = run.team_id
			JOIN jobs entry
			  ON entry.id = selected.job_id
			 AND entry.pipeline_id = instance.id
			WHERE run.id = $1
			  AND run.team_id = $2
			  AND run.planned_build_id = $3
			  AND run.status = 'canceling'
		)
	`, int64(id), teamID, buildID).Scan(&linked)
	return linked, err
}

func (factory *agentWorkflowRunsFactory) List(
	ctx context.Context,
	filter AgentWorkflowRunListFilter,
) ([]AgentWorkflowRun, error) {
	if filter.TeamID <= 0 || filter.Limit < 0 || filter.Limit > 1000 {
		return nil, fmt.Errorf("db: workflow-run list requires a team and a limit from 0 to 1000")
	}
	if filter.Status != "" {
		if err := filter.Status.Validate(); err != nil {
			return nil, err
		}
	}
	query := `SELECT ` + agentWorkflowRunColumns + ` FROM agent_workflow_runs WHERE team_id = $1`
	args := []any{filter.TeamID}
	appendFilter := func(column string, value any) {
		args = append(args, value)
		query += ` AND ` + column + ` = $` + strconv.Itoa(len(args))
	}
	if filter.WorkflowName != "" {
		appendFilter("workflow_name", filter.WorkflowName)
	}
	if filter.Status != "" {
		appendFilter("status", string(filter.Status))
	}
	if filter.OriginKind != "" {
		appendFilter("origin_kind", filter.OriginKind)
	}
	if filter.OriginReference != "" {
		appendFilter("origin_reference", filter.OriginReference)
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 100
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))
	return queryWorkflowRuns(ctx, factory.conn, factory.conn.EncryptionStrategy(), query, args...)
}

func (factory *agentWorkflowRunsFactory) LinkExecution(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	link AgentWorkflowRunExecutionLink,
) error {
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := linkAgentWorkflowRunExecution(ctx, tx, id, link); err != nil {
		return err
	}
	return tx.Commit()
}

// linkAgentWorkflowRunExecution accepts a transaction so pipeline-run
// creation can attach durable ownership before the new execution is visible.
func linkAgentWorkflowRunExecution(
	ctx context.Context,
	queryer snapshotQueryer,
	id snapshot.WorkflowRunID,
	link AgentWorkflowRunExecutionLink,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := link.Validate(); err != nil {
		return err
	}
	var runTeamID, actualTemplateID, actualInstanceID, templateTeamID, instanceTeamID int
	err := queryer.QueryRowContext(ctx, `
		SELECT r.team_id, pr.template_pipeline_id, pr.instance_pipeline_id,
		       template.team_id, instance.team_id
		FROM agent_workflow_runs r
		JOIN pipeline_runs pr ON pr.id = $2
		JOIN pipelines template ON template.id = pr.template_pipeline_id
		JOIN pipelines instance ON instance.id = pr.instance_pipeline_id
		WHERE r.id = $1
		FOR UPDATE OF r, pr, template, instance
	`, int64(id), link.PipelineRunID).Scan(
		&runTeamID, &actualTemplateID, &actualInstanceID, &templateTeamID, &instanceTeamID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run or pipeline-run execution association is absent")
	}
	if err != nil {
		return err
	}
	if actualTemplateID != link.TemplatePipelineID || actualInstanceID != link.InstancePipelineID {
		return fmt.Errorf("db: workflow-run pipeline execution association does not match its pipeline run")
	}
	if templateTeamID != runTeamID || instanceTeamID != runTeamID {
		return fmt.Errorf("db: workflow-run pipeline execution is not owned by its team")
	}
	var ownerID int64
	err = queryer.QueryRowContext(ctx, `
		SELECT id
		FROM agent_workflow_runs
		WHERE id <> $1
		  AND (pipeline_run_id = $2 OR instance_pipeline_id = $3)
		FOR UPDATE
	`, int64(id), link.PipelineRunID, link.InstancePipelineID).Scan(&ownerID)
	if err == nil {
		return fmt.Errorf("db: workflow-run pipeline execution is already owned by another run")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var linkedID int64
	err = queryer.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET pipeline_run_id = $2,
		    template_pipeline_id = $3,
		    instance_pipeline_id = $4,
		    concrete_config = $5,
		    concrete_config_hash = $6,
		    updated_at = now()
		WHERE id = $1
		  AND (
			(pipeline_run_id IS NULL AND template_pipeline_id IS NULL
			 AND instance_pipeline_id IS NULL AND concrete_config IS NULL
			 AND concrete_config_hash IS NULL)
			OR
			(pipeline_run_id = $2 AND template_pipeline_id = $3
			 AND instance_pipeline_id = $4 AND concrete_config = $5::jsonb
			 AND concrete_config_hash = $6)
		  )
		RETURNING id
	`, int64(id), link.PipelineRunID, link.TemplatePipelineID, link.InstancePipelineID,
		[]byte(link.ConcreteConfig), link.ConcreteConfigHash).Scan(&linkedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run execution link is absent or conflicts with immutable provenance")
	}
	return err
}

func (factory *agentWorkflowRunsFactory) RecordPlan(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	plan AgentWorkflowRunPlan,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := captureAgentWorkflowRunPlan(ctx, tx, id, plan); err != nil {
		return err
	}
	return tx.Commit()
}

type agentWorkflowRunBuildAssociation struct {
	runID                snapshot.WorkflowRunID
	workflowDefinitionID int
	concreteConfig       json.RawMessage
	status               AgentWorkflowRunStatus
	executionStatus      *AgentWorkflowRunExecutionStatus
	plannedBuildID       *int64
	buildStatus          BuildStatus
}

func lockAgentWorkflowRunForBuild(
	ctx context.Context,
	tx Tx,
	buildID int64,
) (agentWorkflowRunBuildAssociation, bool, error) {
	if buildID <= 0 {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build ID must be positive")
	}
	var (
		association      agentWorkflowRunBuildAssociation
		runID            int64
		concreteConfig   []byte
		status           string
		executionStatus  sql.NullString
		plannedBuildID   sql.NullInt64
		runTeamID        int
		pipelineRunID    sql.NullInt64
		templatePipeline sql.NullInt64
		instancePipeline sql.NullInt64
		buildStatus      string
		buildTeamID      int
		buildPipelineID  sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT r.id, r.workflow_definition_id, r.concrete_config, r.status,
		       r.execution_status, r.planned_build_id, r.team_id,
		       r.pipeline_run_id, r.template_pipeline_id, r.instance_pipeline_id,
		       b.status, b.team_id, b.pipeline_id
		FROM builds b
		JOIN agent_workflow_runs r ON r.instance_pipeline_id = b.pipeline_id
		WHERE b.id = $1
		FOR UPDATE OF r, b
	`, buildID).Scan(
		&runID, &association.workflowDefinitionID, &concreteConfig, &status,
		&executionStatus, &plannedBuildID, &runTeamID,
		&pipelineRunID, &templatePipeline, &instancePipeline,
		&buildStatus, &buildTeamID, &buildPipelineID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentWorkflowRunBuildAssociation{}, false, nil
	}
	if err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if !pipelineRunID.Valid || !templatePipeline.Valid || !instancePipeline.Valid ||
		!buildPipelineID.Valid || buildPipelineID.Int64 != instancePipeline.Int64 ||
		runTeamID != buildTeamID || len(concreteConfig) == 0 {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build has an incomplete durable execution chain")
	}
	var locked int
	err = tx.QueryRowContext(ctx, `
		SELECT 1
		FROM pipeline_runs execution
		JOIN pipelines template ON template.id = execution.template_pipeline_id
		JOIN pipelines instance ON instance.id = execution.instance_pipeline_id
		WHERE execution.id = $1
		  AND execution.template_pipeline_id = $2
		  AND execution.instance_pipeline_id = $3
		  AND template.team_id = $4
		  AND instance.team_id = $4
		FOR UPDATE OF execution, template, instance
	`, pipelineRunID.Int64, templatePipeline.Int64, instancePipeline.Int64, runTeamID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build execution chain is absent or inconsistent")
	}
	if err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}

	association.runID = snapshot.WorkflowRunID(runID)
	association.concreteConfig = cloneJSON(concreteConfig)
	association.status = AgentWorkflowRunStatus(status)
	association.buildStatus = BuildStatus(buildStatus)
	if err := association.runID.Validate(); err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if err := association.status.Validate(); err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if !validAgentWorkflowRunBuildStatus(association.buildStatus) {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: invalid workflow-run build status %q", association.buildStatus)
	}
	if executionStatus.Valid {
		value := AgentWorkflowRunExecutionStatus(executionStatus.String)
		if err := value.Validate(); err != nil {
			return agentWorkflowRunBuildAssociation{}, false, err
		}
		association.executionStatus = &value
	}
	if plannedBuildID.Valid {
		value := plannedBuildID.Int64
		association.plannedBuildID = &value
	}
	return association, true, nil
}

func validAgentWorkflowRunBuildStatus(status BuildStatus) bool {
	switch status {
	case BuildStatusPending, BuildStatusStarted, BuildStatusSucceeded,
		BuildStatusFailed, BuildStatusErrored, BuildStatusAborted:
		return true
	default:
		return false
	}
}

func agentWorkflowRunExecutionStatusForBuild(status BuildStatus) (AgentWorkflowRunExecutionStatus, error) {
	switch status {
	case BuildStatusSucceeded:
		return AgentWorkflowRunExecutionStatusSucceeded, nil
	case BuildStatusFailed:
		return AgentWorkflowRunExecutionStatusFailed, nil
	case BuildStatusErrored:
		return AgentWorkflowRunExecutionStatusErrored, nil
	case BuildStatusAborted:
		return AgentWorkflowRunExecutionStatusAborted, nil
	default:
		return "", fmt.Errorf("db: workflow-run execution status requires a terminal build, got %q", status)
	}
}

func workflowRunBuildCaptureResult(
	association agentWorkflowRunBuildAssociation,
	disposition AgentWorkflowRunBuildDisposition,
) AgentWorkflowRunBuildCaptureResult {
	return AgentWorkflowRunBuildCaptureResult{
		WorkflowRunID:        association.runID,
		WorkflowDefinitionID: association.workflowDefinitionID,
		ConcreteConfig:       cloneJSON(association.concreteConfig),
		Disposition:          disposition,
	}
}

func recordAgentWorkflowRunAnomaly(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	kind agentWorkflowRunAnomalyKind,
	buildID int64,
	status BuildStatus,
) error {
	if err := runID.Validate(); err != nil {
		return err
	}
	if buildID <= 0 || !validAgentWorkflowRunBuildStatus(status) {
		return fmt.Errorf("db: workflow-run anomaly requires a positive build and valid status")
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_run_anomalies
			(workflow_run_id, kind, build_id, build_status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (workflow_run_id, kind, build_id) DO NOTHING
	`, int64(runID), string(kind), buildID, string(status))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 1 {
		return err
	}
	var storedStatus BuildStatus
	err = tx.QueryRowContext(ctx, `
		SELECT build_status
		FROM agent_workflow_run_anomalies
		WHERE workflow_run_id = $1 AND kind = $2 AND build_id = $3
		FOR UPDATE
	`, int64(runID), string(kind), buildID).Scan(&storedStatus)
	if err != nil {
		return err
	}
	if storedStatus != status {
		return fmt.Errorf("db: workflow-run anomaly conflicts with immutable build status %q", storedStatus)
	}
	return nil
}

func prepareAgentWorkflowRunBuildStart(
	ctx context.Context,
	tx Tx,
	buildID int64,
) (AgentWorkflowRunBuildCaptureResult, error) {
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, buildID)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if !found {
		return AgentWorkflowRunBuildCaptureResult{Disposition: AgentWorkflowRunBuildDispositionOrdinary}, nil
	}
	selected := association.plannedBuildID != nil && *association.plannedBuildID == buildID
	active := association.status == AgentWorkflowRunStatusAdmitting ||
		association.status == AgentWorkflowRunStatusRunning ||
		association.status == AgentWorkflowRunStatusCanceling
	if !selected || !active {
		if err := recordAgentWorkflowRunAnomaly(
			ctx, tx, association.runID, agentWorkflowRunAnomalyLaterBuildStarted, buildID, association.buildStatus,
		); err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionAnomalous), nil
	}
	if association.status == AgentWorkflowRunStatusCanceling {
		return AgentWorkflowRunBuildCaptureResult{}, &AgentWorkflowRunCancelingError{WorkflowRunID: association.runID}
	}
	if association.buildStatus != BuildStatusStarted {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: selected workflow-run build must be started before plan capture, got %q", association.buildStatus,
		)
	}
	return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
}

func captureAgentWorkflowRunPlan(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	plan AgentWorkflowRunPlan,
) error {
	if err := runID.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, plan.BuildID)
	if err != nil {
		return err
	}
	if !found || association.runID != runID {
		return fmt.Errorf("db: workflow-run or planned build is absent")
	}
	if association.plannedBuildID == nil || *association.plannedBuildID != plan.BuildID {
		return fmt.Errorf("db: workflow-run build is not the preselected execution build")
	}
	if association.status != AgentWorkflowRunStatusAdmitting && association.status != AgentWorkflowRunStatusRunning {
		return fmt.Errorf("db: workflow-run plan cannot be captured while status is %q", association.status)
	}
	if association.buildStatus != BuildStatusStarted {
		return fmt.Errorf("db: workflow-run plan requires its selected build to be started")
	}

	var (
		storedPlan              sql.NullString
		storedPlanNonce         sql.NullString
		storedPlanHash          sql.NullString
		storedPlanHashNonce     sql.NullString
		storedDependencies      sql.NullString
		storedDependenciesNonce sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		SELECT actual_plan, actual_plan_nonce,
		       actual_plan_hash, actual_plan_hash_nonce,
		       resolved_dependencies, resolved_dependencies_nonce
		FROM agent_workflow_runs
		WHERE id = $1
	`, int64(runID)).Scan(
		&storedPlan, &storedPlanNonce, &storedPlanHash, &storedPlanHashNonce,
		&storedDependencies, &storedDependenciesNonce,
	)
	if err != nil {
		return err
	}

	empty := !storedPlan.Valid && !storedPlanNonce.Valid &&
		!storedPlanHash.Valid && !storedPlanHashNonce.Valid &&
		!storedDependencies.Valid && !storedDependenciesNonce.Valid
	complete := storedPlan.Valid && storedPlanHash.Valid && storedDependencies.Valid
	if !empty {
		if !complete {
			return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
		}
		var planNonce *string
		if storedPlanNonce.Valid {
			planNonce = &storedPlanNonce.String
		}
		plaintextPlan, err := tx.EncryptionStrategy().Decrypt(storedPlan.String, planNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run actual plan: %w", err)
		}
		var hashNonce *string
		if storedPlanHashNonce.Valid {
			hashNonce = &storedPlanHashNonce.String
		}
		plaintextHash, err := tx.EncryptionStrategy().Decrypt(storedPlanHash.String, hashNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run actual plan hash: %w", err)
		}
		var dependenciesNonce *string
		if storedDependenciesNonce.Valid {
			dependenciesNonce = &storedDependenciesNonce.String
		}
		plaintextDependencies, err := tx.EncryptionStrategy().Decrypt(storedDependencies.String, dependenciesNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run resolved dependencies: %w", err)
		}
		if !semanticJSONEqual(plaintextPlan, plan.ActualPlan) ||
			string(plaintextHash) != plan.ActualPlanHash ||
			!semanticJSONEqual(plaintextDependencies, plan.ResolvedDependencies) {
			return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
		}
		return nil
	}

	encryptedPlan, planNonce, err := tx.EncryptionStrategy().Encrypt(plan.ActualPlan)
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run actual plan: %w", err)
	}
	encryptedPlanHash, planHashNonce, err := tx.EncryptionStrategy().Encrypt([]byte(plan.ActualPlanHash))
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run actual plan hash: %w", err)
	}
	encryptedDependencies, dependenciesNonce, err := tx.EncryptionStrategy().Encrypt(plan.ResolvedDependencies)
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run resolved dependencies: %w", err)
	}
	var capturedID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET actual_plan = $3,
		    actual_plan_nonce = $4,
		    actual_plan_hash = $5,
		    actual_plan_hash_nonce = $6,
		    resolved_dependencies = $7,
		    resolved_dependencies_nonce = $8,
		    updated_at = now()
		WHERE id = $1
		  AND planned_build_id = $2
		  AND status IN ('admitting', 'running')
		  AND actual_plan IS NULL
		  AND actual_plan_nonce IS NULL
		  AND actual_plan_hash IS NULL
		  AND actual_plan_hash_nonce IS NULL
		  AND resolved_dependencies IS NULL
		  AND resolved_dependencies_nonce IS NULL
		RETURNING id
	`, int64(runID), plan.BuildID, encryptedPlan, planNonce, encryptedPlanHash,
		planHashNonce, encryptedDependencies, dependenciesNonce).Scan(&capturedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
	}
	return err
}

func (factory *agentWorkflowRunsFactory) CaptureExecutionStatus(
	ctx context.Context,
	buildID int64,
	status BuildStatus,
) (AgentWorkflowRunBuildCaptureResult, error) {
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	defer Rollback(tx)
	result, err := captureAgentWorkflowRunExecutionStatus(ctx, tx, buildID, status)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	return result, nil
}

func captureAgentWorkflowRunExecutionStatus(
	ctx context.Context,
	tx Tx,
	buildID int64,
	status BuildStatus,
) (AgentWorkflowRunBuildCaptureResult, error) {
	executionStatus, err := agentWorkflowRunExecutionStatusForBuild(status)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, buildID)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if !found {
		return AgentWorkflowRunBuildCaptureResult{Disposition: AgentWorkflowRunBuildDispositionOrdinary}, nil
	}
	if association.buildStatus != status {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: workflow-run build status %q does not match copied outcome %q", association.buildStatus, status,
		)
	}
	selected := association.plannedBuildID != nil && *association.plannedBuildID == buildID
	if !selected {
		if err := recordAgentWorkflowRunAnomaly(
			ctx, tx, association.runID, agentWorkflowRunAnomalyLaterBuildCompleted, buildID, status,
		); err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionAnomalous), nil
	}
	terminal := association.status == AgentWorkflowRunStatusSucceeded ||
		association.status == AgentWorkflowRunStatusFailed ||
		association.status == AgentWorkflowRunStatusErrored ||
		association.status == AgentWorkflowRunStatusAborted
	if terminal {
		if association.executionStatus != nil && *association.executionStatus == executionStatus {
			return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
		}
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: selected workflow-run execution outcome conflicts with immutable history",
		)
	}

	var captured int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET execution_status = COALESCE(execution_status, $3),
		    updated_at = CASE WHEN execution_status IS NULL THEN now() ELSE updated_at END
		WHERE id = $1
		  AND planned_build_id = $2
		  AND status IN ('admitting', 'running', 'canceling')
		  AND (execution_status IS NULL OR execution_status = $3)
		RETURNING id
	`, int64(association.runID), buildID, string(executionStatus)).Scan(&captured)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf("db: workflow-run execution outcome conflicts with immutable history")
	}
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
}

func terminalizeAgentWorkflowRunPlanningError(
	ctx context.Context,
	tx Tx,
	buildID int64,
	message string,
) (AgentWorkflowRunBuildCaptureResult, error) {
	result, err := captureAgentWorkflowRunExecutionStatus(ctx, tx, buildID, BuildStatusErrored)
	if err != nil || result.Disposition != AgentWorkflowRunBuildDispositionSelected {
		return result, err
	}
	message = truncateAgentWorkflowRunMessage(message, 4*1024)
	var terminalID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = 'errored',
		    error_message = $2,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND status IN ('admitting', 'running', 'canceling')
		  AND execution_status = 'errored'
		RETURNING id
	`, int64(result.WorkflowRunID), message).Scan(&terminalID)
	if errors.Is(err, sql.ErrNoRows) {
		var status AgentWorkflowRunStatus
		var execution AgentWorkflowRunExecutionStatus
		var storedMessage string
		err = tx.QueryRowContext(ctx, `
			SELECT status, execution_status, error_message
			FROM agent_workflow_runs WHERE id = $1
		`, int64(result.WorkflowRunID)).Scan(&status, &execution, &storedMessage)
		if err == nil && status == AgentWorkflowRunStatusErrored &&
			execution == AgentWorkflowRunExecutionStatusErrored && storedMessage == message {
			return result, nil
		}
		if err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf("db: workflow-run planning error conflicts with immutable terminal history")
	}
	return result, err
}

func truncateAgentWorkflowRunMessage(message string, limit int) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	if len(message) <= limit {
		return message
	}
	message = message[:limit]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func (factory *agentWorkflowRunsFactory) Transition(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	from AgentWorkflowRunStatus,
	to AgentWorkflowRunStatus,
	errorMessage string,
) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	if err := from.Validate(); err != nil {
		return false, err
	}
	if err := to.Validate(); err != nil {
		return false, err
	}
	if err := validateAgentWorkflowRunTransition(from, to); err != nil {
		return false, err
	}
	directAdmissionFailure := from == AgentWorkflowRunStatusAdmitting && to == AgentWorkflowRunStatusErrored
	if workflowRunTerminal(to) && !directAdmissionFailure {
		return false, fmt.Errorf("db: terminal workflow-run transitions require locked Finalize evidence")
	}
	if len(errorMessage) > 64*1024 {
		return false, fmt.Errorf("db: workflow-run error message exceeds 64 KiB")
	}
	terminal := to == AgentWorkflowRunStatusSucceeded || to == AgentWorkflowRunStatusFailed ||
		to == AgentWorkflowRunStatusErrored || to == AgentWorkflowRunStatusAborted
	result, err := factory.conn.ExecContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = $3,
		    error_message = $4,
		    started_at = CASE WHEN $3 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE WHEN $5 THEN COALESCE(completed_at, now()) ELSE completed_at END,
		    updated_at = now()
		WHERE id = $1 AND status = $2
		  AND (
			NOT $6
			OR (
				pipeline_run_id IS NULL
				AND template_pipeline_id IS NULL
				AND instance_pipeline_id IS NULL
				AND concrete_config IS NULL
				AND concrete_config_hash IS NULL
				AND planned_build_id IS NULL
				AND actual_plan IS NULL
				AND actual_plan_hash IS NULL
				AND resolved_dependencies IS NULL
				AND execution_status IS NULL
			)
		  )
	`, int64(id), string(from), string(to), errorMessage, terminal, directAdmissionFailure)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (factory *agentWorkflowRunsFactory) ClaimForReconciliation(
	ctx context.Context,
	now time.Time,
	delay time.Duration,
	limit int,
) ([]snapshot.WorkflowRunID, error) {
	if now.IsZero() || delay <= 0 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("db: workflow-run reconciliation requires a time, positive delay, and limit between 1 and 1000")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	rows, err := tx.QueryContext(ctx, `
		WITH due AS (
			SELECT id
			FROM agent_workflow_runs
			WHERE status IN ('admitting', 'running', 'canceling')
			  AND reconcile_after <= $1
			ORDER BY reconcile_after, id
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		)
		UPDATE agent_workflow_runs AS run
		SET reconcile_after = $2
		FROM due
		WHERE run.id = due.id
		RETURNING run.id
	`, now, now.Add(delay), limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	ids := make([]snapshot.WorkflowRunID, 0, limit)
	for rows.Next() {
		var rawID int64
		if err := rows.Scan(&rawID); err != nil {
			return nil, err
		}
		id := snapshot.WorkflowRunID(rawID)
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("db: invalid claimed workflow-run ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (factory *agentWorkflowRunsFactory) InspectForReconciliation(
	ctx context.Context,
	id snapshot.WorkflowRunID,
) (AgentWorkflowRunReconciliationView, bool, error) {
	if err := id.Validate(); err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	defer Rollback(tx)
	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1
		FOR SHARE
	`, int64(id)), tx.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunReconciliationView{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	view := AgentWorkflowRunReconciliationView{Run: run}
	if run.PlannedBuildID != nil {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM builds WHERE id = $1`, *run.PlannedBuildID).Scan(&status)
		if err == nil {
			view.SelectedBuildExists = true
			view.SelectedBuildStatus = BuildStatus(status)
			if !validAgentWorkflowRunBuildStatus(view.SelectedBuildStatus) {
				return AgentWorkflowRunReconciliationView{}, false, fmt.Errorf("db: invalid selected workflow-run build status %q", status)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return AgentWorkflowRunReconciliationView{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	return view, true, nil
}

func (factory *agentWorkflowRunsFactory) AdvanceAdmission(
	ctx context.Context,
	id snapshot.WorkflowRunID,
) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	// CreateRunForWorkflowRun copies this complete link while holding the run
	// row in the same transaction that creates and selects the entry build.
	// Those copied scalars are the durable admission evidence: pipeline, build,
	// and template rows are deliberately allowed to be garbage-collected before
	// a crashed web process resumes this CAS.
	result, err := factory.conn.ExecContext(ctx, `
		UPDATE agent_workflow_runs AS run
		SET status = 'running',
		    started_at = COALESCE(started_at, now()),
		    updated_at = now()
		WHERE run.id = $1
		  AND run.status = 'admitting'
		  AND run.pipeline_run_id IS NOT NULL
		  AND run.template_pipeline_id IS NOT NULL
		  AND run.instance_pipeline_id IS NOT NULL
		  AND run.concrete_config IS NOT NULL
		  AND run.concrete_config_hash IS NOT NULL
		  AND run.planned_build_id IS NOT NULL
	`, int64(id))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (factory *agentWorkflowRunsFactory) Finalize(
	ctx context.Context,
	finalization AgentWorkflowRunFinalization,
) (AgentWorkflowRunFinalizationResult, bool, error) {
	if err := finalization.Validate(); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	defer Rollback(tx)
	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1
		FOR UPDATE
	`, int64(finalization.WorkflowRunID)), tx.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	if workflowRunTerminal(run.Status) {
		return workflowRunFinalizationResult(run), false, nil
	}
	if run.Status != finalization.ExpectedStatus ||
		!equalAgentWorkflowRunExecutionStatus(run.ExecutionStatus, finalization.ExpectedExecutionStatus) ||
		!equalOptionalString(run.ActualPlanHash, finalization.ExpectedActualPlanHash) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}

	terminalStatus := finalization.TerminalStatus
	errorMessage := finalization.ErrorMessage
	if terminalStatus == AgentWorkflowRunStatusSucceeded {
		terminalStatus, errorMessage, err = validateAgentWorkflowRunOutputEvidence(
			ctx, tx, run, finalization.ExpectedOutputs,
		)
		if err != nil {
			return AgentWorkflowRunFinalizationResult{}, false, err
		}
	}
	if err := validateAgentWorkflowRunTransition(run.Status, terminalStatus); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	errorMessage = truncateAgentWorkflowRunMessage(errorMessage, 4*1024)

	var completedAt time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = $5,
		    error_message = $6,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND status = $2
		  AND execution_status IS NOT DISTINCT FROM $3
		  AND (actual_plan_hash IS NOT NULL) = $4
		RETURNING completed_at
	`, int64(finalization.WorkflowRunID), string(finalization.ExpectedStatus),
		optionalAgentWorkflowRunExecutionStatus(finalization.ExpectedExecutionStatus),
		finalization.ExpectedActualPlanHash != nil, string(terminalStatus), errorMessage).Scan(&completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	return AgentWorkflowRunFinalizationResult{
		Status: terminalStatus, ErrorMessage: errorMessage, CompletedAt: completedAt,
	}, true, nil
}

func workflowRunTerminal(status AgentWorkflowRunStatus) bool {
	return status == AgentWorkflowRunStatusSucceeded || status == AgentWorkflowRunStatusFailed ||
		status == AgentWorkflowRunStatusErrored || status == AgentWorkflowRunStatusAborted
}

func workflowRunFinalizationResult(run AgentWorkflowRun) AgentWorkflowRunFinalizationResult {
	result := AgentWorkflowRunFinalizationResult{Status: run.Status, ErrorMessage: run.ErrorMessage}
	if run.CompletedAt != nil {
		result.CompletedAt = *run.CompletedAt
	}
	return result
}

func equalAgentWorkflowRunExecutionStatus(left, right *AgentWorkflowRunExecutionStatus) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalAgentWorkflowRunExecutionStatus(status *AgentWorkflowRunExecutionStatus) any {
	if status == nil {
		return nil
	}
	return string(*status)
}

type agentWorkflowRunOutputBinding struct {
	snapshotID   snapshot.SnapshotID
	typeRef      snapshot.TypeRef
	digest       snapshot.Digest
	contentState snapshot.ContentState
}

func validateAgentWorkflowRunOutputEvidence(
	ctx context.Context,
	tx Tx,
	run AgentWorkflowRun,
	expected []AgentWorkflowRunExpectedOutput,
) (AgentWorkflowRunStatus, string, error) {
	expectedByPort := make(map[string]AgentWorkflowRunExpectedOutput, len(expected))
	for _, output := range expected {
		expectedByPort[output.Port] = output
		if agentWorkflowRunOutputHasAmbiguousProducer(output) {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has ambiguous producer evidence", output.Port), nil
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT binding.port_name, value.id, value.type_name, value.type_version,
		       value.digest, value.content_state
		FROM agent_workflow_run_snapshots binding
		JOIN agent_snapshots value ON value.id = binding.snapshot_id
		WHERE binding.workflow_run_id = $1
		  AND binding.direction = 'output'
		ORDER BY binding.port_name
	`, int64(run.ID))
	if err != nil {
		return "", "", err
	}
	bindings := make(map[string]agentWorkflowRunOutputBinding)
	for rows.Next() {
		var (
			port, typeName, digest, contentState string
			snapshotID                           int64
			typeVersion                          int
		)
		if err := rows.Scan(&port, &snapshotID, &typeName, &typeVersion, &digest, &contentState); err != nil {
			Close(rows)
			return "", "", err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			Close(rows)
			return "", "", err
		}
		bindings[port] = agentWorkflowRunOutputBinding{
			snapshotID: snapshot.SnapshotID(snapshotID), typeRef: typeRef,
			digest: snapshot.Digest(digest), contentState: snapshot.ContentState(contentState),
		}
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return "", "", err
	}
	for port := range bindings {
		if _, found := expectedByPort[port]; !found {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("unexpected workflow output %q", port), nil
		}
	}
	for _, output := range expected {
		if output.WorkflowDefinitionID != run.WorkflowDefinitionID || output.WorkflowRunID != run.ID {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has mismatched workflow identity", output.Port), nil
		}
		binding, found := bindings[output.Port]
		if !found {
			if output.Optional {
				continue
			}
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("required output %q is missing", output.Port), nil
		}
		if binding.typeRef != output.Type {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has the wrong type", output.Port), nil
		}
		if run.PlannedBuildID == nil {
			return AgentWorkflowRunStatusErrored, "workflow output provenance has no selected build", nil
		}
		matched, err := agentWorkflowRunOutputHasMatchingProduction(ctx, tx, run, binding, output)
		if err != nil {
			return "", "", err
		}
		if !matched {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has no matching producer evidence", output.Port), nil
		}
		if binding.contentState != snapshot.ContentStateAvailable {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q content is not available", output.Port), nil
		}
		var granted bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_snapshot_grants
				WHERE snapshot_id = $1 AND team_id = $2
			)
		`, int64(binding.snapshotID), run.TeamID).Scan(&granted); err != nil {
			return "", "", err
		}
		if !granted {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q has no durable team grant", output.Port), nil
		}
		actor := fmt.Sprintf("workflow-run:%d:output:%s", int64(run.ID), output.Port)
		var retained bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_snapshot_retention_claims
				WHERE snapshot_id = $1 AND team_id = $2
				  AND class = 'workflow' AND expires_at IS NULL AND actor = $3
			)
		`, int64(binding.snapshotID), run.TeamID, actor).Scan(&retained); err != nil {
			return "", "", err
		}
		if !retained {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q has no permanent workflow claim", output.Port), nil
		}
		var located bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM agent_snapshot_locations WHERE digest = $1)
		`, binding.digest.String()).Scan(&located); err != nil {
			return "", "", err
		}
		if !located {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q has no durable location", output.Port), nil
		}
	}
	return AgentWorkflowRunStatusSucceeded, "", nil
}

func agentWorkflowRunOutputHasMatchingProduction(
	ctx context.Context,
	tx Tx,
	run AgentWorkflowRun,
	binding agentWorkflowRunOutputBinding,
	output AgentWorkflowRunExpectedOutput,
) (bool, error) {
	productionRows, err := tx.QueryContext(ctx, `
		SELECT plan_id, step_kind, step_name, output_port
		FROM agent_snapshot_productions
		WHERE occurrence_kind = 'build'
		  AND snapshot_id = $1
		  AND team_id = $2
		  AND workflow_run_id = $3
		  AND workflow_definition_id = $4
		  AND build_id = $5
		ORDER BY id
	`, int64(binding.snapshotID), run.TeamID, int64(run.ID), run.WorkflowDefinitionID, *run.PlannedBuildID)
	if err != nil {
		return false, err
	}
	defer Close(productionRows)
	matched := false
	for productionRows.Next() {
		var producer AgentWorkflowRunExpectedProducer
		if err := productionRows.Scan(
			&producer.PlanID, &producer.StepKind, &producer.StepName, &producer.LocalOutputPort,
		); err != nil {
			return false, err
		}
		for _, allowed := range output.Producers {
			if producer == allowed {
				matched = true
			}
		}
	}
	return matched, productionRows.Err()
}

func agentWorkflowRunOutputHasAmbiguousProducer(output AgentWorkflowRunExpectedOutput) bool {
	if len(output.Producers) < 2 {
		return false
	}
	logical := output.Producers[0]
	for _, producer := range output.Producers[1:] {
		if producer.StepKind != logical.StepKind || producer.StepName != logical.StepName ||
			producer.LocalOutputPort != logical.LocalOutputPort {
			return true
		}
	}
	return false
}

func queryWorkflowRuns(
	ctx context.Context,
	queryer snapshotQueryer,
	encryptionStrategy encryption.Strategy,
	query string,
	args ...any,
) ([]AgentWorkflowRun, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	runs := make([]AgentWorkflowRun, 0)
	for rows.Next() {
		run, err := scanAgentWorkflowRun(rows, encryptionStrategy)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (factory *agentWorkflowRunsFactory) Snapshots(
	ctx context.Context,
	runID snapshot.WorkflowRunID,
) ([]AgentWorkflowRunSnapshotBinding, error) {
	if err := runID.Validate(); err != nil {
		return nil, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT b.direction, b.port_name,
		       s.id, s.type_name, s.type_version, s.digest
		FROM agent_workflow_run_snapshots b
		JOIN agent_snapshots s ON s.id = b.snapshot_id
		WHERE b.workflow_run_id = $1
		ORDER BY b.direction, b.port_name
	`, int64(runID))
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	bindings := make([]AgentWorkflowRunSnapshotBinding, 0)
	for rows.Next() {
		var direction, port, typeName, digest string
		var id int64
		var typeVersion int
		if err := rows.Scan(&direction, &port, &id, &typeName, &typeVersion, &digest); err != nil {
			return nil, err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			return nil, err
		}
		binding := AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID,
			Direction:     AgentWorkflowRunSnapshotDirection(direction),
			PortName:      port,
			Snapshot: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest),
			},
		}
		if err := binding.Direction.Validate(); err != nil {
			return nil, err
		}
		if err := binding.Snapshot.Validate(); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

// InputBindingMatches authoritatively verifies both the current workflow
// execution/build linkage and one exact immutable input binding in a single
// database read. A nil ref means the named input port must be unbound.
func (factory *agentWorkflowRunsFactory) InputBindingMatches(
	ctx context.Context,
	teamID int,
	buildID int,
	runID snapshot.WorkflowRunID,
	port string,
	ref *snapshot.SnapshotRef,
) (bool, error) {
	if teamID <= 0 || buildID <= 0 || strings.TrimSpace(port) == "" {
		return false, fmt.Errorf("db: workflow input binding requires positive team/build IDs and a port")
	}
	if err := runID.Validate(); err != nil {
		return false, err
	}

	var snapshotID int64
	var typeName, digest string
	var typeVersion int
	hasRef := ref != nil
	if ref != nil {
		if err := ref.Validate(); err != nil {
			return false, err
		}
		var err error
		typeName, typeVersion, err = splitSnapshotType(ref.Type)
		if err != nil {
			return false, err
		}
		snapshotID = int64(ref.ID)
		digest = ref.Digest.String()
	}

	var matches bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs r
			JOIN pipeline_runs execution
			  ON execution.id = r.pipeline_run_id
			 AND execution.template_pipeline_id = r.template_pipeline_id
			 AND execution.instance_pipeline_id = r.instance_pipeline_id
			JOIN pipelines template ON template.id = r.template_pipeline_id
			JOIN pipelines instance ON instance.id = r.instance_pipeline_id
			JOIN builds build ON build.id = $2
			WHERE r.id = $3
			  AND r.team_id = $1
			  AND r.planned_build_id = $2
			  AND template.team_id = $1
			  AND instance.team_id = $1
			  AND build.team_id = $1
			  AND build.pipeline_id = instance.id
			  AND (
				($6 AND EXISTS (
					SELECT 1
					FROM agent_workflow_run_snapshots binding
					JOIN agent_snapshots value ON value.id = binding.snapshot_id
					WHERE binding.workflow_run_id = r.id
					  AND binding.direction = 'input'
					  AND binding.port_name = $4
					  AND binding.snapshot_id = $5
					  AND value.type_name = $7
					  AND value.type_version = $8
					  AND value.digest = $9
				))
				OR
				(NOT $6 AND NOT EXISTS (
					SELECT 1
					FROM agent_workflow_run_snapshots binding
					WHERE binding.workflow_run_id = r.id
					  AND binding.direction = 'input'
					  AND binding.port_name = $4
				))
			  )
		)
	`, teamID, buildID, int64(runID), port, snapshotID, hasRef, typeName, typeVersion, digest).Scan(&matches)
	return matches, err
}
