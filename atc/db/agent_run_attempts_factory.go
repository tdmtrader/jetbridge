package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/snapshot"
)

// AgentRunAttemptsFactory is PostgreSQL's execution-attempt and mutation-fence
// authority.
//
//counterfeiter:generate . AgentRunAttemptsFactory
type AgentRunAttemptsFactory interface {
	checkpoint.AttemptStore
}

func NewAgentRunAttemptsFactory(conn DbConn) AgentRunAttemptsFactory {
	return &agentRunAttemptsFactory{conn: conn}
}

type agentRunAttemptsFactory struct {
	conn DbConn
}

var _ checkpoint.AttemptStore = (*agentRunAttemptsFactory)(nil)

const agentRunAttemptColumns = `
	a.id,
	h.workflow_run_id,
	h.build_id, h.plan_id, h.function_id,
	a.attempt_number, a.max_total_attempts, a.state, a.is_current,
	a.materialization_id, a.source_attempt_number, a.source_checkpoint_id,
	a.source_checkpoint_generation, a.recovery_mode, a.source_interruption_reason,
	a.interruption_reason, a.fence_token::text, a.fence_expires_at,
	a.created_at, a.updated_at, a.interrupted_at, a.terminal_at
`

func (factory *agentRunAttemptsFactory) AllocateInitial(
	ctx context.Context,
	request checkpoint.AllocateInitialAttemptRequest,
) (checkpoint.Attempt, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.Attempt{}, err
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	defer Rollback(tx)

	head, err := getOrCreateCheckpointHead(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	existing, err := agentRunAttemptByNumberForUpdate(ctx, tx, head.id, 1)
	if err == nil {
		if existing.MaxTotalAttempts != request.EffectiveMaxTotalAttempts() ||
			existing.MaterializationID != request.MaterializationID ||
			existing.SourceExecutionAttempt != 0 {
			return checkpoint.Attempt{}, fmt.Errorf(
				"%w: initial execution attempt was allocated with different authority",
				checkpoint.ErrConflict,
			)
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Attempt{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Attempt{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.Attempt{}, err
	}

	attempt, err := scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		INSERT INTO agent_run_attempts
			(head_id, attempt_number, max_total_attempts, state, is_current, materialization_id)
		VALUES ($1, 1, $2, 'scheduling', TRUE, $3)
		RETURNING
			id,
			$4::bigint,
			$5::bigint, $6::text, $7::text,
			attempt_number, max_total_attempts, state, is_current,
			materialization_id, source_attempt_number, source_checkpoint_id,
			source_checkpoint_generation, recovery_mode, source_interruption_reason,
			interruption_reason, fence_token::text, fence_expires_at,
			created_at, updated_at, interrupted_at, terminal_at
	`, head.id, request.EffectiveMaxTotalAttempts(), request.MaterializationID,
		checkpointWorkflowRunIDValue(request.Identity.WorkflowRunID),
		request.Identity.BuildID, request.Identity.PlanID, request.Identity.FunctionID,
	))
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Attempt{}, err
	}
	return attempt, nil
}

func (factory *agentRunAttemptsFactory) Current(
	ctx context.Context,
	identity checkpoint.Identity,
) (checkpoint.Attempt, bool, error) {
	identity = identity.Clone()
	if err := identity.Validate(); err != nil {
		return checkpoint.Attempt{}, false, err
	}
	head, err := checkpointHead(ctx, factory.conn, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Attempt{}, false, nil
	}
	if err != nil {
		return checkpoint.Attempt{}, false, err
	}
	attempt, err := agentRunCurrentAttempt(ctx, factory.conn, head.id, "")
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Attempt{}, false, nil
	}
	if err != nil {
		return checkpoint.Attempt{}, false, err
	}
	return attempt, true, nil
}

func (factory *agentRunAttemptsFactory) AcquireFence(
	ctx context.Context,
	request checkpoint.AcquireAttemptFenceRequest,
) (checkpoint.AttemptFence, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.AttemptFence{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	defer Rollback(tx)

	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.AttemptFence{}, err
	}
	attempt, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return checkpoint.AttemptFence{}, currentAttemptError(err)
	}
	if attempt.ExecutionAttempt != request.ExecutionAttempt {
		return checkpoint.AttemptFence{}, staleFenceError("execution attempt is no longer current")
	}
	if !attemptCanOwnFence(attempt.State) {
		return checkpoint.AttemptFence{}, staleFenceError(
			fmt.Sprintf("execution attempt is %s", attempt.State),
		)
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	if attempt.Fence != nil {
		if attempt.Fence.Token == request.Token {
			if attempt.Fence.ExpiresAt.After(now) {
				if err := tx.Commit(); err != nil {
					return checkpoint.AttemptFence{}, err
				}
				return *attempt.Fence, nil
			}
			return checkpoint.AttemptFence{}, staleFenceError("attempt fence token was released or expired")
		}
		if attempt.Fence.ExpiresAt.After(now) {
			return checkpoint.AttemptFence{}, fmt.Errorf(
				"%w: current execution attempt already has an unexpired fence",
				checkpoint.ErrConflict,
			)
		}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_run_attempt_fence_tokens (attempt_id, token)
		VALUES ($1, $2)
		ON CONFLICT (token) DO NOTHING
	`, attempt.ID, request.Token)
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	if inserted != 1 {
		return checkpoint.AttemptFence{}, staleFenceError(
			"attempt fence token was already issued",
		)
	}

	var fence checkpoint.AttemptFence
	fence.ExecutionAttempt = request.ExecutionAttempt
	fence.Token = request.Token
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_run_attempts
		SET fence_token = $2,
			fence_expires_at = clock_timestamp() + ($3::double precision * INTERVAL '1 second')
		WHERE id = $1 AND is_current
		RETURNING fence_expires_at
	`, attempt.ID, request.Token, request.TTL.Seconds()).Scan(&fence.ExpiresAt)
	if err != nil {
		return checkpoint.AttemptFence{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.AttemptFence{}, err
	}
	return fence, nil
}

func (factory *agentRunAttemptsFactory) ReleaseFence(
	ctx context.Context,
	request checkpoint.ReleaseAttemptFenceRequest,
) error {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)

	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return err
	}
	attempt, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return currentAttemptError(err)
	}
	if attempt.ExecutionAttempt != request.ExecutionAttempt ||
		attempt.Fence == nil || attempt.Fence.Token != request.Token {
		return staleFenceError("attempt fence token does not match current authority")
	}
	if attempt.Fence.ExpiresAt.IsZero() {
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET fence_expires_at = NULL
		WHERE id = $1 AND is_current AND fence_token = $2
	`, attempt.ID, request.Token)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return staleFenceError("attempt fence release lost authority")
	}
	return tx.Commit()
}

func (factory *agentRunAttemptsFactory) Transition(
	ctx context.Context,
	request checkpoint.TransitionAttemptRequest,
) (checkpoint.Attempt, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.Attempt{}, err
	}
	if request.State == checkpoint.AttemptInterrupted {
		return checkpoint.Attempt{}, fmt.Errorf(
			"checkpoint: interrupted transitions require a typed interruption reason",
		)
	}
	if request.State.Terminal() {
		return checkpoint.Attempt{}, fmt.Errorf(
			"checkpoint: terminal transitions require terminal authority",
		)
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.Attempt{}, err
	}
	attempt, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return checkpoint.Attempt{}, currentAttemptError(err)
	}
	if attempt.ExecutionAttempt != request.ExecutionAttempt {
		return checkpoint.Attempt{}, staleFenceError("execution attempt is no longer current")
	}
	if err := requireAttemptFence(ctx, tx, attempt, request.Fence); err != nil {
		return checkpoint.Attempt{}, err
	}
	if attempt.State == request.State {
		if err := tx.Commit(); err != nil {
			return checkpoint.Attempt{}, err
		}
		return attempt, nil
	}
	if attempt.State != request.ExpectedState {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: execution attempt state is %q, expected %q",
			checkpoint.ErrConflict, attempt.State, request.ExpectedState,
		)
	}
	updated, err := scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		UPDATE agent_run_attempts AS a
		SET state = $3
		FROM agent_run_checkpoint_heads AS h
		WHERE a.id = $1
		  AND a.state = $2
		  AND a.is_current
		  AND a.fence_token = $4::uuid
		  AND a.fence_expires_at > clock_timestamp()
		  AND h.id = a.head_id
		RETURNING `+agentRunAttemptColumns,
		attempt.ID, request.ExpectedState, request.State, request.Fence.Token,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Attempt{}, staleFenceError(
			"attempt fence expired before state transition",
		)
	}
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Attempt{}, err
	}
	return updated, nil
}

func (factory *agentRunAttemptsFactory) MarkInterrupted(
	ctx context.Context,
	request checkpoint.MarkAttemptInterruptedRequest,
) (checkpoint.Attempt, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.Attempt{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.Attempt{}, err
	}
	attempt, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return checkpoint.Attempt{}, currentAttemptError(err)
	}
	if attempt.ExecutionAttempt != request.ExecutionAttempt {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: interrupted execution attempt is no longer current",
			checkpoint.ErrConflict,
		)
	}
	if attempt.State == checkpoint.AttemptInterrupted {
		if attempt.InterruptionReason != request.Reason {
			return checkpoint.Attempt{}, fmt.Errorf(
				"%w: execution attempt was interrupted for %q, not %q",
				checkpoint.ErrConflict, attempt.InterruptionReason, request.Reason,
			)
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Attempt{}, err
		}
		return attempt, nil
	}
	if !attempt.State.CanTransitionTo(checkpoint.AttemptInterrupted) {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: execution attempt state %q cannot be interrupted",
			checkpoint.ErrConflict, attempt.State,
		)
	}
	interrupted, err := scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		UPDATE agent_run_attempts AS a
		SET state = 'interrupted',
			interruption_reason = $2,
			interrupted_at = clock_timestamp(),
			fence_expires_at = NULL
		FROM agent_run_checkpoint_heads AS h
		WHERE a.id = $1 AND a.state = $3 AND a.is_current AND h.id = a.head_id
		RETURNING `+agentRunAttemptColumns,
		attempt.ID, request.Reason, attempt.State,
	))
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Attempt{}, err
	}
	return interrupted, nil
}

