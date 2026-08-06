package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db/encryption"
)

type AgentSnapshotsFactory interface {
	snapshot.MetadataStore
	LocationsForDigest(context.Context, snapshot.Digest) ([]snapshot.Location, error)
}

func NewAgentSnapshotsFactory(conn DbConn) AgentSnapshotsFactory {
	return &agentSnapshotsFactory{conn: conn}
}

type agentSnapshotsFactory struct {
	conn DbConn
}

var _ snapshot.MetadataStore = (*agentSnapshotsFactory)(nil)

type snapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) squirrel.RowScanner
}

type agentSnapshotLeaseConnection struct {
	conn               *sql.Conn
	encryptionStrategy encryption.Strategy
}

func (connection *agentSnapshotLeaseConnection) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	tx, err := connection.conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &agentSnapshotLeaseTx{Tx: tx, encryptionStrategy: connection.encryptionStrategy}, nil
}

func (connection *agentSnapshotLeaseConnection) ExecContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	return connection.conn.ExecContext(ctx, query, args...)
}

func (connection *agentSnapshotLeaseConnection) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return connection.conn.QueryContext(ctx, query, args...)
}

func (connection *agentSnapshotLeaseConnection) QueryRowContext(
	ctx context.Context,
	query string,
	args ...any,
) squirrel.RowScanner {
	return connection.conn.QueryRowContext(ctx, query, args...)
}

type agentSnapshotLeaseTx struct {
	*sql.Tx
	encryptionStrategy encryption.Strategy
}

func (tx *agentSnapshotLeaseTx) QueryRow(query string, args ...any) squirrel.RowScanner {
	return tx.Tx.QueryRow(query, args...)
}

func (tx *agentSnapshotLeaseTx) QueryRowContext(
	ctx context.Context,
	query string,
	args ...any,
) squirrel.RowScanner {
	return tx.Tx.QueryRowContext(ctx, query, args...)
}

func (tx *agentSnapshotLeaseTx) EncryptionStrategy() encryption.Strategy {
	return tx.encryptionStrategy
}

func (factory *agentSnapshotsFactory) leasedConnection(
	lease snapshot.DigestLease,
	digests ...snapshot.Digest,
) (*agentSnapshotLeaseConnection, func(), error) {
	for _, digest := range digests {
		if err := digest.Validate(); err != nil {
			return nil, nil, err
		}
	}
	held, ok := lease.(*agentSnapshotDigestLease)
	if !ok || held == nil {
		return nil, nil, fmt.Errorf("db: snapshot digest lease was not issued by the database lock manager")
	}
	connection, release, err := held.acquireConnection(factory.conn, digests)
	if err != nil {
		return nil, nil, err
	}
	return &agentSnapshotLeaseConnection{
		conn:               connection,
		encryptionStrategy: factory.conn.EncryptionStrategy(),
	}, release, nil
}

func (factory *agentSnapshotsFactory) StageUpload(
	ctx context.Context,
	lease snapshot.DigestLease,
	request snapshot.StageUploadRequest,
) (snapshot.StagedUpload, error) {
	if err := request.Validate(); err != nil {
		return snapshot.StagedUpload{}, err
	}
	connection, release, err := factory.leasedConnection(lease, request.Digest)
	if err != nil {
		return snapshot.StagedUpload{}, err
	}
	defer release()

	row := connection.QueryRowContext(ctx, `
		INSERT INTO agent_snapshot_staged_uploads
			(digest, team_id, attempt, lease_expires_at)
		SELECT $1, t.id, $3, $4
		FROM teams t
		WHERE t.id = $2
		RETURNING id, digest, team_id, attempt, lease_expires_at, created_at
	`, request.Digest.String(), request.TeamID, request.Attempt, request.LeaseExpiresAt)
	staged, err := scanStagedUpload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot.StagedUpload{}, fmt.Errorf("db: snapshot stage team %d does not exist", request.TeamID)
	}
	return staged, err
}

func (factory *agentSnapshotsFactory) CommitSealBatch(
	ctx context.Context,
	lease snapshot.DigestLease,
	commit snapshot.SealCommit,
) (map[string]snapshot.SealedOutput, error) {
	if err := commit.Validate(); err != nil {
		return nil, err
	}
	digests := make(map[snapshot.Digest]struct{}, len(commit.Outputs))
	for _, output := range commit.Outputs {
		digests[output.Digest] = struct{}{}
	}

	orderedDigests := make([]snapshot.Digest, 0, len(digests))
	for digest := range digests {
		orderedDigests = append(orderedDigests, digest)
	}
	sort.Slice(orderedDigests, func(i, j int) bool { return orderedDigests[i] < orderedDigests[j] })
	connection, release, err := factory.leasedConnection(lease, orderedDigests...)
	if err != nil {
		return nil, err
	}
	defer release()

	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	now, err := databaseNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := validateSealInvocation(ctx, tx, commit.Context); err != nil {
		return nil, err
	}
	if err := validateSealInputs(ctx, tx, commit.Context); err != nil {
		return nil, err
	}
	if err := validateSealStages(ctx, tx, commit, now); err != nil {
		return nil, err
	}

	for _, digest := range orderedDigests {
		state, err := loadDigestState(ctx, tx, digest, now, true)
		if err != nil {
			return nil, err
		}
		for _, output := range commit.Outputs {
			if output.Digest == digest {
				if err := state.ValidateCommit(output); err != nil {
					return nil, err
				}
			}
		}
	}

	manifestByClientKey := make(map[string]snapshot.Snapshot, len(commit.Outputs))
	for _, output := range commit.Outputs {
		manifest, err := insertOrVerifySnapshot(ctx, tx, commit.Context.TeamID, output)
		if err != nil {
			return nil, err
		}
		manifestByClientKey[output.ClientKey] = manifest
	}
	for _, digest := range orderedDigests {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_snapshots SET content_state = 'available'
			WHERE digest = $1 AND team_id = $2 AND content_state = 'expired'
		`, digest.String(), commit.Context.TeamID); err != nil {
			return nil, err
		}
	}

	for _, output := range commit.Outputs {
		for _, location := range output.Locations {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agent_snapshot_locations (digest, driver, key, node)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (digest, driver, key, node) DO NOTHING
			`, location.Digest.String(), location.Driver, location.Key, location.Node); err != nil {
				return nil, err
			}
		}
	}

	sealed := make(map[string]snapshot.SealedOutput, len(commit.Outputs))
	for _, output := range commit.Outputs {
		manifest := manifestByClientKey[output.ClientKey]
		productionID, productionCreated, err := insertOrVerifyProduction(ctx, tx, commit.Context, output, manifest.ID)
		if err != nil {
			return nil, err
		}
		if productionCreated {
			for _, retention := range output.Retention {
				if err := insertOrVerifyRetention(ctx, tx, commit.Context.TeamID, manifest.ID, retention); err != nil {
					return nil, err
				}
			}
		}
		if err := insertOrVerifyLineage(ctx, tx, productionID, productionCreated, commit.Context); err != nil {
			return nil, err
		}
		// Exposure lineage references the lineage row it describes, so it is
		// recorded only once that row exists.
		if err := insertOrVerifyExposures(ctx, tx, productionID, productionCreated, commit.Context); err != nil {
			return nil, err
		}
		if commit.Context.Build != nil && commit.Context.Build.WorkflowRunID != nil && output.WorkflowPort != "" {
			if err := bindWorkflowRunSnapshot(
				ctx, tx, *commit.Context.Build.WorkflowRunID, "output", output.WorkflowPort, manifest.ID,
			); err != nil {
				return nil, err
			}
		}
		sealed[output.ClientKey] = snapshot.SealedOutput{
			Port: output.Port,
			Snapshot: snapshot.SnapshotRef{
				ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest,
			},
		}
	}

	stageIDs := make([]int64, 0, len(commit.Outputs))
	seenStages := make(map[int64]struct{}, len(commit.Outputs))
	for _, output := range commit.Outputs {
		if _, found := seenStages[output.StagedUploadID]; found {
			continue
		}
		seenStages[output.StagedUploadID] = struct{}{}
		stageIDs = append(stageIDs, output.StagedUploadID)
	}
	if err := deleteStagesByID(ctx, tx, stageIDs); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sealed, nil
}

