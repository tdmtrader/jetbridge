package db

import (
	sq "github.com/Masterminds/squirrel"
)

// All scheduler-debt writers enter the owning Run boundary before job rows.
// Resource changes can affect multiple Runs; lock their headers in ID order and
// skip cancelled owners without suppressing unaffected consumers of the resource.
func requestScheduleJobs(tx Tx, jobIDs []int, strict bool) error {
	if len(jobIDs) == 0 {
		return nil
	}
	owners := psql.Select("p.pipeline_run_id").From("pipelines p").Join("jobs j ON j.pipeline_id=p.id").Where(sq.Eq{"j.id": jobIDs})
	rows, err := psql.Select("r.id").From("pipeline_runs r").Where(sq.Expr("r.id IN (?)", owners)).OrderBy("r.id").Suffix("FOR UPDATE OF r").RunWith(tx).Query()
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			Close(rows)
			return err
		}
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return err
	}
	rows, err = psql.Select("j.id", "coalesce(r.cancel_requested_at IS NOT NULL,false)").From("jobs j").Join("pipelines p ON p.id=j.pipeline_id").LeftJoin("pipeline_runs r ON r.id=p.pipeline_run_id").Where(sq.Eq{"j.id": jobIDs}).OrderBy("j.id").RunWith(tx).Query()
	if err != nil {
		return err
	}
	var admitted []int
	cancelled := false
	for rows.Next() {
		var id int
		var fenced bool
		if err := rows.Scan(&id, &fenced); err != nil {
			Close(rows)
			return err
		}
		if fenced {
			cancelled = true
		} else {
			admitted = append(admitted, id)
		}
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return err
	}
	if strict && cancelled {
		return ErrPipelineRunCancelling
	}
	if strict && len(admitted) != len(jobIDs) {
		return NonOneRowAffectedError{int64(len(admitted))}
	}
	for _, id := range admitted {
		if _, err := psql.Update("jobs").Set("schedule_requested", sq.Expr("now()")).Where(sq.Eq{"id": id}).RunWith(tx).Exec(); err != nil {
			return err
		}
	}
	if len(admitted) > 0 {
		_, err = tx.Exec("SELECT pg_notify('scheduler',$1)", intsToCSV(admitted))
	}
	return err
}
