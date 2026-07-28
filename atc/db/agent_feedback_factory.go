package db

import (
	"encoding/json"
	"fmt"

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

// Save writes one human verdict on one finding of one sealed review snapshot.
//
// The INSERT ... SELECT is also the authorization check: it produces a row only
// when the review projection exists AND the named team owns its
// snapshot, so a caller cannot record feedback against a review it cannot see.
// Zero rows affected therefore means "no such review for this team", never a
// silent no-op.
func (f *agentFeedbackFactory) Save(rec *feedback.StoredFeedback) error {
	snapshotBytes, _ := json.Marshal(rec.FindingSnapshot)
	if err := rec.ReviewSnapshotID.Validate(); err != nil {
		return err
	}
	if rec.ReviewTeamName == "" {
		return fmt.Errorf("db: feedback requires a review team")
	}
	result, err := f.conn.Exec(`
		INSERT INTO agent_feedback
			(review_snapshot_id, review_team_id, finding_id, finding_type,
			 finding_snapshot, verdict, confidence, notes, reviewer, source)
		SELECT r.snapshot_id, t.id, $3, $4, $5, $6, $7, $8, $9, $10
		FROM agent_reviews r
		JOIN teams t ON t.name = $2
		JOIN agent_snapshots snapshot ON snapshot.id = r.snapshot_id AND snapshot.team_id = t.id
		WHERE r.snapshot_id = $1
		ON CONFLICT (review_snapshot_id, review_team_id, finding_id, reviewer)
		DO UPDATE SET
			verdict = EXCLUDED.verdict,
			confidence = EXCLUDED.confidence,
			notes = EXCLUDED.notes,
			finding_snapshot = EXCLUDED.finding_snapshot,
			finding_type = EXCLUDED.finding_type,
			source = EXCLUDED.source,
			updated_at = now()
	`, int64(rec.ReviewSnapshotID), rec.ReviewTeamName, rec.FindingID, rec.FindingType,
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

func (f *agentFeedbackFactory) GetByReviewSnapshot(id snapshot.SnapshotID, teamName string) ([]feedback.StoredFeedback, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	rows, err := f.conn.Query(`
		SELECT fb.review_snapshot_id, t.name, fb.finding_id, fb.finding_type,
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

func scanFeedbackRows(rows interface {
	Next() bool
	Scan(dest ...any) error
}) ([]feedback.StoredFeedback, error) {
	var results []feedback.StoredFeedback
	for rows.Next() {
		var (
			reviewSnapshotID                 int64
			reviewTeamName                   string
			findingID, findingType           string
			snapshotBytes                    []byte
			verdict, notes, reviewer, source string
			confidence                       float64
		)
		err := rows.Scan(
			&reviewSnapshotID,
			&reviewTeamName,
			&findingID, &findingType,
			&snapshotBytes, &verdict, &confidence, &notes,
			&reviewer, &source,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, feedback.StoredFeedback{
			ReviewSnapshotID: snapshot.SnapshotID(reviewSnapshotID),
			ReviewTeamName:   reviewTeamName,
			FindingID:        findingID,
			FindingType:      findingType,
			FindingSnapshot:  json.RawMessage(snapshotBytes),
			Verdict:          verdict,
			Confidence:       confidence,
			Notes:            notes,
			Reviewer:         reviewer,
			Source:           source,
		})
	}
	if results == nil {
		results = []feedback.StoredFeedback{}
	}
	return results, nil
}