func databaseNow(ctx context.Context, queryer snapshotQueryer) (time.Time, error) {
	var now time.Time
	err := queryer.QueryRowContext(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

func validateSealInvocation(ctx context.Context, tx Tx, commit snapshot.SealCommitContext) error {
	if commit.Upload != nil {
		var teamName string
		err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE id = $1`, commit.TeamID).Scan(&teamName)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot upload team %d does not exist", commit.TeamID)
		}
		if err != nil {
			return err
		}
		if teamName != commit.TeamName {
			return fmt.Errorf("%w: snapshot upload team name does not match current team", snapshot.ErrConflict)
		}
		return nil
	}
	build := commit.Build
	if build == nil {
		return fmt.Errorf("db: snapshot build occurrence is required")
	}
	var teamName string
	var buildPipelineID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT t.name, b.pipeline_id
		FROM builds b
		JOIN teams t ON t.id = b.team_id
		WHERE b.id = $1 AND b.team_id = $2
		FOR UPDATE OF b
	`, build.BuildID, commit.TeamID).Scan(&teamName, &buildPipelineID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: snapshot producer build %d is not owned by team %d", build.BuildID, commit.TeamID)
	}
	if err != nil {
		return err
	}
	if teamName != commit.TeamName {
		return fmt.Errorf("db: snapshot producer team name does not match current team")
	}

	if build.WorkflowDefinitionID != nil {
		var definitionKind workflow.DefinitionKind
		if err := tx.QueryRowContext(ctx, `
			SELECT definition_kind FROM agent_workflow_definitions
			WHERE id = $1
		`, *build.WorkflowDefinitionID).Scan(&definitionKind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("db: snapshot workflow definition %d does not exist", *build.WorkflowDefinitionID)
			}
			return err
		}
		if definitionKind != workflow.DefinitionKindWorkflow && definitionKind != workflow.DefinitionKindNode {
			return fmt.Errorf("db: snapshot workflow definition %d has an invalid kind", *build.WorkflowDefinitionID)
		}
	}

	if build.WorkflowRunID != nil {
		var definitionID int
		var definitionKind workflow.DefinitionKind
		var plannedBuildID, instancePipelineID sql.NullInt64
		var status AgentWorkflowRunStatus
		var actualPlanCaptured bool
		err := tx.QueryRowContext(ctx, `
			SELECT workflow_definition_id, definition_kind, planned_build_id, instance_pipeline_id,
			       status,
			       actual_plan IS NOT NULL
			         AND actual_plan_hash IS NOT NULL
			         AND resolved_dependencies IS NOT NULL
			FROM agent_workflow_runs
			WHERE id = $1 AND team_id = $2
			FOR UPDATE
		`, int64(*build.WorkflowRunID), commit.TeamID).Scan(
			&definitionID, &definitionKind, &plannedBuildID, &instancePipelineID, &status, &actualPlanCaptured,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot workflow run is not owned by the producer team")
		}
		if err != nil {
			return err
		}
		if build.WorkflowDefinitionID == nil || definitionID != *build.WorkflowDefinitionID {
			return fmt.Errorf("db: snapshot workflow run and definition do not match")
		}
		var persistedDefinitionKind workflow.DefinitionKind
		if err := tx.QueryRowContext(ctx, `
			SELECT definition_kind FROM agent_workflow_definitions WHERE id = $1
		`, definitionID).Scan(&persistedDefinitionKind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("db: snapshot workflow definition %d does not exist", definitionID)
			}
			return err
		}
		if definitionKind != persistedDefinitionKind {
			return fmt.Errorf("db: snapshot workflow run and definition kinds do not match")
		}
		if status != AgentWorkflowRunStatusAdmitting && status != AgentWorkflowRunStatusRunning && status != AgentWorkflowRunStatusCanceling {
			return fmt.Errorf("db: snapshot workflow output requires an active workflow run")
		}
		if !plannedBuildID.Valid || plannedBuildID.Int64 != int64(build.BuildID) {
			return fmt.Errorf("db: snapshot workflow output build does not match the workflow run planned build")
		}
		if !instancePipelineID.Valid || !buildPipelineID.Valid || instancePipelineID.Int64 != buildPipelineID.Int64 {
			return fmt.Errorf("db: snapshot workflow output build does not belong to the linked workflow instance")
		}
		if !actualPlanCaptured {
			return fmt.Errorf("db: snapshot workflow output requires captured actual plan provenance")
		}
	}
	return nil
}

