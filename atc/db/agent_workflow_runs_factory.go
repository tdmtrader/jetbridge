package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

//counterfeiter:generate . AgentWorkflowRunsFactory
type AgentWorkflowRunsFactory interface {
	CreateWithInputs(context.Context, AgentWorkflowRunCreateRequest) (AgentWorkflowRun, bool, error)
	FindByIdempotencyKey(context.Context, int, string) (AgentWorkflowRun, bool, error)
	Get(context.Context, int, snapshot.WorkflowRunID) (AgentWorkflowRun, bool, error)
	List(context.Context, AgentWorkflowRunListFilter) ([]AgentWorkflowRun, error)
	LinkExecution(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunExecutionLink) error
	RecordPlan(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunPlan) error
	Transition(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunStatus, AgentWorkflowRunStatus, string) (bool, error)
	ListForReconciliation(context.Context, int) ([]AgentWorkflowRun, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]AgentWorkflowRunSnapshotBinding, error)
	InputBindingMatches(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error)
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
	existing, found, err := findWorkflowRunByIdempotencyKey(ctx, tx, request.TeamID, request.IdempotencyKey, true)
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
	`, request.TeamID, request.IdempotencyKey))
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
	run, err := scanAgentWorkflowRun(queryer.QueryRowContext(ctx, query, teamID, key))
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
		var teamID int
		err := tx.QueryRowContext(ctx, `SELECT team_id FROM agent_workflow_runs WHERE id = $1`, int64(*request.RetryOfWorkflowRunID)).Scan(&teamID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
		if err != nil {
			return err
		}
		if teamID != request.TeamID {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
	}
	return nil
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
	return findWorkflowRunByIdempotencyKey(ctx, factory.conn, teamID, key, false)
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
	`, int64(id), teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRun{}, false, nil
	}
	return run, err == nil, err
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
	return queryWorkflowRuns(ctx, factory.conn, query, args...)
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
	var found int64
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
		[]byte(link.ConcreteConfig), link.ConcreteConfigHash).Scan(&found)
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

	var runTeamID, buildTeamID int
	var instancePipelineID, buildPipelineID sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT r.team_id, r.instance_pipeline_id, b.team_id, b.pipeline_id
		FROM agent_workflow_runs r
		JOIN builds b ON b.id = $2
		WHERE r.id = $1
		FOR UPDATE OF r, b
	`, int64(id), plan.BuildID).Scan(
		&runTeamID, &instancePipelineID, &buildTeamID, &buildPipelineID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run or planned build is absent")
	}
	if err != nil {
		return err
	}
	if runTeamID != buildTeamID {
		return fmt.Errorf("db: planned build is not owned by the workflow-run team")
	}
	if !instancePipelineID.Valid || !buildPipelineID.Valid || instancePipelineID.Int64 != buildPipelineID.Int64 {
		return fmt.Errorf("db: planned build does not belong to the linked workflow instance")
	}
	var found int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET planned_build_id = $2,
		    actual_plan = $3,
		    actual_plan_hash = $4,
		    resolved_dependencies = $5,
		    updated_at = now()
		WHERE id = $1
		  AND (
			((planned_build_id IS NULL OR planned_build_id = $2)
			 AND actual_plan IS NULL AND actual_plan_hash IS NULL
			 AND resolved_dependencies IS NULL)
			OR
			(planned_build_id = $2 AND actual_plan = $3::jsonb
			 AND actual_plan_hash = $4
			 AND resolved_dependencies IS NOT DISTINCT FROM $5::jsonb)
		  )
		RETURNING id
	`, int64(id), plan.BuildID, []byte(plan.ActualPlan), plan.ActualPlanHash,
		nullableJSON(plan.ResolvedDependencies)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
	}
	if err != nil {
		return err
	}
	return tx.Commit()
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
	`, int64(id), string(from), string(to), errorMessage, terminal)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (factory *agentWorkflowRunsFactory) ListForReconciliation(
	ctx context.Context,
	limit int,
) ([]AgentWorkflowRun, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("db: workflow-run reconciliation limit must be between 1 and 1000")
	}
	return queryWorkflowRuns(ctx, factory.conn, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE status IN ('admitting', 'running', 'canceling')
		ORDER BY updated_at, id
		LIMIT $1
	`, limit)
}

func queryWorkflowRuns(
	ctx context.Context,
	queryer snapshotQueryer,
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
		run, err := scanAgentWorkflowRun(rows)
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
