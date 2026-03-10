package db

import (
	"database/sql"

	sq "github.com/Masterminds/squirrel"
)

var componentsQuery = psql.Select("c.id, c.name").
	From("components c")

//counterfeiter:generate . Component
type Component interface {
	ID() int
	Name() string

	Reload() (bool, error)
}

type component struct {
	id   int
	name string

	conn DbConn
}

func (c *component) ID() int      { return c.id }
func (c *component) Name() string { return c.name }

func (c *component) Reload() (bool, error) {
	row := componentsQuery.Where(sq.Eq{"c.id": c.id}).
		RunWith(c.conn).
		QueryRow()

	err := scanComponent(c, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func scanComponent(c *component, row scannable) error {
	err := row.Scan(
		&c.id,
		&c.name,
	)
	if err != nil {
		return err
	}

	return nil
}
