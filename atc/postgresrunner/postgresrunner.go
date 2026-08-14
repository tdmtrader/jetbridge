package postgresrunner

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/tedsuo/ifrit/ginkgomon_v2"
)

type Runner struct {
	Port int
}

func (runner Runner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	defer ginkgo.GinkgoRecover()

	pgBase := filepath.Join(os.TempDir(), "concourse-pg-runner")

	err := os.MkdirAll(pgBase, 0755)
	Expect(err).NotTo(HaveOccurred())

	tmpdir, err := os.MkdirTemp(pgBase, "postgres")
	Expect(err).NotTo(HaveOccurred())

	currentUser, err := user.Current()
	Expect(err).NotTo(HaveOccurred())

	initdbPath, err := exec.LookPath("initdb")
	Expect(err).NotTo(HaveOccurred())

	postgresPath, err := exec.LookPath("postgres")
	Expect(err).NotTo(HaveOccurred())

	initCmd := exec.Command(initdbPath, "-U", "postgres", "-D", tmpdir, "-E", "UTF8", "--no-locale")
	startCmd := exec.Command(postgresPath, "-k", "/tmp", "-D", tmpdir, "-h", "127.0.0.1", "-p", strconv.Itoa(runner.Port))

	if currentUser.Uid == "0" {
		pgUser, err := user.Lookup("postgres")
		Expect(err).NotTo(HaveOccurred())

		var uid, gid uint32
		_, err = fmt.Sscanf(pgUser.Uid, "%d", &uid)
		Expect(err).NotTo(HaveOccurred())

		_, err = fmt.Sscanf(pgUser.Gid, "%d", &gid)
		Expect(err).NotTo(HaveOccurred())

		err = os.Chown(tmpdir, int(uid), int(gid))
		Expect(err).NotTo(HaveOccurred())

		credential := &syscall.Credential{Uid: uid, Gid: gid}

		initCmd.SysProcAttr = &syscall.SysProcAttr{}
		initCmd.SysProcAttr.Credential = credential

		startCmd.SysProcAttr = &syscall.SysProcAttr{}
		startCmd.SysProcAttr.Credential = credential
	}

	session, err := gexec.Start(
		initCmd,
		gexec.NewPrefixedWriter("[o][initdb] ", ginkgo.GinkgoWriter),
		gexec.NewPrefixedWriter("[e][initdb] ", ginkgo.GinkgoWriter),
	)
	Expect(err).NotTo(HaveOccurred())

	<-session.Exited

	Expect(session).To(gexec.Exit(0))

	// Optimize for non-durability: https://www.postgresql.org/docs/13/non-durability.html
	appendToFile(filepath.Join(tmpdir, "postgresql.conf"), `
fsync = off
synchronous_commit = off
full_page_writes = off
random_page_cost = 1.1
`)

	ginkgoRunner := &ginkgomon_v2.Runner{
		Name:          "postgres",
		Command:       startCmd,
		AnsiColorCode: "90m",
		StartCheck:    "database system is ready to accept connections",
		Cleanup: func() {
			os.RemoveAll(tmpdir)
		},
	}

	return ginkgoRunner.Run(signals, ready)
}

func appendToFile(path string, content string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0755)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()

	_, err = f.WriteString(content)
	Expect(err).ToNot(HaveOccurred())
}

func (runner *Runner) MigrateToVersion(version int) {
	err := migration.NewOpenHelper(
		"pgx",
		runner.DataSourceName(),
		nil,
		nil,
		nil,
	).MigrateToVersion(version)
	Expect(err).NotTo(HaveOccurred())
}

