package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

const agentPRBindingColumns = `
	id, team_id, provider, repository, external_id, url, source_ref, target_ref,
	originating_workflow_run_id, originating_publication_occurrence_id,
	monitor_workflow_definition_id, monitor_workflow_version, pipeline_id,
	acknowledged_cursor, last_observation_snapshot_id,
	last_acknowledged_action_digest, last_acknowledged_workflow_run_id,
	last_reconciled_source_sha, last_reconciled_target_sha, last_reconciled_at,
	lifecycle_state, attention_reason, paused, operator_terminated,
	observation_requested_at,
	active_base_revision, active_action_digest, active_observation_snapshot_id,
	active_cursor, active_source_sha, active_target_sha,
	active_reservation_token, active_reservation_expires_at,
	active_workflow_run_id, terminal_observation_snapshot_id, terminal_at,
	revision, created_at, updated_at`

var _ pullrequest.BindingStore = (*agentPRBindingsFactory)(nil)

type agentPRBindingsFactory struct {
	conn DbConn
}

func NewAgentPRBindingsFactory(conn DbConn) pullrequest.BindingStore {
	return &agentPRBindingsFactory{conn: conn}
}

func (factory *agentPRBindingsFactory) Create(
	ctx context.Context,
	request pullrequest.CreateBinding,
) (pullrequest.Binding, bool, error) {
	if ctx == nil {
		return pullrequest.Binding{}, false, fmt.Errorf("db: PR binding create requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, false, err
	}
	defer Rollback(tx)

	if err := validateAgentPRMonitorDefinition(
		ctx, tx, request.MonitorWorkflowDefinitionID, request.MonitorWorkflowVersion,
	); err != nil {
		return pullrequest.Binding{}, false, err
	}
	if err := validateAgentPRBindingOrigin(ctx, tx, request); err != nil {
		return pullrequest.Binding{}, false, err
	}
	if request.LastObservationSnapshotID > 0 {
		if err := validateAgentPRSnapshotOwner(ctx, tx, request.TeamID, request.LastObservationSnapshotID); err != nil {
			return pullrequest.Binding{}, false, err
		}
	}
	cursor, err := encodeAgentPRCursor(request.AcknowledgedCursor)
	if err != nil {
		return pullrequest.Binding{}, false, err
	}
	lastReconciledAt := normalizeAgentPRBindingTime(request.LastReconciledAt)
	var insertedID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_pr_bindings
			(team_id, provider, repository, external_id, url, source_ref, target_ref,
			 originating_workflow_run_id, originating_publication_occurrence_id,
			 monitor_workflow_definition_id, monitor_workflow_version,
			 acknowledged_cursor, last_observation_snapshot_id,
			 last_reconciled_source_sha, last_reconciled_target_sha, last_reconciled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16)
		ON CONFLICT (team_id, provider, repository, external_id) DO NOTHING
		RETURNING id
	`, request.TeamID, string(request.Locator.Provider), request.Locator.Repository,
		request.Locator.ExternalID, request.URL, request.SourceRef, request.TargetRef,
		nullablePositiveInt64(int64(request.OriginatingWorkflowRunID)),
		nullablePositiveInt64(request.OriginatingPublicationOccurrence),
		request.MonitorWorkflowDefinitionID, request.MonitorWorkflowVersion, cursor,
		nullablePositiveInt64(int64(request.LastObservationSnapshotID)),
		request.LastReconciledSourceSHA, request.LastReconciledTargetSHA,
		lastReconciledAt,
	).Scan(&insertedID)
	created := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return pullrequest.Binding{}, false, err
	}

	binding, found, err := findAgentPRBindingByExternal(
		ctx, tx, request.TeamID, request.Locator, true,
	)
	if err != nil {
		return pullrequest.Binding{}, false, err
	}
	if !found {
		return pullrequest.Binding{}, false, fmt.Errorf("db: inserted PR binding was not found")
	}
	if !sameAgentPRBindingCreate(binding, request) {
		return pullrequest.Binding{}, false, fmt.Errorf(
			"%w: provider locator already has different binding authority",
			pullrequest.ErrBindingConflict,
		)
	}
	if created && binding.ID != insertedID {
		return pullrequest.Binding{}, false, fmt.Errorf("db: inserted PR binding identity drifted")
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, false, err
	}
	return binding, created, nil
}

func (factory *agentPRBindingsFactory) Get(
	ctx context.Context,
	teamID int,
	bindingID int64,
) (pullrequest.Binding, bool, error) {
	if ctx == nil || teamID <= 0 || bindingID <= 0 {
		return pullrequest.Binding{}, false, fmt.Errorf("db: PR binding lookup requires context, team, and binding")
	}
	return findAgentPRBinding(ctx, factory.conn, teamID, bindingID, false)
}

func (factory *agentPRBindingsFactory) GetByExternal(
	ctx context.Context,
	teamID int,
	locator pullrequest.Locator,
) (pullrequest.Binding, bool, error) {
	if ctx == nil || teamID <= 0 {
		return pullrequest.Binding{}, false, fmt.Errorf("db: PR binding lookup requires context and team")
	}
	// A nonempty active-shaped locator is the only key persisted by this store.
	if locator.Provider != pullrequest.ProviderGitHub &&
		locator.Provider != pullrequest.ProviderAzureDevOps {
		return pullrequest.Binding{}, false, fmt.Errorf("db: unsupported PR binding provider")
	}
	if strings.TrimSpace(locator.Repository) == "" || strings.TrimSpace(locator.ExternalID) == "" {
		return pullrequest.Binding{}, false, fmt.Errorf("db: PR binding locator is incomplete")
	}
	return findAgentPRBindingByExternal(ctx, factory.conn, teamID, locator, false)
}

func (factory *agentPRBindingsFactory) ReserveLaunch(
	ctx context.Context,
	request pullrequest.ReserveLaunch,
) (pullrequest.LaunchReservation, bool, error) {
	if ctx == nil {
		return pullrequest.LaunchReservation{}, false, fmt.Errorf("db: PR reservation requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	defer Rollback(tx)

	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, request.TeamID, request.BindingID)
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	if !found {
		return pullrequest.LaunchReservation{}, false, pullrequest.ErrBindingNotFound
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	if binding.State.Terminal() {
		return pullrequest.LaunchReservation{}, false, pullrequest.ErrBindingImmutable
	}
	if binding.Paused || binding.OperatorTerminated ||
		binding.State == pullrequest.BindingAttentionRequired {
		return pullrequest.LaunchReservation{}, false, pullrequest.ErrBindingBusy
	}
	if binding.Active != nil {
		exactReplay := sameAgentPRReservationRequest(*binding.Active, request) &&
			binding.Active.BaseRevision == request.ExpectedRevision
		// Only the original, still-unattached claim may replay from its base
		// projection. Attachment or any intervening binding update makes that
		// projected source version stale.
		if exactReplay &&
			(binding.Active.WorkflowRunID != nil ||
				binding.Revision != binding.Active.BaseRevision+1) {
			return pullrequest.LaunchReservation{}, false, pullrequest.ErrStaleBindingRevision
		}
		if exactReplay && binding.Active.ExpiresAt.After(now) {
			if err := tx.Commit(); err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			return *binding.Active, true, nil
		}
		if binding.Active.WorkflowRunID != nil || binding.Active.ExpiresAt.After(now) {
			if err := tx.Commit(); err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			return pullrequest.LaunchReservation{}, false, nil
		}
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.LaunchReservation{}, false, pullrequest.ErrStaleBindingRevision
	}
	if err := validateAgentPRSnapshotOwner(
		ctx, tx, request.TeamID, request.ObservationSnapshotID,
	); err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	token, err := newAgentPRReservationToken()
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	cursor, err := encodeAgentPRCursor(request.Cursor)
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	expiresAt := normalizeAgentPRBindingTime(now.Add(request.ExpiresIn))
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET active_base_revision=$4, active_action_digest=$5,
		    active_observation_snapshot_id=$6, active_cursor=$7::jsonb,
		    active_source_sha=$8, active_target_sha=$9,
		    active_reservation_token=$10, active_reservation_expires_at=$11,
		    active_workflow_run_id=NULL, revision=revision+1, updated_at=$12
		WHERE team_id=$1 AND id=$2 AND revision=$3
	`, request.TeamID, request.BindingID, request.ExpectedRevision,
		request.ExpectedRevision, request.ActionDigest, int64(request.ObservationSnapshotID),
		cursor, request.SourceSHA, request.TargetSHA, token, expiresAt, now)
	if err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	reservation := pullrequest.LaunchReservation{
		BindingID: request.BindingID, BindingRevision: request.ExpectedRevision + 1,
		BaseRevision: request.ExpectedRevision, ActionDigest: request.ActionDigest,
		ObservationSnapshotID: request.ObservationSnapshotID, Cursor: request.Cursor,
		SourceSHA: request.SourceSHA, TargetSHA: request.TargetSHA,
		Token: token, ExpiresAt: expiresAt,
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.LaunchReservation{}, false, err
	}
	return reservation, true, nil
}

