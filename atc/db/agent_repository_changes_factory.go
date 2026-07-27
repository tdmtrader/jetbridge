package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/repodiff"
	"github.com/concourse/concourse/agent/snapshot"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentRepositoryChangesFactory
type AgentRepositoryChangesFactory interface {
	projection.RepositoryChangeStore
	GetRepositoryChangeProjection(context.Context, snapshot.SnapshotID) (projection.RepositoryChange, bool, error)
}

func NewAgentRepositoryChangesFactory(conn DbConn) AgentRepositoryChangesFactory {
	return NewAgentRepositoryChangesFactoryForTeam(conn, "main")
}

func NewAgentRepositoryChangesFactoryForTeam(conn DbConn, teamName string) AgentRepositoryChangesFactory {
	return &agentRepositoryChangesFactory{conn: conn, ticketTeamName: teamName}
}

type agentRepositoryChangesFactory struct {
	conn           DbConn
	ticketTeamName string
}

func (factory *agentRepositoryChangesFactory) FindRepositoryChangeInput(ctx context.Context, id snapshot.SnapshotID) (projection.RepositoryChangeProjectionInput, bool, error) {
	if err := id.Validate(); err != nil {
		return projection.RepositoryChangeProjectionInput{}, false, err
	}
	manifest, err := scanAgentSnapshot(factory.conn.QueryRowContext(ctx, `
		SELECT `+agentSnapshotColumns+`
		FROM agent_snapshots
		WHERE id = $1 AND type_name = 'repository-change' AND type_version = 1
	`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return projection.RepositoryChangeProjectionInput{}, false, nil
	}
	if err != nil {
		return projection.RepositoryChangeProjectionInput{}, false, err
	}
	input := projection.RepositoryChangeProjectionInput{Snapshot: manifest, Inputs: []projection.RepositoryChangeBaseInput{}}
	var productionID int64
	err = factory.conn.QueryRowContext(ctx, `
		SELECT production.id
		FROM agent_snapshot_productions production
		JOIN agent_snapshots subject ON subject.id = production.snapshot_id
		WHERE production.snapshot_id = $1
		  AND NULLIF(subject.intrinsic_metadata->>'repository_id', '') IS NOT NULL
		  AND NULLIF(subject.intrinsic_metadata->>'base_sha', '') IS NOT NULL
		  AND EXISTS (
			SELECT 1
			FROM agent_snapshot_lineage lineage
			JOIN agent_snapshots base ON base.id = lineage.input_snapshot_id
			WHERE lineage.production_id = production.id
			  AND base.type_name = 'repository' AND base.type_version = 1
			  AND base.intrinsic_metadata->>'repository_id' = subject.intrinsic_metadata->>'repository_id'
			  AND base.intrinsic_metadata->>'head_sha' = subject.intrinsic_metadata->>'base_sha'
		  )
		ORDER BY production.created_at, production.id
		LIMIT 1
	`, int64(id)).Scan(&productionID)
	if errors.Is(err, sql.ErrNoRows) {
		// Preserve the subject so the projector can record a durable invalid
		// status after re-reading its canonical document. Returning lineage from
		// a semantically different occurrence would fabricate a base instead.
		return input, true, nil
	}
	if err != nil {
		return projection.RepositoryChangeProjectionInput{}, false, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT input_port, input_snapshot_id
		FROM agent_snapshot_lineage
		WHERE production_id = $1
		ORDER BY position
	`, productionID)
	if err != nil {
		return projection.RepositoryChangeProjectionInput{}, false, err
	}
	type lineageInput struct {
		port string
		id   int64
	}
	lineage := make([]lineageInput, 0)
	for rows.Next() {
		var value lineageInput
		if err := rows.Scan(&value.port, &value.id); err != nil {
			Close(rows)
			return projection.RepositoryChangeProjectionInput{}, false, err
		}
		lineage = append(lineage, value)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return projection.RepositoryChangeProjectionInput{}, false, err
	}
	Close(rows)
	for _, value := range lineage {
		base, err := scanAgentSnapshot(factory.conn.QueryRowContext(ctx, `
			SELECT `+agentSnapshotColumns+` FROM agent_snapshots WHERE id = $1
		`, value.id))
		if err != nil {
			return projection.RepositoryChangeProjectionInput{}, false, err
		}
		input.Inputs = append(input.Inputs, projection.RepositoryChangeBaseInput{Port: value.port, Snapshot: base})
	}
	return input, true, nil
}

func (factory *agentRepositoryChangesFactory) ListUnprojectedRepositoryChanges(ctx context.Context, limit int) ([]snapshot.SnapshotRef, error) {
	if limit <= 0 || limit > 10_000 {
		return nil, fmt.Errorf("db: repository-change projection limit must be between 1 and 10000")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT s.id, s.type_name, s.type_version, s.digest
		FROM agent_snapshots s
		LEFT JOIN agent_repository_change_projections p ON p.snapshot_id = s.id
		WHERE s.type_name = 'repository-change' AND s.type_version = 1
		  AND s.content_state = 'available'
		  AND (p.snapshot_id IS NULL OR p.status IN ('pending', 'unavailable'))
		ORDER BY s.created_at, s.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	refs := make([]snapshot.SnapshotRef, 0)
	for rows.Next() {
		var id int64
		var name, digest string
		var version int
		if err := rows.Scan(&id, &name, &version, &digest); err != nil {
			return nil, err
		}
		typeRef, err := joinSnapshotType(name, version)
		if err != nil {
			return nil, err
		}
		ref := snapshot.SnapshotRef{ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest)}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("db: invalid repository-change candidate: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (factory *agentRepositoryChangesFactory) SetRepositoryChangeProjectionStatus(ctx context.Context, id snapshot.SnapshotID, status projection.RepositoryChangeProjectionStatus, reason string) error {
	if err := id.Validate(); err != nil {
		return err
	}
	switch status {
	case projection.RepositoryChangeProjectionPending:
		if reason != "" {
			return fmt.Errorf("db: pending repository-change projection must not have an error")
		}
	case projection.RepositoryChangeProjectionUnavailable, projection.RepositoryChangeProjectionInvalid:
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("db: failed repository-change projection requires an error")
		}
	default:
		return fmt.Errorf("db: status %q must be written through a ready projection upsert", status)
	}
	if len(reason) > 4096 {
		return fmt.Errorf("db: repository-change projection error exceeds 4096 bytes")
	}
	result, err := factory.conn.ExecContext(ctx, `
		INSERT INTO agent_repository_change_projections (snapshot_id, status, projection_error)
		VALUES ($1, $2, $3)
		ON CONFLICT (snapshot_id) DO UPDATE SET
			status = CASE WHEN agent_repository_change_projections.status = 'ready'
			              THEN agent_repository_change_projections.status ELSE EXCLUDED.status END,
			projection_error = CASE WHEN agent_repository_change_projections.status = 'ready'
			                        THEN agent_repository_change_projections.projection_error ELSE EXCLUDED.projection_error END,
			updated_at = now()
	`, int64(id), string(status), reason)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return errors.Join(err, fmt.Errorf("db: repository-change status update affected %d rows", affected))
	}
	return nil
}

func (factory *agentRepositoryChangesFactory) UpsertRepositoryChangeProjection(ctx context.Context, value projection.RepositoryChange) error {
	if err := validateReadyRepositoryChange(value); err != nil {
		return err
	}
	files := value.Files
	if files == nil {
		files = []repodiff.ChangedFile{}
	}
	encodedFiles, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("db: encode repository-change files: %w", err)
	}
	var resultSHA any
	if value.ResultSHA != "" {
		resultSHA = value.ResultSHA
	}
	_, err = factory.conn.ExecContext(ctx, `
		INSERT INTO agent_repository_change_projections
			(snapshot_id, status, projection_error, repository_id, base_sha, result_sha,
			 result_tree_sha, representation, files, file_count, lines_added, lines_deleted,
			 unified_diff, truncated, truncation_reason)
		VALUES ($1, 'ready', '', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (snapshot_id) DO UPDATE SET
			status = 'ready', projection_error = '', repository_id = EXCLUDED.repository_id,
			base_sha = EXCLUDED.base_sha, result_sha = EXCLUDED.result_sha,
			result_tree_sha = EXCLUDED.result_tree_sha, representation = EXCLUDED.representation,
			files = EXCLUDED.files, file_count = EXCLUDED.file_count,
			lines_added = EXCLUDED.lines_added, lines_deleted = EXCLUDED.lines_deleted,
			unified_diff = EXCLUDED.unified_diff, truncated = EXCLUDED.truncated,
			truncation_reason = EXCLUDED.truncation_reason, updated_at = now()
	`, int64(value.SnapshotID), value.RepositoryID, value.BaseSHA, resultSHA,
		value.ResultTreeSHA, value.Representation, encodedFiles, value.FileCount,
		value.LinesAdded, value.LinesDeleted, value.UnifiedDiff, value.Truncated, value.TruncationReason)
	return err
}

func (factory *agentRepositoryChangesFactory) GetRepositoryChangeProjection(ctx context.Context, id snapshot.SnapshotID) (projection.RepositoryChange, bool, error) {
	if err := id.Validate(); err != nil {
		return projection.RepositoryChange{}, false, err
	}
	var status, projectionError string
	var repositoryID, baseSHA, resultSHA, resultTreeSHA, representation sql.NullString
	var files []byte
	var fileCount, linesAdded, linesDeleted sql.NullInt64
	var unifiedDiff, truncationReason sql.NullString
	var truncated sql.NullBool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT status, projection_error, repository_id, base_sha, result_sha,
		       result_tree_sha, representation, files, file_count, lines_added,
		       lines_deleted, unified_diff, truncated, truncation_reason
		FROM agent_repository_change_projections WHERE snapshot_id = $1
	`, int64(id)).Scan(
		&status, &projectionError, &repositoryID, &baseSHA, &resultSHA,
		&resultTreeSHA, &representation, &files, &fileCount, &linesAdded,
		&linesDeleted, &unifiedDiff, &truncated, &truncationReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return projection.RepositoryChange{}, false, nil
	}
	if err != nil {
		return projection.RepositoryChange{}, false, err
	}
	value := projection.RepositoryChange{
		SnapshotID: id, Status: projection.RepositoryChangeProjectionStatus(status), ProjectionError: projectionError,
		RepositoryID: repositoryID.String, BaseSHA: baseSHA.String, ResultSHA: resultSHA.String,
		ResultTreeSHA: resultTreeSHA.String, Representation: representation.String,
		FileCount: int(fileCount.Int64), LinesAdded: int(linesAdded.Int64), LinesDeleted: int(linesDeleted.Int64),
		UnifiedDiff: unifiedDiff.String, Truncated: truncated.Bool, TruncationReason: truncationReason.String,
		Files: []repodiff.ChangedFile{},
	}
	if len(files) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(files)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value.Files); err != nil {
			return projection.RepositoryChange{}, false, fmt.Errorf("db: decode persisted repository-change files: %w", err)
		}
	}
	switch value.Status {
	case projection.RepositoryChangeProjectionPending, projection.RepositoryChangeProjectionUnavailable, projection.RepositoryChangeProjectionInvalid:
		return value, true, nil
	case projection.RepositoryChangeProjectionReady:
		if err := validateReadyRepositoryChange(value); err != nil {
			return projection.RepositoryChange{}, false, fmt.Errorf("db: invalid ready repository-change projection: %w", err)
		}
		return value, true, nil
	default:
		return projection.RepositoryChange{}, false, fmt.Errorf("db: invalid repository-change projection status %q", value.Status)
	}
}

