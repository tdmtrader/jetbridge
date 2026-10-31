package metric

import (
	"database/sql"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db"
)

// InstrumentDB counts every query made through conn and publishes its open
// connections on each periodic tick.
func InstrumentDB(conn db.DbConn) db.DbConn {
	instrumented := &countingConn{DbConn: conn}
	Metrics.Databases = append(Metrics.Databases, instrumented)
	return instrumented
}

type countingConn struct {
	db.DbConn
}

func (e *countingConn) Query(query string, args ...any) (*sql.Rows, error) {
	Metrics.DatabaseQueries.Inc()

	return e.DbConn.Query(query, args...)
}

func (e *countingConn) QueryRow(query string, args ...any) squirrel.RowScanner {
	Metrics.DatabaseQueries.Inc()

	return e.DbConn.QueryRow(query, args...)
}

func (e *countingConn) Exec(query string, args ...any) (sql.Result, error) {
	Metrics.DatabaseQueries.Inc()

	return e.DbConn.Exec(query, args...)
}

func (e *countingConn) Begin() (db.Tx, error) {
	tx, err := e.DbConn.Begin()
	if err != nil {
		return tx, err
	}

	return &countingTx{Tx: tx}, nil
}

type countingTx struct {
	db.Tx
}

func (e *countingTx) Query(query string, args ...any) (*sql.Rows, error) {
	Metrics.DatabaseQueries.Inc()

	return e.Tx.Query(query, args...)
}

func (e *countingTx) QueryRow(query string, args ...any) squirrel.RowScanner {
	Metrics.DatabaseQueries.Inc()

	return e.Tx.QueryRow(query, args...)
}

func (e *countingTx) Exec(query string, args ...any) (sql.Result, error) {
	Metrics.DatabaseQueries.Inc()

	return e.Tx.Exec(query, args...)
}