func (factory *agentPRBindingsFactory) AttachRun(
	ctx context.Context,
	request pullrequest.AttachRun,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf("db: PR run attachment requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, err := attachAgentPRBindingRun(ctx, tx, request)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return binding, nil
}

// attachAgentPRBindingRun is intentionally package-private so workflow-run
// creation can insert the run and attach it under the same transaction.
func attachAgentPRBindingRun(
	ctx context.Context,
	tx Tx,
	request pullrequest.AttachRun,
) (pullrequest.Binding, error) {
	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, request.TeamID, request.BindingID)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if binding.Active != nil && binding.Active.WorkflowRunID != nil &&
		*binding.Active.WorkflowRunID == request.WorkflowRunID &&
		binding.Active.ActionDigest == request.ActionDigest &&
		binding.Active.Token == request.ReservationToken {
		return binding, nil
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	if binding.State.Terminal() {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if binding.Active == nil ||
		binding.Active.ActionDigest != request.ActionDigest ||
		binding.Active.Token != request.ReservationToken ||
		binding.Active.WorkflowRunID != nil {
		return pullrequest.Binding{}, pullrequest.ErrReservationMismatch
	}
	if !binding.Active.ExpiresAt.After(now) {
		return pullrequest.Binding{}, fmt.Errorf("%w: reservation expired before run attachment", pullrequest.ErrReservationMismatch)
	}
	var (
		runTeamID       int
		definitionID    int
		definitionKind  string
		originKind      string
		originReference string
		status          string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT team_id, workflow_definition_id, definition_kind,
		       origin_kind, origin_reference, status
		FROM agent_workflow_runs
		WHERE id=$1
		FOR UPDATE
	`, int64(request.WorkflowRunID)).Scan(
		&runTeamID, &definitionID, &definitionKind,
		&originKind, &originReference, &status,
	)
	if err != nil {
		return pullrequest.Binding{}, fmt.Errorf("%w: workflow run: %v", pullrequest.ErrReservationMismatch, err)
	}
	if runTeamID != request.TeamID ||
		definitionID != binding.MonitorWorkflowDefinitionID ||
		definitionKind != "workflow" ||
		originKind != "pr-monitor" ||
		originReference != strconv.FormatInt(request.BindingID, 10) ||
		!agentPRRunStatusNonterminal(status) {
		return pullrequest.Binding{}, fmt.Errorf("%w: workflow run authority is not exact", pullrequest.ErrReservationMismatch)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET active_workflow_run_id=$4, revision=revision+1, updated_at=$5
		WHERE team_id=$1 AND id=$2 AND revision=$3
	`, request.TeamID, request.BindingID, request.ExpectedRevision,
		int64(request.WorkflowRunID), now)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, found, err := findAgentPRBinding(ctx, tx, request.TeamID, request.BindingID, false)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) ReleaseLaunch(
	ctx context.Context,
	request pullrequest.ReleaseLaunch,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf("db: PR launch release requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, found, err := lockAgentPRBindingForUpdate(
		ctx, tx, request.TeamID, request.BindingID,
	)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	if binding.State.Terminal() {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if !sameAgentPRReleaseIdentity(binding.Active, request) {
		return pullrequest.Binding{}, pullrequest.ErrReservationMismatch
	}
	if request.WorkflowRunID != nil {
		if err := validateAgentPRRunUnsuccessfulTerminal(
			ctx, tx, request.TeamID, request.BindingID,
			binding.MonitorWorkflowDefinitionID, *request.WorkflowRunID,
		); err != nil {
			return pullrequest.Binding{}, err
		}
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET active_base_revision=NULL, active_action_digest=NULL,
		    active_observation_snapshot_id=NULL, active_cursor=NULL,
		    active_source_sha=NULL, active_target_sha=NULL,
		    active_reservation_token=NULL, active_reservation_expires_at=NULL,
		    active_workflow_run_id=NULL,
		    revision=revision+1, updated_at=$7
		WHERE team_id=$1 AND id=$2 AND revision=$3
		  AND active_action_digest=$4
		  AND active_reservation_token=$5
		  AND active_workflow_run_id IS NOT DISTINCT FROM $6::bigint
	`, request.TeamID, request.BindingID, request.ExpectedRevision,
		request.ActionDigest, request.ReservationToken,
		nullableAgentPRWorkflowRunID(request.WorkflowRunID), now)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, found, err := findAgentPRBinding(
		ctx, tx, request.TeamID, request.BindingID, false,
	)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) AcknowledgeAction(
	ctx context.Context,
	request pullrequest.AcknowledgeAction,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf("db: PR acknowledgement requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, request.TeamID, request.BindingID)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if sameAgentPRAcknowledgement(binding, request) {
		if err := tx.Commit(); err != nil {
			return pullrequest.Binding{}, err
		}
		return binding, nil
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	if binding.State.Terminal() {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if err := validateExactAgentPRAttachedAction(binding, request); err != nil {
		return pullrequest.Binding{}, err
	}
	if err := validateAgentPRRunSucceeded(
		ctx, tx, request.TeamID, request.BindingID,
		binding.MonitorWorkflowDefinitionID, request.WorkflowRunID,
	); err != nil {
		return pullrequest.Binding{}, err
	}
	cursor, err := encodeAgentPRCursor(request.Cursor)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET acknowledged_cursor=$4::jsonb,
		    last_observation_snapshot_id=$5,
		    last_acknowledged_action_digest=$6,
		    last_acknowledged_workflow_run_id=$7,
		    last_reconciled_source_sha=$8,
		    last_reconciled_target_sha=$9,
		    last_reconciled_at=$10,
		    lifecycle_state='active', attention_reason='',
		    active_base_revision=NULL, active_action_digest=NULL,
		    active_observation_snapshot_id=NULL, active_cursor=NULL,
		    active_source_sha=NULL, active_target_sha=NULL,
		    active_reservation_token=NULL, active_reservation_expires_at=NULL,
		    active_workflow_run_id=NULL,
		    revision=revision+1, updated_at=$10
		WHERE team_id=$1 AND id=$2 AND revision=$3
	`, request.TeamID, request.BindingID, request.ExpectedRevision, cursor,
		int64(request.ObservationSnapshotID), request.ActionDigest,
		int64(request.WorkflowRunID), request.SourceSHA, request.TargetSHA, now)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, found, err := findAgentPRBinding(ctx, tx, request.TeamID, request.BindingID, false)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) MarkAttention(
	ctx context.Context,
	teamID int,
	bindingID int64,
	reason string,
) (pullrequest.Binding, error) {
	if ctx == nil || teamID <= 0 || bindingID <= 0 ||
		strings.TrimSpace(reason) != reason || reason == "" ||
		len([]byte(reason)) > 2048 {
		return pullrequest.Binding{}, fmt.Errorf("db: invalid PR binding attention request")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, teamID, bindingID)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if binding.State.Terminal() {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if binding.State == pullrequest.BindingAttentionRequired &&
		binding.AttentionReason == reason && binding.Paused {
		if err := tx.Commit(); err != nil {
			return pullrequest.Binding{}, err
		}
		return binding, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET lifecycle_state='attention_required', attention_reason=$3,
		    paused=true, revision=revision+1, updated_at=$4
		WHERE team_id=$1 AND id=$2 AND revision=$5
	`, teamID, bindingID, reason, now, binding.Revision)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, _, err := findAgentPRBinding(ctx, tx, teamID, bindingID, false)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) MarkTerminal(
	ctx context.Context,
	request pullrequest.TerminalBinding,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf("db: PR terminal transition requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, request.TeamID, request.BindingID)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if binding.State.Terminal() {
		if sameAgentPRTerminal(binding, request) {
			if err := tx.Commit(); err != nil {
				return pullrequest.Binding{}, err
			}
			return binding, nil
		}
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	ack := pullrequest.AcknowledgeAction{
		TeamID: request.TeamID, BindingID: request.BindingID,
		ExpectedRevision: request.ExpectedRevision, ActionDigest: request.ActionDigest,
		ReservationToken: request.ReservationToken, WorkflowRunID: request.WorkflowRunID,
		ObservationSnapshotID: request.ObservationSnapshotID, Cursor: request.Cursor,
		SourceSHA: request.SourceSHA, TargetSHA: request.TargetSHA,
	}
	if err := validateExactAgentPRAttachedAction(binding, ack); err != nil {
		return pullrequest.Binding{}, err
	}
	if err := validateAgentPRRunSucceeded(
		ctx, tx, request.TeamID, request.BindingID,
		binding.MonitorWorkflowDefinitionID, request.WorkflowRunID,
	); err != nil {
		return pullrequest.Binding{}, err
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	cursor, err := encodeAgentPRCursor(request.Cursor)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_pr_bindings
		SET acknowledged_cursor=$4::jsonb,
		    last_observation_snapshot_id=$5,
		    last_acknowledged_action_digest=$6,
		    last_acknowledged_workflow_run_id=$7,
		    last_reconciled_source_sha=$8,
		    last_reconciled_target_sha=$9,
		    last_reconciled_at=$10,
		    lifecycle_state=$11, attention_reason='',
		    terminal_observation_snapshot_id=$5, terminal_at=$10,
		    active_base_revision=NULL, active_action_digest=NULL,
		    active_observation_snapshot_id=NULL, active_cursor=NULL,
		    active_source_sha=NULL, active_target_sha=NULL,
		    active_reservation_token=NULL, active_reservation_expires_at=NULL,
		    active_workflow_run_id=NULL,
		    revision=revision+1, updated_at=$10
		WHERE team_id=$1 AND id=$2 AND revision=$3
	`, request.TeamID, request.BindingID, request.ExpectedRevision, cursor,
		int64(request.ObservationSnapshotID), request.ActionDigest,
		int64(request.WorkflowRunID), request.SourceSHA, request.TargetSHA,
		now, string(request.State))
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, _, err := findAgentPRBinding(ctx, tx, request.TeamID, request.BindingID, false)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) RequestObservation(
	ctx context.Context,
	request pullrequest.OperatorRequest,
) (pullrequest.Binding, error) {
	return factory.applyOperatorRequest(ctx, request, "refresh")
}

func (factory *agentPRBindingsFactory) Pause(
	ctx context.Context,
	request pullrequest.OperatorRequest,
) (pullrequest.Binding, error) {
	return factory.applyOperatorRequest(ctx, request, "pause")
}

func (factory *agentPRBindingsFactory) Resume(
	ctx context.Context,
	request pullrequest.OperatorRequest,
) (pullrequest.Binding, error) {
	return factory.applyOperatorRequest(ctx, request, "resume")
}

func (factory *agentPRBindingsFactory) Terminate(
	ctx context.Context,
	request pullrequest.OperatorRequest,
) (pullrequest.Binding, error) {
	return factory.applyOperatorRequest(ctx, request, "terminate")
}

func (factory *agentPRBindingsFactory) applyOperatorRequest(
	ctx context.Context,
	request pullrequest.OperatorRequest,
	operation string,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf("db: PR operator request requires context")
	}
	if err := request.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	defer Rollback(tx)
	binding, found, err := lockAgentPRBindingForUpdate(ctx, tx, request.TeamID, request.BindingID)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if !found {
		return pullrequest.Binding{}, pullrequest.ErrBindingNotFound
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	if binding.State.Terminal() {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if operation == "resume" && binding.OperatorTerminated {
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	now, err := agentPRBindingDatabaseTime(ctx, tx)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	var statement string
	switch operation {
	case "refresh":
		statement = `
			UPDATE agent_pr_bindings
			SET observation_requested_at=$4, revision=revision+1, updated_at=$4
			WHERE team_id=$1 AND id=$2 AND revision=$3`
	case "pause":
		statement = `
			UPDATE agent_pr_bindings
			SET paused=true, revision=revision+1, updated_at=$4
			WHERE team_id=$1 AND id=$2 AND revision=$3`
	case "resume":
		statement = `
			UPDATE agent_pr_bindings
			SET paused=false, lifecycle_state='active', attention_reason='',
			    revision=revision+1, updated_at=$4
			WHERE team_id=$1 AND id=$2 AND revision=$3`
	case "terminate":
		// Operator termination drains monitoring. Provider lifecycle and
		// terminal evidence are intentionally untouched.
		statement = `
			UPDATE agent_pr_bindings
			SET operator_terminated=true, paused=true,
			    revision=revision+1, updated_at=$4
			WHERE team_id=$1 AND id=$2 AND revision=$3`
	default:
		return pullrequest.Binding{}, fmt.Errorf("db: unsupported PR operator request")
	}
	result, err := tx.ExecContext(
		ctx, statement, request.TeamID, request.BindingID,
		request.ExpectedRevision, now,
	)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := requireAgentPRBindingCAS(result); err != nil {
		return pullrequest.Binding{}, err
	}
	updated, _, err := findAgentPRBinding(ctx, tx, request.TeamID, request.BindingID, false)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return pullrequest.Binding{}, err
	}
	return updated, nil
}

func (factory *agentPRBindingsFactory) ListAudit(
	ctx context.Context,
	teamID int,
	bindingID int64,
	filter pullrequest.AuditFilter,
) ([]pullrequest.AuditEntry, error) {
	if ctx == nil || teamID <= 0 || bindingID <= 0 {
		return nil, fmt.Errorf("db: PR audit requires context, team, and binding")
	}
	normalized, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	binding, found, err := findAgentPRBinding(ctx, factory.conn, teamID, bindingID, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, pullrequest.ErrBindingNotFound
	}
	originating := int64(0)
	if binding.OriginatingWorkflowRunID != nil {
		originating = int64(*binding.OriginatingWorkflowRunID)
	}
	query := `
		SELECT id, origin_kind, origin_reference, status, created_at, completed_at
		FROM agent_workflow_runs
		WHERE team_id=$1
		  AND (
		      id=$2
		      OR (definition_kind='workflow' AND origin_kind='pr-monitor'
		          AND origin_reference=$3)
		  )`
	arguments := []any{teamID, originating, strconv.FormatInt(bindingID, 10)}
	if normalized.BeforeWorkflowRunID > 0 {
		query += ` AND id < $4`
		arguments = append(arguments, int64(normalized.BeforeWorkflowRunID))
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(arguments)+1)
	arguments = append(arguments, normalized.Limit)
	rows, err := factory.conn.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]pullrequest.AuditEntry, 0, normalized.Limit)
	for rows.Next() {
		var (
			entry       pullrequest.AuditEntry
			runID       int64
			completedAt sql.NullTime
		)
		if err := rows.Scan(
			&runID, &entry.OriginKind, &entry.OriginReference,
			&entry.Status, &entry.CreatedAt, &completedAt,
		); err != nil {
			return nil, err
		}
		entry.WorkflowRunID = snapshot.WorkflowRunID(runID)
		if completedAt.Valid {
			value := completedAt.Time
			entry.CompletedAt = &value
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (factory *agentPRBindingsFactory) ListActive(
	ctx context.Context,
	teamID int,
) ([]pullrequest.Binding, error) {
	if ctx == nil || teamID <= 0 {
		return nil, fmt.Errorf("db: active PR bindings require context and team")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT `+agentPRBindingColumns+`
		FROM agent_pr_bindings
		WHERE team_id=$1 AND lifecycle_state='active'
		  AND NOT paused AND NOT operator_terminated
		ORDER BY updated_at, id
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []pullrequest.Binding{}
	for rows.Next() {
		binding, err := scanAgentPRBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func findAgentPRBinding(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	bindingID int64,
	lock bool,
) (pullrequest.Binding, bool, error) {
	query := `SELECT ` + agentPRBindingColumns + `
		FROM agent_pr_bindings WHERE team_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanAgentPRBindingFound(queryer.QueryRowContext(ctx, query, teamID, bindingID))
}

func findAgentPRBindingByExternal(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	locator pullrequest.Locator,
	lock bool,
) (pullrequest.Binding, bool, error) {
	query := `SELECT ` + agentPRBindingColumns + `
		FROM agent_pr_bindings
		WHERE team_id=$1 AND provider=$2 AND repository=$3 AND external_id=$4`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanAgentPRBindingFound(queryer.QueryRowContext(
		ctx, query, teamID, string(locator.Provider), locator.Repository, locator.ExternalID,
	))
}

// lockAgentPRBindingForUpdate is shared with workflow-run allocation so the
// run insert and reservation attachment can be one transaction.
func lockAgentPRBindingForUpdate(
	ctx context.Context,
	tx Tx,
	teamID int,
	bindingID int64,
) (pullrequest.Binding, bool, error) {
	return findAgentPRBinding(ctx, tx, teamID, bindingID, true)
}

func scanAgentPRBindingFound(
	row interface{ Scan(...any) error },
) (pullrequest.Binding, bool, error) {
	binding, err := scanAgentPRBinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequest.Binding{}, false, nil
	}
	return binding, err == nil, err
}

func scanAgentPRBinding(row interface{ Scan(...any) error }) (pullrequest.Binding, error) {
	var (
		binding                pullrequest.Binding
		provider               string
		originatingRun         sql.NullInt64
		originatingPublication sql.NullInt64
		pipelineID             sql.NullInt64
		acknowledgedCursor     []byte
		lastObservation        sql.NullInt64
		lastAction             sql.NullString
		lastRun                sql.NullInt64
		state                  string
		observationRequestedAt sql.NullTime
		activeBaseRevision     sql.NullInt64
		activeAction           sql.NullString
		activeObservation      sql.NullInt64
		activeCursor           []byte
		activeSource           sql.NullString
		activeTarget           sql.NullString
		activeToken            sql.NullString
		activeExpiresAt        sql.NullTime
		activeRun              sql.NullInt64
		terminalObservation    sql.NullInt64
		terminalAt             sql.NullTime
	)
	err := row.Scan(
		&binding.ID, &binding.TeamID, &provider,
		&binding.Locator.Repository, &binding.Locator.ExternalID,
		&binding.URL, &binding.SourceRef, &binding.TargetRef,
		&originatingRun, &originatingPublication,
		&binding.MonitorWorkflowDefinitionID, &binding.MonitorWorkflowVersion,
		&pipelineID, &acknowledgedCursor, &lastObservation,
		&lastAction, &lastRun,
		&binding.LastReconciledSourceSHA, &binding.LastReconciledTargetSHA,
		&binding.LastReconciledAt, &state, &binding.AttentionReason,
		&binding.Paused, &binding.OperatorTerminated, &observationRequestedAt,
		&activeBaseRevision, &activeAction, &activeObservation, &activeCursor,
		&activeSource, &activeTarget, &activeToken, &activeExpiresAt, &activeRun,
		&terminalObservation, &terminalAt,
		&binding.Revision, &binding.CreatedAt, &binding.UpdatedAt,
	)
	if err != nil {
		return pullrequest.Binding{}, err
	}
	binding.Locator.Provider = pullrequest.Provider(provider)
	cursor, err := decodeAgentPRCursor(acknowledgedCursor)
	if err != nil {
		return pullrequest.Binding{}, fmt.Errorf("db: decode acknowledged PR cursor: %w", err)
	}
	binding.AcknowledgedCursor = cursor
	binding.State = pullrequest.BindingState(state)
	if err := binding.State.Validate(); err != nil {
		return pullrequest.Binding{}, err
	}
	if originatingRun.Valid {
		value := snapshot.WorkflowRunID(originatingRun.Int64)
		binding.OriginatingWorkflowRunID = &value
	}
	if originatingPublication.Valid {
		value := originatingPublication.Int64
		binding.OriginatingPublicationOccurrence = &value
	}
	if pipelineID.Valid {
		value := int(pipelineID.Int64)
		binding.PipelineID = &value
	}
	if lastObservation.Valid {
		value := snapshot.SnapshotID(lastObservation.Int64)
		binding.LastObservationSnapshotID = &value
	}
	if lastAction.Valid {
		binding.LastAcknowledgedActionDigest = lastAction.String
	}
	if lastRun.Valid {
		value := snapshot.WorkflowRunID(lastRun.Int64)
		binding.LastAcknowledgedWorkflowRunID = &value
	}
	if observationRequestedAt.Valid {
		value := normalizeAgentPRBindingTime(observationRequestedAt.Time)
		binding.ObservationRequestedAt = &value
	}
	if activeAction.Valid {
		cursor, err := decodeAgentPRCursor(activeCursor)
		if err != nil {
			return pullrequest.Binding{}, fmt.Errorf("db: decode active PR cursor: %w", err)
		}
		reservation := pullrequest.LaunchReservation{
			BindingID: binding.ID, BindingRevision: binding.Revision,
			BaseRevision: activeBaseRevision.Int64, ActionDigest: activeAction.String,
			ObservationSnapshotID: snapshot.SnapshotID(activeObservation.Int64),
			Cursor:                cursor, SourceSHA: activeSource.String, TargetSHA: activeTarget.String,
			Token: activeToken.String, ExpiresAt: normalizeAgentPRBindingTime(activeExpiresAt.Time),
		}
		if activeRun.Valid {
			value := snapshot.WorkflowRunID(activeRun.Int64)
			reservation.WorkflowRunID = &value
		}
		binding.Active = &reservation
	}
	if terminalObservation.Valid {
		value := snapshot.SnapshotID(terminalObservation.Int64)
		binding.TerminalObservationSnapshotID = &value
	}
	if terminalAt.Valid {
		value := normalizeAgentPRBindingTime(terminalAt.Time)
		binding.TerminalAt = &value
	}
	binding.LastReconciledAt = normalizeAgentPRBindingTime(binding.LastReconciledAt)
	binding.CreatedAt = normalizeAgentPRBindingTime(binding.CreatedAt)
	binding.UpdatedAt = normalizeAgentPRBindingTime(binding.UpdatedAt)
	return binding, nil
}

func validateAgentPRMonitorDefinition(
	ctx context.Context,
	queryer snapshotQueryer,
	definitionID int,
	version int,
) error {
	var valid bool
	err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_workflow_definitions
			WHERE id=$1 AND definition_kind='workflow' AND version=$2
		)
	`, definitionID, version).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%w: monitor definition is not an exact workflow revision", pullrequest.ErrBindingConflict)
	}
	return nil
}

func validateAgentPRBindingOrigin(
	ctx context.Context,
	queryer snapshotQueryer,
	request pullrequest.CreateBinding,
) error {
	if request.OriginatingWorkflowRunID == 0 {
		return nil
	}
	var valid bool
	err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_publication_occurrences occurrence
			JOIN agent_workflow_runs run
			  ON run.id=occurrence.workflow_run_id
			 AND run.team_id=occurrence.team_id
			WHERE occurrence.id=$1 AND occurrence.team_id=$2
			  AND occurrence.workflow_run_id=$3
			  AND run.definition_kind='workflow'
		)
	`, request.OriginatingPublicationOccurrence, request.TeamID,
		int64(request.OriginatingWorkflowRunID)).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%w: originating publication occurrence is not exact", pullrequest.ErrBindingConflict)
	}
	return nil
}

func validateAgentPRSnapshotOwner(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	snapshotID snapshot.SnapshotID,
) error {
	var valid bool
	err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_snapshots
			WHERE id=$1 AND team_id=$2
			  AND type_name='pull-request' AND type_version=1
		)
	`, int64(snapshotID), teamID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%w: observation is not an owned pull-request/v1 snapshot", pullrequest.ErrBindingConflict)
	}
	return nil
}

func validateExactAgentPRAttachedAction(
	binding pullrequest.Binding,
	request pullrequest.AcknowledgeAction,
) error {
	if binding.Active == nil || binding.Active.WorkflowRunID == nil ||
		*binding.Active.WorkflowRunID != request.WorkflowRunID ||
		binding.Active.ActionDigest != request.ActionDigest ||
		binding.Active.Token != request.ReservationToken ||
		binding.Active.ObservationSnapshotID != request.ObservationSnapshotID ||
		binding.Active.Cursor != request.Cursor ||
		binding.Active.SourceSHA != request.SourceSHA ||
		binding.Active.TargetSHA != request.TargetSHA {
		return pullrequest.ErrReservationMismatch
	}
	return nil
}

func sameAgentPRReleaseIdentity(
	active *pullrequest.LaunchReservation,
	request pullrequest.ReleaseLaunch,
) bool {
	if active == nil ||
		active.ActionDigest != request.ActionDigest ||
		active.Token != request.ReservationToken {
		return false
	}
	if request.WorkflowRunID == nil {
		return active.WorkflowRunID == nil
	}
	return active.WorkflowRunID != nil &&
		*active.WorkflowRunID == *request.WorkflowRunID
}

func validateAgentPRRunUnsuccessfulTerminal(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	bindingID int64,
	definitionID int,
	runID snapshot.WorkflowRunID,
) error {
	var status AgentWorkflowRunStatus
	err := queryer.QueryRowContext(ctx, `
		SELECT status FROM agent_workflow_runs
		WHERE id=$1 AND team_id=$2 AND definition_kind='workflow'
		  AND workflow_definition_id=$3
		  AND origin_kind='pr-monitor' AND origin_reference=$4
		FOR UPDATE
	`, int64(runID), teamID, definitionID, strconv.FormatInt(bindingID, 10)).Scan(&status)
	if err != nil {
		return fmt.Errorf(
			"%w: attached workflow run is not exact",
			pullrequest.ErrReservationMismatch,
		)
	}
	switch status {
	case AgentWorkflowRunStatusFailed,
		AgentWorkflowRunStatusErrored,
		AgentWorkflowRunStatusAborted:
		return nil
	default:
		return fmt.Errorf(
			"%w: attached workflow run is not unsuccessfully terminal",
			pullrequest.ErrReservationMismatch,
		)
	}
}

func validateAgentPRRunSucceeded(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	bindingID int64,
	definitionID int,
	runID snapshot.WorkflowRunID,
) error {
	var status string
	err := queryer.QueryRowContext(ctx, `
		SELECT status FROM agent_workflow_runs
		WHERE id=$1 AND team_id=$2 AND definition_kind='workflow'
		  AND workflow_definition_id=$3
		  AND origin_kind='pr-monitor' AND origin_reference=$4
	`, int64(runID), teamID, definitionID, strconv.FormatInt(bindingID, 10)).Scan(&status)
	if err != nil || status != "succeeded" {
		return fmt.Errorf("%w: attached workflow run did not succeed", pullrequest.ErrReservationMismatch)
	}
	return nil
}

func agentPRRunStatusNonterminal(status string) bool {
	return status == "admitting" || status == "running" || status == "canceling"
}

func sameAgentPRBindingCreate(
	binding pullrequest.Binding,
	request pullrequest.CreateBinding,
) bool {
	if binding.TeamID != request.TeamID || binding.Locator != request.Locator ||
		binding.URL != request.URL || binding.SourceRef != request.SourceRef ||
		binding.TargetRef != request.TargetRef ||
		binding.MonitorWorkflowDefinitionID != request.MonitorWorkflowDefinitionID ||
		binding.MonitorWorkflowVersion != request.MonitorWorkflowVersion ||
		binding.AcknowledgedCursor != request.AcknowledgedCursor ||
		binding.LastReconciledSourceSHA != request.LastReconciledSourceSHA ||
		binding.LastReconciledTargetSHA != request.LastReconciledTargetSHA ||
		!binding.LastReconciledAt.Equal(normalizeAgentPRBindingTime(request.LastReconciledAt)) {
		return false
	}
	if request.OriginatingWorkflowRunID == 0 {
		if binding.OriginatingWorkflowRunID != nil ||
			binding.OriginatingPublicationOccurrence != nil {
			return false
		}
	} else if binding.OriginatingWorkflowRunID == nil ||
		*binding.OriginatingWorkflowRunID != request.OriginatingWorkflowRunID ||
		binding.OriginatingPublicationOccurrence == nil ||
		*binding.OriginatingPublicationOccurrence != request.OriginatingPublicationOccurrence {
		return false
	}
	if request.LastObservationSnapshotID == 0 {
		return binding.LastObservationSnapshotID == nil
	}
	return binding.LastObservationSnapshotID != nil &&
		*binding.LastObservationSnapshotID == request.LastObservationSnapshotID
}

func sameAgentPRReservationRequest(
	reservation pullrequest.LaunchReservation,
	request pullrequest.ReserveLaunch,
) bool {
	return reservation.BindingID == request.BindingID &&
		reservation.ActionDigest == request.ActionDigest &&
		reservation.ObservationSnapshotID == request.ObservationSnapshotID &&
		reservation.Cursor == request.Cursor &&
		reservation.SourceSHA == request.SourceSHA &&
		reservation.TargetSHA == request.TargetSHA
}

func sameAgentPRAcknowledgement(
	binding pullrequest.Binding,
	request pullrequest.AcknowledgeAction,
) bool {
	return binding.Active == nil &&
		binding.LastAcknowledgedWorkflowRunID != nil &&
		*binding.LastAcknowledgedWorkflowRunID == request.WorkflowRunID &&
		binding.LastAcknowledgedActionDigest == request.ActionDigest &&
		binding.LastObservationSnapshotID != nil &&
		*binding.LastObservationSnapshotID == request.ObservationSnapshotID &&
		binding.AcknowledgedCursor == request.Cursor &&
		binding.LastReconciledSourceSHA == request.SourceSHA &&
		binding.LastReconciledTargetSHA == request.TargetSHA
}

func sameAgentPRTerminal(
	binding pullrequest.Binding,
	request pullrequest.TerminalBinding,
) bool {
	return binding.State == request.State &&
		binding.TerminalObservationSnapshotID != nil &&
		*binding.TerminalObservationSnapshotID == request.ObservationSnapshotID &&
		binding.LastAcknowledgedWorkflowRunID != nil &&
		*binding.LastAcknowledgedWorkflowRunID == request.WorkflowRunID &&
		binding.LastAcknowledgedActionDigest == request.ActionDigest &&
		binding.AcknowledgedCursor == request.Cursor &&
		binding.LastReconciledSourceSHA == request.SourceSHA &&
		binding.LastReconciledTargetSHA == request.TargetSHA
}

func encodeAgentPRCursor(cursor pullrequest.Cursor) (string, error) {
	if err := cursor.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(string(cursor))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeAgentPRCursor(encoded []byte) (pullrequest.Cursor, error) {
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", err
	}
	cursor := pullrequest.Cursor(value)
	if err := cursor.Validate(); err != nil {
		return "", err
	}
	return cursor, nil
}

func newAgentPRReservationToken() (string, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("db: generate PR reservation token: %w", err)
	}
	return hex.EncodeToString(entropy[:]), nil
}

func agentPRBindingDatabaseTime(ctx context.Context, queryer snapshotQueryer) (time.Time, error) {
	var now time.Time
	err := queryer.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return normalizeAgentPRBindingTime(now), err
}

func normalizeAgentPRBindingTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func requireAgentPRBindingCAS(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return pullrequest.ErrStaleBindingRevision
	}
	return nil
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableAgentPRWorkflowRunID(value *snapshot.WorkflowRunID) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}