func validateSealInputs(ctx context.Context, tx Tx, commit snapshot.SealCommitContext) error {
	for _, port := range commit.InputOrder {
		ref := commit.Inputs[port]
		persisted, err := authorizedSnapshotByRef(ctx, tx, commit.TeamID, ref, true)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot input %q is absent, unavailable, or unauthorized", port)
		}
		if err != nil {
			return err
		}
		if persisted.ID != ref.ID || persisted.Type != ref.Type || persisted.Digest != ref.Digest {
			return fmt.Errorf("db: snapshot input %q does not match its persisted identity", port)
		}
		if commit.Build != nil && commit.Build.WorkflowRunID != nil {
			var authorizedByWorkflowProvenance bool
			err := tx.QueryRowContext(ctx, `
				SELECT
					EXISTS (
						SELECT 1
						FROM agent_workflow_run_snapshots
						WHERE workflow_run_id = $1
						  AND direction = 'input'
						  AND port_name = $2
						  AND snapshot_id = $3
					)
					OR EXISTS (
						SELECT 1
						FROM agent_snapshot_productions
						WHERE occurrence_kind = 'build'
						  AND workflow_run_id = $1
						  AND build_id = $4
						  AND team_id = $5
						  AND output_port = $2
						  AND snapshot_id = $3
					)
			`, int64(*commit.Build.WorkflowRunID), port, int64(ref.ID),
				commit.Build.BuildID, commit.TeamID).Scan(&authorizedByWorkflowProvenance)
			if err != nil {
				return err
			}
			if !authorizedByWorkflowProvenance {
				return fmt.Errorf("db: snapshot input %q does not match the workflow-run binding", port)
			}
		}
	}
	return nil
}

