package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/lock"
)

type PipelineRunStatus string

const (
	PipelineRunRunning   PipelineRunStatus = "running"
	PipelineRunSucceeded PipelineRunStatus = "succeeded"
	PipelineRunFailed    PipelineRunStatus = "failed"
	PipelineRunErrored   PipelineRunStatus = "errored"
	PipelineRunAborted   PipelineRunStatus = "aborted"
)

//counterfeiter:generate . PipelineRun
type PipelineRun interface {
	ID() int
	TemplatePipelineID() int
	InstancePipelineID() (int, bool)
	Number() int
	Params() map[string]any
	Status() PipelineRunStatus
	CreatedBy() string
	CreatedAt() time.Time
	CompletedAt() (time.Time, bool)
	Archived() bool

	// InstancePipeline loads the instanced pipeline executing this run.
	InstancePipeline() (Pipeline, bool, error)

	// CheckComplete reports whether the run's instance pipeline is quiescent
	// (no job builds pending or started, at least one job build exists, no
	// active unpaused job awaiting scheduling) and, if so, the worst-of
	// aggregate status (errored > aborted > failed > succeeded). A run whose
	// instance pipeline has been destroyed can never become quiescent and
	// completes as errored.
	CheckComplete() (PipelineRunStatus, bool, error)

	Finish(status PipelineRunStatus) error
	Reopen() error
	Archive() error
}

type pipelineRun struct {
	conn        DbConn
	lockFactory lock.LockFactory

	id                 int
	templatePipelineID int
	instancePipelineID sql.NullInt64
	number             int
	params             map[string]any
	status             PipelineRunStatus
	createdBy          string
	createdAt          time.Time
	completedAt        sql.NullTime
	archived           bool
}

func newPipelineRun(conn DbConn, lockFactory lock.LockFactory) *pipelineRun {
	return &pipelineRun{conn: conn, lockFactory: lockFactory}
}

func (r *pipelineRun) ID() int                 { return r.id }
func (r *pipelineRun) TemplatePipelineID() int { return r.templatePipelineID }
func (r *pipelineRun) Number() int             { return r.number }
func (r *pipelineRun) Params() map[string]any  { return r.params }
func (r *pipelineRun) Status() PipelineRunStatus {
	return r.status
}
func (r *pipelineRun) CreatedBy() string    { return r.createdBy }
func (r *pipelineRun) CreatedAt() time.Time { return r.createdAt }
func (r *pipelineRun) Archived() bool       { return r.archived }

func (r *pipelineRun) InstancePipelineID() (int, bool) {
	if !r.instancePipelineID.Valid {
		return 0, false
	}
	return int(r.instancePipelineID.Int64), true
}

func (r *pipelineRun) CompletedAt() (time.Time, bool) {
	if !r.completedAt.Valid {
		return time.Time{}, false
	}
	return r.completedAt.Time, true
}

func (r *pipelineRun) InstancePipeline() (Pipeline, bool, error) {
	id, ok := r.InstancePipelineID()
	if !ok {
		return nil, false, nil
	}
	pipeline := newPipeline(r.conn, r.lockFactory)
	err := scanPipeline(
		pipeline,
		pipelinesQuery.Where(sq.Eq{"p.id": id}).RunWith(r.conn).QueryRow(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pipeline, true, nil
}

func (r *pipelineRun) CheckComplete() (PipelineRunStatus, bool, error) {
	instanceID, ok := r.InstancePipelineID()
	if !ok {
		// the instance pipeline was destroyed out from under the run
		// (instance_pipeline_id is ON DELETE SET NULL): it can never become
		// quiescent, so terminate as errored instead of staying 'running'
		// forever. (review finding, 2026-07-11)
		return PipelineRunErrored, true, nil
	}

	var active, total, unscheduled int
	err := r.conn.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE b.status IN ('pending','started')),
			COUNT(*)
		FROM builds b
		WHERE b.pipeline_id = $1 AND b.job_id IS NOT NULL`, instanceID).
		Scan(&active, &total)
	if err != nil {
		return "", false, err
	}

	err = r.conn.QueryRow(`
		SELECT COUNT(*)
		FROM jobs j
		WHERE j.pipeline_id = $1
		AND j.active AND NOT j.paused
		AND j.schedule_requested > j.last_scheduled`, instanceID).
		Scan(&unscheduled)
	if err != nil {
		return "", false, err
	}

	if active > 0 || total == 0 || unscheduled > 0 {
		return "", false, nil
	}

	rows, err := r.conn.Query(`
		SELECT DISTINCT ON (b.job_id) b.status
		FROM builds b
		WHERE b.pipeline_id = $1 AND b.job_id IS NOT NULL
		ORDER BY b.job_id, b.id DESC`, instanceID)
	if err != nil {
		return "", false, err
	}
	defer Close(rows)

	worst, worstSeverity := PipelineRunSucceeded, 1
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return "", false, err
		}
		mapped, severity := runStatusFromBuildStatus(status)
		if severity > worstSeverity {
			worst, worstSeverity = mapped, severity
		}
	}

	return worst, true, nil
}

func runStatusFromBuildStatus(status string) (PipelineRunStatus, int) {
	switch BuildStatus(status) {
	case BuildStatusErrored:
		return PipelineRunErrored, 4
	case BuildStatusAborted:
		return PipelineRunAborted, 3
	case BuildStatusFailed:
		return PipelineRunFailed, 2
	default:
		return PipelineRunSucceeded, 1
	}
}

func (r *pipelineRun) Finish(status PipelineRunStatus) error {
	_, err := psql.Update("pipeline_runs").
		Set("status", string(status)).
		Set("completed_at", sq.Expr("now()")).
		Where(sq.Eq{"id": r.id}).
		RunWith(r.conn).
		Exec()
	if err == nil {
		r.status = status
	}
	return err
}

func (r *pipelineRun) Reopen() error {
	_, err := psql.Update("pipeline_runs").
		Set("status", string(PipelineRunRunning)).
		Set("completed_at", nil).
		Where(sq.Eq{"id": r.id}).
		RunWith(r.conn).
		Exec()
	if err == nil {
		r.status = PipelineRunRunning
		r.completedAt = sql.NullTime{}
	}
	return err
}

func (r *pipelineRun) Archive() error {
	instance, found, err := r.InstancePipeline()
	if err != nil {
		return err
	}
	if found {
		// existing pipeline-archival machinery (soft-archive + GC notify)
		if err := instance.Archive(); err != nil {
			return err
		}
	}
	_, err = psql.Update("pipeline_runs").
		Set("archived", true).
		Where(sq.Eq{"id": r.id}).
		RunWith(r.conn).
		Exec()
	if err == nil {
		r.archived = true
	}
	return err
}

func scanPipelineRun(r *pipelineRun, scan scannable) error {
	var params sql.NullString
	var status string
	err := scan.Scan(&r.id, &r.templatePipelineID, &r.instancePipelineID, &r.number,
		&params, &status, &r.createdBy, &r.createdAt, &r.completedAt, &r.archived)
	if err != nil {
		return err
	}
	r.status = PipelineRunStatus(status)
	if params.Valid {
		if err := json.Unmarshal([]byte(params.String), &r.params); err != nil {
			return err
		}
	}
	return nil
}
