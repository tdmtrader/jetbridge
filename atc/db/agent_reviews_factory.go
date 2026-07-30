package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/snapshot"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentReviewsFactory
type AgentReviewsFactory interface {
	reviews.Store
	reviews.ProjectionReader
	projection.ReviewStore
}

// UpsertReviewProjection is the only writer of agent_reviews. A review row
// exists because a review/v1 snapshot was sealed and projected, and the
// snapshot is its identity.
func (f *agentReviewsFactory) UpsertReviewProjection(ctx context.Context, rec *reviews.StoredReview) error {
	if rec == nil || rec.ProductionID == nil {
		return fmt.Errorf("db: projected review requires snapshot and production IDs")
	}
	if err := rec.SnapshotID.Validate(); err != nil {
		return err
	}
	severityCounts, err := json.Marshal(nonNilCounts(rec.SeverityCounts))
	if err != nil {
		return err
	}
	// Only the record's own judgment and its snapshot/production links are
	// stored. WHERE a review was produced is never copied onto the row: one
	// snapshot can be produced in several builds and runs, so a single column
	// could only ever name one of them, and every read below resolves the
	// occurrence from the production it selected.
	_, err = psql.Insert("agent_reviews").
		Columns(
			"conclusion", "summary", "severity_counts", "review",
			"snapshot_id", "workflow_run_id", "production_id",
		).
		Values(
			rec.Conclusion, rec.Summary, severityCounts, []byte(rec.Review),
			int64(rec.SnapshotID), optionalWorkflowRunID(rec.WorkflowRunID), *rec.ProductionID,
		).
		Suffix(`ON CONFLICT (snapshot_id) DO UPDATE SET
			conclusion = EXCLUDED.conclusion,
			summary = EXCLUDED.summary,
			severity_counts = EXCLUDED.severity_counts,
			review = EXCLUDED.review,
			workflow_run_id = COALESCE(EXCLUDED.workflow_run_id, agent_reviews.workflow_run_id),
			production_id = COALESCE(agent_reviews.production_id, EXCLUDED.production_id),
			updated_at = now()`).
		RunWith(f.conn).
		ExecContext(ctx)
	return err
}

func optionalWorkflowRunID(id *snapshot.WorkflowRunID) any {
	if id == nil {
		return nil
	}
	return int64(*id)
}

func nonNilCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return map[string]int{}
	}
	return counts
}

func NewAgentReviewsFactory(conn DbConn) AgentReviewsFactory {
	return &agentReviewsFactory{conn: conn}
}

type agentReviewsFactory struct {
	conn DbConn
}

// reviewProjectionColumns is the ONE read shape. agent_reviews holds one
// decoded value per sealed review snapshot; the occurrence fields — build,
// pipeline, job, publisher, timestamp — come from the exact
// agent_snapshot_productions row the query selected, never from a copy on the
// review row, because a single snapshot can be produced in several builds and
// runs and only the selected occurrence is the one being rendered.
const reviewProjectionColumns = `p.build_id, COALESCE(b.name, ''), authorized_team.name,
	COALESCE(pipe.name, ''), COALESCE(j.name, ''), COALESCE(wr.workflow_name, ''),
	r.conclusion, r.summary, r.severity_counts, COALESCE(p.created_by, ''),
	EXTRACT(EPOCH FROM COALESCE(p.created_at, r.created_at))::bigint,
	(SELECT COUNT(DISTINCT fb.finding_id) FROM agent_feedback fb
	  WHERE fb.review_snapshot_id = r.snapshot_id
	    AND fb.review_team_id = authorized_team.id),
	r.snapshot_id, p.workflow_run_id, p.id`

