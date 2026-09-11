package controller

// The controller's database handle.
//
// It is a plain *sql.DB and NOT the ATC's DbConn, and that is a deliberate
// narrowing rather than a shortcut. A controller must not run migrations: Req 57
// makes the activation epoch the thing that attests compatible migrations, and
// a controller that migrated on startup would be a second migrator racing the
// web node over the schema it is supposed to be reading. It also needs no
// encryption strategy, no lock factory and no notification bus beyond the one
// channel it listens on, so taking the ATC's connection would be taking four
// capabilities to use none of them.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// The pgx stdlib driver, registered as "pgx", which is the name every other
	// PostgreSQL caller in this repository opens with.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/concourse/concourse/hangar/output"
)

// OpenDatabase opens one controller's connection.
//
// The pool is deliberately tiny. A controller does one bounded unit per wake
// under one lease, so a second concurrent transaction is not work it has -- it
// is a bug it would hide, and a pool that allowed one would let a method that
// needs two connections deadlock invisibly in production instead of failing in
// a suite.
func OpenDatabase(dsn string, maxConns int) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("%w: a controller needs a database connection string",
			output.ErrIncomplete)
	}
	if maxConns <= 0 {
		maxConns = 2
	}

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: opening the controller database: %v",
			output.ErrInfrastructure, err)
	}
	conn.SetMaxOpenConns(maxConns)
	conn.SetMaxIdleConns(maxConns)
	conn.SetConnMaxLifetime(time.Hour)

	return conn, nil
}

// SQLTransactor adapts a *sql.DB to the Transactor port.
//
// Commit is mapped through the caller's own classifier, because two of this
// plane's constraint triggers are DEFERRED and their refusals therefore arrive
// at COMMIT and nowhere earlier. A controller handed an unclassified commit
// failure would read a denial -- the policy gate, say -- as an ambiguous commit
// and retry it forever.
type SQLTransactor struct {
	DB *sql.DB

	// CommitError classifies a commit failure in the output leaf's vocabulary.
	// It is a function rather than an import so that this package, which the
	// three controllers share, does not name the database package they wire.
	CommitError func(error) error
}

func (transactor SQLTransactor) Begin() (Transaction, error) {
	tx, err := transactor.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: beginning a controller transaction: %v",
			output.ErrInfrastructure, err)
	}

	return sqlTransaction{tx: tx, commitError: transactor.CommitError}, nil
}

type sqlTransaction struct {
	tx          *sql.Tx
	commitError func(error) error
}

func (transaction sqlTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return transaction.tx.ExecContext(ctx, query, args...)
}

func (transaction sqlTransaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return transaction.tx.QueryContext(ctx, query, args...)
}

func (transaction sqlTransaction) Commit() error {
	err := transaction.tx.Commit()
	if transaction.commitError == nil {
		return err
	}

	return transaction.commitError(err)
}

func (transaction sqlTransaction) Rollback() error { return transaction.tx.Rollback() }
