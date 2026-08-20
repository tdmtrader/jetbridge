package db

import (
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc"
)

type PipelineRunReclaimLifecycle interface {
	ReclaimCandidateRunIDs(limit int) ([]int, error)
	DestroyReclaimableRun(runID int) (bool, error)
	DeferRunReclaim(runID int, retryAt time.Time) error
}

func NewPipelineRunReclaimLifecycle(conn DbConn) PipelineRunReclaimLifecycle {
	return &pipelineRunReclaimLifecycle{conn: conn}
}

type pipelineRunReclaimLifecycle struct {
	conn DbConn
}

func (l *pipelineRunReclaimLifecycle) ReclaimCandidateRunIDs(limit int) ([]int, error) {
	if limit <= 0 {
		return nil, nil
	}
	numberIDs, err := l.candidateIDs(`
		SELECT r.id
		FROM pipeline_runs r
		JOIN pipelines template ON template.id = r.template_pipeline_id
		WHERE template.run_retention_keep_last IS NOT NULL
		  AND r.status IN ('succeeded', 'failed', 'errored', 'aborted')
		  AND r.number <= template.last_run_number - template.run_retention_keep_last
		  AND (r.reclaim_retry_after IS NULL OR r.reclaim_retry_after <= now())
		  AND EXISTS (SELECT 1 FROM pipelines child WHERE child.pipeline_run_id = r.id)
		ORDER BY r.number, r.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	ageIDs, err := l.candidateIDs(`
		SELECT r.id
		FROM pipeline_runs r
		JOIN pipelines template ON template.id = r.template_pipeline_id
		WHERE template.run_retention_ttl_days IS NOT NULL
		  AND r.status IN ('succeeded', 'failed', 'errored', 'aborted')
		  AND r.completed_at < now() - template.run_retention_ttl_days * interval '1 day'
		  AND (r.reclaim_retry_after IS NULL OR r.reclaim_retry_after <= now())
		  AND EXISTS (SELECT 1 FROM pipelines child WHERE child.pipeline_run_id = r.id)
		ORDER BY r.completed_at, r.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int, 0, limit)
	seen := make(map[int]struct{}, limit)
	for _, candidates := range [][]int{numberIDs, ageIDs} {
		for _, id := range candidates {
			if _, found := seen[id]; found {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) == limit {
				return ids, nil
			}
		}
	}
	return ids, nil
}

func (l *pipelineRunReclaimLifecycle) candidateIDs(query string, limit int) ([]int, error) {
	rows, err := l.conn.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (l *pipelineRunReclaimLifecycle) DestroyReclaimableRun(runID int) (bool, error) {
	tx, err := l.conn.Begin()
	if err != nil {
		return false, err
	}
	defer Rollback(tx)

	var templateID int
	err = tx.QueryRow(`
		SELECT template.id
		FROM pipelines template
		JOIN pipeline_runs run ON run.template_pipeline_id = template.id
		WHERE run.id = $1
		FOR SHARE OF template
	`, runID).Scan(&templateID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	run, err := lockPipelineRun(tx, runID)
	if err == ErrPipelineRunNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if run.Status() == atc.RunStatusRunning {
		return false, nil
	}
	payloadID, found := run.InstancePipelineID()
	if !found {
		return false, nil
	}

	var eligible bool
	err = tx.QueryRow(`
		SELECT
		  (template.run_retention_keep_last IS NOT NULL
		    AND run.number <= template.last_run_number - template.run_retention_keep_last)
		  OR
		  (template.run_retention_ttl_days IS NOT NULL
		    AND run.completed_at < now() - template.run_retention_ttl_days * interval '1 day')
		FROM pipeline_runs run
		JOIN pipelines template ON template.id = run.template_pipeline_id
		WHERE run.id = $1 AND template.id = $2
	`, runID, templateID).Scan(&eligible)
	if err == sql.ErrNoRows || !eligible {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var blocked bool
	err = tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM builds
			WHERE pipeline_run_id = $1
			  AND run_job_name IS NOT NULL
			  AND status IN ('pending', 'started')
		)
	`, runID).Scan(&blocked)
	if err != nil {
		return false, err
	}
	if blocked {
		return false, nil
	}

	_, err = tx.Exec(`
		UPDATE builds
		SET job_id = NULL, pipeline_id = NULL
		WHERE pipeline_run_id = $1 AND run_job_name IS NOT NULL
	`, runID)
	if err != nil {
		return false, err
	}
	result, err := tx.Exec(`DELETE FROM pipelines WHERE id = $1 AND pipeline_run_id = $2`, payloadID, runID)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if deleted != 1 {
		return false, nil
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (l *pipelineRunReclaimLifecycle) DeferRunReclaim(runID int, retryAt time.Time) error {
	_, err := l.conn.Exec(`UPDATE pipeline_runs SET reclaim_retry_after = $2 WHERE id = $1`, runID, retryAt)
	return err
}

func rejectPipelineRunPayloadMutation(tx Tx, pipelineID int) error {
	_, isPayload, err := lockPipelineRunForPayload(tx, pipelineID)
	if err != nil {
		return err
	}
	if isPayload {
		return ErrPipelineRunPayloadMutation
	}
	return nil
}
