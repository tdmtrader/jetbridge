package db

import (
	"encoding/json"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/agent/api/feedback"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentFeedbackFactory
type AgentFeedbackFactory interface {
	feedback.Store
}

func NewAgentFeedbackFactory(conn DbConn) AgentFeedbackFactory {
	return &agentFeedbackFactory{conn: conn}
}

type agentFeedbackFactory struct {
	conn DbConn
}

func (f *agentFeedbackFactory) Save(rec *feedback.StoredFeedback) error {
	snapshotBytes, _ := json.Marshal(rec.FindingSnapshot)

	_, err := psql.Insert("agent_feedback").
		Columns(
			"repo", "commit_sha", "finding_id", "finding_type",
			"finding_snapshot", "verdict", "confidence", "notes",
			"reviewer", "source",
		).
		Values(
			rec.ReviewRef.Repo, rec.ReviewRef.Commit, rec.FindingID, rec.FindingType,
			snapshotBytes, rec.Verdict, rec.Confidence, rec.Notes,
			rec.Reviewer, rec.Source,
		).
		Suffix(`ON CONFLICT (repo, commit_sha, finding_id, reviewer) DO UPDATE SET
			verdict = EXCLUDED.verdict,
			confidence = EXCLUDED.confidence,
			notes = EXCLUDED.notes,
			finding_snapshot = EXCLUDED.finding_snapshot,
			finding_type = EXCLUDED.finding_type,
			source = EXCLUDED.source,
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentFeedbackFactory) GetByReview(repo, commit string) ([]feedback.StoredFeedback, error) {
	query := psql.Select(
		"repo", "commit_sha", "finding_id", "finding_type",
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

func (f *agentFeedbackFactory) GetAll() ([]feedback.StoredFeedback, error) {
	rows, err := psql.Select(
		"repo", "commit_sha", "finding_id", "finding_type",
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
			repo, commitSha, findingID, findingType string
			snapshotBytes                           []byte
			verdict, notes, reviewer, source        string
			confidence                              float64
		)
		err := rows.Scan(
			&repo, &commitSha, &findingID, &findingType,
			&snapshotBytes, &verdict, &confidence, &notes,
			&reviewer, &source,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, feedback.StoredFeedback{
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
		})
	}
	if results == nil {
		results = []feedback.StoredFeedback{}
	}
	return results, nil
}
