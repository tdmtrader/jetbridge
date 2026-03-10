package db

import (
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

//counterfeiter:generate . ComponentFactory
type ComponentFactory interface {
	CreateOrUpdate(atc.Component) (Component, error)
	Find(string) (Component, bool, error)
}

type componentFactory struct {
	conn DbConn
}

func NewComponentFactory(conn DbConn) ComponentFactory {
	return &componentFactory{
		conn: conn,
	}
}

func (f *componentFactory) Find(componentName string) (Component, bool, error) {
	c := &component{
		conn: f.conn,
	}

	row := componentsQuery.
		Where(sq.Eq{"c.name": componentName}).
		RunWith(f.conn).
		QueryRow()

	err := scanComponent(c, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	return c, true, nil
}

func (f *componentFactory) CreateOrUpdate(c atc.Component) (Component, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}

	defer Rollback(tx)

	obj := &component{
		conn: f.conn,
	}

	row := psql.Insert("components").
		Columns("name").
		Values(c.Name).
		Suffix(`
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
			RETURNING id, name
		`).
		RunWith(tx).
		QueryRow()

	err = scanComponent(obj, row)
	if err != nil {
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return obj, nil
}