func validateSealStages(ctx context.Context, tx Tx, commit snapshot.SealCommit, now time.Time) error {
	seen := make(map[int64]snapshot.Digest, len(commit.Outputs))
	for _, output := range commit.Outputs {
		if prior, found := seen[output.StagedUploadID]; found {
			if prior != output.Digest {
				return fmt.Errorf("db: snapshot staged upload is associated with multiple digests")
			}
			continue
		}
		seen[output.StagedUploadID] = output.Digest
		staged, err := scanStagedUpload(tx.QueryRowContext(ctx, `
			SELECT id, digest, team_id, attempt, lease_expires_at, created_at
			FROM agent_snapshot_staged_uploads
			WHERE id = $1
			FOR UPDATE
		`, output.StagedUploadID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot staged upload %d does not exist", output.StagedUploadID)
		}
		if err != nil {
			return err
		}
		if staged.Digest != output.Digest || staged.TeamID != commit.Context.TeamID || staged.Attempt != commit.Context.StageAttempt() {
			return fmt.Errorf("db: snapshot staged upload %d does not match digest, team, and attempt", staged.ID)
		}
		if !staged.LeaseExpiresAt.After(now) {
			return fmt.Errorf("db: snapshot staged upload %d lease has expired", staged.ID)
		}
	}
	return nil
}

func insertOrVerifySnapshot(
	ctx context.Context,
	tx Tx,
	teamID int,
	output snapshot.SealCommitOutput,
) (snapshot.Snapshot, error) {
	typeName, typeVersion, err := splitSnapshotType(output.Port.Type)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count, representation, intrinsic_metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (team_id, type_name, type_version, digest) DO NOTHING
	`, teamID, typeName, typeVersion, output.Digest.String(), output.ByteSize, output.FileCount,
		output.Representation, nullableJSON(output.IntrinsicMetadata))
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	persisted, err := scanAgentSnapshot(tx.QueryRowContext(ctx, `
		SELECT `+agentSnapshotColumns+`
		FROM agent_snapshots
		WHERE team_id = $1 AND type_name = $2 AND type_version = $3 AND digest = $4
		FOR UPDATE
	`, teamID, typeName, typeVersion, output.Digest.String()))
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	if persisted.ByteSize != output.ByteSize || persisted.FileCount != output.FileCount ||
		persisted.Representation != output.Representation ||
		!semanticJSONEqual(persisted.IntrinsicMetadata, output.IntrinsicMetadata) {
		return snapshot.Snapshot{}, fmt.Errorf("db: snapshot immutable manifest conflicts with committed output")
	}
	return persisted, nil
}

func insertOrVerifyProduction(
	ctx context.Context,
	tx Tx,
	commit snapshot.SealCommitContext,
	output snapshot.SealCommitOutput,
	snapshotID snapshot.SnapshotID,
) (int64, bool, error) {
	if commit.Upload != nil {
		return insertOrVerifyUploadProduction(ctx, tx, commit, output, snapshotID)
	}
	build := commit.Build
	if build == nil {
		return 0, false, fmt.Errorf("db: snapshot build occurrence is required")
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by, plan_id,
			 attempt, step_kind, step_name, output_port, workflow_definition_id,
			 workflow_run_id, source_metadata)
		VALUES ($1, 'build', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (build_id, plan_id, attempt, output_port) WHERE occurrence_kind = 'build' DO NOTHING
	`, int64(snapshotID), build.BuildID, commit.TeamID, commit.TeamName, commit.CreatedBy,
		build.PlanID, build.Attempt, build.StepKind, build.StepName, output.Port.Name,
		optionalInt(build.WorkflowDefinitionID), optionalInt64(build.WorkflowRunID),
		nullableJSON(output.SourceMetadata))
	if err != nil {
		return 0, false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM agent_snapshot_productions
		WHERE build_id = $1 AND plan_id = $2 AND attempt = $3 AND output_port = $4
		  AND snapshot_id = $5 AND team_id = $6 AND team_name = $7
		  AND created_by = $8 AND step_kind = $9 AND step_name = $10
		  AND workflow_definition_id IS NOT DISTINCT FROM $11
		  AND workflow_run_id IS NOT DISTINCT FROM $12
		  AND source_metadata IS NOT DISTINCT FROM $13::jsonb
		FOR UPDATE
	`, build.BuildID, build.PlanID, build.Attempt, output.Port.Name,
		int64(snapshotID), commit.TeamID, commit.TeamName, commit.CreatedBy,
		build.StepKind, build.StepName, optionalInt(build.WorkflowDefinitionID),
		optionalInt64(build.WorkflowRunID), nullableJSON(output.SourceMetadata)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("%w: snapshot production invocation conflicts with immutable provenance", snapshot.ErrConflict)
	}
	return id, rowsAffected == 1, err
}

func insertOrVerifyUploadProduction(
	ctx context.Context,
	tx Tx,
	commit snapshot.SealCommitContext,
	output snapshot.SealCommitOutput,
	snapshotID snapshot.SnapshotID,
) (int64, bool, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, team_id, team_name, created_by,
			 upload_idempotency_key, source_metadata)
		VALUES ($1, 'upload', $2, $3, $4, $5, $6)
		ON CONFLICT (team_id, upload_idempotency_key) WHERE occurrence_kind = 'upload' DO NOTHING
	`, int64(snapshotID), commit.TeamID, commit.TeamName, commit.CreatedBy,
		commit.Upload.IdempotencyKey, nullableJSON(output.SourceMetadata))
	if err != nil {
		return 0, false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM agent_snapshot_productions
		WHERE occurrence_kind = 'upload'
		  AND team_id = $1 AND upload_idempotency_key = $2
		  AND snapshot_id = $3
		FOR UPDATE
	`, commit.TeamID, commit.Upload.IdempotencyKey, int64(snapshotID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("%w: snapshot upload idempotency key conflicts with immutable provenance", snapshot.ErrConflict)
	}
	return id, rowsAffected == 1, err
}

func insertOrVerifyRetention(
	ctx context.Context,
	tx Tx,
	teamID int,
	snapshotID snapshot.SnapshotID,
	retention snapshot.RetentionSpec,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_retention_claims
			(snapshot_id, team_id, class, workflow_run_id, expires_at, actor, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING
	`, int64(snapshotID), teamID, string(retention.Class), optionalWorkflowRunID(retention.WorkflowRunID),
		retention.ExpiresAt, retention.Actor, retention.Reason)
	if err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM agent_snapshot_retention_claims
		WHERE snapshot_id = $1 AND team_id = $2 AND class = $3 AND actor = $4
		  AND expires_at IS NOT DISTINCT FROM $5
		  AND reason = $6
		  AND workflow_run_id IS NOT DISTINCT FROM $7
		FOR UPDATE
	`, int64(snapshotID), teamID, string(retention.Class), retention.Actor,
		retention.ExpiresAt, retention.Reason, optionalWorkflowRunID(retention.WorkflowRunID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: snapshot retention claim conflicts with immutable policy")
	}
	return err
}

func insertOrVerifyLineage(
	ctx context.Context,
	tx Tx,
	productionID int64,
	productionCreated bool,
	commit snapshot.SealCommitContext,
) error {
	for position, port := range commit.InputOrder {
		ref := commit.Inputs[port]
		if productionCreated {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO agent_snapshot_lineage
					(production_id, position, input_port, input_snapshot_id)
				VALUES ($1, $2, $3, $4)
			`, productionID, position, port, int64(ref.ID))
			if err != nil {
				return err
			}
		}
		var foundID int64
		err := tx.QueryRowContext(ctx, `
			SELECT input_snapshot_id
			FROM agent_snapshot_lineage
			WHERE production_id = $1 AND position = $2 AND input_port = $3
		`, productionID, position, port).Scan(&foundID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot lineage conflicts with immutable production inputs")
		}
		if err != nil {
			return err
		}
		if foundID != int64(ref.ID) {
			return fmt.Errorf("db: snapshot lineage conflicts with immutable production inputs")
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_snapshot_lineage WHERE production_id = $1
	`, productionID).Scan(&count); err != nil {
		return err
	}
	if count != len(commit.InputOrder) {
		return fmt.Errorf("db: snapshot lineage conflicts with immutable production inputs")
	}
	return nil
}

// insertOrVerifyExposures records what the production was actually shown of
// each exposed input: the materialization mode and the mount path when there
// was one. It is occurrence data, so it lives next to lineage and never on the
// deduplicated agent_snapshots row, whose (type_name, type_version, digest)
// identity must stay a pure function of the sealed bytes.
//
// Like lineage, it is immutable: a second commit of the same occurrence must
// report the same exposure or be refused.
func insertOrVerifyExposures(
	ctx context.Context,
	tx Tx,
	productionID int64,
	productionCreated bool,
	commit snapshot.SealCommitContext,
) error {
	exposures := commit.Exposures()
	for _, port := range commit.InputOrder {
		exposure := exposures[port]
		ref := commit.Inputs[port]
		if productionCreated {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO agent_snapshot_exposures
					(production_id, input_port, input_snapshot_id, materialization_mode, tree_digest, mount_path)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, productionID, port, int64(ref.ID), exposure.Mode.String(),
				exposure.TreeDigest.String(), nullableMountPath(exposure.MountPath))
			if err != nil {
				return err
			}
		}
		var (
			mode       string
			treeDigest string
			mountPath  sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
			SELECT materialization_mode, tree_digest, mount_path
			FROM agent_snapshot_exposures
			WHERE production_id = $1 AND input_port = $2 AND input_snapshot_id = $3
		`, productionID, port, int64(ref.ID)).Scan(&mode, &treeDigest, &mountPath)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot exposure lineage is absent for input %q", port)
		}
		if err != nil {
			return err
		}
		if mode != exposure.Mode.String() || treeDigest != exposure.TreeDigest.String() ||
			mountPath.String != exposure.MountPath || mountPath.Valid != (exposure.MountPath != "") {
			return fmt.Errorf("db: snapshot exposure lineage conflicts with the immutable production exposure for input %q", port)
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_snapshot_exposures WHERE production_id = $1
	`, productionID).Scan(&count); err != nil {
		return err
	}
	if count != len(commit.InputOrder) {
		return fmt.Errorf("db: snapshot exposure lineage conflicts with immutable production inputs")
	}
	return nil
}

func nullableMountPath(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func bindWorkflowRunSnapshot(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	direction string,
	port string,
	snapshotID snapshot.SnapshotID,
) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_run_snapshots
			(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $2 = 'input' THEN now() ELSE NULL END)
		ON CONFLICT (workflow_run_id, direction, port_name) DO NOTHING
	`, int64(runID), direction, port, int64(snapshotID))
	if err != nil {
		return err
	}
	var foundID int64
	err = tx.QueryRowContext(ctx, `
		SELECT snapshot_id
		FROM agent_workflow_run_snapshots
		WHERE workflow_run_id = $1 AND direction = $2 AND port_name = $3
	`, int64(runID), direction, port).Scan(&foundID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run snapshot binding conflicts with immutable snapshot")
	}
	if err != nil {
		return err
	}
	if foundID != int64(snapshotID) {
		return fmt.Errorf("db: workflow-run snapshot binding conflicts with immutable snapshot")
	}
	return nil
}

