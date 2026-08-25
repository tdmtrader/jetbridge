package db

import (
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/lock"
)

type ResourceFactory interface {
	Resource(int) (Resource, bool, error)
	VisibleResources([]string) ([]Resource, error)
	AllResources() ([]Resource, error)
}

type resourceFactory struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

func NewResourceFactory(conn DbConn, lockFactory lock.LockFactory) ResourceFactory {
	return &resourceFactory{
		conn:        conn,
		lockFactory: lockFactory,
	}
}

func (r *resourceFactory) Resource(resourceID int) (Resource, bool, error) {
	resource := newEmptyResource(r.conn, r.lockFactory)
	row := resourcesQuery.
		Where(sq.Eq{"r.id": resourceID}).
		RunWith(r.conn).
		QueryRow()

	err := scanResource(resource, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	return resource, true, nil
}

func (r *resourceFactory) VisibleResources(teamNames []string) ([]Resource, error) {
	// Run payload pipelines are excluded from both cluster-wide resource
	// enumerations so that GET /api/v1/resources -- which is unpaginated --
	// scales with #templates + #regular pipelines, never #runs. This is NOT
	// interchangeable with `p.template = false`: a template shell never runs,
	// so the scheduling and checking surfaces exclude it by that column, but a
	// run payload DOES run and those surfaces must keep it (check_factory.go
	// :173, :218). Use pipeline_run_id only where the response is a list a
	// human reads.
	rows, err := resourcesQuery.
		Where(sq.Or{
			sq.Eq{"t.name": teamNames},
			sq.And{
				sq.NotEq{"t.name": teamNames},
				sq.Eq{"p.public": true},
			},
		}).
		Where(sq.Eq{"p.pipeline_run_id": nil}).
		OrderBy("r.id ASC").
		RunWith(r.conn).
		Query()
	if err != nil {
		return nil, err
	}

	return scanResources(rows, r.conn, r.lockFactory)
}

func (r *resourceFactory) AllResources() ([]Resource, error) {
	rows, err := resourcesQuery.
		Where(sq.Eq{"p.pipeline_run_id": nil}).
		OrderBy("r.id ASC").
		RunWith(r.conn).
		Query()
	if err != nil {
		return nil, err
	}

	return scanResources(rows, r.conn, r.lockFactory)
}

func scanResources(resourceRows *sql.Rows, conn DbConn, lockFactory lock.LockFactory) ([]Resource, error) {
	var resources []Resource

	for resourceRows.Next() {
		resource := newEmptyResource(conn, lockFactory)
		err := scanResource(resource, resourceRows)
		if err != nil {
			return nil, err
		}

		resources = append(resources, resource)
	}

	return resources, nil
}
