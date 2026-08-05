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

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

const agentPRBindingColumns = `
	id, team_id, provider, repository, external_id, url, source_ref, target_ref,
	destination, approval_policy_version,
	originating_workflow_run_id, originating_publication_occurrence_id,
	creation_publication_occurrence_id,
	approved_baseline_repository_snapshot_id,
	approved_baseline_validation_snapshot_id,
	approved_baseline_publication_occurrence_id,
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
	acceptedBaseline, err := validateAgentPRBindingOrigin(
		ctx, tx, request,
	)
	if err != nil {
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
			 destination, approval_policy_version,
			 originating_workflow_run_id, originating_publication_occurrence_id,
			 creation_publication_occurrence_id,
			 approved_baseline_repository_snapshot_id,
			 approved_baseline_validation_snapshot_id,
			 approved_baseline_publication_occurrence_id,
			 monitor_workflow_definition_id, monitor_workflow_version,
			 acknowledged_cursor, last_observation_snapshot_id,
			 last_reconciled_source_sha, last_reconciled_target_sha, last_reconciled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb,$19,$20,$21,$22)
		ON CONFLICT (team_id, provider, repository, external_id) DO NOTHING
		RETURNING id
	`, request.TeamID, string(request.Locator.Provider), request.Locator.Repository,
		request.Locator.ExternalID, request.URL, request.SourceRef, request.TargetRef,
		request.Destination, request.ApprovalPolicyVersion,
		nullablePositiveInt64(int64(request.OriginatingWorkflowRunID)),
		nullablePositiveInt64(request.OriginatingPublicationOccurrence),
		nullablePositiveInt64(request.CreationPublicationOccurrenceID),
		nullablePositiveInt64(int64(acceptedBaseline.Candidate.ID)),
		nullablePositiveInt64(int64(acceptedBaseline.Validation.ID)),
		nullablePositiveInt64(acceptedBaseline.PublicationOccurrenceID),
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
	if locator.Provider != pullrequest.ProviderGitHub {
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
		if exactReplay {
			// A crash after atomic run allocation/attachment but before the
			// binder returns must re-enter that same durable run. Only the
			// attachment's single revision increment is admissible.
			if binding.Active.WorkflowRunID != nil {
				if binding.Revision !=
					binding.Active.BindingRevision+1 {
					return pullrequest.LaunchReservation{}, false,
						pullrequest.ErrStaleBindingRevision
				}
				if err := tx.Commit(); err != nil {
					return pullrequest.LaunchReservation{}, false, err
				}
				return *binding.Active, true, nil
			}
			if binding.Revision != binding.Active.BindingRevision {
				return pullrequest.LaunchReservation{}, false,
					pullrequest.ErrStaleBindingRevision
			}
			if binding.Active.ExpiresAt.After(now) {
				if err := tx.Commit(); err != nil {
					return pullrequest.LaunchReservation{}, false, err
				}
				return *binding.Active, true, nil
			}
			// The source version still names the exact base projection, but
			// its unattached lease expired after reservation incremented the
			// binding revision. Rotate only the lease identity under this
			// locked row. Keeping the reservation revision stable lets the
			// same captured admission attach, while the old token can no
			// longer race a recovered launcher.
			token, err := newAgentPRReservationToken()
			if err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			expiresAt := normalizeAgentPRBindingTime(
				now.Add(request.ExpiresIn),
			)
			result, err := tx.ExecContext(ctx, `
				UPDATE agent_pr_bindings
				SET active_reservation_token=$4,
				    active_reservation_expires_at=$5,
				    updated_at=$6
				WHERE team_id=$1 AND id=$2 AND revision=$3
				  AND active_reservation_token=$7
				  AND active_workflow_run_id IS NULL
				  AND active_reservation_expires_at <= $6
			`, request.TeamID, request.BindingID, binding.Revision,
				token, expiresAt, now, binding.Active.Token)
			if err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			if err := requireAgentPRBindingCAS(result); err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			recovered := *binding.Active
			recovered.Token = token
			recovered.ExpiresAt = expiresAt
			if err := tx.Commit(); err != nil {
				return pullrequest.LaunchReservation{}, false, err
			}
			return recovered, true, nil
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
		if err := validateAgentPRRunTerminal(
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
		int64(request.WorkflowRunID), request.ReconciledSourceSHA,
		request.TargetSHA, now)
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
		SourceSHA:           request.SourceSHA,
		ReconciledSourceSHA: request.SourceSHA,
		TargetSHA:           request.TargetSHA,
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

func (factory *agentPRBindingsFactory) MarkDirectTerminal(
	ctx context.Context,
	request pullrequest.DirectTerminalBinding,
) (pullrequest.Binding, error) {
	if ctx == nil {
		return pullrequest.Binding{}, fmt.Errorf(
			"db: direct PR terminal transition requires context",
		)
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
	if binding.State.Terminal() {
		if sameAgentPRDirectTerminal(binding, request) {
			if err := tx.Commit(); err != nil {
				return pullrequest.Binding{}, err
			}
			return binding, nil
		}
		return pullrequest.Binding{}, pullrequest.ErrBindingImmutable
	}
	if binding.Active != nil {
		return pullrequest.Binding{}, pullrequest.ErrBindingBusy
	}
	if binding.Revision != request.ExpectedRevision {
		return pullrequest.Binding{}, pullrequest.ErrStaleBindingRevision
	}
	if err := validateAgentPRSnapshotOwner(
		ctx, tx, request.TeamID, request.ObservationSnapshotID,
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
		    last_reconciled_source_sha=$6,
		    last_reconciled_target_sha=$7,
		    last_reconciled_at=$8,
		    lifecycle_state=$9, attention_reason='',
		    terminal_observation_snapshot_id=$5, terminal_at=$8,
		    revision=revision+1, updated_at=$8
		WHERE team_id=$1 AND id=$2 AND revision=$3
		  AND active_action_digest IS NULL
	`, request.TeamID, request.BindingID, request.ExpectedRevision,
		cursor, int64(request.ObservationSnapshotID),
		request.SourceSHA, request.TargetSHA, now, string(request.State))
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
		creationPublication    sql.NullInt64
		baselineRepository     int64
		baselineValidation     int64
		baselinePublication    int64
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
		&binding.Destination, &binding.ApprovalPolicyVersion,
		&originatingRun, &originatingPublication,
		&creationPublication,
		&baselineRepository, &baselineValidation, &baselinePublication,
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
	if creationPublication.Valid {
		value := creationPublication.Int64
		binding.CreationPublicationOccurrenceID = &value
	}
	if baselineRepository <= 0 || baselineValidation <= 0 ||
		baselinePublication <= 0 {
		return pullrequest.Binding{}, fmt.Errorf(
			"db: PR binding approved baseline authority is invalid",
		)
	}
	binding.ApprovedBaselineRepositorySnapshotID =
		snapshot.SnapshotID(baselineRepository)
	binding.ApprovedBaselineValidationSnapshotID =
		snapshot.SnapshotID(baselineValidation)
	binding.ApprovedBaselinePublicationOccurrenceID =
		baselinePublication
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
			BindingID:       binding.ID,
			BindingRevision: activeBaseRevision.Int64 + 1,
			BaseRevision:    activeBaseRevision.Int64, ActionDigest: activeAction.String,
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

func validateAndNameAgentPRMonitorDefinition(
	ctx context.Context,
	queryer snapshotQueryer,
	definitionID int,
	version int,
) (string, error) {
	var name string
	err := queryer.QueryRowContext(ctx, `
		SELECT name FROM agent_workflow_definitions
		WHERE id=$1 AND definition_kind='workflow' AND version=$2
	`, definitionID, version).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf(
			"%w: monitor definition is not an exact workflow revision",
			pullrequest.ErrBindingConflict,
		)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf(
			"%w: monitor definition name is invalid",
			pullrequest.ErrBindingConflict,
		)
	}
	return name, nil
}

func validateAgentPRBindingOrigin(
	ctx context.Context,
	queryer snapshotQueryer,
	request pullrequest.CreateBinding,
) (pullrequest.AcceptedReviewAuthority, error) {
	if err := validateAgentPRBindingPublicationOccurrence(
		ctx, queryer, request.OriginatingPublicationOccurrence,
		request.TeamID, request.OriginatingWorkflowRunID,
		publisher.OperationPublishPRBranch,
	); err != nil {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: originating branch publication occurrence is not exact",
			pullrequest.ErrBindingConflict,
		)
	}
	branchRecord, found, err := getAgentPublicationOccurrence(
		ctx, queryer, request.OriginatingPublicationOccurrence, false,
	)
	if err != nil {
		return pullrequest.AcceptedReviewAuthority{}, err
	}
	if !found {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: originating branch publication occurrence is unavailable",
			pullrequest.ErrBindingConflict,
		)
	}
	accepted, found, err := resolveAcceptedReviewAuthority(
		ctx, queryer, request.TeamID,
		request.OriginatingPublicationOccurrence,
	)
	if err != nil {
		return pullrequest.AcceptedReviewAuthority{}, err
	}
	if !found {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: originating publication has no exact accepted-review authority",
			pullrequest.ErrBindingConflict,
		)
	}
	protectedAccepted, err := accepted.Protected()
	if err != nil ||
		protectedAccepted.TeamID != request.TeamID ||
		protectedAccepted.PublicationOccurrenceID !=
			request.OriginatingPublicationOccurrence {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: originating accepted-review authority is invalid",
			pullrequest.ErrBindingConflict,
		)
	}

	if err := validateAgentPRBindingPublicationOccurrence(
		ctx, queryer, request.CreationPublicationOccurrenceID,
		request.TeamID, request.OriginatingWorkflowRunID,
		publisher.OperationCreatePR,
	); err != nil {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: creation publication occurrence is not exact",
			pullrequest.ErrBindingConflict,
		)
	}
	record, found, err := getAgentPublicationOccurrence(
		ctx, queryer, request.CreationPublicationOccurrenceID, false,
	)
	if err != nil {
		return pullrequest.AcceptedReviewAuthority{}, err
	}
	if !found {
		return pullrequest.AcceptedReviewAuthority{}, fmt.Errorf(
			"%w: creation publication occurrence is not exact",
			pullrequest.ErrBindingConflict,
		)
	}
	if err := validateAgentPRBindingInitialPublications(
		request, protectedAccepted, branchRecord.publication,
		record.publication,
	); err != nil {
		return pullrequest.AcceptedReviewAuthority{}, err
	}
	return protectedAccepted, nil
}

func validateAgentPRBindingPublicationOccurrence(
	ctx context.Context,
	queryer snapshotQueryer,
	occurrenceID int64,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
	operationKind publisher.OperationKind,
) error {
	var exact bool
	err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_publication_occurrences occurrence
			JOIN agent_publications publication
			  ON publication.id=occurrence.publication_id
			JOIN agent_workflow_runs run
			  ON run.id=occurrence.workflow_run_id
			 AND run.team_id=occurrence.team_id
			WHERE occurrence.id=$1
			  AND occurrence.team_id=$2
			  AND occurrence.workflow_run_id=$3
			  AND occurrence.status='succeeded'
			  AND publication.team_id=occurrence.team_id
			  AND publication.status='succeeded'
			  AND publication.operation_kind=$4
			  AND run.definition_kind='workflow'
		)
	`, occurrenceID, teamID, int64(workflowRunID), string(operationKind)).Scan(
		&exact,
	)
	if err != nil {
		return err
	}
	if !exact {
		return pullrequest.ErrBindingConflict
	}
	return nil
}

func validateAgentPRBindingInitialPublications(
	request pullrequest.CreateBinding,
	accepted pullrequest.AcceptedReviewAuthority,
	branchPublication publisher.Publication,
	creationPublication publisher.Publication,
) error {
	protectedAccepted, err := accepted.Protected()
	if err != nil ||
		protectedAccepted.TeamID != request.TeamID ||
		protectedAccepted.PublicationOccurrenceID !=
			request.OriginatingPublicationOccurrence {
		return fmt.Errorf(
			"%w: initial accepted-review authority is invalid",
			pullrequest.ErrBindingConflict,
		)
	}
	accepted = protectedAccepted
	branchValid := branchPublication.ID ==
		snapshot.DatabaseID(request.OriginatingPublicationOccurrence) &&
		branchPublication.Status == publisher.StatusSucceeded &&
		branchPublication.Result.Status == publisher.StatusSucceeded &&
		branchPublication.Result.Validate() == nil &&
		branchPublication.OperationKind ==
			publisher.OperationPublishPRBranch &&
		branchPublication.PRAction != nil &&
		branchPublication.PRAction.Kind ==
			publisher.OperationPublishPRBranch &&
		branchPublication.PRAction.Branch != nil &&
		branchPublication.PRAction.ValidatePersisted() == nil
	creationValid := creationPublication.ID ==
		snapshot.DatabaseID(request.CreationPublicationOccurrenceID) &&
		creationPublication.Status == publisher.StatusSucceeded &&
		creationPublication.Result.Status == publisher.StatusSucceeded &&
		creationPublication.Result.Validate() == nil &&
		creationPublication.OperationKind == publisher.OperationCreatePR &&
		creationPublication.PRAction != nil &&
		creationPublication.PRAction.Kind == publisher.OperationCreatePR &&
		creationPublication.PRAction.PullRequest != nil &&
		creationPublication.PRAction.ValidatePersisted() == nil
	if !branchValid || !creationValid {
		return fmt.Errorf(
			"%w: initial branch or creation publication is invalid",
			pullrequest.ErrBindingConflict,
		)
	}

	branch := branchPublication.PRAction.Branch
	creation := creationPublication.PRAction.PullRequest
	expectedProvider := publisher.PRProvider(request.Locator.Provider)
	if branch.Authority != creation.Authority ||
		branch.Authority.TeamID != request.TeamID ||
		branch.Authority.WorkflowRunID !=
			request.OriginatingWorkflowRunID ||
		branch.Locator.Provider != expectedProvider ||
		branch.Locator.Repository != request.Locator.Repository ||
		branch.Locator.ExternalID != "" ||
		branch.SourceRef != request.SourceRef ||
		branch.TargetRef != request.TargetRef ||
		branch.ExpectedTargetSHA != request.LastReconciledTargetSHA ||
		branch.NewSourceSHA != request.LastReconciledSourceSHA ||
		branch.Destination != request.Destination ||
		branch.ApprovalPolicyVersion !=
			request.ApprovalPolicyVersion ||
		branchPublication.Result.ExternalID != request.SourceRef ||
		branchPublication.Result.URL != "" ||
		branchPublication.Result.HeadSHA !=
			request.LastReconciledSourceSHA ||
		branchPublication.Result.BaseSHA !=
			request.LastReconciledTargetSHA ||
		branchPublication.Result.Detail != "" {
		return fmt.Errorf(
			"%w: branch publication does not establish binding authority",
			pullrequest.ErrBindingConflict,
		)
	}
	if creation.Locator.Provider != expectedProvider ||
		creation.Locator.Repository != request.Locator.Repository ||
		creation.Locator.ExternalID != "" ||
		creation.SourceRef != request.SourceRef ||
		creation.SourceSHA != request.LastReconciledSourceSHA ||
		creation.TargetRef != request.TargetRef ||
		creation.TargetSHA != request.LastReconciledTargetSHA ||
		creation.Destination != request.Destination ||
		creation.ApprovalPolicyVersion !=
			request.ApprovalPolicyVersion ||
		creationPublication.Result.ExternalID !=
			request.Locator.ExternalID ||
		creationPublication.Result.URL != request.URL ||
		creationPublication.Result.HeadSHA !=
			request.LastReconciledSourceSHA ||
		creationPublication.Result.BaseSHA !=
			request.LastReconciledTargetSHA ||
		creationPublication.Result.Detail != "" {
		return fmt.Errorf(
			"%w: creation publication does not establish binding authority",
			pullrequest.ErrBindingConflict,
		)
	}
	if branch.Observation != creation.Observation ||
		branch.Candidate != creation.Candidate ||
		branch.Validation != creation.Validation ||
		branch.Impact != creation.Impact ||
		!sameAgentPRAcceptedReviewEvidence(
			branch.Evidence, creation.Evidence,
		) ||
		!agentPRAcceptedReviewMatchesAuthority(
			branch.Evidence, accepted,
		) {
		return fmt.Errorf(
			"%w: initial branch and creation evidence do not share exact authority",
			pullrequest.ErrBindingConflict,
		)
	}
	return nil
}

func sameAgentPRAcceptedReviewEvidence(
	left publisher.PublicationEvidence,
	right publisher.PublicationEvidence,
) bool {
	if left.Kind != publisher.EvidenceAcceptedReview ||
		right.Kind != publisher.EvidenceAcceptedReview ||
		left.AcceptedReview == nil || right.AcceptedReview == nil ||
		left.HumanWait != nil || right.HumanWait != nil ||
		left.Validate() != nil || right.Validate() != nil {
		return false
	}
	return left.AcceptedReview.Review == right.AcceptedReview.Review &&
		left.AcceptedReview.Candidate ==
			right.AcceptedReview.Candidate &&
		left.AcceptedReview.Validation ==
			right.AcceptedReview.Validation &&
		left.AcceptedReview.ReviewWorkflowRunID ==
			right.AcceptedReview.ReviewWorkflowRunID &&
		left.AcceptedReview.OutcomeRevision ==
			right.AcceptedReview.OutcomeRevision &&
		left.AcceptedReview.AcceptedBy ==
			right.AcceptedReview.AcceptedBy &&
		left.AcceptedReview.AcceptedAt.Equal(
			right.AcceptedReview.AcceptedAt,
		)
}

func agentPRAcceptedReviewMatchesAuthority(
	evidence publisher.PublicationEvidence,
	accepted pullrequest.AcceptedReviewAuthority,
) bool {
	if evidence.Kind != publisher.EvidenceAcceptedReview ||
		evidence.AcceptedReview == nil ||
		evidence.HumanWait != nil ||
		evidence.Validate() != nil {
		return false
	}
	value := evidence.AcceptedReview
	return accepted.TeamID > 0 &&
		accepted.PublicationOccurrenceID > 0 &&
		value.Review == accepted.Review &&
		value.Candidate == accepted.Candidate &&
		value.Validation == accepted.Validation &&
		value.ReviewWorkflowRunID == accepted.ReviewWorkflowRunID &&
		value.OutcomeRevision == accepted.OutcomeRevision
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

func validateAgentPRRunTerminal(
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
		AgentWorkflowRunStatusAborted,
		AgentWorkflowRunStatusSucceeded:
		return nil
	default:
		return fmt.Errorf(
			"%w: attached workflow run is not terminal",
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
		binding.Destination != request.Destination ||
		binding.ApprovalPolicyVersion != request.ApprovalPolicyVersion ||
		binding.MonitorWorkflowDefinitionID != request.MonitorWorkflowDefinitionID ||
		binding.MonitorWorkflowVersion != request.MonitorWorkflowVersion {
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
	if binding.CreationPublicationOccurrenceID == nil ||
		*binding.CreationPublicationOccurrenceID !=
			request.CreationPublicationOccurrenceID {
		return false
	}
	return true
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
		binding.LastReconciledSourceSHA == request.ReconciledSourceSHA &&
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

func sameAgentPRDirectTerminal(
	binding pullrequest.Binding,
	request pullrequest.DirectTerminalBinding,
) bool {
	return binding.Active == nil &&
		binding.State == request.State &&
		binding.TerminalObservationSnapshotID != nil &&
		*binding.TerminalObservationSnapshotID ==
			request.ObservationSnapshotID &&
		binding.LastObservationSnapshotID != nil &&
		*binding.LastObservationSnapshotID ==
			request.ObservationSnapshotID &&
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
