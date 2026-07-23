package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

type AgentWorkflowWaitsFactory interface {
	workflowwait.Store
}

func NewAgentWorkflowWaitsFactory(conn DbConn, bindingRetention ...time.Duration) AgentWorkflowWaitsFactory {
	retention := snapshot.DefaultBindingRetention
	if len(bindingRetention) > 0 {
		retention = bindingRetention[0]
	}
	return &agentWorkflowWaitsFactory{conn: conn, bindingRetention: retention}
}

type agentWorkflowWaitsFactory struct {
	conn             DbConn
	bindingRetention time.Duration
}

const agentWorkflowWaitColumns = `
	w.id, w.team_id, w.workflow_run_id, w.build_id_evidence, w.plan_id, w.attempt, w.output_name,
	w.question_name, question.id, question.type_name, question.type_version, question.digest,
	w.expected_type_name, w.expected_type_version, w.deadline, w.timeout_policy,
	default_value.id, default_value.type_name, default_value.type_version, default_value.digest,
	w.workflow_port, w.workflow_definition_id, w.status,
	answer.id, answer.type_name, answer.type_version, answer.digest,
	w.resolved_by, w.resolved_by_display_name, w.resolution_source,
	w.created_at, w.updated_at, w.resolved_at`

const agentWorkflowWaitJoins = `
	FROM agent_workflow_waits w
	JOIN agent_snapshots question ON question.id = w.question_snapshot_id
	LEFT JOIN agent_snapshots default_value ON default_value.id = w.default_snapshot_id
	LEFT JOIN agent_snapshots answer ON answer.id = w.answer_snapshot_id`

func (factory *agentWorkflowWaitsFactory) CreateOrGet(
	ctx context.Context,
	request workflowwait.CreateRequest,
) (workflowwait.Wait, bool, error) {
	// Snapshot identity and type are authorization claims, not configuration
	// diagnostics.  Reject a forged/mismatched ref with the same opaque result
	// as a snapshot the team cannot access.
	if request.Question.Type != snapshot.TypeRef("question/v1") ||
		(request.Default != nil && request.Default.Type != request.ExpectedType) {
		return workflowwait.Wait{}, false, workflowwait.ErrUnavailable
	}
	if err := request.Validate(); err != nil {
		return workflowwait.Wait{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	defer Rollback(tx)

	runDefinitionID, err := authorizeWorkflowWaitExecution(ctx, tx, request.Key)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	if request.WorkflowDefinitionID != 0 && request.WorkflowDefinitionID != runDefinitionID {
		return workflowwait.Wait{}, false, workflowwait.ErrUnavailable
	}
	if err := authorizeWorkflowWaitSnapshot(ctx, tx, request.Key.TeamID, request.Question, snapshot.TypeRef("question/v1")); err != nil {
		return workflowwait.Wait{}, false, err
	}
	if request.Default != nil {
		if err := authorizeWorkflowWaitSnapshot(ctx, tx, request.Key.TeamID, *request.Default, request.ExpectedType); err != nil {
			return workflowwait.Wait{}, false, err
		}
	}
	expectedName, expectedVersion, err := splitSnapshotType(request.ExpectedType)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_waits
			(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt, output_name,
			 question_name, question_snapshot_id, expected_type_name, expected_type_version,
			 deadline, timeout_policy, default_snapshot_id, workflow_port, workflow_definition_id)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (build_id_evidence, plan_id, attempt, output_name) DO NOTHING
	`, request.Key.TeamID, int64(request.Key.WorkflowRunID), request.Key.BuildID, request.Key.PlanID,
		request.Key.Attempt, request.Key.OutputName, request.QuestionName, int64(request.Question.ID),
		expectedName, expectedVersion, request.Deadline.UTC(), string(request.TimeoutPolicy),
		optionalSnapshotRefID(request.Default), nullableNonblank(request.WorkflowPort),
		nullablePositiveInt(request.WorkflowDefinitionID))
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait, found, err := getWorkflowWaitByKey(ctx, tx, request.Key, true)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	if !found || !workflowWaitMatchesCreate(wait, request) {
		return workflowwait.Wait{}, false, workflowwait.ErrConflict
	}
	if err := retainWorkflowWaitInput(ctx, tx, wait, request.Question, "question"); err != nil {
		return workflowwait.Wait{}, false, err
	}
	if request.Default != nil {
		if err := retainWorkflowWaitInput(ctx, tx, wait, *request.Default, "default"); err != nil {
			return workflowwait.Wait{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return workflowwait.Wait{}, false, err
	}
	return wait, rows == 1, nil
}

func (factory *agentWorkflowWaitsFactory) Get(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	id workflowwait.ID,
) (workflowwait.Wait, bool, error) {
	if teamID <= 0 || runID.Validate() != nil || id.Validate() != nil {
		return workflowwait.Wait{}, false, fmt.Errorf("db: workflow wait scoped identity is invalid")
	}
	return scanWorkflowWait(factory.conn.QueryRowContext(ctx, `
		SELECT `+agentWorkflowWaitColumns+agentWorkflowWaitJoins+`
		WHERE w.id = $1 AND w.team_id = $2 AND w.workflow_run_id = $3
	`, int64(id), teamID, int64(runID)))
}

func (factory *agentWorkflowWaitsFactory) List(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
) ([]workflowwait.Wait, error) {
	if teamID <= 0 || runID.Validate() != nil {
		return nil, fmt.Errorf("db: workflow wait scoped identity is invalid")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT `+agentWorkflowWaitColumns+agentWorkflowWaitJoins+`
		WHERE w.team_id = $1 AND w.workflow_run_id = $2
		ORDER BY w.created_at, w.id
	`, teamID, int64(runID))
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	waits := make([]workflowwait.Wait, 0)
	for rows.Next() {
		wait, found, err := scanWorkflowWait(rows)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("db: workflow wait disappeared while listing")
		}
		waits = append(waits, wait)
	}
	return waits, rows.Err()
}

