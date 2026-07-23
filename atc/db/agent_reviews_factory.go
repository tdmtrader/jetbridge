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

func (f *agentReviewsFactory) UpsertReviewProjection(ctx context.Context, rec *reviews.StoredReview) error {
	if rec == nil || rec.SnapshotID == nil || rec.ProductionID == nil {
		return fmt.Errorf("db: projected review requires snapshot and production IDs")
	}
	if err := rec.SnapshotID.Validate(); err != nil {
		return err
	}
	var buildID any
	if rec.BuildID > 0 {
		buildID = rec.BuildID
	}
	_, err := psql.Insert("agent_reviews").
		Columns(
			"build_id", "build_name", "team_name", "pipeline_name", "job_name",
			"repo", "commit_sha", "branch",
			"score", "max_score", "pass", "proven_count", "observation_count",
			"summary", "agent_model", "duration_seconds", "submitted_by", "review",
			"ticket_id", "pipeline_run_id", "snapshot_id", "workflow_run_id", "production_id",
		).
		Values(
			buildID, rec.BuildName, rec.TeamName, rec.PipelineName, rec.JobName,
			rec.Repo, rec.CommitSha, rec.Branch,
			rec.Score, rec.MaxScore, rec.Pass, rec.ProvenCount, rec.ObservationCount,
			rec.Summary, rec.AgentModel, rec.DurationSeconds, rec.SubmittedBy, []byte(rec.Review),
			rec.TicketID, rec.PipelineRunID, int64(*rec.SnapshotID), optionalWorkflowRunID(rec.WorkflowRunID), *rec.ProductionID,
		).
		Suffix(`ON CONFLICT (snapshot_id) WHERE snapshot_id IS NOT NULL DO UPDATE SET
			build_id = EXCLUDED.build_id,
			build_name = EXCLUDED.build_name,
			team_name = EXCLUDED.team_name,
			pipeline_name = EXCLUDED.pipeline_name,
			job_name = EXCLUDED.job_name,
			repo = EXCLUDED.repo,
			commit_sha = EXCLUDED.commit_sha,
			branch = EXCLUDED.branch,
			score = EXCLUDED.score,
			max_score = EXCLUDED.max_score,
			pass = EXCLUDED.pass,
			proven_count = EXCLUDED.proven_count,
			observation_count = EXCLUDED.observation_count,
			summary = EXCLUDED.summary,
			agent_model = EXCLUDED.agent_model,
			duration_seconds = EXCLUDED.duration_seconds,
			submitted_by = EXCLUDED.submitted_by,
			review = EXCLUDED.review,
			ticket_id = COALESCE(EXCLUDED.ticket_id, agent_reviews.ticket_id),
			pipeline_run_id = COALESCE(EXCLUDED.pipeline_run_id, agent_reviews.pipeline_run_id),
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

func NewAgentReviewsFactory(conn DbConn) AgentReviewsFactory {
	return &agentReviewsFactory{conn: conn}
}

type agentReviewsFactory struct {
	conn DbConn
}

func (f *agentReviewsFactory) Upsert(rec *reviews.StoredReview) error {
	_, err := psql.Insert("agent_reviews").
		Columns(
			"build_id", "build_name", "team_name", "pipeline_name", "job_name",
			"repo", "commit_sha", "branch",
			"score", "max_score", "pass", "proven_count", "observation_count",
			"summary", "agent_model", "duration_seconds", "submitted_by", "review",
			"ticket_id", "pipeline_run_id",
		).
		Values(
			rec.BuildID, rec.BuildName, rec.TeamName, rec.PipelineName, rec.JobName,
			rec.Repo, rec.CommitSha, rec.Branch,
			rec.Score, rec.MaxScore, rec.Pass, rec.ProvenCount, rec.ObservationCount,
			rec.Summary, rec.AgentModel, rec.DurationSeconds, rec.SubmittedBy, []byte(rec.Review),
			rec.TicketID, rec.PipelineRunID,
		).
		Suffix(`ON CONFLICT (build_id, repo, commit_sha)
			WHERE snapshot_id IS NULL
			DO UPDATE SET
			build_name = EXCLUDED.build_name,
			team_name = EXCLUDED.team_name,
			pipeline_name = EXCLUDED.pipeline_name,
			job_name = EXCLUDED.job_name,
			branch = EXCLUDED.branch,
			score = EXCLUDED.score,
			max_score = EXCLUDED.max_score,
			pass = EXCLUDED.pass,
			proven_count = EXCLUDED.proven_count,
			observation_count = EXCLUDED.observation_count,
			summary = EXCLUDED.summary,
			agent_model = EXCLUDED.agent_model,
			duration_seconds = EXCLUDED.duration_seconds,
			submitted_by = EXCLUDED.submitted_by,
			review = EXCLUDED.review,
			ticket_id = COALESCE(EXCLUDED.ticket_id, agent_reviews.ticket_id),
			pipeline_run_id = COALESCE(EXCLUDED.pipeline_run_id, agent_reviews.pipeline_run_id),
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

const reviewColumns = `r.build_id, r.build_name, r.team_name, r.pipeline_name, r.job_name,
	r.repo, r.commit_sha, r.branch,
	r.score, r.max_score, r.pass, r.proven_count, r.observation_count,
	r.summary, r.agent_model, r.duration_seconds, r.submitted_by,
	EXTRACT(EPOCH FROM r.created_at)::bigint,
	(SELECT COUNT(DISTINCT fb.finding_id) FROM agent_feedback fb
	  WHERE (r.snapshot_id IS NOT NULL AND fb.review_snapshot_id = r.snapshot_id)
	     OR (r.snapshot_id IS NULL AND fb.review_snapshot_id IS NULL
	         AND fb.repo = r.repo AND fb.commit_sha = r.commit_sha)),
	r.ticket_id, r.pipeline_run_id, r.snapshot_id, r.workflow_run_id, r.production_id`

// projectedReviewColumns deliberately takes all occurrence fields from the
// exact production selected by the query. agent_reviews is one decoded value
// projection per snapshot; productions are the durable occurrence history.
const projectedReviewColumns = `p.build_id, COALESCE(b.name, ''), authorized_team.name,
	COALESCE(pipe.name, ''), COALESCE(j.name, ''),
	r.repo, r.commit_sha, r.branch,
	r.score, r.max_score, r.pass, r.proven_count, r.observation_count,
	r.summary, r.agent_model, r.duration_seconds, COALESCE(p.created_by, ''),
	EXTRACT(EPOCH FROM COALESCE(p.created_at, r.created_at))::bigint,
	(SELECT COUNT(DISTINCT fb.finding_id) FROM agent_feedback fb
	  WHERE fb.review_snapshot_id = r.snapshot_id
	    AND fb.review_team_id = authorized_team.id),
	COALESCE(ticket.id, r.ticket_id), COALESCE(wr.pipeline_run_id, r.pipeline_run_id),
	r.snapshot_id, p.workflow_run_id, p.id`

const contextualReviewColumns = `COALESCE(p.build_id, r.build_id),
	CASE WHEN r.snapshot_id IS NULL THEN r.build_name ELSE COALESCE(b.name, '') END,
	COALESCE(p.team_name, r.team_name),
	CASE WHEN r.snapshot_id IS NULL THEN r.pipeline_name ELSE COALESCE(pipe.name, '') END,
	CASE WHEN r.snapshot_id IS NULL THEN r.job_name ELSE COALESCE(j.name, '') END,
	r.repo, r.commit_sha, r.branch,
	r.score, r.max_score, r.pass, r.proven_count, r.observation_count,
	r.summary, r.agent_model, r.duration_seconds,
	CASE WHEN r.snapshot_id IS NULL THEN r.submitted_by ELSE COALESCE(p.created_by, '') END,
	EXTRACT(EPOCH FROM COALESCE(p.created_at, r.created_at))::bigint,
	(SELECT COUNT(DISTINCT fb.finding_id) FROM agent_feedback fb
	  WHERE (r.snapshot_id IS NOT NULL AND fb.review_snapshot_id = r.snapshot_id
	         AND fb.review_team_id = p.team_id)
	     OR (r.snapshot_id IS NULL AND fb.review_snapshot_id IS NULL
	         AND fb.repo = r.repo AND fb.commit_sha = r.commit_sha)),
	COALESCE(ticket.id, r.ticket_id), COALESCE(wr.pipeline_run_id, r.pipeline_run_id),
	r.snapshot_id, p.workflow_run_id, p.id`

func (f *agentReviewsFactory) GetByBuild(buildID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+contextualReviewColumns+`, r.review
		 FROM agent_reviews r
		 LEFT JOIN agent_snapshot_productions p
		   ON r.snapshot_id IS NOT NULL AND p.snapshot_id = r.snapshot_id AND p.build_id = $1
		 LEFT JOIN builds b ON b.id = p.build_id
		 LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		 LEFT JOIN jobs j ON j.id = b.job_id
		 LEFT JOIN agent_workflow_runs wr ON wr.id = p.workflow_run_id
		 LEFT JOIN agent_tickets ticket ON ticket.workflow_run_id = wr.id
		 WHERE (r.snapshot_id IS NULL AND r.build_id = $1) OR p.id IS NOT NULL
		 ORDER BY COALESCE(p.created_at, r.created_at), COALESCE(p.id, r.id)`,
		buildID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, true)
}

func (f *agentReviewsFactory) ListByTeam(team string, filter reviews.ListFilter) ([]reviews.StoredReview, error) {
	query := `SELECT ` + contextualReviewColumns + `
		 FROM agent_reviews r
		 LEFT JOIN agent_snapshot_productions p
		   ON r.snapshot_id IS NOT NULL AND p.snapshot_id = r.snapshot_id AND p.team_name = $1
		 LEFT JOIN builds b ON b.id = p.build_id
		 LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		 LEFT JOIN jobs j ON j.id = b.job_id
		 LEFT JOIN agent_workflow_runs wr ON wr.id = p.workflow_run_id
		 LEFT JOIN agent_tickets ticket ON ticket.workflow_run_id = wr.id
		 WHERE ((r.snapshot_id IS NULL AND r.team_name = $1) OR p.id IS NOT NULL)`
	args := []any{team}
	if filter.Pipeline != "" {
		args = append(args, filter.Pipeline)
		placeholder := `$` + strconv.Itoa(len(args))
		query += ` AND ((r.snapshot_id IS NULL AND r.pipeline_name = ` + placeholder + `)
		               OR (r.snapshot_id IS NOT NULL AND pipe.name = ` + placeholder + `))`
	}
	if filter.Repo != "" {
		args = append(args, filter.Repo)
		query += ` AND r.repo = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY COALESCE(p.created_at, r.created_at) DESC, COALESCE(p.id, r.id) DESC`
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

func (f *agentReviewsFactory) ListByTicket(ticketID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+reviewColumns+`
		 FROM agent_reviews r WHERE r.ticket_id = $1 ORDER BY r.created_at ASC, r.id ASC`,
		ticketID,
	)
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
		`SELECT `+projectedReviewColumns+`, r.review
		 FROM agent_reviews r
		 JOIN teams authorized_team ON authorized_team.name = $1
		 JOIN agent_snapshot_grants grant_row
		   ON grant_row.snapshot_id = r.snapshot_id AND grant_row.team_id = authorized_team.id
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
		 LEFT JOIN agent_tickets ticket ON ticket.workflow_run_id = wr.id
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
		`SELECT `+projectedReviewColumns+`, r.review
		 FROM agent_workflow_runs wr
		 JOIN teams authorized_team ON authorized_team.id = wr.team_id AND authorized_team.name = $1
		 JOIN agent_snapshot_productions p ON p.workflow_run_id = wr.id
		 JOIN agent_reviews r ON r.snapshot_id = p.snapshot_id
		 JOIN agent_snapshot_grants grant_row
		   ON grant_row.snapshot_id = r.snapshot_id AND grant_row.team_id = wr.team_id
		 LEFT JOIN builds b ON b.id = p.build_id
		 LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		 LEFT JOIN jobs j ON j.id = b.job_id
		 LEFT JOIN agent_tickets ticket ON ticket.workflow_run_id = wr.id
		 WHERE wr.id = $2 AND wr.workflow_name = $3
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
		input                  = projection.ReviewInput{Snapshot: manifest}
		workflowRunID, buildID sql.NullInt64
		pipelineRunID          sql.NullInt64
	)
	err = f.conn.QueryRowContext(ctx, `
		SELECT p.id, p.workflow_run_id, p.build_id, COALESCE(b.name, ''),
		       p.team_name, COALESCE(pipe.name, ''), COALESCE(j.name, ''),
		       wr.pipeline_run_id, p.created_by
		FROM agent_snapshot_productions p
		LEFT JOIN builds b ON b.id = p.build_id
		LEFT JOIN pipelines pipe ON pipe.id = b.pipeline_id
		LEFT JOIN jobs j ON j.id = b.job_id
		LEFT JOIN agent_workflow_runs wr ON wr.id = p.workflow_run_id
		WHERE p.snapshot_id = $1
		ORDER BY (p.workflow_run_id IS NULL), p.created_at, p.id
		LIMIT 1
	`, int64(id)).Scan(
		&input.ProductionID, &workflowRunID, &buildID, &input.BuildName,
		&input.TeamName, &input.PipelineName, &input.JobName,
		&pipelineRunID, &input.SubmittedBy,
	)
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
	if buildID.Valid {
		value := int(buildID.Int64)
		input.BuildID = &value
	}
	if pipelineRunID.Valid {
		value := int(pipelineRunID.Int64)
		input.PipelineRunID = &value
	}
	return input, true, nil
}

func scanReviewRows(rows *sql.Rows, withPayload bool) ([]reviews.StoredReview, error) {
	results := []reviews.StoredReview{}
	for rows.Next() {
		var rec reviews.StoredReview
		var payload []byte
		var buildID sql.NullInt64
		dest := []any{
			&buildID, &rec.BuildName, &rec.TeamName, &rec.PipelineName, &rec.JobName,
			&rec.Repo, &rec.CommitSha, &rec.Branch,
			&rec.Score, &rec.MaxScore, &rec.Pass, &rec.ProvenCount, &rec.ObservationCount,
			&rec.Summary, &rec.AgentModel, &rec.DurationSeconds, &rec.SubmittedBy,
			&rec.CreatedAt, &rec.EvaluatedCount,
			&rec.TicketID, &rec.PipelineRunID, &rec.SnapshotID, &rec.WorkflowRunID, &rec.ProductionID,
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
		results = append(results, rec)
	}
	return results, rows.Err()
}
