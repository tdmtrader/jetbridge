package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/google/uuid"
)

//counterfeiter:generate . AgentChildExecutionsFactory
type AgentChildExecutionsFactory interface {
	Create(context.Context, string, broker.ExecutionIdentity) (AgentChildExecution, error)
	Advance(context.Context, AdvanceAgentChildExecution) (AgentChildExecution, error)
	BindWorkspace(context.Context, BindAgentChildWorkspace) (AgentChildExecution, error)
	ReconcileExpiredLeases(context.Context, int) ([]AgentChildExecution, error)
	Find(context.Context, int, string) (AgentChildExecution, bool, error)
}

type AgentChildExecution struct {
	ID string
	broker.ExecutionIdentity
	IdentityDigest         string
	State                  broker.ExecutionState
	Sequence               int64
	BrokerInstance         string
	LeaseExpiresAt         *time.Time
	TranscriptObject       string
	ResultSnapshotID       *int64
	ResultSnapshot         *snapshot.SnapshotRef
	ResultBody             json.RawMessage
	WorkspaceSnapshot      *snapshot.SnapshotRef
	WorkspaceCaptureDigest string
	ObservedUsage          json.RawMessage
	DurationMS             *int64
	ErrorCode              string
	ErrorRetryable         *bool
	ErrorSummary           string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	TerminalAt             *time.Time
}

type BindAgentChildWorkspace struct {
	ID               string
	TeamID           int
	ExpectedSequence int64
	Snapshot         snapshot.SnapshotRef
	CaptureDigest    string
}

type AdvanceAgentChildExecution struct {
	ID               string
	TeamID           int
	ExpectedSequence int64
	State            broker.ExecutionState
	Phase            string
	BrokerInstance   string
	LeaseExpiresAt   time.Time
	TranscriptObject string
	ResultSnapshotID int64
	ResultSnapshot   *snapshot.SnapshotRef
	ResultBody       json.RawMessage
	ObservedUsage    json.RawMessage
	DurationMS       *int64
	ErrorCode        string
	ErrorRetryable   *bool
	ErrorSummary     string
	Detail           json.RawMessage
}

type agentChildExecutionsFactory struct {
	conn DbConn
}

func NewAgentChildExecutionsFactory(conn DbConn) AgentChildExecutionsFactory {
	return &agentChildExecutionsFactory{conn: conn}
}

const agentChildExecutionColumns = `
	id::text, team_id, workflow_run_id, node_plan_id, parent_attempt,
	idempotency_key, identity_digest, tool, tier, effort, profile_id,
	profile_digest, input_digest, attachments, state, sequence,
	broker_instance, lease_expires_at, transcript_object, result_snapshot_id,
	result_snapshot_type, result_snapshot_digest, result_body, observed_usage,
	workspace_snapshot_id, workspace_snapshot_type, workspace_snapshot_digest,
	workspace_capture_digest,
	duration_ms, error_code, error_retryable, error_summary,
	created_at, updated_at, terminal_at
`

