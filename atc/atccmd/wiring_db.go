// The database plumbing the web composition root wires first: the encryption
// key every connection encrypts columns with, constructDBConn (one named pool,
// counted by the metrics and query-logged under --log-db-queries), and
// constructLockConns, the single-connection pools the lock factory holds its
// advisory locks on. The migrate command shares constructLockConns.

package atccmd

import (
	"database/sql"
	"fmt"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
)

func (cmd *RunCommand) newKey() *encryption.Key {
	var newKey *encryption.Key
	if cmd.EncryptionKey.AEAD != nil {
		newKey = encryption.NewKey(cmd.EncryptionKey.AEAD)
	}
	return newKey
}

func (cmd *RunCommand) oldKey() *encryption.Key {
	var oldKey *encryption.Key
	if cmd.OldEncryptionKey.AEAD != nil {
		oldKey = encryption.NewKey(cmd.OldEncryptionKey.AEAD)
	}
	return oldKey
}

func (cmd *RunCommand) constructDBConn(
	driverName string,
	logger lager.Logger,
	maxConns int,
	idleConns int,
	connectionName string,
	lockFactory lock.LockFactory,
) (db.DbConn, error) {
	dbConn, err := db.Open(logger.Session("db"), driverName, cmd.Postgres.ConnectionString(), cmd.newKey(), cmd.oldKey(), connectionName, lockFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %s", err)
	}

	// Instrument with Metrics
	dbConn = metric.InstrumentDB(dbConn)

	// Instrument with Logging
	if cmd.LogDBQueries {
		dbConn = db.Log(logger.Session("log-dbconn"), dbConn)
	}

	// Prepare
	dbConn.SetMaxOpenConns(maxConns)
	dbConn.SetMaxIdleConns(idleConns)

	return dbConn, nil
}

type Closer interface {
	Close() error
}

func constructLockConns(driverName, connectionString string) ([lock.FactoryCount]*sql.DB, error) {
	conns := [lock.FactoryCount]*sql.DB{}
	for i := range lock.FactoryCount {
		dbConn, err := sql.Open(driverName, connectionString)
		if err != nil {
			return conns, err
		}

		dbConn.SetMaxOpenConns(1)
		dbConn.SetMaxIdleConns(1)
		dbConn.SetConnMaxLifetime(0)

		conns[i] = dbConn
	}
	return conns, nil
}