func (factory *agentRunAttemptsFactory) BeginRecovery(
	ctx context.Context,
	request checkpoint.BeginRecoveryRequest,
) (checkpoint.Attempt, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.Attempt{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}

	existing, err := agentRunReplacementForUpdate(ctx, tx, head.id, request.SourceExecutionAttempt)
	if err == nil {
		if !sameRecoveryRequest(existing, request) {
			return checkpoint.Attempt{}, fmt.Errorf(
				"%w: interrupted source already allocated a different recovery attempt",
				checkpoint.ErrConflict,
			)
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Attempt{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Attempt{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.Attempt{}, err
	}
	source, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return checkpoint.Attempt{}, currentAttemptError(err)
	}
	if source.ExecutionAttempt != request.SourceExecutionAttempt ||
		source.State != checkpoint.AttemptInterrupted {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: recovery source is not the current interrupted attempt",
			checkpoint.ErrConflict,
		)
	}
	if source.InterruptionReason != request.Reason {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: recovery reason %q does not match interruption %q",
			checkpoint.ErrConflict, request.Reason, source.InterruptionReason,
		)
	}
	if source.Fence != nil && !source.Fence.ExpiresAt.IsZero() {
		now, err := checkpointDatabaseNow(ctx, tx)
		if err != nil {
			return checkpoint.Attempt{}, err
		}
		if source.Fence.ExpiresAt.After(now) {
			return checkpoint.Attempt{}, fmt.Errorf(
				"%w: interrupted source retains an unexpired attempt fence",
				checkpoint.ErrConflict,
			)
		}
	}
	nextAttempt := source.ExecutionAttempt + 1
	if nextAttempt > source.MaxTotalAttempts {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: %w: maximum is %d",
			checkpoint.ErrConflict, checkpoint.ErrAttemptLimit, source.MaxTotalAttempts,
		)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET is_current = FALSE
		WHERE id = $1 AND is_current AND state = 'interrupted'
	`, source.ID)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if updated != 1 {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: interrupted source lost current authority",
			checkpoint.ErrConflict,
		)
	}

	recovery, err := scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		INSERT INTO agent_run_attempts
			(head_id, attempt_number, max_total_attempts, state, is_current,
			 materialization_id, source_attempt_number, source_checkpoint_id,
			 source_checkpoint_generation, recovery_mode, source_interruption_reason)
		VALUES ($1, $2, $3, 'scheduling', TRUE, $4, $5, $6, $7, $8, $9)
		RETURNING
			id,
			$10::bigint, $10::bigint,
			$11::bigint, $12::text, $13::text,
			attempt_number, max_total_attempts, state, is_current,
			materialization_id, source_attempt_number, source_checkpoint_id,
			source_checkpoint_generation, recovery_mode, source_interruption_reason,
			interruption_reason, fence_token::text, fence_expires_at,
			created_at, updated_at, interrupted_at, terminal_at
	`, head.id, nextAttempt, source.MaxTotalAttempts, request.MaterializationID,
		request.SourceExecutionAttempt, checkpointAttemptNullableInt64(request.SourceCheckpointID),
		request.SourceCheckpointGeneration, request.Mode, request.Reason,
		checkpointWorkflowRunIDValue(request.Identity.WorkflowRunID),
		request.Identity.BuildID, request.Identity.PlanID, request.Identity.FunctionID,
	))
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Attempt{}, err
	}
	return recovery, nil
}

