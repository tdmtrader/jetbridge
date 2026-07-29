package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/google/uuid"
)

const (
	checkpointStageTTL       = time.Hour
	checkpointUploadTTL      = time.Hour
	checkpointSupersededTTL  = time.Hour
	checkpointTerminalTTL    = 24 * time.Hour
	checkpointMetadataTTL    = 30 * 24 * time.Hour
	checkpointHangarWriteTTL = 5 * time.Minute
	checkpointObjectClaimTTL = 5 * time.Minute
)

// AgentRunCheckpointsFactory is PostgreSQL's checkpoint authority. Hangar I/O
// is intentionally outside its transactions, bounded by the opaque upload and
// deletion tickets this factory issues.
//
//counterfeiter:generate . AgentRunCheckpointsFactory
type AgentRunCheckpointsFactory interface {
	checkpoint.Store
	checkpoint.EffectJournal
	ReconcileUnreferencedUploadingObject(context.Context, checkpoint.ObjectDeleteClaim, *hangar.ObjectRef) (bool, error)
}

func NewAgentRunCheckpointsFactory(conn DbConn) AgentRunCheckpointsFactory {
	return &agentRunCheckpointsFactory{conn: conn}
}

type agentRunCheckpointsFactory struct {
	conn DbConn
}

var _ checkpoint.Store = (*agentRunCheckpointsFactory)(nil)
var _ checkpoint.EffectJournal = (*agentRunCheckpointsFactory)(nil)

type checkpointHeadRow struct {
	id                      int64
	workflowRunProvenanceID sql.NullInt64
	workflowRunID           sql.NullInt64
	buildID                 int64
	planID                  string
	functionID              string
	latestCheckpointID      sql.NullInt64
	nextGeneration          int
	active                  bool
	terminalAt              sql.NullTime
}

type checkpointStageRow struct {
	id                         int64
	head                       checkpointHeadRow
	archiveObjectID            sql.NullInt64
	generation                 int
	expectedPreviousGeneration int
	executionAttempt           int
	status                     checkpoint.CheckpointStatus
	stageExpiresAt             time.Time
}

type checkpointObjectRow struct {
	id                           int64
	kind                         hangar.Kind
	digest                       hangar.Digest
	key                          string
	generation                   sql.NullInt64
	status                       string
	uploadToken                  sql.NullString
	uploadExpiresAt              sql.NullTime
	deleteToken                  sql.NullString
	deleteLeaseExpiresAt         sql.NullTime
	reconciliationToken          sql.NullString
	reconciliationLeaseExpiresAt sql.NullTime
	missingObservedAt            sql.NullTime
}

func (factory *agentRunCheckpointsFactory) Begin(ctx context.Context, request checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	defer Rollback(tx)

	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	head, err := getOrCreateCheckpointHead(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	if err := requireActiveCheckpointHead(head); err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	stageExpiresAt := now.Add(checkpointStageTTL)

	// A worker that missed its deadline must not reserve the head forever. The
	// reservation itself remains consumed because next_generation lives on head.
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoints
		SET status = 'aborted', aborted_at = $2, archive_object_id = NULL
		WHERE head_id = $1 AND status = 'staged' AND stage_expires_at <= $2
	`, head.id, now)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}

	var existing int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM agent_run_checkpoints
		WHERE head_id = $1 AND status = 'staged'
		FOR UPDATE
	`, head.id).Scan(&existing)
	if err == nil {
		return checkpoint.StagedCheckpoint{}, fmt.Errorf("%w: checkpoint generation %d is already staged", checkpoint.ErrConflict, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return checkpoint.StagedCheckpoint{}, err
	}

	previousGeneration, err := latestGeneration(ctx, tx, head)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	generation := head.nextGeneration
	_, err = tx.ExecContext(ctx, `UPDATE agent_run_checkpoint_heads SET next_generation = next_generation + 1 WHERE id = $1`, head.id)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	var stageID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_run_checkpoints
			(head_id, generation, expected_previous_generation, execution_attempt, status, manifest, stage_expires_at)
		VALUES ($1, $2, $3, $4, 'staged', '{}'::jsonb, $5)
		RETURNING id
	`, head.id, generation, previousGeneration, request.ExecutionAttempt, stageExpiresAt).Scan(&stageID)
	if err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.StagedCheckpoint{}, err
	}
	return checkpoint.StagedCheckpoint{
		ID: stageID, HeadID: head.id, Identity: request.Identity, Generation: generation,
		ExpectedPreviousGeneration: previousGeneration, ExecutionAttempt: request.ExecutionAttempt,
		StageExpiresAt: stageExpiresAt,
	}, nil
}

func (factory *agentRunCheckpointsFactory) Abort(ctx context.Context, request checkpoint.AbortRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	stage, err := checkpointStageForUpdate(ctx, tx, request.StagedCheckpointID)
	if err != nil {
		return err
	}
	switch stage.status {
	case checkpoint.CheckpointAborted, checkpoint.CheckpointExpired:
		return tx.Commit()
	case checkpoint.CheckpointStaged:
		now, err := checkpointDatabaseNow(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE agent_run_checkpoints
			SET status = 'aborted', aborted_at = $2, archive_object_id = NULL
			WHERE id = $1 AND status = 'staged'
		`, stage.id, now)
		if err != nil {
			return err
		}
		return tx.Commit()
	default:
		return fmt.Errorf("%w: checkpoint %d is %s, not staged", checkpoint.ErrConflict, stage.id, stage.status)
	}
}