func deleteStagesByID(ctx context.Context, tx Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `DELETE FROM agent_snapshot_staged_uploads WHERE id IN (`
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	query += `)`
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return fmt.Errorf("db: snapshot seal did not consume every staged upload")
	}
	return nil
}

func authorizedSnapshotByRef(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	ref snapshot.SnapshotRef,
	available bool,
) (snapshot.Snapshot, error) {
	query := `
		SELECT s.id, s.type_name, s.type_version, s.digest, s.byte_size,
		       s.file_count, s.representation, s.intrinsic_metadata,
		       s.content_state, s.created_at
		FROM agent_snapshots s
		WHERE s.id = $1 AND s.team_id = $2`
	if available {
		query += ` AND s.content_state = 'available'`
	}
	return scanAgentSnapshot(queryer.QueryRowContext(ctx, query, int64(ref.ID), teamID))
}

func (factory *agentSnapshotsFactory) GetAuthorized(
	ctx context.Context,
	teamID int,
	id snapshot.SnapshotID,
) (snapshot.Snapshot, bool, error) {
	if teamID <= 0 {
		return snapshot.Snapshot{}, false, fmt.Errorf("db: snapshot team ID must be positive")
	}
	if err := id.Validate(); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	value, err := scanAgentSnapshot(factory.conn.QueryRowContext(ctx, `
		SELECT s.id, s.type_name, s.type_version, s.digest, s.byte_size,
		       s.file_count, s.representation, s.intrinsic_metadata,
		       s.content_state, s.created_at
		FROM agent_snapshots s
		WHERE s.id = $1 AND s.team_id = $2
	`, int64(id), teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot.Snapshot{}, false, nil
	}
	return value, err == nil, err
}

// FindResourceCaptureOutput is a deliberately narrow authorization query for
// the server-owned resource capture adapter. It does not accept a caller-
// supplied build or snapshot ID: both are derived through the exact succeeded
// pipeline run, its instance pipeline, immutable-template ownership marker,
// expected output declaration, and server-authored production metadata.
func (factory *agentSnapshotsFactory) FindResourceCaptureOutput(
	ctx context.Context,
	teamID int,
	pipelineRunID int64,
	operationKey string,
	outputPort string,
	expectedType snapshot.TypeRef,
) (snapshot.Snapshot, bool, error) {
	if teamID <= 0 || pipelineRunID <= 0 {
		return snapshot.Snapshot{}, false, fmt.Errorf("db: resource capture team and pipeline run IDs must be positive")
	}
	decodedKey, err := hex.DecodeString(operationKey)
	if err != nil || len(decodedKey) != sha256.Size || operationKey != strings.ToLower(operationKey) {
		return snapshot.Snapshot{}, false, fmt.Errorf("db: resource capture operation key must be a lowercase SHA-256 digest")
	}
	port := snapshot.Port{Name: outputPort, Type: expectedType}
	if err := port.Validate(); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	typeName, typeVersion, err := splitSnapshotType(expectedType)
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT s.id, s.type_name, s.type_version, s.digest, s.byte_size,
		       s.file_count, s.representation, s.intrinsic_metadata,
		       s.content_state, s.created_at
		FROM pipeline_runs run
		JOIN pipelines template ON template.id = run.template_pipeline_id
		JOIN agent_workflow_run_templates owned ON owned.pipeline_id = template.id
		JOIN pipelines instance ON instance.id = run.instance_pipeline_id
		JOIN builds build ON build.pipeline_id = instance.id AND build.team_id = $1
		JOIN agent_snapshot_productions production
		  ON production.build_id = build.id
		 AND production.occurrence_kind = 'build'
		 AND production.team_id = $1
		JOIN agent_snapshots s ON s.id = production.snapshot_id AND s.team_id = $1
		WHERE run.id = $2
		  AND run.status = 'succeeded'
		  AND template.team_id = $1
		  AND instance.team_id = $1
		  AND template.name ~ (
		    '^agent-resource-capture-' || left($3, 24) || '-[0-9a-f]{12}$'
		  )
		  AND template.template = true
		  AND template.instance_vars IS NULL
		  AND template.archived = false
		  AND instance.name = template.name
		  AND instance.instance_vars IS NOT NULL
		  AND production.output_port = $4
		  AND production.step_kind = 'task'
		  AND production.step_name = 'seal-snapshot'
		  AND production.source_metadata ->> 'adapter' = 'resource-version'
		  AND production.source_metadata ->> 'operation_key' = $3
		  AND s.type_name = $5
		  AND s.type_version = $6
		  AND s.content_state = 'available'
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_workflow_runs workflow_run
		    WHERE workflow_run.template_pipeline_id = template.id
		  )
		ORDER BY s.id
		LIMIT 2
	`, teamID, pipelineRunID, operationKey, outputPort, typeName, typeVersion)
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return snapshot.Snapshot{}, false, err
		}
		return snapshot.Snapshot{}, false, nil
	}
	value, err := scanAgentSnapshot(rows)
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	if rows.Next() {
		return snapshot.Snapshot{}, false, fmt.Errorf("db: resource capture produced multiple matching outputs")
	}
	if err := rows.Err(); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	return value, true, nil
}

func (factory *agentSnapshotsFactory) GetAuthorizedDetail(
	ctx context.Context,
	teamID int,
	id snapshot.SnapshotID,
) (snapshot.Detail, bool, error) {
	manifest, found, err := factory.GetAuthorized(ctx, teamID, id)
	if err != nil || !found {
		return snapshot.Detail{}, found, err
	}
	detail := snapshot.Detail{
		Manifest: manifest, RetentionClaims: make([]snapshot.RetentionClaim, 0),
		Productions: make([]snapshot.ProductionDetail, 0), Downstream: make([]snapshot.ProductionSummary, 0),
	}
	if err := factory.conn.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_snapshot_locations WHERE digest = $1
	`, manifest.Digest.String()).Scan(&detail.ReplicaCount); err != nil {
		return snapshot.Detail{}, false, err
	}

	rows, err := factory.conn.QueryContext(ctx, `
		SELECT id, snapshot_id, class, workflow_run_id, expires_at, actor, reason, created_at
		FROM agent_snapshot_retention_claims
		WHERE snapshot_id = $1 AND team_id = $2
		ORDER BY created_at, id
	`, int64(id), teamID)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	for rows.Next() {
		claim, scanErr := scanRetentionClaim(rows)
		if scanErr != nil {
			Close(rows)
			return snapshot.Detail{}, false, scanErr
		}
		detail.RetentionClaims = append(detail.RetentionClaims, claim)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	snapshot.SortRetentionClaims(detail.RetentionClaims)

	rows, err = factory.conn.QueryContext(ctx, `
		SELECT p.id, p.occurrence_kind, p.created_by,
		       p.build_id, p.plan_id, p.attempt, p.step_kind, p.step_name,
		       p.output_port, p.workflow_definition_id, p.workflow_run_id, r.workflow_name,
		       p.source_metadata, p.created_at
		FROM agent_snapshot_productions p
		LEFT JOIN agent_workflow_runs r ON r.id = p.workflow_run_id AND r.team_id = p.team_id
		WHERE p.snapshot_id = $1 AND p.team_id = $2
		ORDER BY p.created_at, p.id
	`, int64(id), teamID)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	for rows.Next() {
		production, scanErr := scanProductionDetail(rows)
		if scanErr != nil {
			Close(rows)
			return snapshot.Detail{}, false, scanErr
		}
		detail.Productions = append(detail.Productions, production)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	for index := range detail.Productions {
		production := &detail.Productions[index]
		inputRows, inputErr := factory.conn.QueryContext(ctx, `
			SELECT l.position, l.input_port,
			       s.id, s.type_name, s.type_version, s.digest
			FROM agent_snapshot_lineage l
			JOIN agent_snapshots s ON s.id = l.input_snapshot_id AND s.team_id = $2
			WHERE l.production_id = $1
			ORDER BY l.position
		`, production.ID, teamID)
		if inputErr != nil {
			return snapshot.Detail{}, false, inputErr
		}
		for inputRows.Next() {
			var input snapshot.ProductionInput
			var snapshotID int64
			var typeName, digest string
			var typeVersion int
			if scanErr := inputRows.Scan(&input.Position, &input.Port, &snapshotID, &typeName, &typeVersion, &digest); scanErr != nil {
				Close(inputRows)
				return snapshot.Detail{}, false, scanErr
			}
			typeRef, typeErr := joinSnapshotType(typeName, typeVersion)
			if typeErr != nil {
				Close(inputRows)
				return snapshot.Detail{}, false, typeErr
			}
			input.Snapshot = snapshot.SnapshotRef{ID: snapshot.SnapshotID(snapshotID), Type: typeRef, Digest: snapshot.Digest(digest)}
			if inputErr := input.Validate(); inputErr != nil {
				Close(inputRows)
				return snapshot.Detail{}, false, inputErr
			}
			production.Inputs = append(production.Inputs, input)
		}
		inputErr = inputRows.Err()
		Close(inputRows)
		if inputErr != nil {
			return snapshot.Detail{}, false, inputErr
		}
	}

	rows, err = factory.conn.QueryContext(ctx, `
		SELECT p.id, p.occurrence_kind, p.created_by,
		       p.build_id, p.plan_id, p.attempt, p.step_kind, p.step_name,
		       p.output_port, p.workflow_definition_id, p.workflow_run_id, r.workflow_name,
		       p.created_at,
		       output.id, output.type_name, output.type_version, output.digest
		FROM agent_snapshot_productions p
		LEFT JOIN agent_workflow_runs r ON r.id = p.workflow_run_id AND r.team_id = p.team_id
		JOIN agent_snapshots output ON output.id = p.snapshot_id AND output.team_id = $2
		WHERE p.team_id = $2
		  AND EXISTS (
		      SELECT 1 FROM agent_snapshot_lineage l
		      WHERE l.production_id = p.id AND l.input_snapshot_id = $1
		  )
		ORDER BY p.created_at, p.id
	`, int64(id), teamID)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	for rows.Next() {
		production, scanErr := scanProductionSummary(rows)
		if scanErr != nil {
			Close(rows)
			return snapshot.Detail{}, false, scanErr
		}
		detail.Downstream = append(detail.Downstream, production)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return snapshot.Detail{}, false, err
	}
	if err := detail.Validate(); err != nil {
		return snapshot.Detail{}, false, fmt.Errorf("db: invalid authorized snapshot detail: %w", err)
	}
	return detail.Clone(), true, nil
}

func (factory *agentSnapshotsFactory) ListAuthorized(
	ctx context.Context,
	teamID int,
	filter snapshot.SnapshotListFilter,
) ([]snapshot.Snapshot, error) {
	if teamID <= 0 {
		return nil, fmt.Errorf("db: snapshot team ID must be positive")
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	query := `
		SELECT s.id, s.type_name, s.type_version, s.digest, s.byte_size,
		       s.file_count, s.representation, s.intrinsic_metadata,
		       s.content_state, s.created_at
		FROM agent_snapshots s
		WHERE s.team_id = $1`
	args := []any{teamID}
	if filter.Type != "" {
		name, version, err := splitSnapshotType(filter.Type)
		if err != nil {
			return nil, err
		}
		args = append(args, name)
		query += ` AND s.type_name = $` + strconv.Itoa(len(args))
		args = append(args, version)
		query += ` AND s.type_version = $` + strconv.Itoa(len(args))
	}
	if filter.ContentState != "" {
		args = append(args, string(filter.ContentState))
		query += ` AND s.content_state = $` + strconv.Itoa(len(args))
	}
	if filter.CreatedAfter != nil {
		args = append(args, *filter.CreatedAfter)
		query += ` AND s.created_at > $` + strconv.Itoa(len(args))
	}
	if filter.Before != nil {
		args = append(args, filter.Before.CreatedAt.UTC(), filter.Before.ID)
		query += ` AND (s.created_at, s.id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	query += ` ORDER BY s.created_at DESC, s.id DESC`
	limit := filter.Limit
	if limit == 0 {
		limit = 100
	}
	args = append(args, limit)
	query += ` LIMIT $` + strconv.Itoa(len(args))

	rows, err := factory.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	values := make([]snapshot.Snapshot, 0)
	for rows.Next() {
		value, err := scanAgentSnapshot(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (factory *agentSnapshotsFactory) LocationsForDigest(
	ctx context.Context,
	digest snapshot.Digest,
) ([]snapshot.Location, error) {
	if err := digest.Validate(); err != nil {
		return nil, err
	}
	return locationsForDigest(ctx, factory.conn, digest)
}

func locationsForDigest(
	ctx context.Context,
	queryer snapshotQueryer,
	digest snapshot.Digest,
) ([]snapshot.Location, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT digest, driver, key, node
		FROM agent_snapshot_locations
		WHERE digest = $1
		ORDER BY driver, key, node
	`, digest.String())
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	locations := make([]snapshot.Location, 0)
	for rows.Next() {
		location, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, rows.Err()
}

func (factory *agentSnapshotsFactory) DigestState(
	ctx context.Context,
	lease snapshot.DigestLease,
	digest snapshot.Digest,
	now time.Time,
) (snapshot.DigestState, error) {
	if now.IsZero() {
		return snapshot.DigestState{}, fmt.Errorf("db: snapshot digest state time is required")
	}
	connection, release, err := factory.leasedConnection(lease, digest)
	if err != nil {
		return snapshot.DigestState{}, err
	}
	defer release()
	return loadDigestState(ctx, connection, digest, now, false)
}

func loadDigestState(
	ctx context.Context,
	queryer snapshotQueryer,
	digest snapshot.Digest,
	now time.Time,
	lock bool,
) (snapshot.DigestState, error) {
	state := snapshot.DigestState{Digest: digest}
	manifestQuery := `SELECT ` + agentSnapshotColumns + ` FROM agent_snapshots WHERE digest = $1 ORDER BY id`
	if lock {
		manifestQuery += ` FOR UPDATE`
	}
	rows, err := queryer.QueryContext(ctx, manifestQuery, digest.String())
	if err != nil {
		return state, err
	}
	for rows.Next() {
		value, scanErr := scanAgentSnapshot(rows)
		if scanErr != nil {
			Close(rows)
			return state, scanErr
		}
		state.Snapshots = append(state.Snapshots, value)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return state, err
	}

	rows, err = queryer.QueryContext(ctx, `
		SELECT id, digest, team_id, attempt, lease_expires_at, created_at
		FROM agent_snapshot_staged_uploads
		WHERE digest = $1
		ORDER BY id
	`, digest.String())
	if err != nil {
		return state, err
	}
	for rows.Next() {
		staged, scanErr := scanStagedUpload(rows)
		if scanErr != nil {
			Close(rows)
			return state, scanErr
		}
		state.Stages = append(state.Stages, staged)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return state, err
	}

	rows, err = queryer.QueryContext(ctx, `
		SELECT digest, driver, key, node
		FROM agent_snapshot_locations
		WHERE digest = $1
		ORDER BY driver, key, node
	`, digest.String())
	if err != nil {
		return state, err
	}
	for rows.Next() {
		location, scanErr := scanLocation(rows)
		if scanErr != nil {
			Close(rows)
			return state, scanErr
		}
		state.Locations = append(state.Locations, location)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return state, err
	}

	err = queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_snapshot_retention_claims c
			JOIN agent_snapshots s ON s.id = c.snapshot_id
			WHERE s.digest = $1
			  AND (c.expires_at IS NULL OR c.expires_at > $2)
		)
	`, digest.String(), now).Scan(&state.HasActiveRetention)
	if err != nil {
		return state, err
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	return state, nil
}

func (factory *agentSnapshotsFactory) RemoveStagedUploads(
	ctx context.Context,
	lease snapshot.DigestLease,
	digest snapshot.Digest,
	ids []int64,
) error {
	if len(ids) == 0 {
		_, release, err := factory.leasedConnection(lease, digest)
		if err != nil {
			return err
		}
		release()
		return nil
	}
	query := `DELETE FROM agent_snapshot_staged_uploads WHERE digest = $1 AND id IN (`
	args := []any{digest.String()}
	for i, id := range ids {
		if id <= 0 {
			return fmt.Errorf("db: staged upload ID must be positive")
		}
		if i > 0 {
			query += ","
		}
		args = append(args, id)
		query += "$" + strconv.Itoa(len(args))
	}
	query += `)`
	connection, release, err := factory.leasedConnection(lease, digest)
	if err != nil {
		return err
	}
	defer release()
	_, err = connection.ExecContext(ctx, query, args...)
	return err
}

func (factory *agentSnapshotsFactory) Pin(
	ctx context.Context,
	lease snapshot.DigestLease,
	teamID int,
	actor string,
	ref snapshot.SnapshotRef,
	reason string,
) (snapshot.RetentionClaim, error) {
	if err := ref.Validate(); err != nil {
		return snapshot.RetentionClaim{}, err
	}
	if teamID <= 0 || strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return snapshot.RetentionClaim{}, fmt.Errorf("db: snapshot pin team, actor, and reason are required")
	}
	connection, release, err := factory.leasedConnection(lease, ref.Digest)
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	defer release()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	defer Rollback(tx)
	value, err := authorizedSnapshotByRef(ctx, tx, teamID, ref, false)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (value.Type != ref.Type || value.Digest != ref.Digest) {
		return snapshot.RetentionClaim{}, fmt.Errorf("%w: snapshot pin identity is unavailable or unauthorized", snapshot.ErrNotFound)
	}
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	if value.ContentState != snapshot.ContentStateAvailable {
		return snapshot.RetentionClaim{}, fmt.Errorf("%w: snapshot content must be recaptured before pinning", snapshot.ErrExpired)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_retention_claims
			(snapshot_id, team_id, class, actor, reason)
		VALUES ($1, $2, 'pin', $3, $4)
		ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING
	`, int64(ref.ID), teamID, actor, reason)
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	claim, err := scanRetentionClaim(tx.QueryRowContext(ctx, `
		SELECT id, snapshot_id, class, workflow_run_id, expires_at, actor, reason, created_at
		FROM agent_snapshot_retention_claims
		WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = $3
		FOR UPDATE
	`, int64(ref.ID), teamID, actor))
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	if claim.Reason != reason {
		return snapshot.RetentionClaim{}, fmt.Errorf("%w: snapshot pin conflicts with immutable reason", snapshot.ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return snapshot.RetentionClaim{}, err
	}
	return claim, nil
}

func (factory *agentSnapshotsFactory) Unpin(
	ctx context.Context,
	lease snapshot.DigestLease,
	teamID int,
	actor string,
	ref snapshot.SnapshotRef,
) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if teamID <= 0 || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("db: snapshot unpin team and actor are required")
	}
	connection, release, err := factory.leasedConnection(lease, ref.Digest)
	if err != nil {
		return err
	}
	defer release()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	value, err := authorizedSnapshotByRef(ctx, tx, teamID, ref, false)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (value.Type != ref.Type || value.Digest != ref.Digest) {
		return fmt.Errorf("%w: snapshot unpin identity is unavailable or unauthorized", snapshot.ErrNotFound)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		DELETE FROM agent_snapshot_retention_claims
		WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = $3
	`, int64(ref.ID), teamID, actor)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentSnapshotsFactory) MarkDigestExpired(
	ctx context.Context,
	lease snapshot.DigestLease,
	digest snapshot.Digest,
	now time.Time,
) (bool, error) {
	if now.IsZero() {
		return false, fmt.Errorf("db: snapshot expiry time is required")
	}
	connection, release, err := factory.leasedConnection(lease, digest)
	if err != nil {
		return false, err
	}
	defer release()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	state, err := loadDigestState(ctx, tx, digest, now, true)
	if err != nil {
		return false, err
	}
	if !state.CanExpire(now) {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_snapshots
		SET content_state = 'expired'
		WHERE digest = $1 AND content_state = 'available'
	`, digest.String())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (factory *agentSnapshotsFactory) AddLocation(
	ctx context.Context,
	lease snapshot.DigestLease,
	location snapshot.Location,
) error {
	if err := location.Validate(); err != nil {
		return err
	}
	connection, release, err := factory.leasedConnection(lease, location.Digest)
	if err != nil {
		return err
	}
	defer release()
	result, err := connection.ExecContext(ctx, `
		INSERT INTO agent_snapshot_locations (digest, driver, key, node)
		SELECT $1, $2, $3, $4
		WHERE EXISTS (
			SELECT 1 FROM agent_snapshots
			WHERE digest = $1 AND content_state = 'available'
		)
		ON CONFLICT (digest, driver, key, node) DO NOTHING
	`, location.Digest.String(), location.Driver, location.Key, location.Node)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		locations, err := locationsForDigest(ctx, connection, location.Digest)
		if err != nil {
			return err
		}
		for _, existing := range locations {
			if existing == location {
				return nil
			}
		}
		return fmt.Errorf("db: cannot add a location without an available snapshot manifest")
	}
	return nil
}