func (factory *agentWorkflowWaitsFactory) ReserveResolution(
	ctx context.Context,
	request workflowwait.ReserveResolutionRequest,
) (workflowwait.Wait, workflowwait.ResolutionIntent, bool, error) {
	if err := validateWorkflowWaitResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, false, err
	}
	defer Rollback(tx)
	_, _, runStatus, err := lockWorkflowWaitRun(ctx, tx, request.TeamID, request.WorkflowRunID)
	if err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, false, err
	}
	wait, found, err := getWorkflowWaitByID(ctx, tx, request.TeamID, request.WorkflowRunID, request.WaitID, true)
	if err != nil || !found {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, found, err
	}
	intent, reserved, err := getWorkflowWaitResolutionIntent(ctx, tx, wait.ID)
	if err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
	}
	if wait.Status == workflowwait.StatusResolved {
		if !reserved || intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
		}
		return wait, intent, true, nil
	}
	if wait.Status != workflowwait.StatusWaiting {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
	}
	if runStatus != AgentWorkflowRunStatusRunning {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
	}
	if reserved {
		if intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, workflowwait.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
		}
		return wait, intent, true, nil
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
	}
	if !now.Before(wait.Deadline) {
		wait, err = expireLockedWorkflowWait(ctx, tx, wait, now, factory.bindingRetention)
		if err != nil {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
		}
		if err := tx.Commit(); err != nil {
			return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
		}
		return wait, workflowwait.ResolutionIntent{}, true, workflowwait.ErrExpired
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_waits
		SET resolution_intent_answer = $2, resolution_intent_actor = $3,
		    resolution_intent_display_name = $4, resolution_intent_at = $5,
		    updated_at = $5
		WHERE id = $1 AND status = 'waiting'
		  AND resolution_intent_answer IS NULL
		  AND resolution_intent_actor IS NULL
		  AND resolution_intent_display_name IS NULL
		  AND resolution_intent_at IS NULL
	`, int64(wait.ID), request.AnswerValue, request.Actor, request.DisplayName, now)
	if err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		if err == nil {
			err = workflowwait.ErrConflict
		}
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
	}
	intent = workflowwait.ResolutionIntent{
		AnswerValue: request.AnswerValue,
		Actor:       request.Actor,
		DisplayName: request.DisplayName,
		ReservedAt:  now,
	}
	if err := tx.Commit(); err != nil {
		return workflowwait.Wait{}, workflowwait.ResolutionIntent{}, true, err
	}
	wait.UpdatedAt = now
	return wait, intent, true, nil
}

func (factory *agentWorkflowWaitsFactory) PendingResolutions(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	limit int,
) ([]workflowwait.PendingResolution, error) {
	if teamID <= 0 || runID.Validate() != nil || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("db: workflow wait pending resolution scope is invalid")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT id
		FROM agent_workflow_waits
		WHERE team_id = $1 AND workflow_run_id = $2 AND status = 'waiting'
		  AND resolution_intent_answer IS NOT NULL
		  AND resolution_intent_actor IS NOT NULL
		  AND resolution_intent_display_name IS NOT NULL
		  AND resolution_intent_at IS NOT NULL
		ORDER BY resolution_intent_at, id
		LIMIT $3
	`, teamID, int64(runID), limit)
	if err != nil {
		return nil, err
	}
	ids := make([]workflowwait.ID, 0, limit)
	for rows.Next() {
		var raw int64
		if err := rows.Scan(&raw); err != nil {
			Close(rows)
			return nil, err
		}
		id := workflowwait.ID(raw)
		if err := id.Validate(); err != nil {
			Close(rows)
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return nil, err
	}
	Close(rows)

	pending := make([]workflowwait.PendingResolution, 0, len(ids))
	for _, id := range ids {
		wait, found, err := factory.Get(ctx, teamID, runID, id)
		if err != nil {
			return nil, err
		}
		if !found || wait.Status != workflowwait.StatusWaiting {
			continue
		}
		intent, reserved, err := getWorkflowWaitResolutionIntent(ctx, factory.conn, id)
		if err != nil {
			return nil, err
		}
		if !reserved {
			continue
		}
		pending = append(pending, workflowwait.PendingResolution{Wait: wait, Intent: intent})
	}
	return pending, nil
}