// reviewOccurrenceJoins resolves one production occurrence into the names a
// reader sees. The build, pipeline and job joins are LEFT because an upload
// occurrence has no build at all.
const reviewOccurrenceJoins = `
	JOIN teams authorized_team ON authorized_team.id = p.team_id
	LEFT JOIN builds b ON b.id = p.build_id
	LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
	LEFT JOIN jobs j ON j.id = b.job_id
	LEFT JOIN agent_workflow_runs wr ON wr.id = p.workflow_run_id`

func (f *agentReviewsFactory) GetByBuild(buildID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+reviewProjectionColumns+`, r.review
		 FROM agent_reviews r
		 JOIN agent_snapshot_productions p
		   ON p.snapshot_id = r.snapshot_id AND p.build_id = $1`+
			reviewOccurrenceJoins+`
		 ORDER BY p.created_at, p.id`,
		buildID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, true)
}

func (f *agentReviewsFactory) ListByTeam(team string, filter reviews.ListFilter) ([]reviews.StoredReview, error) {
	query := `SELECT ` + reviewProjectionColumns + `
		 FROM agent_reviews r
		 JOIN agent_snapshot_productions p
		   ON p.snapshot_id = r.snapshot_id AND p.team_name = $1` +
		reviewOccurrenceJoins
	args := []any{team}
	if filter.Pipeline != "" {
		args = append(args, filter.Pipeline)
		query += ` WHERE pipe.name = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY p.created_at DESC, p.id DESC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += ` LIMIT $` + strconv.Itoa(len(args))
	}

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, false)
}

func (f *agentReviewsFactory) GetBySnapshot(teamName string, id snapshot.SnapshotID) (reviews.StoredReview, bool, error) {
	if err := id.Validate(); err != nil {
		return reviews.StoredReview{}, false, err
	}
	rows, err := f.conn.Query(
		`SELECT `+reviewProjectionColumns+`, r.review
		 FROM agent_reviews r
		 JOIN teams authorized_team ON authorized_team.name = $1
		 JOIN agent_snapshots snapshot
		   ON snapshot.id = r.snapshot_id AND snapshot.team_id = authorized_team.id
		 LEFT JOIN LATERAL (
		   SELECT production.*
		   FROM agent_snapshot_productions production
		   WHERE production.snapshot_id = r.snapshot_id
		     AND production.team_id = authorized_team.id
		   ORDER BY (production.workflow_run_id IS NULL), production.created_at, production.id
		   LIMIT 1
		 ) p ON true
		 LEFT JOIN builds b ON b.id = p.build_id
		 LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		 LEFT JOIN jobs j ON j.id = b.job_id
		 LEFT JOIN agent_workflow_runs wr ON wr.id = p.workflow_run_id
		 WHERE r.snapshot_id = $2`, teamName, int64(id),
	)
	if err != nil {
		return reviews.StoredReview{}, false, err
	}
	defer rows.Close()
	values, err := scanReviewRows(rows, true)
	if err != nil || len(values) == 0 {
		return reviews.StoredReview{}, false, err
	}
	return values[0], true, nil
}

func (f *agentReviewsFactory) ListByWorkflowRun(teamName, workflowName string, id snapshot.WorkflowRunID) ([]reviews.StoredReview, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	rows, err := f.conn.Query(
		`SELECT `+reviewProjectionColumns+`, r.review
		 FROM agent_workflow_runs wr
		 JOIN teams authorized_team ON authorized_team.id = wr.team_id AND authorized_team.name = $1
		 JOIN agent_snapshot_productions p ON p.workflow_run_id = wr.id
		 JOIN agent_reviews r ON r.snapshot_id = p.snapshot_id
		 JOIN agent_snapshots snapshot
		   ON snapshot.id = r.snapshot_id AND snapshot.team_id = wr.team_id
		 LEFT JOIN builds b ON b.id = p.build_id
		 LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		 LEFT JOIN jobs j ON j.id = b.job_id
		 WHERE wr.id = $2 AND wr.workflow_name = $3 AND wr.definition_kind = 'workflow'
		 ORDER BY p.created_at ASC, p.id ASC`, teamName, int64(id), workflowName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, true)
}

func (f *agentReviewsFactory) ListUnprojectedReviews(ctx context.Context, limit int) ([]snapshot.SnapshotRef, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("db: review projection limit must be positive")
	}
	rows, err := f.conn.QueryContext(ctx, `
		SELECT s.id, s.digest
		FROM agent_snapshots s
		WHERE s.type_name = 'review' AND s.type_version = 1
		  AND s.content_state = 'available'
		  AND EXISTS (SELECT 1 FROM agent_snapshot_productions p WHERE p.snapshot_id = s.id)
		  AND NOT EXISTS (SELECT 1 FROM agent_reviews r WHERE r.snapshot_id = s.id)
		ORDER BY s.created_at, s.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]snapshot.SnapshotRef, 0)
	for rows.Next() {
		var id int64
		var digest string
		if err := rows.Scan(&id, &digest); err != nil {
			return nil, err
		}
		ref := snapshot.SnapshotRef{ID: snapshot.SnapshotID(id), Type: "review/v1", Digest: snapshot.Digest(digest)}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("db: invalid review projection candidate: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (f *agentReviewsFactory) FindReviewInput(ctx context.Context, id snapshot.SnapshotID) (projection.ReviewInput, bool, error) {
	if err := id.Validate(); err != nil {
		return projection.ReviewInput{}, false, err
	}
	manifest, err := scanAgentSnapshot(f.conn.QueryRowContext(ctx, `
		SELECT `+agentSnapshotColumns+` FROM agent_snapshots WHERE id = $1
	`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return projection.ReviewInput{}, false, nil
	}
	if err != nil {
		return projection.ReviewInput{}, false, err
	}
	var (
		input         = projection.ReviewInput{Snapshot: manifest}
		workflowRunID sql.NullInt64
	)
	err = f.conn.QueryRowContext(ctx, `
		SELECT p.id, p.workflow_run_id, p.team_name
		FROM agent_snapshot_productions p
		WHERE p.snapshot_id = $1
		ORDER BY (p.workflow_run_id IS NULL), p.created_at, p.id
		LIMIT 1
	`, int64(id)).Scan(&input.ProductionID, &workflowRunID, &input.TeamName)
	if errors.Is(err, sql.ErrNoRows) {
		return projection.ReviewInput{}, false, nil
	}
	if err != nil {
		return projection.ReviewInput{}, false, err
	}
	if workflowRunID.Valid {
		value := snapshot.WorkflowRunID(workflowRunID.Int64)
		input.WorkflowRunID = &value
	}
	return input, true, nil
}

func scanReviewRows(rows *sql.Rows, withPayload bool) ([]reviews.StoredReview, error) {
	results := []reviews.StoredReview{}
	for rows.Next() {
		var rec reviews.StoredReview
		var payload []byte
		var buildID sql.NullInt64
		var severityCounts []byte
		dest := []any{
			&buildID, &rec.BuildName, &rec.TeamName, &rec.PipelineName, &rec.JobName, &rec.WorkflowName,
			&rec.Conclusion, &rec.Summary, &severityCounts, &rec.SubmittedBy,
			&rec.CreatedAt, &rec.EvaluatedCount,
			&rec.SnapshotID, &rec.WorkflowRunID, &rec.ProductionID,
		}
		if withPayload {
			dest = append(dest, &payload)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if withPayload {
			rec.Review = json.RawMessage(payload)
		}
		if buildID.Valid {
			rec.BuildID = int(buildID.Int64)
		}
		rec.SeverityCounts = map[string]int{}
		if len(severityCounts) > 0 {
			if err := json.Unmarshal(severityCounts, &rec.SeverityCounts); err != nil {
				return nil, fmt.Errorf("db: invalid stored review severity counts: %w", err)
			}
		}
		results = append(results, rec)
	}
	return results, rows.Err()
}
