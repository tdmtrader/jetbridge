package db

import (
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/concourse/concourse/agent/api/reviews"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentReviewsFactory
type AgentReviewsFactory interface {
	reviews.Store
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
		Suffix(`ON CONFLICT (build_id, repo, commit_sha) DO UPDATE SET
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
	  WHERE fb.repo = r.repo AND fb.commit_sha = r.commit_sha),
	r.ticket_id, r.pipeline_run_id`

func (f *agentReviewsFactory) GetByBuild(buildID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+reviewColumns+`, r.review
		 FROM agent_reviews r WHERE r.build_id = $1 ORDER BY r.created_at ASC, r.id ASC`,
		buildID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, true)
}

func (f *agentReviewsFactory) ListByTeam(team string, filter reviews.ListFilter) ([]reviews.StoredReview, error) {
	query := `SELECT ` + reviewColumns + `
		 FROM agent_reviews r WHERE r.team_name = $1`
	args := []any{team}
	if filter.Pipeline != "" {
		args = append(args, filter.Pipeline)
		query += ` AND r.pipeline_name = $` + strconv.Itoa(len(args))
	}
	if filter.Repo != "" {
		args = append(args, filter.Repo)
		query += ` AND r.repo = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY r.created_at DESC, r.id DESC`
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

func scanReviewRows(rows *sql.Rows, withPayload bool) ([]reviews.StoredReview, error) {
	results := []reviews.StoredReview{}
	for rows.Next() {
		var rec reviews.StoredReview
		var payload []byte
		dest := []any{
			&rec.BuildID, &rec.BuildName, &rec.TeamName, &rec.PipelineName, &rec.JobName,
			&rec.Repo, &rec.CommitSha, &rec.Branch,
			&rec.Score, &rec.MaxScore, &rec.Pass, &rec.ProvenCount, &rec.ObservationCount,
			&rec.Summary, &rec.AgentModel, &rec.DurationSeconds, &rec.SubmittedBy,
			&rec.CreatedAt, &rec.EvaluatedCount,
			&rec.TicketID, &rec.PipelineRunID,
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
		results = append(results, rec)
	}
	return results, rows.Err()
}