func (factory *agentChildExecutionsFactory) BindWorkspace(
	ctx context.Context,
	request BindAgentChildWorkspace,
) (AgentChildExecution, error) {
	if factory == nil || factory.conn == nil {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution connection is required")
	}
	if _, err := uuid.Parse(request.ID); err != nil || request.TeamID <= 0 || request.ExpectedSequence < 0 {
		return AgentChildExecution{}, fmt.Errorf("db: workspace binding identity is invalid")
	}
	if request.Snapshot.Validate() != nil || request.Snapshot.Type != "repository-change/v1" {
		return AgentChildExecution{}, fmt.Errorf("db: workspace binding requires exact repository-change/v1 snapshot")
	}
	if _, err := snapshot.ParseDigest(request.CaptureDigest); err != nil {
		return AgentChildExecution{}, fmt.Errorf("db: workspace binding requires exact capture digest")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentChildExecution{}, err
	}
	defer Rollback(tx)
	current, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		SELECT `+agentChildExecutionColumns+`
		FROM agent_child_executions
		WHERE id = $1::uuid AND team_id = $2
		FOR UPDATE
	`, request.ID, request.TeamID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution not found")
	}
	if err != nil {
		return AgentChildExecution{}, err
	}
	if current.WorkspaceSnapshot != nil {
		if *current.WorkspaceSnapshot != request.Snapshot ||
			current.WorkspaceCaptureDigest != request.CaptureDigest {
			return AgentChildExecution{}, fmt.Errorf("db: workspace binding conflicts with immutable capture")
		}
		if err := tx.Commit(); err != nil {
			return AgentChildExecution{}, err
		}
		return current, nil
	}
	if current.Sequence != request.ExpectedSequence {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution sequence conflict")
	}
	if current.Tool != broker.ToolRequestReview || current.State != broker.ExecutionCapturing {
		return AgentChildExecution{}, fmt.Errorf("db: workspace binding requires capturing request_review execution")
	}
	updated, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		UPDATE agent_child_executions
		SET sequence = sequence + 1,
		    workspace_snapshot_id = $3,
		    workspace_snapshot_type = $4,
		    workspace_snapshot_digest = $5,
		    workspace_capture_digest = $6
		WHERE id = $1::uuid AND team_id = $2 AND sequence = $7
		RETURNING `+agentChildExecutionColumns,
		request.ID, request.TeamID, request.Snapshot.ID, request.Snapshot.Type,
		request.Snapshot.Digest, request.CaptureDigest, request.ExpectedSequence,
	))
	if err != nil {
		return AgentChildExecution{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_child_execution_events
			(execution_id, sequence, state, phase)
		VALUES ($1::uuid, $2, $3, 'workspace_captured')
	`, request.ID, updated.Sequence, updated.State); err != nil {
		return AgentChildExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentChildExecution{}, err
	}
	return updated, nil
}

func (factory *agentChildExecutionsFactory) Create(
	ctx context.Context,
	id string,
	identity broker.ExecutionIdentity,
) (AgentChildExecution, error) {
	if factory == nil || factory.conn == nil {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution connection is required")
	}
	if _, err := uuid.Parse(id); err != nil {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution ID must be a UUID: %w", err)
	}
	fingerprint, err := identity.Fingerprint()
	if err != nil {
		return AgentChildExecution{}, err
	}
	attachments := append([]string(nil), identity.Attachments...)
	sort.Strings(attachments)
	encodedAttachments, err := json.Marshal(attachments)
	if err != nil {
		return AgentChildExecution{}, err
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentChildExecution{}, err
	}
	defer Rollback(tx)

	existing, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		SELECT `+agentChildExecutionColumns+`
		FROM agent_child_executions
		WHERE workflow_run_id = $1 AND node_plan_id = $2
		  AND parent_attempt = $3 AND idempotency_key = $4
		FOR UPDATE
	`, identity.WorkflowRunID, identity.NodePlanID, identity.ParentAttempt, identity.IdempotencyKey))
	if err == nil {
		if existing.IdentityDigest != fingerprint {
			return AgentChildExecution{}, fmt.Errorf(
				"db: agent child execution identity conflicts with idempotency key")
		}
		if err := tx.Commit(); err != nil {
			return AgentChildExecution{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentChildExecution{}, err
	}

	created, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		INSERT INTO agent_child_executions
			(id, team_id, workflow_run_id, node_plan_id, parent_attempt,
			 idempotency_key, identity_digest, tool, tier, effort, profile_id,
			 profile_digest, input_digest, attachments)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (workflow_run_id, node_plan_id, parent_attempt, idempotency_key)
		DO NOTHING
		RETURNING `+agentChildExecutionColumns,
		id, identity.TeamID, identity.WorkflowRunID, identity.NodePlanID,
		identity.ParentAttempt, identity.IdempotencyKey, fingerprint,
		identity.Tool, identity.Selector.Tier, identity.Selector.Effort,
		identity.ProfileID, identity.ProfileDigest, identity.InputDigest,
		encodedAttachments,
	))
	if errors.Is(err, sql.ErrNoRows) {
		created, err = scanAgentChildExecution(tx.QueryRowContext(ctx, `
			SELECT `+agentChildExecutionColumns+`
			FROM agent_child_executions
			WHERE workflow_run_id = $1 AND node_plan_id = $2
			  AND parent_attempt = $3 AND idempotency_key = $4
			FOR UPDATE
		`, identity.WorkflowRunID, identity.NodePlanID,
			identity.ParentAttempt, identity.IdempotencyKey))
		if err == nil && created.IdentityDigest != fingerprint {
			return AgentChildExecution{}, fmt.Errorf(
				"db: agent child execution identity conflicts with idempotency key")
		}
	}
	if err != nil {
		return AgentChildExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentChildExecution{}, err
	}
	return created, nil
}

func (factory *agentChildExecutionsFactory) Advance(
	ctx context.Context,
	request AdvanceAgentChildExecution,
) (AgentChildExecution, error) {
	if factory == nil || factory.conn == nil {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution connection is required")
	}
	if _, err := uuid.Parse(request.ID); err != nil {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution ID must be a UUID: %w", err)
	}
	if request.TeamID <= 0 || request.ExpectedSequence < 0 {
		return AgentChildExecution{}, fmt.Errorf("db: child execution team and expected sequence are invalid")
	}
	if strings.TrimSpace(request.Phase) == "" {
		return AgentChildExecution{}, fmt.Errorf("db: child execution phase is required")
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentChildExecution{}, err
	}
	defer Rollback(tx)
	current, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		SELECT `+agentChildExecutionColumns+`
		FROM agent_child_executions
		WHERE id = $1::uuid AND team_id = $2
		FOR UPDATE
	`, request.ID, request.TeamID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentChildExecution{}, fmt.Errorf("db: agent child execution not found")
	}
	if err != nil {
		return AgentChildExecution{}, err
	}
	if current.Sequence != request.ExpectedSequence {
		return AgentChildExecution{}, fmt.Errorf(
			"db: agent child execution sequence conflict: got %d, expected %d",
			current.Sequence, request.ExpectedSequence)
	}
	if err := broker.ValidateExecutionTransition(current.State, request.State); err != nil {
		return AgentChildExecution{}, err
	}
	if err := validateAdvanceRequest(request); err != nil {
		return AgentChildExecution{}, err
	}
	lease := nullableTime(request.LeaseExpiresAt)
	resultSnapshot := nullableAgentChildPositiveInt64(request.ResultSnapshotID)
	if request.ResultSnapshot != nil {
		resultSnapshot = request.ResultSnapshot.ID
	}
	usage := nullableChildJSON(request.ObservedUsage)
	retryable := request.ErrorRetryable
	updated, err := scanAgentChildExecution(tx.QueryRowContext(ctx, `
		UPDATE agent_child_executions
		SET state = $3, sequence = sequence + 1,
		    broker_instance = COALESCE(NULLIF($4, ''), broker_instance),
		    lease_expires_at = $5,
		    transcript_object = COALESCE(NULLIF($6, ''), transcript_object),
		    result_snapshot_id = COALESCE($7, result_snapshot_id),
		    result_snapshot_type = COALESCE(NULLIF($8, ''), result_snapshot_type),
		    result_snapshot_digest = COALESCE(NULLIF($9, ''), result_snapshot_digest),
		    result_body = COALESCE($10, result_body),
		    observed_usage = COALESCE($11, observed_usage),
		    duration_ms = COALESCE($12, duration_ms),
		    error_code = NULLIF($13, ''),
		    error_retryable = $14,
		    error_summary = NULLIF($15, '')
		WHERE id = $1::uuid AND team_id = $2 AND sequence = $16
		RETURNING `+agentChildExecutionColumns,
		request.ID, request.TeamID, request.State, request.BrokerInstance,
		lease, request.TranscriptObject, resultSnapshot, snapshotType(request.ResultSnapshot), snapshotDigest(request.ResultSnapshot), nullableChildJSON(request.ResultBody), usage, request.DurationMS,
		request.ErrorCode, retryable, request.ErrorSummary, request.ExpectedSequence,
	))
	if err != nil {
		return AgentChildExecution{}, err
	}
	detail := nullableChildJSON(request.Detail)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_child_execution_events
			(execution_id, sequence, state, phase, detail)
		VALUES ($1::uuid, $2, $3, $4, $5)
	`, request.ID, updated.Sequence, updated.State, request.Phase, detail); err != nil {
		return AgentChildExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentChildExecution{}, err
	}
	return updated, nil
}

// ReconcileExpiredLeases marks up to limit expired, nonterminal broker leases
// as retryable broker_lost errors. The claim and terminal transition share one
// transaction so concurrent reconcilers do not contend for the same execution.
func (factory *agentChildExecutionsFactory) ReconcileExpiredLeases(
	ctx context.Context,
	limit int,
) ([]AgentChildExecution, error) {
	if factory == nil || factory.conn == nil {
		return nil, fmt.Errorf("db: agent child execution connection is required")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("db: positive child execution reconciliation limit is required")
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		WITH claimed AS (
			SELECT id AS execution_id, sequence AS expected_sequence
			FROM agent_child_executions
			WHERE state NOT IN ('succeeded', 'errored', 'cancelled', 'timed_out')
			  AND lease_expires_at IS NOT NULL
			  AND lease_expires_at <= clock_timestamp()
			ORDER BY lease_expires_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE agent_child_executions execution
		SET state = $2,
		    sequence = execution.sequence + 1,
		    lease_expires_at = NULL,
		    error_code = 'broker_lost',
		    error_retryable = TRUE,
		    error_summary = 'broker lease expired before a terminal result was recorded'
		FROM claimed
		WHERE execution.id = claimed.execution_id
		  AND execution.sequence = claimed.expected_sequence
		  AND execution.state NOT IN ('succeeded', 'errored', 'cancelled', 'timed_out')
		  AND execution.lease_expires_at IS NOT NULL
		  AND execution.lease_expires_at <= clock_timestamp()
		RETURNING `+agentChildExecutionColumns,
		limit, broker.ExecutionErrored,
	)
	if err != nil {
		return nil, err
	}
	reconciled := []AgentChildExecution{}
	for rows.Next() {
		execution, err := scanAgentChildExecution(rows)
		if err != nil {
			return nil, err
		}
		reconciled = append(reconciled, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for _, execution := range reconciled {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_child_execution_events
				(execution_id, sequence, state, phase)
			VALUES ($1::uuid, $2, $3, 'broker_lost')
		`, execution.ID, execution.Sequence, execution.State); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return reconciled, nil
}

func (factory *agentChildExecutionsFactory) Find(
	ctx context.Context,
	teamID int,
	id string,
) (AgentChildExecution, bool, error) {
	if teamID <= 0 {
		return AgentChildExecution{}, false, fmt.Errorf("db: team ID must be positive")
	}
	execution, err := scanAgentChildExecution(factory.conn.QueryRowContext(ctx, `
		SELECT `+agentChildExecutionColumns+`
		FROM agent_child_executions
		WHERE id = $1::uuid AND team_id = $2
	`, id, teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentChildExecution{}, false, nil
	}
	return execution, err == nil, err
}

func validateAdvanceRequest(request AdvanceAgentChildExecution) error {
	terminalError := request.State == broker.ExecutionErrored ||
		request.State == broker.ExecutionCancelled ||
		request.State == broker.ExecutionTimedOut
	hasResult := request.ResultSnapshotID > 0 || request.ResultSnapshot != nil || len(request.ResultBody) > 0
	if terminalError && (strings.TrimSpace(request.ErrorCode) == "" || request.ErrorRetryable == nil) {
		return fmt.Errorf("db: terminal child execution error code and retryability are required")
	}
	if terminalError && hasResult {
		return fmt.Errorf("db: terminal child execution error cannot carry a result")
	}
	if request.State == broker.ExecutionSucceeded {
		if request.ResultSnapshot == nil || request.ResultSnapshot.ID <= 0 || len(request.ResultBody) == 0 {
			return fmt.Errorf("db: succeeded child execution requires a result snapshot")
		}
		if request.ResultSnapshotID > 0 && request.ResultSnapshot.ID != snapshot.SnapshotID(request.ResultSnapshotID) {
			return fmt.Errorf("db: succeeded child execution result snapshot ID conflicts with its reference")
		}
		if err := request.ResultSnapshot.Validate(); err != nil {
			return fmt.Errorf("db: succeeded child execution result snapshot is invalid: %w", err)
		}
		if !json.Valid(request.ResultBody) {
			return fmt.Errorf("db: succeeded child execution result body must be JSON")
		}
		if request.ErrorCode != "" || request.ErrorRetryable != nil || request.ErrorSummary != "" {
			return fmt.Errorf("db: succeeded child execution cannot carry terminal error fields")
		}
	}
	if !terminalError && request.State != broker.ExecutionSucceeded &&
		(request.ErrorCode != "" || request.ErrorRetryable != nil || request.ErrorSummary != "" || hasResult) {
		return fmt.Errorf("db: nonterminal child execution cannot carry terminal result fields")
	}
	return nil
}

func scanAgentChildExecution(row interface{ Scan(...any) error }) (AgentChildExecution, error) {
	var execution AgentChildExecution
	var tool, tier, effort, state string
	var attachments []byte
	var brokerInstance, transcriptObject, errorCode, errorSummary sql.NullString
	var leaseExpiresAt, terminalAt sql.NullTime
	var resultSnapshotID, durationMS sql.NullInt64
	var resultSnapshotType, resultSnapshotDigest sql.NullString
	var workspaceSnapshotID sql.NullInt64
	var workspaceSnapshotType, workspaceSnapshotDigest sql.NullString
	var workspaceCaptureDigest sql.NullString
	var observedUsage, resultBody []byte
	var errorRetryable sql.NullBool
	err := row.Scan(
		&execution.ID, &execution.TeamID, &execution.WorkflowRunID,
		&execution.NodePlanID, &execution.ParentAttempt, &execution.IdempotencyKey,
		&execution.IdentityDigest, &tool, &tier, &effort, &execution.ProfileID,
		&execution.ProfileDigest, &execution.InputDigest, &attachments, &state,
		&execution.Sequence, &brokerInstance, &leaseExpiresAt, &transcriptObject,
		&resultSnapshotID, &resultSnapshotType, &resultSnapshotDigest, &resultBody, &observedUsage,
		&workspaceSnapshotID, &workspaceSnapshotType, &workspaceSnapshotDigest,
		&workspaceCaptureDigest, &durationMS, &errorCode,
		&errorRetryable, &errorSummary, &execution.CreatedAt, &execution.UpdatedAt,
		&terminalAt,
	)
	if err != nil {
		return AgentChildExecution{}, err
	}
	execution.Tool = broker.Tool(tool)
	execution.Selector = broker.Selector{Tier: broker.Tier(tier), Effort: broker.Effort(effort)}
	execution.State = broker.ExecutionState(state)
	if err := json.Unmarshal(attachments, &execution.Attachments); err != nil {
		return AgentChildExecution{}, fmt.Errorf("db: decode child execution attachments: %w", err)
	}
	execution.BrokerInstance = brokerInstance.String
	execution.TranscriptObject = transcriptObject.String
	execution.ErrorCode = errorCode.String
	execution.ErrorSummary = errorSummary.String
	if leaseExpiresAt.Valid {
		execution.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if terminalAt.Valid {
		execution.TerminalAt = &terminalAt.Time
	}
	if resultSnapshotID.Valid {
		execution.ResultSnapshotID = &resultSnapshotID.Int64
		if resultSnapshotType.Valid && resultSnapshotDigest.Valid {
			execution.ResultSnapshot = &snapshot.SnapshotRef{ID: snapshot.SnapshotID(resultSnapshotID.Int64), Type: snapshot.TypeRef(resultSnapshotType.String), Digest: snapshot.Digest(resultSnapshotDigest.String)}
		}
	}
	if workspaceSnapshotID.Valid && workspaceSnapshotType.Valid && workspaceSnapshotDigest.Valid {
		execution.WorkspaceSnapshot = &snapshot.SnapshotRef{
			ID:     snapshot.SnapshotID(workspaceSnapshotID.Int64),
			Type:   snapshot.TypeRef(workspaceSnapshotType.String),
			Digest: snapshot.Digest(workspaceSnapshotDigest.String),
		}
	}
	if workspaceCaptureDigest.Valid {
		execution.WorkspaceCaptureDigest = workspaceCaptureDigest.String
	}
	if durationMS.Valid {
		execution.DurationMS = &durationMS.Int64
	}
	if errorRetryable.Valid {
		execution.ErrorRetryable = &errorRetryable.Bool
	}
	if len(observedUsage) > 0 {
		execution.ObservedUsage = append(json.RawMessage(nil), observedUsage...)
	}
	if len(resultBody) > 0 {
		execution.ResultBody = append(json.RawMessage(nil), resultBody...)
	}
	return execution, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableAgentChildPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableChildJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

func snapshotType(value *snapshot.SnapshotRef) string {
	if value == nil {
		return ""
	}
	return string(value.Type)
}
func snapshotDigest(value *snapshot.SnapshotRef) string {
	if value == nil {
		return ""
	}
	return string(value.Digest)
}