func (factory *agentRunAttemptsFactory) MarkManualReview(
	ctx context.Context,
	request checkpoint.MarkAttemptManualReviewRequest,
) (checkpoint.Attempt, error) {
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return checkpoint.Attempt{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	attempt, err := agentRunCurrentAttemptForUpdate(ctx, tx, head.id)
	if err != nil {
		return checkpoint.Attempt{}, currentAttemptError(err)
	}
	if attempt.ExecutionAttempt != request.ExecutionAttempt {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: manual-review execution attempt is no longer current",
			checkpoint.ErrConflict,
		)
	}
	if attempt.State == checkpoint.AttemptManualReview {
		if err := tx.Commit(); err != nil {
			return checkpoint.Attempt{}, err
		}
		return attempt, nil
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.Attempt{}, err
	}
	if attempt.State != request.ExpectedState {
		return checkpoint.Attempt{}, fmt.Errorf(
			"%w: execution attempt state is %q, expected %q",
			checkpoint.ErrConflict, attempt.State, request.ExpectedState,
		)
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	manual, err := scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		UPDATE agent_run_attempts AS a
		SET state = 'manual_review_required', terminal_at = $2, fence_expires_at = NULL
		FROM agent_run_checkpoint_heads AS h
		WHERE a.id = $1 AND a.state = $3 AND a.is_current AND h.id = a.head_id
		RETURNING `+agentRunAttemptColumns,
		attempt.ID, now, request.ExpectedState,
	))
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoint_heads
		SET active = FALSE, terminal_at = $2
		WHERE id = $1 AND active AND terminal_at IS NULL
	`, head.id, now)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Attempt{}, err
	}
	return manual, nil
}