func (factory *agentWorkflowWaitsFactory) Resolve(
	ctx context.Context,
	request workflowwait.ResolveRequest,
) (workflowwait.Wait, bool, error) {
	if err := validateWorkflowWaitResolutionIdentity(
		request.TeamID,
		request.WorkflowRunID,
		request.WaitID,
		request.AnswerValue,
		request.Actor,
		request.DisplayName,
	); err != nil || request.Answer.Validate() != nil || request.ReservedAt.IsZero() {
		return workflowwait.Wait{}, false, fmt.Errorf("db: workflow wait resolution is invalid")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	defer Rollback(tx)
	_, _, runStatus, err := lockWorkflowWaitRun(ctx, tx, request.TeamID, request.WorkflowRunID)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait, found, err := getWorkflowWaitByID(ctx, tx, request.TeamID, request.WorkflowRunID, request.WaitID, true)
	if err != nil || !found {
		return workflowwait.Wait{}, found, err
	}
	if wait.Status == workflowwait.StatusResolved && wait.Answer != nil &&
		*wait.Answer == request.Answer && wait.ResolvedBy == request.Actor {
		intent, reserved, intentErr := getWorkflowWaitResolutionIntent(ctx, tx, wait.ID)
		if intentErr != nil {
			return workflowwait.Wait{}, true, intentErr
		}
		if reserved && intent.AnswerValue == request.AnswerValue && intent.Actor == request.Actor &&
			intent.DisplayName == request.DisplayName && intent.ReservedAt.Equal(request.ReservedAt) {
			if err := tx.Commit(); err != nil {
				return workflowwait.Wait{}, true, err
			}
			return wait, true, nil
		}
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	if wait.Status != workflowwait.StatusWaiting {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	if runStatus != AgentWorkflowRunStatusRunning {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	intent, reserved, err := getWorkflowWaitResolutionIntent(ctx, tx, wait.ID)
	if err != nil {
		return workflowwait.Wait{}, true, err
	}
	if !reserved || intent.AnswerValue != request.AnswerValue || intent.Actor != request.Actor ||
		intent.DisplayName != request.DisplayName || !intent.ReservedAt.Equal(request.ReservedAt) {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return workflowwait.Wait{}, true, err
	}
	if err := authorizeWorkflowWaitSnapshot(ctx, tx, request.TeamID, request.Answer, wait.ExpectedType); err != nil {
		return workflowwait.Wait{}, true, err
	}
	if err := materializeWorkflowWaitAnswer(
		ctx, tx, wait, request.Answer, request.Actor, now, factory.bindingRetention,
	); err != nil {
		return workflowwait.Wait{}, true, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_waits
		SET status = 'resolved', answer_snapshot_id = $2, resolved_by = $3,
		    resolved_by_display_name = $4, resolution_source = 'human',
		    resolved_at = $5, updated_at = $5
		WHERE id = $1 AND status = 'waiting'
	`, int64(wait.ID), int64(request.Answer.ID), request.Actor, intent.DisplayName, now)
	if err != nil {
		return workflowwait.Wait{}, true, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		if err == nil {
			err = workflowwait.ErrConflict
		}
		return workflowwait.Wait{}, true, err
	}
	wait, found, err = getWorkflowWaitByID(ctx, tx, request.TeamID, request.WorkflowRunID, request.WaitID, false)
	if err != nil || !found {
		return workflowwait.Wait{}, true, err
	}
	if err := tx.Commit(); err != nil {
		return workflowwait.Wait{}, true, err
	}
	return wait, true, nil
}

func (factory *agentWorkflowWaitsFactory) Expire(
	ctx context.Context,
	key workflowwait.ExecutionKey,
	now time.Time,
) (workflowwait.Wait, bool, error) {
	if err := key.Validate(); err != nil || now.IsZero() {
		return workflowwait.Wait{}, false, fmt.Errorf("db: workflow wait expiry is invalid")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	defer Rollback(tx)
	_, _, runStatus, err := lockWorkflowWaitRun(ctx, tx, key.TeamID, key.WorkflowRunID)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait, found, err := getWorkflowWaitByKey(ctx, tx, key, true)
	if err != nil || !found {
		return workflowwait.Wait{}, found, err
	}
	if wait.Status == workflowwait.StatusWaiting && runStatus != AgentWorkflowRunStatusRunning {
		return workflowwait.Wait{}, true, workflowwait.ErrConflict
	}
	databaseTime, err := databaseNow(ctx, tx)
	if err != nil {
		return workflowwait.Wait{}, true, err
	}
	if wait.Status == workflowwait.StatusWaiting && !databaseTime.Before(wait.Deadline) {
		_, reserved, intentErr := getWorkflowWaitResolutionIntent(ctx, tx, wait.ID)
		if intentErr != nil {
			return workflowwait.Wait{}, true, intentErr
		}
		if !reserved {
			wait, err = expireLockedWorkflowWait(ctx, tx, wait, databaseTime, factory.bindingRetention)
			if err != nil {
				return workflowwait.Wait{}, true, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return workflowwait.Wait{}, true, err
	}
	return wait, true, nil
}

func (factory *agentWorkflowWaitsFactory) CancelRun(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	actor string,
	now time.Time,
) (int, error) {
	if teamID <= 0 || runID.Validate() != nil || strings.TrimSpace(actor) == "" || now.IsZero() {
		return 0, fmt.Errorf("db: workflow wait cancellation is invalid")
	}
	result, err := factory.conn.ExecContext(ctx, `
		UPDATE agent_workflow_waits w
		SET status = 'cancelled', resolved_by = $3, resolution_source = 'cancel',
		    resolution_intent_answer = NULL, resolution_intent_actor = NULL,
		    resolution_intent_display_name = NULL, resolution_intent_at = NULL,
		    resolved_at = clock.now, updated_at = clock.now
		FROM (SELECT now() AS now) clock
		WHERE w.team_id = $1 AND w.workflow_run_id = $2 AND w.status = 'waiting'
		  AND EXISTS (
		      SELECT 1 FROM agent_workflow_runs run
		      WHERE run.id = w.workflow_run_id AND run.team_id = w.team_id
		        AND run.status IN ('canceling', 'aborted')
		  )
	`, teamID, int64(runID), actor)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func authorizeWorkflowWaitExecution(ctx context.Context, tx Tx, key workflowwait.ExecutionKey) (int, error) {
	var definitionID int
	err := tx.QueryRowContext(ctx, `
		SELECT run.workflow_definition_id
		FROM agent_workflow_runs run
		JOIN pipeline_runs execution
		  ON execution.id = run.pipeline_run_id
		 AND execution.template_pipeline_id = run.template_pipeline_id
		 AND execution.instance_pipeline_id = run.instance_pipeline_id
		JOIN pipelines template ON template.id = run.template_pipeline_id AND template.team_id = run.team_id
		JOIN pipelines instance ON instance.id = run.instance_pipeline_id AND instance.team_id = run.team_id
		JOIN builds selected
		  ON selected.id = run.planned_build_id
		 AND selected.pipeline_id = instance.id
		 AND selected.team_id = run.team_id
		WHERE run.id = $1 AND run.team_id = $2 AND run.planned_build_id = $3
		  AND run.status = 'running'
		FOR UPDATE OF run
	`, int64(key.WorkflowRunID), key.TeamID, key.BuildID).Scan(&definitionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, workflowwait.ErrUnavailable
	}
	return definitionID, err
}

func authorizeWorkflowWaitSnapshot(
	ctx context.Context,
	tx Tx,
	teamID int,
	ref snapshot.SnapshotRef,
	expected snapshot.TypeRef,
) error {
	value, err := authorizedSnapshotByRef(ctx, tx, teamID, ref, true)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowwait.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if value.ID != ref.ID || value.Type != expected || value.Type != ref.Type || value.Digest != ref.Digest {
		return workflowwait.ErrUnavailable
	}
	return nil
}

func expireLockedWorkflowWait(
	ctx context.Context,
	tx Tx,
	wait workflowwait.Wait,
	now time.Time,
	bindingRetention time.Duration,
) (workflowwait.Wait, error) {
	var answerID any
	if wait.TimeoutPolicy == workflowwait.TimeoutDefault {
		if wait.Default == nil {
			return workflowwait.Wait{}, workflowwait.ErrConflict
		}
		if err := authorizeWorkflowWaitSnapshot(ctx, tx, wait.Key.TeamID, *wait.Default, wait.ExpectedType); err != nil {
			return workflowwait.Wait{}, err
		}
		if err := materializeWorkflowWaitAnswer(
			ctx, tx, wait, *wait.Default, "system:timeout", now, bindingRetention,
		); err != nil {
			return workflowwait.Wait{}, err
		}
		answerID = int64(wait.Default.ID)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_waits
		SET status = 'timed_out', answer_snapshot_id = $2, resolved_by = 'system:timeout',
		    resolution_source = 'timeout',
		    resolution_intent_answer = NULL, resolution_intent_actor = NULL,
		    resolution_intent_display_name = NULL, resolution_intent_at = NULL,
		    resolved_at = $3, updated_at = $3
		WHERE id = $1 AND status = 'waiting'
	`, int64(wait.ID), answerID, now)
	if err != nil {
		return workflowwait.Wait{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		if err == nil {
			err = workflowwait.ErrConflict
		}
		return workflowwait.Wait{}, err
	}
	updatedWait, found, err := getWorkflowWaitByID(ctx, tx, wait.Key.TeamID, wait.Key.WorkflowRunID, wait.ID, false)
	if err != nil || !found {
		return workflowwait.Wait{}, err
	}
	return updatedWait, nil
}

func materializeWorkflowWaitAnswer(
	ctx context.Context,
	tx Tx,
	wait workflowwait.Wait,
	answer snapshot.SnapshotRef,
	actor string,
	now time.Time,
	bindingRetention time.Duration,
) error {
	if now.IsZero() || bindingRetention <= 0 {
		return fmt.Errorf("db: workflow wait answer retention requires a time and positive duration")
	}
	teamName, definitionID, err := lockActiveWorkflowWaitRun(
		ctx,
		tx,
		wait.Key.TeamID,
		wait.Key.WorkflowRunID,
	)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
			 plan_id, attempt, step_kind, step_name, output_port,
			 workflow_definition_id, workflow_run_id)
		VALUES ($1, 'build', $2, $3, $4, $5, $6, $7, 'await_snapshot', $8, $8, $9, $10)
		ON CONFLICT (build_id, plan_id, attempt, output_port)
		WHERE occurrence_kind = 'build' DO NOTHING
	`, int64(answer.ID), wait.Key.BuildID, wait.Key.TeamID, teamName, actor,
		wait.Key.PlanID, wait.Key.Attempt, wait.Key.OutputName, definitionID, int64(wait.Key.WorkflowRunID))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	var productionID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM agent_snapshot_productions
		WHERE occurrence_kind = 'build' AND build_id = $1 AND plan_id = $2 AND attempt = $3
		  AND output_port = $4 AND snapshot_id = $5 AND team_id = $6 AND team_name = $7
		  AND created_by = $8 AND step_kind = 'await_snapshot' AND step_name = $4
		  AND workflow_definition_id = $9 AND workflow_run_id = $10
		FOR UPDATE
	`, wait.Key.BuildID, wait.Key.PlanID, wait.Key.Attempt, wait.Key.OutputName,
		int64(answer.ID), wait.Key.TeamID, teamName, actor, definitionID, int64(wait.Key.WorkflowRunID)).Scan(&productionID)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowwait.ErrConflict
	}
	if err != nil {
		return err
	}
	if rows == 1 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
			VALUES ($1, 0, $2, $3)
		`, productionID, wait.QuestionName, int64(wait.Question.ID))
		if err != nil {
			return err
		}
	}
	var lineageID int64
	err = tx.QueryRowContext(ctx, `
		SELECT input_snapshot_id FROM agent_snapshot_lineage
		WHERE production_id = $1 AND position = 0 AND input_port = $2
	`, productionID, wait.QuestionName).Scan(&lineageID)
	if err != nil || lineageID != int64(wait.Question.ID) {
		if err == nil {
			err = workflowwait.ErrConflict
		}
		return err
	}

	claimActor := fmt.Sprintf("workflow-run:%d:wait:%d", int64(wait.Key.WorkflowRunID), int64(wait.ID))
	retention := snapshot.RetentionSpec{
		Class: snapshot.RetentionClassRun, WorkflowRunID: &wait.Key.WorkflowRunID,
		Actor: claimActor, Reason: "active workflow wait answer",
	}
	if wait.WorkflowPort != "" {
		claimActor = fmt.Sprintf("workflow-run:%d:output:%s", int64(wait.Key.WorkflowRunID), wait.WorkflowPort)
		retention = snapshot.RetentionSpec{
			Class: snapshot.RetentionClassWorkflow,
			Actor: claimActor, Reason: "durable workflow-run output",
		}
		if err := bindWorkflowRunSnapshot(ctx, tx, wait.Key.WorkflowRunID, "output", wait.WorkflowPort, answer.ID); err != nil {
			return err
		}
	}
	if err := insertOrVerifyRetention(ctx, tx, wait.Key.TeamID, answer.ID, retention); err != nil {
		return err
	}
	if wait.WorkflowPort != "" {
		return nil
	}
	expiresAt := now.Add(bindingRetention)
	return insertOrVerifyRetention(ctx, tx, wait.Key.TeamID, answer.ID, snapshot.RetentionSpec{
		Class: snapshot.RetentionClassBinding, ExpiresAt: &expiresAt,
		Actor: claimActor, Reason: "workflow wait answer post-run grace",
	})
}

// lockActiveWorkflowWaitRun serializes answer acceptance/materialization with
// workflow-run terminalization. Whichever transaction wins determines the
// durable outcome: an answer committed first is cleaned up by Finalize, while
// terminalization committed first prevents a new non-expiring run claim.
func lockActiveWorkflowWaitRun(
	ctx context.Context,
	tx Tx,
	teamID int,
	runID snapshot.WorkflowRunID,
) (string, int, error) {
	teamName, definitionID, status, err := lockWorkflowWaitRun(ctx, tx, teamID, runID)
	if err != nil {
		return "", 0, err
	}
	if status != AgentWorkflowRunStatusRunning {
		return "", 0, workflowwait.ErrConflict
	}
	return teamName, definitionID, nil
}

func lockWorkflowWaitRun(
	ctx context.Context,
	tx Tx,
	teamID int,
	runID snapshot.WorkflowRunID,
) (string, int, AgentWorkflowRunStatus, error) {
	var teamName string
	var definitionID int
	var status AgentWorkflowRunStatus
	err := tx.QueryRowContext(ctx, `
		SELECT team_name, workflow_definition_id, status
		FROM agent_workflow_runs
		WHERE id = $1 AND team_id = $2
		FOR UPDATE
	`, int64(runID), teamID).Scan(&teamName, &definitionID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", workflowwait.ErrUnavailable
	}
	if err != nil {
		return "", 0, "", err
	}
	return teamName, definitionID, status, nil
}

func retainWorkflowWaitInput(
	ctx context.Context,
	tx Tx,
	wait workflowwait.Wait,
	ref snapshot.SnapshotRef,
	kind string,
) error {
	return insertOrVerifyRetention(ctx, tx, wait.Key.TeamID, ref.ID, snapshot.RetentionSpec{
		Class:         snapshot.RetentionClassRun,
		WorkflowRunID: &wait.Key.WorkflowRunID,
		Actor:         fmt.Sprintf("workflow-run:%d:wait:%d:%s", int64(wait.Key.WorkflowRunID), int64(wait.ID), kind),
		Reason:        "active workflow wait " + kind,
	})
}

func getWorkflowWaitByKey(
	ctx context.Context,
	queryer snapshotQueryer,
	key workflowwait.ExecutionKey,
	lock bool,
) (workflowwait.Wait, bool, error) {
	query := `SELECT ` + agentWorkflowWaitColumns + agentWorkflowWaitJoins + `
		WHERE w.team_id = $1 AND w.workflow_run_id = $2 AND w.build_id_evidence = $3
		  AND w.plan_id = $4 AND w.attempt = $5 AND w.output_name = $6`
	if lock {
		query += ` FOR UPDATE OF w`
	}
	return scanWorkflowWait(queryer.QueryRowContext(ctx, query, key.TeamID, int64(key.WorkflowRunID),
		key.BuildID, key.PlanID, key.Attempt, key.OutputName))
}

func getWorkflowWaitByID(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	runID snapshot.WorkflowRunID,
	id workflowwait.ID,
	lock bool,
) (workflowwait.Wait, bool, error) {
	query := `SELECT ` + agentWorkflowWaitColumns + agentWorkflowWaitJoins + `
		WHERE w.id = $1 AND w.team_id = $2 AND w.workflow_run_id = $3`
	if lock {
		query += ` FOR UPDATE OF w`
	}
	return scanWorkflowWait(queryer.QueryRowContext(ctx, query, int64(id), teamID, int64(runID)))
}

func scanWorkflowWait(row scannable) (workflowwait.Wait, bool, error) {
	var (
		wait                                                         workflowwait.Wait
		id, runID, buildID, questionID                               int64
		questionTypeName, questionDigest, expectedTypeName           string
		questionTypeVersion, expectedTypeVersion                     int
		defaultID, answerID                                          sql.NullInt64
		defaultTypeName, defaultDigest, answerTypeName, answerDigest sql.NullString
		defaultTypeVersion, answerTypeVersion                        sql.NullInt64
		workflowPort                                                 sql.NullString
		resolvedAt                                                   sql.NullTime
		workflowDefinitionID                                         sql.NullInt64
		status, timeoutPolicy                                        string
	)
	err := row.Scan(
		&id, &wait.Key.TeamID, &runID, &buildID, &wait.Key.PlanID, &wait.Key.Attempt, &wait.Key.OutputName,
		&wait.QuestionName, &questionID, &questionTypeName, &questionTypeVersion, &questionDigest,
		&expectedTypeName, &expectedTypeVersion, &wait.Deadline, &timeoutPolicy,
		&defaultID, &defaultTypeName, &defaultTypeVersion, &defaultDigest,
		&workflowPort, &workflowDefinitionID, &status,
		&answerID, &answerTypeName, &answerTypeVersion, &answerDigest,
		&wait.ResolvedBy, &wait.ResolvedByDisplayName, &wait.ResolutionSource,
		&wait.CreatedAt, &wait.UpdatedAt, &resolvedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowwait.Wait{}, false, nil
	}
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait.ID = workflowwait.ID(id)
	wait.Key.WorkflowRunID = snapshot.WorkflowRunID(runID)
	wait.Key.BuildID = buildID
	questionType, err := joinSnapshotType(questionTypeName, questionTypeVersion)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait.Question = snapshot.SnapshotRef{ID: snapshot.SnapshotID(questionID), Type: questionType, Digest: snapshot.Digest(questionDigest)}
	wait.ExpectedType, err = joinSnapshotType(expectedTypeName, expectedTypeVersion)
	if err != nil {
		return workflowwait.Wait{}, false, err
	}
	wait.TimeoutPolicy = workflowwait.TimeoutPolicy(timeoutPolicy)
	wait.Status = workflowwait.Status(status)
	if workflowPort.Valid {
		wait.WorkflowPort = workflowPort.String
	}
	if workflowDefinitionID.Valid {
		wait.WorkflowDefinitionID = int(workflowDefinitionID.Int64)
	}
	if defaultID.Valid {
		typ, err := joinSnapshotType(defaultTypeName.String, int(defaultTypeVersion.Int64))
		if err != nil {
			return workflowwait.Wait{}, false, err
		}
		value := snapshot.SnapshotRef{ID: snapshot.SnapshotID(defaultID.Int64), Type: typ, Digest: snapshot.Digest(defaultDigest.String)}
		wait.Default = &value
	}
	if answerID.Valid {
		typ, err := joinSnapshotType(answerTypeName.String, int(answerTypeVersion.Int64))
		if err != nil {
			return workflowwait.Wait{}, false, err
		}
		value := snapshot.SnapshotRef{ID: snapshot.SnapshotID(answerID.Int64), Type: typ, Digest: snapshot.Digest(answerDigest.String)}
		wait.Answer = &value
	}
	if resolvedAt.Valid {
		parsed := resolvedAt.Time
		wait.ResolvedAt = &parsed
	}
	if err := wait.Validate(); err != nil {
		return workflowwait.Wait{}, false, err
	}
	return wait, true, nil
}

func getWorkflowWaitResolutionIntent(
	ctx context.Context,
	queryer snapshotQueryer,
	waitID workflowwait.ID,
) (workflowwait.ResolutionIntent, bool, error) {
	var answer, actor, displayName sql.NullString
	var reservedAt sql.NullTime
	err := queryer.QueryRowContext(ctx, `
		SELECT resolution_intent_answer, resolution_intent_actor,
		       resolution_intent_display_name, resolution_intent_at
		FROM agent_workflow_waits
		WHERE id = $1
	`, int64(waitID)).Scan(&answer, &actor, &displayName, &reservedAt)
	if err != nil {
		return workflowwait.ResolutionIntent{}, false, err
	}
	present := answer.Valid || actor.Valid || displayName.Valid || reservedAt.Valid
	if !present {
		return workflowwait.ResolutionIntent{}, false, nil
	}
	if !answer.Valid || !actor.Valid || !displayName.Valid || !reservedAt.Valid {
		return workflowwait.ResolutionIntent{}, false, fmt.Errorf("db: workflow wait resolution intent is incomplete")
	}
	return workflowwait.ResolutionIntent{
		AnswerValue: answer.String,
		Actor:       actor.String,
		DisplayName: displayName.String,
		ReservedAt:  reservedAt.Time,
	}, true, nil
}

func validateWorkflowWaitResolutionIdentity(
	teamID int,
	runID snapshot.WorkflowRunID,
	waitID workflowwait.ID,
	answerValue string,
	actor string,
	displayName string,
) error {
	if teamID <= 0 || runID.Validate() != nil || waitID.Validate() != nil {
		return fmt.Errorf("db: workflow wait resolution scope is invalid")
	}
	for label, value := range map[string]string{
		"answer":       answerValue,
		"actor":        actor,
		"display name": displayName,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 16<<10 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("db: workflow wait resolution %s is invalid", label)
		}
	}
	return nil
}

func workflowWaitMatchesCreate(wait workflowwait.Wait, request workflowwait.CreateRequest) bool {
	return wait.Key == request.Key && wait.QuestionName == request.QuestionName && wait.Question == request.Question &&
		wait.ExpectedType == request.ExpectedType && wait.TimeoutPolicy == request.TimeoutPolicy &&
		equalWorkflowWaitRef(wait.Default, request.Default) && wait.WorkflowPort == request.WorkflowPort &&
		wait.WorkflowDefinitionID == request.WorkflowDefinitionID
}

func equalWorkflowWaitRef(left, right *snapshot.SnapshotRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalSnapshotRefID(ref *snapshot.SnapshotRef) any {
	if ref == nil {
		return nil
	}
	return int64(ref.ID)
}

func nullableNonblank(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
