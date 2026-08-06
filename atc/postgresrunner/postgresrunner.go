package postgresrunner

import (
	"context"
	"database/sql"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/gomega"
)

func (runner *Runner) MigrateToVersion(version int) {
	err := migration.NewOpenHelper(
		"pgx",
		runner.DataSourceName(),
		nil,
		nil,
		nil,
	).MigrateToVersion(version)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func (runner *Runner) TryOpenDBAtVersion(version int) (*sql.DB, error) {
	dsn, err := runner.activeDSN()
	if err != nil {
		return nil, err
	}
	dbConn, err := migration.NewOpenHelper(
		"pgx",
		dsn,
		nil,
		nil,
		nil,
	).OpenAtVersion(version)
	if err != nil {
		return nil, err
	}

	// only allow one connection so that we can detect any code paths that
	// require more than one, which will deadlock if it's at the limit
	dbConn.SetMaxOpenConns(1)

	return dbConn, nil
}

func (runner *Runner) OpenDBAtVersion(version int) *sql.DB {
	dbConn, err := runner.TryOpenDBAtVersion(version)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return dbConn
}

func (runner *Runner) OpenDB() *sql.DB {
	dbConn, err := migration.NewOpenHelper(
		"pgx",
		runner.DataSourceName(),
		nil,
		nil,
		nil,
	).Open()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	// only allow one connection so that we can detect any code paths that
	// require more than one, which will deadlock if it's at the limit
	dbConn.SetMaxOpenConns(1)
	dbConn.SetMaxIdleConns(1)

	return dbConn
}

func (runner *Runner) OpenConn() db.DbConn {
	dsn, err := runner.activeDSN()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	dbConn, err := db.Open(
		lagertest.NewTestLogger("postgres-runner"),
		"pgx",
		dsn,
		nil,
		nil,
		"postgresrunner",
		nil,
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	// only allow one connection so that we can detect any code paths that
	// require more than one, which will deadlock if it's at the limit
	dbConn.SetMaxOpenConns(1)
	dbConn.SetMaxIdleConns(1)

	return joinLimitValidatorConn{dbConn}
}

func (runner *Runner) OpenSingleton() *sql.DB {
	dbConn, err := sql.Open("pgx", runner.DataSourceName())
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	// only allow one connection, period. this matches production code use case,
	// as this is used for advisory locks.
	dbConn.SetMaxIdleConns(1)
	dbConn.SetMaxOpenConns(1)
	dbConn.SetConnMaxLifetime(0)

	return dbConn
}

func (runner *Runner) DataSourceName() string {
	dsn, err := runner.activeDSN()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return dsn
}

func (runner *Runner) CreateEmptyTestDB() {
	ExpectWithOffset(1, runner.createEmptyTestDB(context.Background())).To(Succeed())
}

func (runner *Runner) CreateTestDBFromTemplate() {
	ExpectWithOffset(1, runner.createTestDBFromTemplate(context.Background())).To(Succeed())
}

func (runner *Runner) DropTestDB() {
	ExpectWithOffset(1, runner.dropTestDB(context.Background())).To(Succeed())
}

func (runner *Runner) Truncate() {
	config, err := pgx.ParseConfig(runner.DataSourceName())
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(context.Background(), config)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_, truncateErr := conn.Exec(context.Background(), truncateSQL)
	closeErr := conn.Close(context.Background())
	ExpectWithOffset(1, truncateErr).NotTo(HaveOccurred())
	ExpectWithOffset(1, closeErr).NotTo(HaveOccurred())
}

const truncateSQL = `
	SET client_min_messages TO WARNING;

	CREATE OR REPLACE FUNCTION truncate_tables() RETURNS void AS $$
	DECLARE
		statements CURSOR FOR
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename != 'migrations_history';
	BEGIN
		FOR stmt IN statements LOOP
			IF stmt.tablename = 'agent_experiment_audit_events' THEN
				-- Production deliberately rejects all destructive audit
				-- mutations, including TRUNCATE. The isolated DB-suite
				-- owner bypasses that one trigger only for fixture reset.
				EXECUTE 'ALTER TABLE agent_experiment_audit_events DISABLE TRIGGER agent_experiment_audit_events_append_only;';
				EXECUTE 'TRUNCATE TABLE agent_experiment_audit_events RESTART IDENTITY CASCADE;';
				EXECUTE 'ALTER TABLE agent_experiment_audit_events ENABLE TRIGGER agent_experiment_audit_events_append_only;';
			ELSE
				EXECUTE 'TRUNCATE TABLE ' || quote_ident(stmt.tablename) || ' RESTART IDENTITY CASCADE;';
			END IF;
		END LOOP;
	END;
	$$ LANGUAGE plpgsql;

	CREATE OR REPLACE FUNCTION drop_ephemeral_sequences() RETURNS void AS $$
	DECLARE
		statements CURSOR FOR
			SELECT relname FROM pg_class
			WHERE relname LIKE 'build_event_id_seq_%';
	BEGIN
		FOR stmt IN statements LOOP
			EXECUTE 'DROP SEQUENCE ' || quote_ident(stmt.relname) || ';';
		END LOOP;
	END;
	$$ LANGUAGE plpgsql;

	CREATE OR REPLACE FUNCTION drop_ephemeral_tables() RETURNS void AS $$
	DECLARE
		statements CURSOR FOR
			SELECT relname FROM pg_class
			WHERE relname LIKE 'pipeline_build_events_%'
			AND relkind = 'r';
		team_statements CURSOR FOR
			SELECT relname FROM pg_class
			WHERE relname LIKE 'team_build_events_%'
			AND relkind = 'r';
	BEGIN
		FOR stmt IN statements LOOP
			EXECUTE 'DROP TABLE ' || quote_ident(stmt.relname) || ';';
		END LOOP;
		FOR stmt IN team_statements LOOP
			EXECUTE 'DROP TABLE ' || quote_ident(stmt.relname) || ';';
		END LOOP;
	END;
	$$ LANGUAGE plpgsql;

	CREATE OR REPLACE FUNCTION reset_global_sequences() RETURNS void AS $$
	DECLARE
		statements CURSOR FOR
			SELECT relname FROM pg_class
			WHERE relname IN ('one_off_name', 'config_version_seq');
	BEGIN
		FOR stmt IN statements LOOP
			EXECUTE 'ALTER SEQUENCE ' || quote_ident(stmt.relname) || ' RESTART WITH 1;';
		END LOOP;
	END;
	$$ LANGUAGE plpgsql;

	SELECT truncate_tables();
	SELECT drop_ephemeral_sequences();
	SELECT drop_ephemeral_tables();
	SELECT reset_global_sequences();
`