func agentRunCurrentAttemptForUpdate(ctx context.Context, tx Tx, headID int64) (checkpoint.Attempt, error) {
	return agentRunCurrentAttempt(ctx, tx, headID, " FOR UPDATE OF a")
}

func agentRunCurrentAttempt(
	ctx context.Context,
	queryer checkpointQueryer,
	headID int64,
	locking string,
) (checkpoint.Attempt, error) {
	return scanAgentRunAttempt(queryer.QueryRowContext(ctx, `
		SELECT `+agentRunAttemptColumns+`
		FROM agent_run_attempts AS a
		JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
		WHERE a.head_id = $1 AND a.is_current`+locking,
		headID,
	))
}

func agentRunAttemptByNumberForUpdate(
	ctx context.Context,
	tx Tx,
	headID int64,
	attempt int,
) (checkpoint.Attempt, error) {
	return scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		SELECT `+agentRunAttemptColumns+`
		FROM agent_run_attempts AS a
		JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
		WHERE a.head_id = $1 AND a.attempt_number = $2
		FOR UPDATE OF a
	`, headID, attempt))
}

func agentRunReplacementForUpdate(
	ctx context.Context,
	tx Tx,
	headID int64,
	sourceAttempt int,
) (checkpoint.Attempt, error) {
	return scanAgentRunAttempt(tx.QueryRowContext(ctx, `
		SELECT `+agentRunAttemptColumns+`
		FROM agent_run_attempts AS a
		JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
		WHERE a.head_id = $1 AND a.source_attempt_number = $2
		FOR UPDATE OF a
	`, headID, sourceAttempt))
}

func scanAgentRunAttempt(scanner scannable) (checkpoint.Attempt, error) {
	var (
		attempt                  checkpoint.Attempt
		workflowRunID            sql.NullInt64
		sourceAttempt            sql.NullInt64
		sourceCheckpointID       sql.NullInt64
		mode                     sql.NullString
		sourceInterruptionReason sql.NullString
		interruptionReason       sql.NullString
		fenceToken               sql.NullString
		fenceExpiresAt           sql.NullTime
		interruptedAt            sql.NullTime
		terminalAt               sql.NullTime
	)
	err := scanner.Scan(
		&attempt.ID,
		&workflowRunID,
		&attempt.Identity.BuildID, &attempt.Identity.PlanID, &attempt.Identity.FunctionID,
		&attempt.ExecutionAttempt, &attempt.MaxTotalAttempts, &attempt.State, &attempt.Current,
		&attempt.MaterializationID, &sourceAttempt, &sourceCheckpointID,
		&attempt.SourceCheckpointGeneration, &mode, &sourceInterruptionReason,
		&interruptionReason, &fenceToken, &fenceExpiresAt,
		&attempt.CreatedAt, &attempt.UpdatedAt, &interruptedAt, &terminalAt,
	)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if workflowRunID.Valid {
		value := snapshot.WorkflowRunID(workflowRunID.Int64)
		attempt.Identity.WorkflowRunID = &value
	}
	if sourceAttempt.Valid {
		attempt.SourceExecutionAttempt = int(sourceAttempt.Int64)
	}
	if sourceCheckpointID.Valid {
		value := sourceCheckpointID.Int64
		attempt.SourceCheckpointID = &value
	}
	if mode.Valid {
		attempt.Mode = checkpoint.FallbackMode(mode.String)
	}
	if sourceInterruptionReason.Valid {
		attempt.SourceInterruptionReason = checkpoint.InterruptionReason(sourceInterruptionReason.String)
	}
	if interruptionReason.Valid {
		attempt.InterruptionReason = checkpoint.InterruptionReason(interruptionReason.String)
	}
	if fenceToken.Valid {
		attempt.Fence = &checkpoint.AttemptFence{
			FenceClaim: checkpoint.FenceClaim{
				ExecutionAttempt: attempt.ExecutionAttempt,
				Token:            fenceToken.String,
			},
		}
		if fenceExpiresAt.Valid {
			attempt.Fence.ExpiresAt = fenceExpiresAt.Time
		}
	}
	if interruptedAt.Valid {
		value := interruptedAt.Time
		attempt.InterruptedAt = &value
	}
	if terminalAt.Valid {
		value := terminalAt.Time
		attempt.TerminalAt = &value
	}
	return attempt, nil
}

func requireAttemptFence(
	ctx context.Context,
	tx Tx,
	attempt checkpoint.Attempt,
	claim checkpoint.FenceClaim,
) error {
	if attempt.ExecutionAttempt != claim.ExecutionAttempt ||
		attempt.Fence == nil || attempt.Fence.Token != claim.Token {
		return staleFenceError("attempt fence token does not match current authority")
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return err
	}
	if attempt.Fence.ExpiresAt.IsZero() || !attempt.Fence.ExpiresAt.After(now) {
		return staleFenceError("attempt fence was released or expired")
	}
	return nil
}

func attemptCanOwnFence(state checkpoint.AttemptState) bool {
	switch state {
	case checkpoint.AttemptScheduling, checkpoint.AttemptMaterializing,
		checkpoint.AttemptRunning, checkpoint.AttemptFinalizing:
		return true
	default:
		return false
	}
}

func sameRecoveryRequest(attempt checkpoint.Attempt, request checkpoint.BeginRecoveryRequest) bool {
	if attempt.SourceExecutionAttempt != request.SourceExecutionAttempt ||
		attempt.SourceCheckpointGeneration != request.SourceCheckpointGeneration ||
		attempt.Mode != request.Mode ||
		attempt.SourceInterruptionReason != request.Reason ||
		attempt.MaterializationID != request.MaterializationID {
		return false
	}
	if attempt.SourceCheckpointID == nil || request.SourceCheckpointID == nil {
		return attempt.SourceCheckpointID == nil && request.SourceCheckpointID == nil
	}
	return *attempt.SourceCheckpointID == *request.SourceCheckpointID
}

func checkpointAttemptNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func currentAttemptError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: execution identity has no current attempt", checkpoint.ErrConflict)
	}
	return err
}

func staleFenceError(reason string) error {
	return fmt.Errorf("%w: %w: %s", checkpoint.ErrConflict, checkpoint.ErrStaleFence, reason)
}
