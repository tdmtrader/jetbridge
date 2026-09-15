package db

import (
	"fmt"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/lock"
)

type PipelineFactory interface {
	PipelinePage(teamNames []string, admin bool, team, query string, after, limit int) ([]Pipeline, error)
	VisiblePipelines([]string) ([]Pipeline, error)
	AllPipelines() ([]Pipeline, error)
	PipelinesToSchedule() ([]Pipeline, error)
}

type pipelineFactory struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

func NewPipelineFactory(conn DbConn, lockFactory lock.LockFactory) PipelineFactory {
	return &pipelineFactory{
		conn:        conn,
		lockFactory: lockFactory,
	}
}

func (f *pipelineFactory) VisiblePipelines(teamNames []string) ([]Pipeline, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}

	defer Rollback(tx)

	rows, err := pipelinesQuery.
		Where(sq.Eq{"t.name": teamNames}).
		Where(sq.Eq{"p.pipeline_run_id": nil}).
		OrderBy("t.name ASC", "p.ordering ASC", "p.secondary_ordering ASC").
		RunWith(tx).
		Query()
	if err != nil {
		return nil, err
	}

	currentTeamPipelines, err := scanPipelines(f.conn, f.lockFactory, rows)
	if err != nil {
		return nil, err
	}

	rows, err = pipelinesQuery.
		Where(sq.NotEq{"t.name": teamNames}).
		Where(sq.Eq{"public": true}).
		Where(sq.Eq{"p.pipeline_run_id": nil}).
		OrderBy("t.name ASC", "p.ordering ASC", "p.id ASC").
		RunWith(tx).
		Query()
	if err != nil {
		return nil, err
	}

	otherTeamPublicPipelines, err := scanPipelines(f.conn, f.lockFactory, rows)
	if err != nil {
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return append(currentTeamPipelines, otherTeamPublicPipelines...), nil
}

func (f *pipelineFactory) AllPipelines() ([]Pipeline, error) {
	rows, err := pipelinesQuery.
		Where(sq.Eq{"p.pipeline_run_id": nil}).
		OrderBy("t.name ASC", "p.ordering ASC", "p.secondary_ordering ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}

	return scanPipelines(f.conn, f.lockFactory, rows)
}

func (f *pipelineFactory) PipelinesToSchedule() ([]Pipeline, error) {
	rows, err := pipelinesQuery.
		Join("jobs j ON j.pipeline_id = p.id").
		Where(sq.Eq{"p.template": false}).
		Where(sq.Expr("j.schedule_requested > j.last_scheduled")).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}

	return scanPipelines(f.conn, f.lockFactory, rows)
}

// PipelinePage applies the same public/team visibility as VisiblePipelines before
// literal name filtering and a stable, live ID page. It excludes run payloads.
func (f *pipelineFactory) PipelinePage(teamNames []string, admin bool, team, query string, after, limit int) ([]Pipeline, error) {
	if limit < 1 || limit > 101 || after < 0 {
		return nil, fmt.Errorf("invalid pipeline page")
	}
	q := pipelinesQuery.Where(sq.Eq{"p.pipeline_run_id": nil}).Where(sq.Gt{"p.id": after})
	if !admin {
		q = q.Where(sq.Or{sq.Eq{"t.name": teamNames}, sq.Eq{"p.public": true}})
	}
	if team != "" {
		q = q.Where(sq.Eq{"t.name": team})
	}
	if query != "" {
		q = q.Where(sq.Expr("strpos(lower(p.name),lower(?)) > 0", query))
	}
	rows, err := q.OrderBy("p.id ASC").Limit(uint64(limit)).RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	return scanPipelines(f.conn, f.lockFactory, rows)
}