func (runner *Runner) TryOpenDBAtVersion(version int) (*sql.DB, error) {
	dbConn, err := migration.NewOpenHelper(
		"pgx",
		runner.DataSourceName(),
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
	Expect(err).NotTo(HaveOccurred())
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
	Expect(err).NotTo(HaveOccurred())

	// only allow one connection so that we can detect any code paths that
	// require more than one, which will deadlock if it's at the limit
	dbConn.SetMaxOpenConns(1)
	dbConn.SetMaxIdleConns(1)

	return dbConn
}

func (runner *Runner) OpenConn() db.DbConn {
	return runner.openConn("testdb")
}

func (runner *Runner) openConn(dbName string) db.DbConn {
	dbConn, err := db.Open(
		lagertest.NewTestLogger("postgres-runner"),
		"pgx",
		runner.dataSourceName(dbName),
		nil,
		nil,
		"postgresrunner",
		nil,
	)
	Expect(err).NotTo(HaveOccurred())

	// only allow one connection so that we can detect any code paths that
	// require more than one, which will deadlock if it's at the limit
	dbConn.SetMaxOpenConns(1)
	dbConn.SetMaxIdleConns(1)

	return joinLimitValidatorConn{dbConn}
}

func (runner *Runner) OpenSingleton() *sql.DB {
	dbConn, err := sql.Open("pgx", runner.DataSourceName())
	Expect(err).NotTo(HaveOccurred())

	// only allow one connection, period. this matches production code use case,
	// as this is used for advisory locks.
	dbConn.SetMaxIdleConns(1)
	dbConn.SetMaxOpenConns(1)
	dbConn.SetConnMaxLifetime(0)

	return dbConn
}

func (runner *Runner) DataSourceName() string {
	return runner.dataSourceName("testdb")
}

func (runner *Runner) dataSourceName(dbName string) string {
	return fmt.Sprintf("host=/tmp user=postgres dbname=%s sslmode=disable port=%d", dbName, runner.Port)
}

func (runner *Runner) psqlf(c string, args ...any) int {
	return runner.psql(fmt.Sprintf(c, args...))
}

// psql runs c against the "postgres" database. libpq defaults dbname to the
// user name, so this is what the no-dbname form always connected to; naming it
// keeps the distinction from psqlIn visible, because anything schema-shaped run
// here finds no Concourse tables and still exits 0.
func (runner *Runner) psql(c string) int {
	return runner.psqlIn("postgres", c)
}

func (runner *Runner) psqlIn(dbName string, c string) int {
	cmd := exec.Command("psql", "-h", "/tmp", "-U", "postgres", "-p", strconv.Itoa(runner.Port), dbName, "-q", "-t", "-c", c)
	session, err := gexec.Start(cmd, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())

	<-session.Exited

	return session.ExitCode()
}

func (runner *Runner) InitializeTestDBTemplate() {
	createTemplate := "CREATE DATABASE testdb_template IS_TEMPLATE = true;"
	exitCode := runner.psql(createTemplate)
	if exitCode != 0 {
		exitCode = runner.psql("DROP DATABASE IF EXISTS testdb_template;")
		Expect(exitCode).To(Equal(0), "drop testdb_template")

		exitCode = runner.psql(createTemplate)
		Expect(exitCode).To(Equal(0), "create testdb_template")
	}

	// to run the migration
	conn := runner.openConn("testdb_template")
	err := conn.Close()
	Expect(err).ToNot(HaveOccurred())

	// Optimize for non-durability: https://www.postgresql.org/docs/13/non-durability.html
	exitCode = runner.psql(`
			SET client_min_messages TO WARNING;
			CREATE OR REPLACE FUNCTION mark_tables_as_unlogged() RETURNS void AS $$
			DECLARE
					statements CURSOR FOR
							SELECT tablename FROM pg_tables
							WHERE schemaname = 'public';
			BEGIN
					FOR stmt IN statements LOOP
							EXECUTE 'ALTER TABLE ' || quote_ident(stmt.tablename) || ' SET UNLOGGED;';
					END LOOP;
			END;
			$$ LANGUAGE plpgsql;

			SELECT mark_tables_as_unlogged();
	`)
	Expect(exitCode).To(Equal(0), "mark tables as unlogged")

	exitCode = runner.psqlIn("testdb_template", spreadIDSequencesSQL)
	Expect(exitCode).To(Equal(0), "spread id sequences")

	runner.terminateIdleConnections("testdb_template")
}

// spreadIDSequencesSQL gives every id sequence a range of its own, so that the
// first row of one table cannot share an id with the first row of another. Left
// alone, teams.id, pipelines.id, jobs.id and builds.id are all 1 in a fresh
// database, and a step wired to the wrong one of those still matches
// Equal(1) -- a mutation swapping TeamID for PipelineID in engine's step
// metadata passed for exactly that reason.
//
// The ranges come from the live schema rather than a list, so a table added by a
// future migration is covered without anyone remembering to do it. The ~24
// matches at a stride of a million top out around 24000001, well inside the
// 2147483647 of the narrowest id column (most are integer, a few bigint).
//
// Legacy build_event_id_seq_<n> sequences are dropped defensively by
// Truncate; the end-anchored match excludes them from the persistent ranges.
const spreadIDSequencesSQL = `
			SET client_min_messages TO WARNING;

			DO $spread_id_sequences$
			BEGIN
					PERFORM setval(
							format('%I.%I', schemaname, sequencename)::regclass,
							n * 1000000 + 1,
							false
					)
					FROM (
							SELECT schemaname, sequencename,
									row_number() OVER (ORDER BY sequencename) AS n
							FROM pg_sequences
							WHERE schemaname = 'public' AND sequencename LIKE '%\_id\_seq'
					) sequences;
			END;
			$spread_id_sequences$ LANGUAGE plpgsql;
`

func (runner *Runner) CreateEmptyTestDB() {
	exitCode := runner.psql("CREATE DATABASE testdb;")
	Expect(exitCode).To(Equal(0), "create empty testdb")
}

func (runner *Runner) CreateTestDBFromTemplate() {
	exitCode := runner.psql("CREATE DATABASE testdb TEMPLATE testdb_template;")
	Expect(exitCode).To(Equal(0), "create testdb from template")
}

func (runner *Runner) DropTestDB() {
	runner.terminateIdleConnections("testdb")

	exitCode := runner.psql("DROP DATABASE IF EXISTS testdb;")
	Expect(exitCode).To(Equal(0), "drop testdb")
}

func (runner *Runner) terminateIdleConnections(dbName string) {
	exitCode := runner.psqlf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = '%s' AND state = 'idle';", dbName)
	Expect(exitCode).To(Equal(0), "terminate idle connections")
}

func (runner *Runner) Truncate() {
	conn := runner.OpenSingleton()
	defer func() {
		Expect(conn.Close()).To(Succeed(), "close truncate connection")
	}()

	_, err := conn.Exec(truncateSQL + spreadIDSequencesSQL)
	Expect(err).NotTo(HaveOccurred(), "truncate testdb")
}

// Truncate resets the two global sequences below; spreadIDSequencesSQL resets
// every table ID sequence after this block runs.
const truncateSQL = `
			SET client_min_messages TO WARNING;

			DO $truncate_test_database$
			DECLARE
					table_names text;
					ephemeral_sequence_names text;
					ephemeral_table_names text;
			BEGIN
					SELECT string_agg(format('%I.%I', schemaname, tablename), ', ')
					INTO table_names
					FROM pg_tables
					WHERE schemaname = 'public'
					AND tablename != 'migrations_history'
					AND pg_relation_size(format('%I.%I', schemaname, tablename)::regclass) > 0;

					IF table_names IS NOT NULL THEN
							EXECUTE 'TRUNCATE TABLE ' || table_names || ' CASCADE;';
					END IF;

					SELECT string_agg(format('%I.%I', namespace.nspname, relation.relname), ', ')
					INTO ephemeral_sequence_names
					FROM pg_class relation
					JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
					WHERE namespace.nspname = 'public'
					AND relation.relkind = 'S'
					AND relation.relname ~ '^build_event_id_seq_[0-9]+$';

					IF ephemeral_sequence_names IS NOT NULL THEN
							EXECUTE 'DROP SEQUENCE ' || ephemeral_sequence_names || ';';
					END IF;

					SELECT string_agg(format('%I.%I', namespace.nspname, relation.relname), ', ')
					INTO ephemeral_table_names
					FROM pg_class relation
					JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
					WHERE namespace.nspname = 'public'
					AND relation.relkind = 'r'
					AND (
							relation.relname ~ '^pipeline_build_events_[0-9]+$'
							OR relation.relname ~ '^team_build_events_[0-9]+$'
					);

					IF ephemeral_table_names IS NOT NULL THEN
							EXECUTE 'DROP TABLE ' || ephemeral_table_names || ';';
					END IF;

					PERFORM setval(
							format('%I.%I', namespace.nspname, relation.relname)::regclass,
							1,
							false
					)
					FROM pg_class relation
					JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
					WHERE namespace.nspname = 'public'
					AND relation.relkind = 'S'
					AND relation.relname IN ('one_off_name', 'config_version_seq');
			END;
			$truncate_test_database$ LANGUAGE plpgsql;
`
