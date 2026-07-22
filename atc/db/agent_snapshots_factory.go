package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/snapshot"
)

//counterfeiter:generate . AgentSnapshotsFactory
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

func (factory *agentSnapshotsFactory) StageUpload(
	ctx context.Context,
	lease snapshot.DigestLease,
	request snapshot.StageUploadRequest,
) (snapshot.StagedUpload, error) {
	if err := request.Validate(); err != nil {
		return snapshot.StagedUpload{}, err
	}
	if err := snapshot.RequireDigestLease(lease, request.Digest); err != nil {
		return snapshot.StagedUpload{}, err
	}

	row := factory.conn.QueryRowContext(ctx, `
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
		if err := snapshot.RequireDigestLease(lease, output.Digest); err != nil {
			return nil, err
		}
		digests[output.Digest] = struct{}{}
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
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

	orderedDigests := make([]snapshot.Digest, 0, len(digests))
	for digest := range digests {
		orderedDigests = append(orderedDigests, digest)
	}
	sort.Slice(orderedDigests, func(i, j int) bool { return orderedDigests[i] < orderedDigests[j] })

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
		manifest, err := insertOrVerifySnapshot(ctx, tx, output)
		if err != nil {
			return nil, err
		}
		manifestByClientKey[output.ClientKey] = manifest
	}
	for _, digest := range orderedDigests {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_snapshots SET content_state = 'available'
			WHERE digest = $1 AND content_state = 'expired'
		`, digest.String()); err != nil {
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
		if err := insertGrant(ctx, tx, commit.Context, manifest.ID); err != nil {
			return nil, err
		}
		for _, retention := range output.Retention {
			if err := insertOrVerifyRetention(ctx, tx, commit.Context.TeamID, manifest.ID, retention); err != nil {
				return nil, err
			}
		}
		if err := insertOrVerifyLineage(ctx, tx, productionID, productionCreated, commit.Context); err != nil {
			return nil, err
		}
		if commit.Context.WorkflowRunID != nil {
			if err := bindWorkflowRunSnapshot(
				ctx, tx, *commit.Context.WorkflowRunID, "output", output.Port.Name, manifest.ID,
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
	var teamName string
	err := tx.QueryRowContext(ctx, `
		SELECT t.name
		FROM builds b
		JOIN teams t ON t.id = b.team_id
		WHERE b.id = $1 AND b.team_id = $2
		FOR UPDATE OF b
	`, commit.BuildID, commit.TeamID).Scan(&teamName)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: snapshot producer build %d is not owned by team %d", commit.BuildID, commit.TeamID)
	}
	if err != nil {
		return err
	}
	if teamName != commit.TeamName {
		return fmt.Errorf("db: snapshot producer team name does not match current team")
	}

	if commit.WorkflowDefinitionID != nil {
		var id int
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM agent_workflow_definitions WHERE id = $1
		`, *commit.WorkflowDefinitionID).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("db: snapshot workflow definition %d does not exist", *commit.WorkflowDefinitionID)
			}
			return err
		}
	}

	if commit.WorkflowRunID != nil {
		var definitionID int
		err := tx.QueryRowContext(ctx, `
			SELECT workflow_definition_id
			FROM agent_workflow_runs
			WHERE id = $1 AND team_id = $2
			FOR UPDATE
		`, int64(*commit.WorkflowRunID), commit.TeamID).Scan(&definitionID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: snapshot workflow run is not owned by the producer team")
		}
		if err != nil {
			return err
		}
		if commit.WorkflowDefinitionID != nil && definitionID != *commit.WorkflowDefinitionID {
			return fmt.Errorf("db: snapshot workflow run and definition do not match")
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
		if commit.WorkflowRunID != nil {
			var boundID int64
			err := tx.QueryRowContext(ctx, `
				SELECT snapshot_id
				FROM agent_workflow_run_snapshots
				WHERE workflow_run_id = $1 AND direction = 'input' AND port_name = $2
			`, int64(*commit.WorkflowRunID), port).Scan(&boundID)
			if errors.Is(err, sql.ErrNoRows) || boundID != int64(ref.ID) {
				return fmt.Errorf("db: snapshot input %q does not match the workflow-run binding", port)
			}
			if err != nil {
				return err
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
		if staged.Digest != output.Digest || staged.TeamID != commit.Context.TeamID || staged.Attempt != commit.Context.Attempt {
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
	output snapshot.SealCommitOutput,
) (snapshot.Snapshot, error) {
	typeName, typeVersion, err := splitSnapshotType(output.Port.Type)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_snapshots
			(type_name, type_version, digest, byte_size, file_count, representation, intrinsic_metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (type_name, type_version, digest) DO NOTHING
	`, typeName, typeVersion, output.Digest.String(), output.ByteSize, output.FileCount,
		output.Representation, nullableJSON(output.IntrinsicMetadata))
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	persisted, err := scanAgentSnapshot(tx.QueryRowContext(ctx, `
		SELECT `+agentSnapshotColumns+`
		FROM agent_snapshots
		WHERE type_name = $1 AND type_version = $2 AND digest = $3
		FOR UPDATE
	`, typeName, typeVersion, output.Digest.String()))
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
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_productions
			(snapshot_id, build_id, team_id, team_name, created_by, plan_id,
			 attempt, step_kind, step_name, output_port, workflow_definition_id,
			 workflow_run_id, source_metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (build_id, plan_id, attempt, output_port) DO NOTHING
	`, int64(snapshotID), commit.BuildID, commit.TeamID, commit.TeamName, commit.CreatedBy,
		commit.PlanID, commit.Attempt, commit.StepKind, commit.StepName, output.Port.Name,
		optionalInt(commit.WorkflowDefinitionID), optionalInt64(commit.WorkflowRunID),
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
	`, commit.BuildID, commit.PlanID, commit.Attempt, output.Port.Name,
		int64(snapshotID), commit.TeamID, commit.TeamName, commit.CreatedBy,
		commit.StepKind, commit.StepName, optionalInt(commit.WorkflowDefinitionID),
		optionalInt64(commit.WorkflowRunID), nullableJSON(output.SourceMetadata)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("db: snapshot production invocation conflicts with immutable provenance")
	}
	return id, rowsAffected == 1, err
}

func insertGrant(ctx context.Context, tx Tx, commit snapshot.SealCommitContext, snapshotID snapshot.SnapshotID) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
		VALUES ($1, $2, $3, 'produced by workflow step')
		ON CONFLICT (snapshot_id, team_id) DO NOTHING
	`, int64(snapshotID), commit.TeamID, commit.CreatedBy)
	return err
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
			(snapshot_id, team_id, class, expires_at, actor, reason)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING
	`, int64(snapshotID), teamID, string(retention.Class), retention.ExpiresAt,
		retention.Actor, retention.Reason)
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
		FOR UPDATE
	`, int64(snapshotID), teamID, string(retention.Class), retention.Actor,
		retention.ExpiresAt, retention.Reason).Scan(&id)
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
		if errors.Is(err, sql.ErrNoRows) || foundID != int64(ref.ID) {
			return fmt.Errorf("db: snapshot lineage conflicts with immutable production inputs")
		}
		if err != nil {
			return err
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
			(workflow_run_id, direction, port_name, snapshot_id)
		VALUES ($1, $2, $3, $4)
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
	if errors.Is(err, sql.ErrNoRows) || foundID != int64(snapshotID) {
		return fmt.Errorf("db: workflow-run snapshot binding conflicts with immutable snapshot")
	}
	return err
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
		JOIN agent_snapshot_grants g ON g.snapshot_id = s.id AND g.team_id = $2
		WHERE s.id = $1`
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
		JOIN agent_snapshot_grants g ON g.snapshot_id = s.id
		WHERE s.id = $1 AND g.team_id = $2
	`, int64(id), teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot.Snapshot{}, false, nil
	}
	return value, err == nil, err
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
		JOIN agent_snapshot_grants g ON g.snapshot_id = s.id
		WHERE g.team_id = $1`
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
	rows, err := factory.conn.QueryContext(ctx, `
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
	if err := snapshot.RequireDigestLease(lease, digest); err != nil {
		return snapshot.DigestState{}, err
	}
	if now.IsZero() {
		return snapshot.DigestState{}, fmt.Errorf("db: snapshot digest state time is required")
	}
	return loadDigestState(ctx, factory.conn, digest, now, false)
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
	if err := snapshot.RequireDigestLease(lease, digest); err != nil {
		return err
	}
	if len(ids) == 0 {
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
	_, err := factory.conn.ExecContext(ctx, query, args...)
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
	if err := snapshot.RequireDigestLease(lease, ref.Digest); err != nil {
		return snapshot.RetentionClaim{}, err
	}
	if teamID <= 0 || strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return snapshot.RetentionClaim{}, fmt.Errorf("db: snapshot pin team, actor, and reason are required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	defer Rollback(tx)
	value, err := authorizedSnapshotByRef(ctx, tx, teamID, ref, true)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (value.Type != ref.Type || value.Digest != ref.Digest) {
		return snapshot.RetentionClaim{}, fmt.Errorf("db: snapshot pin identity is unavailable or unauthorized")
	}
	if err != nil {
		return snapshot.RetentionClaim{}, err
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
		SELECT id, snapshot_id, class, expires_at, actor, reason, created_at
		FROM agent_snapshot_retention_claims
		WHERE snapshot_id = $1 AND team_id = $2 AND class = 'pin' AND actor = $3
		FOR UPDATE
	`, int64(ref.ID), teamID, actor))
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	if claim.Reason != reason {
		return snapshot.RetentionClaim{}, fmt.Errorf("db: snapshot pin conflicts with immutable reason")
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
	if err := snapshot.RequireDigestLease(lease, ref.Digest); err != nil {
		return err
	}
	if teamID <= 0 || strings.TrimSpace(actor) == "" {
		return fmt.Errorf("db: snapshot unpin team and actor are required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	value, err := authorizedSnapshotByRef(ctx, tx, teamID, ref, true)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (value.Type != ref.Type || value.Digest != ref.Digest) {
		return fmt.Errorf("db: snapshot unpin identity is unavailable or unauthorized")
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
	if err := snapshot.RequireDigestLease(lease, digest); err != nil {
		return false, err
	}
	if now.IsZero() {
		return false, fmt.Errorf("db: snapshot expiry time is required")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
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
	if err := snapshot.RequireDigestLease(lease, location.Digest); err != nil {
		return err
	}
	result, err := factory.conn.ExecContext(ctx, `
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
		locations, err := factory.LocationsForDigest(ctx, location.Digest)
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
	if err := snapshot.RequireDigestLease(lease, location.Digest); err != nil {
		return err
	}
	_, err := factory.conn.ExecContext(ctx, `
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
			WHERE NOT EXISTS (SELECT 1 FROM agent_snapshots s WHERE s.digest = u.digest)
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
			  AND NOT EXISTS (SELECT 1 FROM agent_snapshot_locations l WHERE l.digest = s.digest)
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
