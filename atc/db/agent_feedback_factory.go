package db

import (
	"database/sql"
	"encoding/json"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/snapshot"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentFeedbackFactory
type AgentFeedbackFactory interface {
	feedback.Store
	feedback.SnapshotStore
}

func NewAgentFeedbackFactory(conn DbConn) AgentFeedbackFactory {
	return &agentFeedbackFactory{conn: conn}
}

type agentFeedbackFactory struct {
	conn DbConn
}

func (f *agentFeedbackFactory) Save(rec *feedback.StoredFeedback) error {
	snapshotBytes, _ := json.Marshal(rec.FindingSnapshot)
	if rec.ReviewSnapshotID != nil {
		if err := rec.ReviewSnapshotID.Validate(); err != nil {
			return err
		}
		if rec.ReviewTeamName == "" {
			return fmt.Errorf("db: projected feedback requires a review team")
		}
		result, err := f.conn.Exec(`
			INSERT INTO agent_feedback
				(review_snapshot_id, review_team_id, repo, commit_sha, finding_id, finding_type,
				 finding_snapshot, verdict, confidence, notes, reviewer, source, ticket_id)
			SELECT r.snapshot_id, t.id, r.repo, r.commit_sha, $3, $4, $5, $6, $7, $8, $9, $10, r.ticket_id
			FROM agent_reviews r
			JOIN teams t ON t.name = $2
			JOIN agent_snapshot_grants g ON g.snapshot_id = r.snapshot_id AND g.team_id = t.id
			WHERE r.snapshot_id = $1
			ON CONFLICT (review_snapshot_id, review_team_id, finding_id, reviewer)
				WHERE review_snapshot_id IS NOT NULL
			DO UPDATE SET
				verdict = EXCLUDED.verdict,
				confidence = EXCLUDED.confidence,
				notes = EXCLUDED.notes,
				finding_snapshot = EXCLUDED.finding_snapshot,
				finding_type = EXCLUDED.finding_type,
				source = EXCLUDED.source,
				repo = EXCLUDED.repo,
				commit_sha = EXCLUDED.commit_sha,
				ticket_id = COALESCE(EXCLUDED.ticket_id, agent_feedback.ticket_id),
				updated_at = now()
		`, int64(*rec.ReviewSnapshotID), rec.ReviewTeamName, rec.FindingID, rec.FindingType,
			snapshotBytes, rec.Verdict, rec.Confidence, rec.Notes, rec.Reviewer, rec.Source)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("%w: snapshot %s", feedback.ErrReviewProjectionNotFound, rec.ReviewSnapshotID.String())
		}
		return nil
	}

	_, err := psql.Insert("agent_feedback").
		Columns(
			"repo", "commit_sha", "finding_id", "finding_type",
			"finding_snapshot", "verdict", "confidence", "notes",
			"reviewer", "source",
			"ticket_id",
		).
		Values(
			rec.ReviewRef.Repo, rec.ReviewRef.Commit, rec.FindingID, rec.FindingType,
			snapshotBytes, rec.Verdict, rec.Confidence, rec.Notes,
			rec.Reviewer, rec.Source,
			sq.Expr(`(SELECT ticket_id FROM agent_reviews
			          WHERE repo = ? AND commit_sha = ?
			          ORDER BY id DESC LIMIT 1)`, rec.ReviewRef.Repo, rec.ReviewRef.Commit),
		).
		Suffix(`ON CONFLICT (repo, commit_sha, finding_id, reviewer)
			WHERE review_snapshot_id IS NULL
			DO UPDATE SET
			verdict = EXCLUDED.verdict,
			confidence = EXCLUDED.confidence,
			notes = EXCLUDED.notes,
			finding_snapshot = EXCLUDED.finding_snapshot,
			finding_type = EXCLUDED.finding_type,
			source = EXCLUDED.source,
			ticket_id = COALESCE(EXCLUDED.ticket_id, agent_feedback.ticket_id),
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentFeedbackFactory) GetByReview(repo, commit string) ([]feedback.StoredFeedback, error) {
	query := psql.Select(
		"review_snapshot_id", "(SELECT name FROM teams WHERE id = review_team_id)", "repo", "commit_sha", "finding_id", "finding_type",
		"finding_snapshot", "verdict", "confidence", "notes",
		"reviewer", "source",
	).From("agent_feedback")

	if repo != "" {
		query = query.Where(sq.Eq{"repo": repo})
	}
	if commit != "" {
		query = query.Where(sq.Eq{"commit_sha": commit})
	}

	query = query.OrderBy("created_at ASC")

	rows, err := query.RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanFeedbackRows(rows)
}

func (f *agentFeedbackFactory) GetByReviewSnapshot(id snapshot.SnapshotID, teamName string) ([]feedback.StoredFeedback, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	rows, err := f.conn.Query(`
		SELECT fb.review_snapshot_id, t.name, fb.repo, fb.commit_sha, fb.finding_id, fb.finding_type,
		       fb.finding_snapshot, fb.verdict, fb.confidence, fb.notes, fb.reviewer, fb.source
		FROM agent_feedback fb
		JOIN teams t ON t.id = fb.review_team_id AND t.name = $2
		WHERE fb.review_snapshot_id = $1
		ORDER BY fb.created_at ASC
	`, int64(id), teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeedbackRows(rows)
}

func (f *agentFeedbackFactory) GetAll() ([]feedback.StoredFeedback, error) {
	rows, err := psql.Select(
		"review_snapshot_id", "(SELECT name FROM teams WHERE id = review_team_id)", "repo", "commit_sha", "finding_id", "finding_type",
		"finding_snapshot", "verdict", "confidence", "notes",
		"reviewer", "source",
	).From("agent_feedback").
		OrderBy("created_at DESC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanFeedbackRows(rows)
}

func scanFeedbackRows(rows interface {
	Next() bool
	Scan(dest ...any) error
}) ([]feedback.StoredFeedback, error) {
	var results []feedback.StoredFeedback
	for rows.Next() {
		var (
			reviewSnapshotID                        sql.NullInt64
			reviewTeamName                          sql.NullString
			repo, commitSha, findingID, findingType string
			snapshotBytes                           []byte
			verdict, notes, reviewer, source        string
			confidence                              float64
		)
		err := rows.Scan(
			&reviewSnapshotID,
			&reviewTeamName,
			&repo, &commitSha, &findingID, &findingType,
			&snapshotBytes, &verdict, &confidence, &notes,
			&reviewer, &source,
		)
		if err != nil {
			return nil, err
		}
		record := feedback.StoredFeedback{
			ReviewRef: feedback.ReviewRef{
				Repo:   repo,
				Commit: commitSha,
			},
			FindingID:       findingID,
			FindingType:     findingType,
			FindingSnapshot: json.RawMessage(snapshotBytes),
			Verdict:         verdict,
			Confidence:      confidence,
			Notes:           notes,
			Reviewer:        reviewer,
			Source:          source,
		}
		if reviewSnapshotID.Valid {
			value := snapshot.SnapshotID(reviewSnapshotID.Int64)
			record.ReviewSnapshotID = &value
		}
		if reviewTeamName.Valid {
			record.ReviewTeamName = reviewTeamName.String
		}
		results = append(results, record)
	}
	if results == nil {
		results = []feedback.StoredFeedback{}
	}
	return results, nil
}
