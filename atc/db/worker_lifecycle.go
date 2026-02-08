package db

import (
	"database/sql"

	sq "github.com/Masterminds/squirrel"
)

//counterfeiter:generate . WorkerLifecycle
type WorkerLifecycle interface {
	DeleteUnresponsiveEphemeralWorkers() ([]string, error)
	GetWorkerStateByName() (map[string]WorkerState, error)
}

type workerLifecycle struct {
	conn DbConn
}

func NewWorkerLifecycle(conn DbConn) WorkerLifecycle {
	return &workerLifecycle{
		conn: conn,
	}
}

func (lifecycle *workerLifecycle) DeleteUnresponsiveEphemeralWorkers() ([]string, error) {
	query, args, err := psql.Delete("workers").
		Where(sq.Eq{"ephemeral": true}).
		Where(sq.Expr("expires < NOW()")).
		Suffix("RETURNING name").
		ToSql()

	if err != nil {
		return []string{}, err
	}

	rows, err := lifecycle.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}

	return workersAffected(rows)
}

func (lifecycle *workerLifecycle) GetWorkerStateByName() (map[string]WorkerState, error) {
	rows, err := psql.Select(`
		name,
		state
	`).
		From("workers").
		RunWith(lifecycle.conn).
		Query()

	if err != nil {
		return nil, err
	}

	defer Close(rows)
	var name string
	var state WorkerState

	workerStateByName := make(map[string]WorkerState)

	for rows.Next() {
		err := rows.Scan(
			&name,
			&state,
		)
		if err != nil {
			return nil, err
		}
		workerStateByName[name] = state
	}

	return workerStateByName, nil

}
func workersAffected(rows *sql.Rows) ([]string, error) {
	var (
		err         error
		workerNames []string
	)

	defer Close(rows)

	for rows.Next() {
		var name string

		err = rows.Scan(&name)
		if err != nil {
			return nil, err
		}

		workerNames = append(workerNames, name)
	}

	return workerNames, err
}