func (factory *agentRunCheckpointsFactory) PrepareObjectUpload(ctx context.Context, request checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	defer Rollback(tx)
	stage, err := checkpointStageForUpdate(ctx, tx, request.StagedCheckpointID)
	if err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	if err := requireLiveStagedCheckpoint(ctx, tx, stage, now); err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}

	object, err := checkpointObjectForUpdate(ctx, tx, request.Digest, request.Key)
	if errors.Is(err, sql.ErrNoRows) {
		token := uuid.NewString()
		err = tx.QueryRowContext(ctx, `
			INSERT INTO agent_checkpoint_objects
				(kind, digest, object_key, status, upload_token, upload_expires_at)
			VALUES ('checkpoints', $1, $2, 'uploading', $3, $4)
			ON CONFLICT (kind, digest, object_key) DO NOTHING
			RETURNING id, kind, digest, object_key, generation, status, upload_token, upload_expires_at,
				delete_token, delete_lease_expires_at, reconciliation_token,
				reconciliation_lease_expires_at, missing_observed_at
		`, string(request.Digest), request.Key, token, now.Add(checkpointUploadTTL)).Scan(
			&object.id, &object.kind, &object.digest, &object.key, &object.generation, &object.status,
			&object.uploadToken, &object.uploadExpiresAt, &object.deleteToken, &object.deleteLeaseExpiresAt,
			&object.reconciliationToken, &object.reconciliationLeaseExpiresAt, &object.missingObservedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			object, err = checkpointObjectForUpdate(ctx, tx, request.Digest, request.Key)
		}
	}
	if err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	if stage.archiveObjectID.Valid && stage.archiveObjectID.Int64 != object.id {
		return checkpoint.ObjectUploadTicket{}, fmt.Errorf("%w: a staged checkpoint cannot change archive objects", checkpoint.ErrConflict)
	}
	switch object.status {
	case "available":
		if !object.generation.Valid {
			return checkpoint.ObjectUploadTicket{}, fmt.Errorf("checkpoint: available object %d has no generation", object.id)
		}
	case "uploading":
		if !object.uploadExpiresAt.Valid || !object.uploadExpiresAt.Time.After(now) {
			return checkpoint.ObjectUploadTicket{}, fmt.Errorf("%w: object upload ticket has expired", checkpoint.ErrExpired)
		}
	case "deleting":
		return checkpoint.ObjectUploadTicket{}, fmt.Errorf("%w: checkpoint object is being deleted", checkpoint.ErrConflict)
	default:
		return checkpoint.ObjectUploadTicket{}, fmt.Errorf("checkpoint: unknown object status %q", object.status)
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_run_checkpoints SET archive_object_id = $2 WHERE id = $1`, stage.id, object.id)
	if err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.ObjectUploadTicket{}, err
	}
	return objectUploadTicket(stage.id, object), nil
}

func (factory *agentRunCheckpointsFactory) CompleteObjectUpload(ctx context.Context, request checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error) {
	if err := request.Validate(); err != nil {
		return hangar.ObjectRef{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return hangar.ObjectRef{}, err
	}
	defer Rollback(tx)
	stage, err := checkpointStageForUpdate(ctx, tx, request.Ticket.StagedCheckpointID)
	if err != nil {
		return hangar.ObjectRef{}, err
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return hangar.ObjectRef{}, err
	}
	if err := requireLiveStagedCheckpoint(ctx, tx, stage, now); err != nil {
		return hangar.ObjectRef{}, err
	}
	if !stage.archiveObjectID.Valid || stage.archiveObjectID.Int64 != request.Ticket.ObjectID {
		return hangar.ObjectRef{}, fmt.Errorf("%w: upload ticket is no longer attached to its staged checkpoint", checkpoint.ErrConflict)
	}
	object, err := checkpointObjectByIDForUpdate(ctx, tx, request.Ticket.ObjectID)
	if err != nil {
		return hangar.ObjectRef{}, err
	}
	if object.kind != request.Object.Kind || object.digest != request.Object.Digest || object.key != request.Object.Key {
		return hangar.ObjectRef{}, fmt.Errorf("%w: uploaded object does not match durable object identity", checkpoint.ErrConflict)
	}
	switch object.status {
	case "available":
		if !object.generation.Valid || object.generation.Int64 != request.Object.Generation {
			return hangar.ObjectRef{}, fmt.Errorf("%w: uploaded object generation differs from completed ticket", checkpoint.ErrConflict)
		}
	case "uploading":
		if !object.uploadToken.Valid || object.uploadToken.String != request.Ticket.UploadToken ||
			!object.uploadExpiresAt.Valid || !object.uploadExpiresAt.Time.After(now) {
			return hangar.ObjectRef{}, fmt.Errorf("%w: upload ticket has expired or does not match", checkpoint.ErrExpired)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE agent_checkpoint_objects
			SET status = 'available', generation = $2, upload_token = NULL, upload_expires_at = NULL,
				reconciliation_token = NULL, reconciliation_lease_expires_at = NULL,
				missing_observed_at = NULL
			WHERE id = $1 AND status = 'uploading' AND upload_token = $3
		`, object.id, request.Object.Generation, request.Ticket.UploadToken)
		if err != nil {
			return hangar.ObjectRef{}, err
		}
	default:
		return hangar.ObjectRef{}, fmt.Errorf("%w: object %d is %s", checkpoint.ErrConflict, object.id, object.status)
	}
	if err := tx.Commit(); err != nil {
		return hangar.ObjectRef{}, err
	}
	return request.Object, nil
}