func validateReadyRepositoryChange(value projection.RepositoryChange) error {
	if err := value.SnapshotID.Validate(); err != nil {
		return err
	}
	if value.Status != projection.RepositoryChangeProjectionReady || value.ProjectionError != "" {
		return fmt.Errorf("db: repository-change projection must be ready without an error")
	}
	if strings.TrimSpace(value.RepositoryID) == "" || strings.TrimSpace(value.BaseSHA) == "" ||
		strings.TrimSpace(value.ResultTreeSHA) == "" || strings.TrimSpace(value.Representation) == "" {
		return fmt.Errorf("db: repository-change projection identity is incomplete")
	}
	if value.FileCount < 0 || value.LinesAdded < 0 || value.LinesDeleted < 0 || value.FileCount != len(value.Files) {
		return fmt.Errorf("db: repository-change projection counts are invalid")
	}
	if len(value.UnifiedDiff) > repodiff.BoundedUnifiedDiffBytes {
		return fmt.Errorf("db: repository-change unified diff exceeds 65536 bytes")
	}
	if value.Truncated && strings.TrimSpace(value.TruncationReason) == "" {
		return fmt.Errorf("db: truncated repository-change projection requires a reason")
	}
	if !value.Truncated && value.TruncationReason != "" {
		return fmt.Errorf("db: untruncated repository-change projection must not have a truncation reason")
	}
	return nil
}

var _ AgentRepositoryChangesFactory = (*agentRepositoryChangesFactory)(nil)