func (factory *agentSnapshotsFactory) RemoveLocation(
	ctx context.Context,
	lease snapshot.DigestLease,
	location snapshot.Location,
) error {
	if err := location.Validate(); err != nil {
		return err
	}
	connection, release, err := factory.leasedConnection(lease, location.Digest)
	if err != nil {
		return err
	}
	defer release()
	_, err = connection.ExecContext(ctx, `
		DELETE FROM agent_snapshot_locations
		WHERE digest = $1 AND driver = $2 AND key = $3 AND node = $4
	`, location.Digest.String(), location.Driver, location.Key, location.Node)
	return err
}

func (factory *agentSnapshotsFactory) DiscoverLifecycleCandidates(
	ctx context.Context,
	request snapshot.LifecyclePageRequest,
) (snapshot.LifecycleCandidatePage, error) {
	if err := request.Validate(); err != nil {
		return snapshot.LifecycleCandidatePage{}, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		WITH candidates AS (
			SELECT DISTINCT u.digest, 'orphan'::text AS kind
			FROM agent_snapshot_staged_uploads u
			WHERE u.lease_expires_at <= now()
			UNION
			SELECT DISTINCT s.digest, 'expiry'::text AS kind
			FROM agent_snapshots s
			WHERE s.content_state = 'available'
			  AND NOT EXISTS (
				SELECT 1 FROM agent_snapshot_retention_claims c
				JOIN agent_snapshots sibling ON sibling.id = c.snapshot_id
				WHERE sibling.digest = s.digest
				  AND (c.expires_at IS NULL OR c.expires_at > now())
			  )
			UNION
			SELECT DISTINCT s.digest, 'repair'::text AS kind
			FROM agent_snapshots s
			WHERE s.content_state = 'available'
			  AND EXISTS (
				SELECT 1 FROM agent_snapshot_retention_claims c
				JOIN agent_snapshots sibling ON sibling.id = c.snapshot_id
				WHERE sibling.digest = s.digest
				  AND (c.expires_at IS NULL OR c.expires_at > now())
			  )
		)
		SELECT digest, kind
		FROM candidates
		WHERE digest || '|' || kind > $1
		ORDER BY digest, kind
		LIMIT $2
	`, string(request.After), request.Limit+1)
	if err != nil {
		return snapshot.LifecycleCandidatePage{}, err
	}
	defer Close(rows)
	candidates := make([]snapshot.LifecycleCandidate, 0, request.Limit+1)
	for rows.Next() {
		var digest, kind string
		if err := rows.Scan(&digest, &kind); err != nil {
			return snapshot.LifecycleCandidatePage{}, err
		}
		candidate := snapshot.LifecycleCandidate{
			Digest: snapshot.Digest(digest), Kind: snapshot.LifecycleCandidateKind(kind),
		}
		if err := candidate.Validate(); err != nil {
			return snapshot.LifecycleCandidatePage{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return snapshot.LifecycleCandidatePage{}, err
	}
	page := snapshot.LifecycleCandidatePage{}
	if len(candidates) > request.Limit {
		page.Candidates = candidates[:request.Limit]
		page.Next = page.Candidates[len(page.Candidates)-1].Cursor()
	} else {
		page.Candidates = candidates
	}
	if err := page.Validate(request); err != nil {
		return snapshot.LifecycleCandidatePage{}, err
	}
	return page, nil
}