func (factory *agentRunCheckpointsFactory) Commit(ctx context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.Manifest{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	defer Rollback(tx)
	stage, err := checkpointStageForUpdate(ctx, tx, request.StagedCheckpointID)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	if headErr := requireActiveCheckpointHead(stage.head); headErr != nil {
		if stage.status == checkpoint.CheckpointStaged {
			if err := abortStagedCheckpoint(ctx, tx, stage.id, now); err != nil {
				return checkpoint.Manifest{}, err
			}
			if err := tx.Commit(); err != nil {
				return checkpoint.Manifest{}, err
			}
		}
		return checkpoint.Manifest{}, headErr
	}
	if stage.status != checkpoint.CheckpointStaged {
		return checkpoint.Manifest{}, fmt.Errorf("%w: checkpoint %d is %s", checkpoint.ErrConflict, stage.id, stage.status)
	}
	if !stage.stageExpiresAt.After(now) {
		if err := abortStagedCheckpoint(ctx, tx, stage.id, now); err != nil {
			return checkpoint.Manifest{}, err
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Manifest{}, err
		}
		return checkpoint.Manifest{}, fmt.Errorf("%w: staged checkpoint expired before commit", checkpoint.ErrExpired)
	}
	if request.ExpectedPreviousGeneration != stage.expectedPreviousGeneration ||
		request.Manifest.CheckpointID != stage.id || request.Manifest.Generation != stage.generation ||
		request.Manifest.ExecutionAttempt != stage.executionAttempt ||
		!sameCheckpointIdentity(request.Manifest.Identity(), headIdentity(stage.head)) {
		if err := supersedeStagedCheckpoint(ctx, tx, stage.id, now); err != nil {
			return checkpoint.Manifest{}, err
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Manifest{}, err
		}
		return checkpoint.Manifest{}, fmt.Errorf("%w: staged checkpoint manifest does not match reservation", checkpoint.ErrConflict)
	}

	canonical := request.Manifest.Canonicalized()
	manifestJSON, err := json.Marshal(canonical)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	if canonical.Archive == nil {
		if stage.archiveObjectID.Valid {
			return checkpoint.Manifest{}, fmt.Errorf("%w: checkpoint-zero manifest cannot retain an archive object", checkpoint.ErrConflict)
		}
	} else {
		if !stage.archiveObjectID.Valid {
			return checkpoint.Manifest{}, fmt.Errorf("%w: manifest archive has no upload ticket", checkpoint.ErrConflict)
		}
		object, err := checkpointObjectByIDForUpdate(ctx, tx, stage.archiveObjectID.Int64)
		if err != nil {
			return checkpoint.Manifest{}, err
		}
		if object.status != "available" || !object.generation.Valid || object.kind != canonical.Archive.Kind ||
			object.digest != canonical.Archive.Digest || object.key != canonical.Archive.Key ||
			object.generation.Int64 != canonical.Archive.Generation {
			return checkpoint.Manifest{}, fmt.Errorf("%w: manifest archive is not the acknowledged upload ticket", checkpoint.ErrConflict)
		}
	}

	currentGeneration, err := latestGeneration(ctx, tx, stage.head)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	if currentGeneration != stage.expectedPreviousGeneration {
		if err := supersedeStagedCheckpoint(ctx, tx, stage.id, now); err != nil {
			return checkpoint.Manifest{}, err
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Manifest{}, err
		}
		return checkpoint.Manifest{}, fmt.Errorf("%w: expected generation %d but latest is %d", checkpoint.ErrConflict, stage.expectedPreviousGeneration, currentGeneration)
	}
	if stage.head.latestCheckpointID.Valid {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_run_checkpoints SET status = 'superseded', superseded_at = $2
			WHERE id = $1 AND status = 'committed'
		`, stage.head.latestCheckpointID.Int64, now)
		if err != nil {
			return checkpoint.Manifest{}, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return checkpoint.Manifest{}, err
			}
			return checkpoint.Manifest{}, fmt.Errorf("%w: latest checkpoint was not committed", checkpoint.ErrConflict)
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoints
		SET status = 'committed', manifest = $2, committed_at = $3
		WHERE id = $1 AND status = 'staged'
	`, stage.id, manifestJSON, now)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_run_checkpoint_heads SET latest_checkpoint_id = $2 WHERE id = $1`, stage.head.id, stage.id)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Manifest{}, err
	}
	return canonical, nil
}

func (factory *agentRunCheckpointsFactory) Latest(ctx context.Context, identity checkpoint.Identity) (checkpoint.Manifest, bool, error) {
	if err := identity.Validate(); err != nil {
		return checkpoint.Manifest{}, false, err
	}
	head, err := checkpointHead(ctx, factory.conn, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Manifest{}, false, nil
	}
	if err != nil {
		return checkpoint.Manifest{}, false, err
	}
	var raw []byte
	err = factory.conn.QueryRowContext(ctx, `
		SELECT c.manifest
		FROM agent_run_checkpoint_heads h
		JOIN agent_run_checkpoints c ON c.id = h.latest_checkpoint_id AND c.status = 'committed'
		WHERE h.id = $1
	`, head.id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint.Manifest{}, false, nil
	}
	if err != nil {
		return checkpoint.Manifest{}, false, err
	}
	manifest, err := manifestFromJSON(raw)
	if err != nil {
		return checkpoint.Manifest{}, false, err
	}
	if !sameCheckpointIdentity(manifest.Identity(), identity) {
		return checkpoint.Manifest{}, false, fmt.Errorf("checkpoint: persisted manifest identity does not match its head")
	}
	return manifest, true, nil
}

func (factory *agentRunCheckpointsFactory) MarkTerminal(ctx context.Context, identity checkpoint.Identity, terminalAt time.Time) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if terminalAt.IsZero() {
		return fmt.Errorf("checkpoint: terminal time is required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, identity)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoint_heads
		SET active = FALSE, terminal_at = COALESCE(terminal_at, $2)
		WHERE id = $1
	`, head.id, terminalAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentRunCheckpointsFactory) ClaimCheckpointExpirations(ctx context.Context, limit int) ([]checkpoint.ExpirationClaim, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("checkpoint: positive expiration claim limit is required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return nil, err
	}

	// Staging expiry is an abort, not a committed recovery point. Clearing the
	// reference exposes any abandoned upload to the separate object reconciler.
	_, err = tx.ExecContext(ctx, `
		WITH stale AS (
			SELECT id FROM agent_run_checkpoints
			WHERE status = 'staged' AND stage_expires_at <= $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE agent_run_checkpoints c
		SET status = 'aborted', aborted_at = $1, archive_object_id = NULL
		FROM stale WHERE c.id = stale.id
	`, now)
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT c.id, c.head_id, c.archive_object_id, c.generation, c.status
		FROM agent_run_checkpoints c
		JOIN agent_run_checkpoint_heads h ON h.id = c.head_id
		WHERE (c.status = 'superseded' AND c.superseded_at <= $1)
		   OR (c.status = 'committed' AND h.active = FALSE AND h.latest_checkpoint_id = c.id
		       AND h.terminal_at <= $2)
		ORDER BY c.id
		FOR UPDATE OF c, h SKIP LOCKED
		LIMIT $3
	`, now.Add(-checkpointSupersededTTL), now.Add(-checkpointTerminalTTL), limit)
	if err != nil {
		return nil, err
	}
	claims := []checkpoint.ExpirationClaim{}
	claimStatuses := map[int64]checkpoint.CheckpointStatus{}
	for rows.Next() {
		var claim checkpoint.ExpirationClaim
		var archiveID sql.NullInt64
		var status checkpoint.CheckpointStatus
		if err := rows.Scan(&claim.CheckpointID, &claim.HeadID, &archiveID, &claim.Generation, &status); err != nil {
			return nil, err
		}
		claim.Token = uuid.NewString()
		if archiveID.Valid {
			value := archiveID.Int64
			claim.ArchiveObjectID = &value
		}
		claimStatuses[claim.CheckpointID] = status
		claims = append(claims, claim)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, claim := range claims {
		_, err := tx.ExecContext(ctx, `
			UPDATE agent_run_checkpoints
			SET status = 'expired', expired_at = $2, expiration_token = $3, archive_object_id = NULL
			WHERE id = $1
		`, claim.CheckpointID, now, claim.Token)
		if err != nil {
			return nil, err
		}
		if claimStatuses[claim.CheckpointID] == checkpoint.CheckpointCommitted {
			_, err = tx.ExecContext(ctx, `
				UPDATE agent_run_checkpoint_heads SET latest_checkpoint_id = NULL
				WHERE id = $1 AND latest_checkpoint_id = $2 AND active = FALSE
			`, claim.HeadID, claim.CheckpointID)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (factory *agentRunCheckpointsFactory) FinalizeCheckpointExpiration(ctx context.Context, claim checkpoint.ExpirationClaim) error {
	if claim.CheckpointID <= 0 || strings.TrimSpace(claim.Token) == "" {
		return fmt.Errorf("checkpoint: expiration claim is invalid")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	var status checkpoint.CheckpointStatus
	var token sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status, expiration_token FROM agent_run_checkpoints WHERE id = $1 FOR UPDATE`, claim.CheckpointID).Scan(&status, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != checkpoint.CheckpointExpired || (token.Valid && token.String != claim.Token) {
		return fmt.Errorf("%w: checkpoint expiration claim is stale", checkpoint.ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_run_checkpoints SET expiration_token = NULL WHERE id = $1 AND expiration_token = $2`, claim.CheckpointID, claim.Token)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentRunCheckpointsFactory) ClaimUnreferencedObjects(ctx context.Context, limit int) ([]checkpoint.ObjectDeleteClaim, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("checkpoint: positive object claim limit is required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	claims := []checkpoint.ObjectDeleteClaim{}
	availableRows, err := tx.QueryContext(ctx, `
		SELECT o.id, o.kind, o.digest, o.object_key, o.generation, o.status, o.upload_token, o.upload_expires_at,
			o.delete_token, o.delete_lease_expires_at, o.reconciliation_token,
			o.reconciliation_lease_expires_at, o.missing_observed_at
		FROM agent_checkpoint_objects o
		WHERE (
			o.status = 'available'
			OR (o.status = 'deleting' AND o.delete_lease_expires_at <= $1::timestamptz)
		  )
		  AND NOT EXISTS (
				SELECT 1 FROM agent_run_checkpoints c
				WHERE c.archive_object_id = o.id AND c.status IN ('staged', 'committed', 'superseded')
		  )
		ORDER BY o.id
		FOR UPDATE OF o SKIP LOCKED
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	availableObjects := []checkpointObjectRow{}
	for availableRows.Next() {
		object, err := scanCheckpointObject(availableRows)
		if err != nil {
			_ = availableRows.Close()
			return nil, err
		}
		availableObjects = append(availableObjects, object)
	}
	if err := availableRows.Close(); err != nil {
		return nil, err
	}
	for _, object := range availableObjects {
		claim, err := deletingObjectClaim(ctx, tx, object, now)
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	if len(claims) < limit {
		uploadingRows, err := tx.QueryContext(ctx, `
			SELECT o.id, o.kind, o.digest, o.object_key, o.generation, o.status, o.upload_token, o.upload_expires_at,
				o.delete_token, o.delete_lease_expires_at, o.reconciliation_token,
				o.reconciliation_lease_expires_at, o.missing_observed_at
			FROM agent_checkpoint_objects o
			WHERE o.status = 'uploading' AND o.upload_expires_at <= $1::timestamptz
			  AND (
				o.reconciliation_token IS NULL
				OR o.reconciliation_lease_expires_at <= $2::timestamptz
			  )
			  AND NOT EXISTS (
					SELECT 1 FROM agent_run_checkpoints c
					WHERE c.archive_object_id = o.id AND c.status IN ('staged', 'committed', 'superseded')
			  )
			ORDER BY o.id
			FOR UPDATE OF o SKIP LOCKED
			LIMIT $3
		`, now.Add(-checkpointHangarWriteTTL), now, limit-len(claims))
		if err != nil {
			return nil, err
		}
		uploadingObjects := []checkpointObjectRow{}
		for uploadingRows.Next() {
			object, err := scanCheckpointObject(uploadingRows)
			if err != nil {
				_ = uploadingRows.Close()
				return nil, err
			}
			uploadingObjects = append(uploadingObjects, object)
		}
		if err := uploadingRows.Close(); err != nil {
			return nil, err
		}
		for _, object := range uploadingObjects {
			reconciliationToken := uuid.NewString()
			result, err := tx.ExecContext(ctx, `
				UPDATE agent_checkpoint_objects
				SET reconciliation_token = $2, reconciliation_lease_expires_at = $3
				WHERE id = $1 AND status = 'uploading'
				  AND (reconciliation_token IS NULL OR reconciliation_lease_expires_at <= $4::timestamptz)
			`, object.id, reconciliationToken, now.Add(checkpointObjectClaimTTL), now)
			if err != nil {
				return nil, err
			}
			claimed, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if claimed != 1 {
				return nil, fmt.Errorf("%w: uploading object reconciliation claim raced", checkpoint.ErrConflict)
			}
			claims = append(claims, checkpoint.ObjectDeleteClaim{
				ObjectID: object.id, Token: reconciliationToken, NeedsUploadInspection: true,
				UploadTicket: checkpoint.ObjectUploadTicket{
					ObjectID: object.id, Kind: object.kind, Digest: object.digest, Key: object.key,
					UploadToken: object.uploadToken.String, ExpiresAt: object.uploadExpiresAt.Time,
				},
			})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (factory *agentRunCheckpointsFactory) ReconcileUnreferencedUploadingObject(ctx context.Context, claim checkpoint.ObjectDeleteClaim, inspected *hangar.ObjectRef) (bool, error) {
	if !claim.NeedsUploadInspection || claim.ObjectID <= 0 || strings.TrimSpace(claim.Token) == "" {
		return false, fmt.Errorf("checkpoint: uploading object reconciliation claim is invalid")
	}
	if inspected != nil {
		if err := validateCheckpointObjectRef(*inspected); err != nil {
			return false, err
		}
		if inspected.Digest != claim.UploadTicket.Digest || inspected.Key != claim.UploadTicket.Key {
			return false, fmt.Errorf("%w: inspected object does not match upload identity", checkpoint.ErrConflict)
		}
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	observedAt, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return false, err
	}
	object, err := checkpointObjectByIDForUpdate(ctx, tx, claim.ObjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if object.status != "uploading" || !object.reconciliationToken.Valid || object.reconciliationToken.String != claim.Token {
		return false, fmt.Errorf("%w: uploading object reconciliation claim is stale", checkpoint.ErrConflict)
	}
	if inspected != nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE agent_checkpoint_objects
			SET status = 'available', generation = $2, upload_token = NULL, upload_expires_at = NULL,
				reconciliation_token = NULL, reconciliation_lease_expires_at = NULL,
				missing_observed_at = NULL
			WHERE id = $1 AND reconciliation_token = $3
		`, object.id, inspected.Generation, claim.Token)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if !object.missingObservedAt.Valid || observedAt.Sub(object.missingObservedAt.Time) >= checkpointHangarWriteTTL {
		if object.missingObservedAt.Valid {
			_, err = tx.ExecContext(ctx, `DELETE FROM agent_checkpoint_objects WHERE id = $1 AND reconciliation_token = $2`, object.id, claim.Token)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE agent_checkpoint_objects
				SET missing_observed_at = $2, reconciliation_token = NULL,
					reconciliation_lease_expires_at = NULL
				WHERE id = $1 AND reconciliation_token = $3
			`, object.id, observedAt, claim.Token)
		}
		if err != nil {
			return false, err
		}
		return object.missingObservedAt.Valid, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_checkpoint_objects
		SET reconciliation_token = NULL, reconciliation_lease_expires_at = NULL
		WHERE id = $1 AND reconciliation_token = $2
	`, object.id, claim.Token)
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func (factory *agentRunCheckpointsFactory) FinalizeObjectDeletion(ctx context.Context, claim checkpoint.ObjectDeleteClaim) error {
	if claim.NeedsUploadInspection || claim.ObjectID <= 0 || strings.TrimSpace(claim.Token) == "" {
		return fmt.Errorf("checkpoint: object delete claim is invalid")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	object, err := checkpointObjectByIDForUpdate(ctx, tx, claim.ObjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if object.status != "deleting" || !object.deleteToken.Valid || object.deleteToken.String != claim.Token {
		return fmt.Errorf("%w: object deletion claim is stale", checkpoint.ErrConflict)
	}
	var references int
	err = tx.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_run_checkpoints
		WHERE archive_object_id = $1 AND status IN ('staged', 'committed', 'superseded')
	`, object.id).Scan(&references)
	if err != nil {
		return err
	}
	if references != 0 {
		return fmt.Errorf("%w: object regained a recovery reference", checkpoint.ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM agent_checkpoint_objects WHERE id = $1 AND delete_token = $2`, object.id, claim.Token)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentRunCheckpointsFactory) CleanupTerminalMetadata(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("checkpoint: positive metadata cleanup limit is required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)
	now, err := checkpointDatabaseNow(ctx, tx)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT h.id
		FROM agent_run_checkpoint_heads h
		WHERE h.active = FALSE AND h.terminal_at <= $1
		  AND h.latest_checkpoint_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM agent_run_checkpoints c WHERE c.head_id = h.id AND c.status = 'staged')
		  AND NOT EXISTS (SELECT 1 FROM agent_run_checkpoints c WHERE c.head_id = h.id AND c.archive_object_id IS NOT NULL)
		  AND NOT EXISTS (SELECT 1 FROM agent_run_checkpoints c WHERE c.head_id = h.id AND c.status = 'committed')
		ORDER BY h.id
		FOR UPDATE SKIP LOCKED
		LIMIT $2
	`, now.Add(-checkpointMetadataTTL), limit)
	if err != nil {
		return 0, err
	}
	headIDs := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		headIDs = append(headIDs, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(headIDs) == 0 {
		return 0, tx.Commit()
	}
	for _, headID := range headIDs {
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_run_events WHERE head_id = $1`, headID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_run_effects WHERE head_id = $1`, headID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_run_checkpoints WHERE head_id = $1`, headID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_run_checkpoint_heads WHERE id = $1`, headID)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(headIDs), nil
}

func (factory *agentRunCheckpointsFactory) BeginEffect(ctx context.Context, request checkpoint.BeginEffectRequest) (checkpoint.Effect, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.Effect{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Effect{}, err
	}
	defer Rollback(tx)
	head, err := getOrCreateCheckpointHead(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Effect{}, err
	}
	effect := request.Effect
	if !head.active || head.terminalAt.Valid {
		stored, err := effectForUpdate(ctx, tx, head.id, request.ExecutionAttempt, effect.ToolCallID)
		if errors.Is(err, sql.ErrNoRows) {
			return checkpoint.Effect{}, fmt.Errorf("%w: checkpoint head is terminal", checkpoint.ErrConflict)
		}
		if err != nil {
			return checkpoint.Effect{}, err
		}
		if !sameEffectIdentity(stored, effect) {
			return checkpoint.Effect{}, fmt.Errorf("%w: effect %q was begun with a different identity", checkpoint.ErrConflict, effect.ToolCallID)
		}
		if err := tx.Commit(); err != nil {
			return checkpoint.Effect{}, err
		}
		return stored, nil
	}
	var stored checkpoint.Effect
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_run_effects
			(head_id, execution_attempt, tool_call_id, tool_name, provider, adapter_version, read_only,
			 idempotency_key, idempotency_contract, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'begun')
		ON CONFLICT (head_id, execution_attempt, tool_call_id) DO NOTHING
		RETURNING tool_call_id, tool_name, provider, adapter_version, idempotency_key, idempotency_contract, read_only, state
	`, head.id, request.ExecutionAttempt, effect.ToolCallID, effect.ToolName, effect.Provider, effect.AdapterVersion,
		effect.ReadOnly, effect.IdempotencyKey, effect.IdempotencyContract).Scan(
		&stored.ToolCallID, &stored.ToolName, &stored.Provider, &stored.AdapterVersion,
		&stored.IdempotencyKey, &stored.IdempotencyContract, &stored.ReadOnly, &stored.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		stored, err = effectForUpdate(ctx, tx, head.id, request.ExecutionAttempt, effect.ToolCallID)
		if err != nil {
			return checkpoint.Effect{}, err
		}
		if !sameEffectIdentity(stored, effect) {
			return checkpoint.Effect{}, fmt.Errorf("%w: effect %q was begun with a different identity", checkpoint.ErrConflict, effect.ToolCallID)
		}
	}
	if err != nil {
		return checkpoint.Effect{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Effect{}, err
	}
	return stored, nil
}

func (factory *agentRunCheckpointsFactory) CommitEffect(ctx context.Context, request checkpoint.CommitEffectRequest) (checkpoint.Effect, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.Effect{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return checkpoint.Effect{}, err
	}
	defer Rollback(tx)
	head, err := checkpointHeadForUpdate(ctx, tx, request.Identity)
	if err != nil {
		return checkpoint.Effect{}, err
	}
	var effect checkpoint.Effect
	// MarkTerminal and BeginEffect serialize on the head row. CommitEffect is
	// intentionally allowed afterward so provider work authorized before that
	// fence can close its begun journal entry without authorizing a new effect.
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_run_effects
		SET state = 'committed', committed_at = now()
		WHERE head_id = $1 AND execution_attempt = $2 AND tool_call_id = $3 AND state = 'begun'
		RETURNING tool_call_id, tool_name, provider, adapter_version, idempotency_key, idempotency_contract, read_only, state
	`, head.id, request.ExecutionAttempt, request.ToolCallID).Scan(
		&effect.ToolCallID, &effect.ToolName, &effect.Provider, &effect.AdapterVersion,
		&effect.IdempotencyKey, &effect.IdempotencyContract, &effect.ReadOnly, &effect.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		effect, err = effectForUpdate(ctx, tx, head.id, request.ExecutionAttempt, request.ToolCallID)
		if errors.Is(err, sql.ErrNoRows) {
			return checkpoint.Effect{}, fmt.Errorf("%w: effect %q", checkpoint.ErrNotFound, request.ToolCallID)
		}
		if err != nil {
			return checkpoint.Effect{}, err
		}
		if effect.State != checkpoint.EffectCommitted {
			return checkpoint.Effect{}, fmt.Errorf("%w: effect %q did not commit", checkpoint.ErrConflict, request.ToolCallID)
		}
	}
	if err != nil {
		return checkpoint.Effect{}, err
	}
	if err := tx.Commit(); err != nil {
		return checkpoint.Effect{}, err
	}
	return effect, nil
}

func (factory *agentRunCheckpointsFactory) ListEffects(ctx context.Context, identity checkpoint.Identity, attempt int) ([]checkpoint.Effect, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if attempt <= 0 {
		return nil, fmt.Errorf("checkpoint: effect execution attempt must be positive")
	}
	head, err := checkpointHead(ctx, factory.conn, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return []checkpoint.Effect{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT tool_call_id, tool_name, provider, adapter_version, idempotency_key, idempotency_contract, read_only, state
		FROM agent_run_effects
		WHERE head_id = $1 AND execution_attempt = $2
		ORDER BY id ASC
	`, head.id, attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	effects := []checkpoint.Effect{}
	for rows.Next() {
		var effect checkpoint.Effect
		if err := rows.Scan(&effect.ToolCallID, &effect.ToolName, &effect.Provider, &effect.AdapterVersion,
			&effect.IdempotencyKey, &effect.IdempotencyContract, &effect.ReadOnly, &effect.State); err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	return effects, rows.Err()
}

func getOrCreateCheckpointHead(ctx context.Context, tx Tx, identity checkpoint.Identity) (checkpointHeadRow, error) {
	workflowRunID := checkpointWorkflowRunIDValue(identity.WorkflowRunID)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_run_checkpoint_heads
			(workflow_run_provenance_id, workflow_run_id, build_id, plan_id, function_id)
		VALUES ($1, (SELECT id FROM agent_workflow_runs WHERE id = $1), $2, $3, $4)
		ON CONFLICT (build_id, plan_id, function_id) DO NOTHING
	`, workflowRunID, identity.BuildID, identity.PlanID, identity.FunctionID)
	if err != nil {
		return checkpointHeadRow{}, err
	}
	head, err := checkpointHeadForUpdate(ctx, tx, identity)
	if err != nil {
		return checkpointHeadRow{}, err
	}
	return head, nil
}

func checkpointHeadForUpdate(ctx context.Context, tx Tx, identity checkpoint.Identity) (checkpointHeadRow, error) {
	return checkpointHead(ctx, tx, identity, " FOR UPDATE")
}

type checkpointQueryer interface {
	QueryRowContext(context.Context, string, ...any) squirrel.RowScanner
}

func checkpointHead(ctx context.Context, queryer checkpointQueryer, identity checkpoint.Identity, suffix ...string) (checkpointHeadRow, error) {
	locking := ""
	if len(suffix) > 0 {
		locking = suffix[0]
	}
	var head checkpointHeadRow
	err := queryer.QueryRowContext(ctx, `
		SELECT id, workflow_run_provenance_id, workflow_run_id, build_id, plan_id, function_id, latest_checkpoint_id, next_generation, active, terminal_at
		FROM agent_run_checkpoint_heads
		WHERE build_id = $1 AND plan_id = $2 AND function_id = $3`+locking,
		identity.BuildID, identity.PlanID, identity.FunctionID).Scan(
		&head.id, &head.workflowRunProvenanceID, &head.workflowRunID, &head.buildID, &head.planID, &head.functionID,
		&head.latestCheckpointID, &head.nextGeneration, &head.active, &head.terminalAt,
	)
	if err == nil && !sameCheckpointIdentity(headIdentity(head), identity) {
		return checkpointHeadRow{}, fmt.Errorf("%w: workflow-run provenance does not match checkpoint head", checkpoint.ErrConflict)
	}
	return head, err
}

func checkpointStageForUpdate(ctx context.Context, tx Tx, stageID int64) (checkpointStageRow, error) {
	var stage checkpointStageRow
	err := tx.QueryRowContext(ctx, `
		SELECT c.id, c.archive_object_id, c.generation, c.expected_previous_generation, c.execution_attempt,
			c.status, c.stage_expires_at,
			h.id, h.workflow_run_provenance_id, h.workflow_run_id, h.build_id, h.plan_id, h.function_id, h.latest_checkpoint_id,
			h.next_generation, h.active, h.terminal_at
		FROM agent_run_checkpoints c
		JOIN agent_run_checkpoint_heads h ON h.id = c.head_id
		WHERE c.id = $1
		FOR UPDATE OF c, h
	`, stageID).Scan(
		&stage.id, &stage.archiveObjectID, &stage.generation, &stage.expectedPreviousGeneration,
		&stage.executionAttempt, &stage.status, &stage.stageExpiresAt,
		&stage.head.id, &stage.head.workflowRunProvenanceID, &stage.head.workflowRunID, &stage.head.buildID, &stage.head.planID, &stage.head.functionID,
		&stage.head.latestCheckpointID, &stage.head.nextGeneration, &stage.head.active, &stage.head.terminalAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointStageRow{}, fmt.Errorf("%w: staged checkpoint %d", checkpoint.ErrNotFound, stageID)
	}
	return stage, err
}

func checkpointObjectForUpdate(ctx context.Context, tx Tx, digest hangar.Digest, key string) (checkpointObjectRow, error) {
	var object checkpointObjectRow
	err := tx.QueryRowContext(ctx, `
		SELECT id, kind, digest, object_key, generation, status, upload_token, upload_expires_at,
			delete_token, delete_lease_expires_at, reconciliation_token,
			reconciliation_lease_expires_at, missing_observed_at
		FROM agent_checkpoint_objects
		WHERE kind = 'checkpoints' AND digest = $1 AND object_key = $2
		FOR UPDATE
	`, string(digest), key).Scan(
		&object.id, &object.kind, &object.digest, &object.key, &object.generation, &object.status,
		&object.uploadToken, &object.uploadExpiresAt, &object.deleteToken, &object.deleteLeaseExpiresAt,
		&object.reconciliationToken, &object.reconciliationLeaseExpiresAt, &object.missingObservedAt,
	)
	return object, err
}

func checkpointObjectByIDForUpdate(ctx context.Context, tx Tx, id int64) (checkpointObjectRow, error) {
	var object checkpointObjectRow
	err := tx.QueryRowContext(ctx, `
		SELECT id, kind, digest, object_key, generation, status, upload_token, upload_expires_at,
			delete_token, delete_lease_expires_at, reconciliation_token,
			reconciliation_lease_expires_at, missing_observed_at
		FROM agent_checkpoint_objects WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&object.id, &object.kind, &object.digest, &object.key, &object.generation, &object.status,
		&object.uploadToken, &object.uploadExpiresAt, &object.deleteToken, &object.deleteLeaseExpiresAt,
		&object.reconciliationToken, &object.reconciliationLeaseExpiresAt, &object.missingObservedAt,
	)
	return object, err
}

func scanCheckpointObject(src scannable) (checkpointObjectRow, error) {
	var object checkpointObjectRow
	err := src.Scan(&object.id, &object.kind, &object.digest, &object.key, &object.generation, &object.status,
		&object.uploadToken, &object.uploadExpiresAt, &object.deleteToken, &object.deleteLeaseExpiresAt,
		&object.reconciliationToken, &object.reconciliationLeaseExpiresAt, &object.missingObservedAt)
	return object, err
}

func objectUploadTicket(stageID int64, object checkpointObjectRow) checkpoint.ObjectUploadTicket {
	ticket := checkpoint.ObjectUploadTicket{
		ObjectID: object.id, StagedCheckpointID: stageID, Kind: object.kind, Digest: object.digest,
		Key: object.key, AlreadyAvailable: object.status == "available",
	}
	if object.generation.Valid {
		ticket.AvailableGeneration = object.generation.Int64
	}
	if object.uploadToken.Valid {
		ticket.UploadToken = object.uploadToken.String
	}
	if object.uploadExpiresAt.Valid {
		ticket.ExpiresAt = object.uploadExpiresAt.Time
	}
	return ticket
}

func requireActiveCheckpointHead(head checkpointHeadRow) error {
	if !head.active || head.terminalAt.Valid {
		return fmt.Errorf("%w: checkpoint head is terminal", checkpoint.ErrConflict)
	}
	return nil
}

func deletingObjectClaim(ctx context.Context, tx Tx, object checkpointObjectRow, now time.Time) (checkpoint.ObjectDeleteClaim, error) {
	if !object.generation.Valid {
		return checkpoint.ObjectDeleteClaim{}, fmt.Errorf("checkpoint: deletable object %d has no generation", object.id)
	}
	ref, err := hangar.NewObjectRef(object.kind, object.digest, object.generation.Int64)
	if err != nil {
		return checkpoint.ObjectDeleteClaim{}, err
	}
	if ref.Key != object.key {
		return checkpoint.ObjectDeleteClaim{}, fmt.Errorf("checkpoint: object %d has noncanonical key", object.id)
	}
	token := uuid.NewString()
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_checkpoint_objects
		SET status = 'deleting', delete_token = $2, delete_lease_expires_at = $3
		WHERE id = $1
		  AND (
			status = 'available'
			OR (status = 'deleting' AND delete_lease_expires_at <= $4::timestamptz)
		  )
	`, object.id, token, now.Add(checkpointObjectClaimTTL), now)
	if err != nil {
		return checkpoint.ObjectDeleteClaim{}, err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return checkpoint.ObjectDeleteClaim{}, err
	}
	if claimed != 1 {
		return checkpoint.ObjectDeleteClaim{}, fmt.Errorf("%w: object deletion claim raced", checkpoint.ErrConflict)
	}
	return checkpoint.ObjectDeleteClaim{ObjectID: object.id, Object: ref, Token: token}, nil
}

func requireLiveStagedCheckpoint(ctx context.Context, tx Tx, stage checkpointStageRow, now time.Time) error {
	if err := requireActiveCheckpointHead(stage.head); err != nil {
		return err
	}
	if stage.status != checkpoint.CheckpointStaged {
		return fmt.Errorf("%w: checkpoint %d is %s", checkpoint.ErrConflict, stage.id, stage.status)
	}
	if stage.stageExpiresAt.After(now) {
		return nil
	}
	if err := abortStagedCheckpoint(ctx, tx, stage.id, now); err != nil {
		return err
	}
	return fmt.Errorf("%w: staged checkpoint %d expired", checkpoint.ErrExpired, stage.id)
}

func abortStagedCheckpoint(ctx context.Context, tx Tx, stageID int64, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoints
		SET status = 'aborted', aborted_at = $2, archive_object_id = NULL
		WHERE id = $1 AND status = 'staged'
	`, stageID, at)
	return err
}

func supersedeStagedCheckpoint(ctx context.Context, tx Tx, stageID int64, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE agent_run_checkpoints
		SET status = 'superseded', superseded_at = $2, archive_object_id = NULL
		WHERE id = $1 AND status = 'staged'
	`, stageID, at)
	return err
}

func latestGeneration(ctx context.Context, tx Tx, head checkpointHeadRow) (int, error) {
	if !head.latestCheckpointID.Valid {
		return 0, nil
	}
	var generation int
	err := tx.QueryRowContext(ctx, `
		SELECT generation FROM agent_run_checkpoints WHERE id = $1 AND head_id = $2 AND status = 'committed'
	`, head.latestCheckpointID.Int64, head.id).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: checkpoint head has no committed latest row", checkpoint.ErrConflict)
	}
	return generation, err
}

func effectForUpdate(ctx context.Context, tx Tx, headID int64, attempt int, toolCallID string) (checkpoint.Effect, error) {
	var effect checkpoint.Effect
	err := tx.QueryRowContext(ctx, `
		SELECT tool_call_id, tool_name, provider, adapter_version, idempotency_key, idempotency_contract, read_only, state
		FROM agent_run_effects
		WHERE head_id = $1 AND execution_attempt = $2 AND tool_call_id = $3
		FOR UPDATE
	`, headID, attempt, toolCallID).Scan(
		&effect.ToolCallID, &effect.ToolName, &effect.Provider, &effect.AdapterVersion,
		&effect.IdempotencyKey, &effect.IdempotencyContract, &effect.ReadOnly, &effect.State,
	)
	return effect, err
}

func checkpointDatabaseNow(ctx context.Context, tx Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

func manifestFromJSON(raw []byte) (checkpoint.Manifest, error) {
	var manifest checkpoint.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return checkpoint.Manifest{}, fmt.Errorf("checkpoint: decode persisted manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return checkpoint.Manifest{}, fmt.Errorf("checkpoint: persisted manifest is invalid: %w", err)
	}
	return manifest, nil
}

func checkpointWorkflowRunIDValue(id *snapshot.WorkflowRunID) any {
	if id == nil {
		return nil
	}
	return int64(*id)
}

func headIdentity(head checkpointHeadRow) checkpoint.Identity {
	identity := checkpoint.Identity{BuildID: head.buildID, PlanID: head.planID, FunctionID: head.functionID}
	if head.workflowRunProvenanceID.Valid {
		workflowRunID := snapshot.WorkflowRunID(head.workflowRunProvenanceID.Int64)
		identity.WorkflowRunID = &workflowRunID
	}
	return identity
}

func sameCheckpointIdentity(left, right checkpoint.Identity) bool {
	if left.BuildID != right.BuildID || left.PlanID != right.PlanID || left.FunctionID != right.FunctionID {
		return false
	}
	if left.WorkflowRunID == nil || right.WorkflowRunID == nil {
		return left.WorkflowRunID == nil && right.WorkflowRunID == nil
	}
	return *left.WorkflowRunID == *right.WorkflowRunID
}

func sameEffectIdentity(left, right checkpoint.Effect) bool {
	return left.ToolCallID == right.ToolCallID && left.ToolName == right.ToolName && left.Provider == right.Provider &&
		left.AdapterVersion == right.AdapterVersion && left.IdempotencyKey == right.IdempotencyKey &&
		left.IdempotencyContract == right.IdempotencyContract && left.ReadOnly == right.ReadOnly
}

func validateCheckpointObjectRef(ref hangar.ObjectRef) error {
	if ref.Kind != hangar.KindCheckpoint || ref.Generation <= 0 {
		return fmt.Errorf("checkpoint: inspected object is not a checkpoint generation")
	}
	key, err := hangar.Key(ref.Kind, ref.Digest)
	if err != nil {
		return err
	}
	if key != ref.Key {
		return fmt.Errorf("checkpoint: inspected object has a noncanonical key")
	}
	return nil
}
