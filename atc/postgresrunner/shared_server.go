package postgresrunner

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	DefaultAdminDSN      = "host=127.0.0.1 port=15432 user=postgres dbname=postgres sslmode=disable"
	reaperAdvisoryLockID = int64(18932900154397524)
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	runIDPattern      = regexp.MustCompile(`^t([1-9][0-9]*)_p([1-9][0-9]*)_([0-9a-f]{8})$`)
	templatePattern   = regexp.MustCompile(`^cc_tpl_(t[1-9][0-9]*_p[1-9][0-9]*_[0-9a-f]{8})$`)
	clonePattern      = regexp.MustCompile(`^cc_db_(t[1-9][0-9]*_p[1-9][0-9]*_[0-9a-f]{8})_n([1-9][0-9]*)_s([1-9][0-9]*)$`)
)

type SuiteConfig struct {
	AdminDSN     string `json:"admin_dsn"`
	RunID        string `json:"run_id"`
	TemplateName string `json:"template_name"`
	CreatedUnix  int64  `json:"created_unix"`
}

type ConnectionInfo struct {
	Host            string
	Port            uint16
	Socket          string
	User            string
	Password        string
	Database        string
	ApplicationName string
	SSLMode         string
	SSLNegotiation  string
	SSLRootCert     string
	SSLCert         string
	SSLKey          string
	ConnectTimeout  time.Duration
}

type Runner struct {
	Port int

	state *runnerState
}

type runnerState struct {
	mu         sync.Mutex
	suite      SuiteConfig
	node       int
	serial     uint64
	allocating bool
	currentDB  string
	ownedDBs   map[string]struct{}
}

func newRunID(now time.Time, pid int, entropy io.Reader) (string, error) {
	if now.Unix() <= 0 {
		return "", fmt.Errorf("run creation time must be after the Unix epoch")
	}
	if pid <= 0 {
		return "", fmt.Errorf("run PID must be positive")
	}
	var suffix [4]byte
	if _, err := io.ReadFull(entropy, suffix[:]); err != nil {
		return "", fmt.Errorf("read run ID entropy: %w", err)
	}
	return fmt.Sprintf("t%d_p%d_%x", now.Unix(), pid, suffix), nil
}

func validateIdentifier(name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("unsafe PostgreSQL identifier %q", name)
	}
	return nil
}

func (r *Runner) AdoptSuiteConfig(config SuiteConfig, node int) error {
	if node <= 0 {
		return fmt.Errorf("Ginkgo node must be positive, got %d", node)
	}
	if err := validateIdentifier(config.RunID); err != nil {
		return fmt.Errorf("invalid run ID: %w", err)
	}
	match := runIDPattern.FindStringSubmatch(config.RunID)
	if match == nil {
		return fmt.Errorf("invalid generated run ID %q", config.RunID)
	}
	createdUnix, err := generatedRunCreation(config.RunID)
	if err != nil || createdUnix != config.CreatedUnix {
		return fmt.Errorf("run ID creation time does not match created_unix")
	}
	if err := validateIdentifier(config.TemplateName); err != nil {
		return fmt.Errorf("invalid template name: %w", err)
	}
	if config.TemplateName != "cc_tpl_"+config.RunID {
		return fmt.Errorf("template name %q does not match run ID %q", config.TemplateName, config.RunID)
	}
	if config.AdminDSN == "" {
		return fmt.Errorf("admin DSN is empty")
	}
	parsed, err := pgx.ParseConfig(config.AdminDSN)
	if err != nil {
		return fmt.Errorf("parse admin DSN: %w", err)
	}
	if _, err := childSSLMode(config.AdminDSN); err != nil {
		return fmt.Errorf("admin DSN cannot be propagated to child PostgreSQL clients: %w", err)
	}

	r.state = &runnerState{
		suite:    config,
		node:     node,
		ownedDBs: map[string]struct{}{},
	}
	r.Port = int(parsed.Port)
	return nil
}

func (r *Runner) DatabaseName() string {
	if r.state == nil {
		return ""
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.state.currentDB
}

func (r *Runner) activeDSN() (string, error) {
	if r.state == nil {
		return "", fmt.Errorf("runner has no adopted suite configuration")
	}
	r.state.mu.Lock()
	dbName := r.state.currentDB
	adminDSN := r.state.suite.AdminDSN
	r.state.mu.Unlock()
	if dbName == "" {
		return "", fmt.Errorf("runner has no active database")
	}
	return dsnForDatabase(adminDSN, dbName)
}

func (r *Runner) ConnectionInfo() ConnectionInfo {
	dsn, err := r.activeDSN()
	if err != nil {
		return ConnectionInfo{}
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return ConnectionInfo{}
	}
	host := config.Host
	socket := ""
	if network, _ := pgconn.NetworkAddress(config.Host, config.Port); network == "unix" {
		socket = config.Host
		host = ""
	}
	sslNegotiation := config.SSLNegotiation
	if sslNegotiation == "" {
		sslNegotiation = "postgres"
	}
	sslMode, err := childSSLMode(dsn)
	if err != nil {
		return ConnectionInfo{}
	}
	return ConnectionInfo{
		Host:            host,
		Port:            config.Port,
		Socket:          socket,
		User:            config.User,
		Password:        config.Password,
		Database:        config.Database,
		ApplicationName: config.RuntimeParams["application_name"],
		SSLMode:         sslMode,
		SSLNegotiation:  sslNegotiation,
		SSLRootCert:     connectionSetting(dsn, "sslrootcert", "PGSSLROOTCERT"),
		SSLCert:         connectionSetting(dsn, "sslcert", "PGSSLCERT"),
		SSLKey:          connectionSetting(dsn, "sslkey", "PGSSLKEY"),
		ConnectTimeout:  config.ConnectTimeout,
	}
}

func (r *Runner) reserveDatabase() (string, error) {
	if r.state == nil {
		return "", fmt.Errorf("runner has no adopted suite configuration")
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.allocating {
		return "", fmt.Errorf("database allocation is already in progress")
	}
	if r.state.currentDB != "" {
		return "", fmt.Errorf("database %q is still active", r.state.currentDB)
	}
	nextSerial := r.state.serial + 1
	name := fmt.Sprintf("cc_db_%s_n%d_s%d", r.state.suite.RunID, r.state.node, nextSerial)
	if err := validateIdentifier(name); err != nil {
		return "", err
	}
	r.state.serial = nextSerial
	r.state.allocating = true
	return name, nil
}

func (r *Runner) CreateSuiteTemplate(ctx context.Context) (config SuiteConfig, err error) {
	adminDSN := configuredAdminDSN()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return SuiteConfig{}, fmt.Errorf("shared PostgreSQL unavailable: %w; run make test-postgres-up", err)
	}
	defer admin.Close(context.Background())
	if err := admin.Ping(ctx); err != nil {
		return SuiteConfig{}, fmt.Errorf("shared PostgreSQL unavailable: %w; run make test-postgres-up", err)
	}
	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock($1)`, reaperAdvisoryLockID); err != nil {
		return SuiteConfig{}, fmt.Errorf("acquire shared PostgreSQL reaper lock: %w", err)
	}
	defer func() {
		_, unlockErr := admin.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, reaperAdvisoryLockID)
		if err == nil && unlockErr != nil {
			err = fmt.Errorf("release shared PostgreSQL reaper lock: %w", unlockErr)
		}
	}()

	if err := reapExpiredRunsOnConn(ctx, admin, time.Now()); err != nil {
		return SuiteConfig{}, err
	}
	runID, err := newRunID(time.Now(), os.Getpid(), rand.Reader)
	if err != nil {
		return SuiteConfig{}, err
	}
	createdUnix, err := generatedRunCreation(runID)
	if err != nil {
		return SuiteConfig{}, err
	}
	config = SuiteConfig{
		AdminDSN:     adminDSN,
		RunID:        runID,
		TemplateName: "cc_tpl_" + runID,
		CreatedUnix:  createdUnix,
	}
	if err := r.AdoptSuiteConfig(config, 1); err != nil {
		return SuiteConfig{}, err
	}

	created := false
	rollbackName := config.TemplateName
	defer func() {
		if err != nil && created {
			rollbackErr := dropGeneratedDatabaseOnConn(context.Background(), admin, rollbackName)
			if rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback suite template %s: %w", rollbackName, rollbackErr))
			}
		}
	}()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+config.TemplateName); err != nil {
		return SuiteConfig{}, fmt.Errorf("create suite template %s: %w", config.TemplateName, err)
	}
	created = true

	templateDSN, err := dsnForDatabase(adminDSN, config.TemplateName)
	if err != nil {
		return SuiteConfig{}, err
	}
	migrationConn, err := db.Open(
		lagertest.NewTestLogger("postgres-runner"),
		"pgx",
		templateDSN,
		nil,
		nil,
		"postgresrunner-template",
		nil,
	)
	if err != nil {
		return SuiteConfig{}, fmt.Errorf("migrate suite template: %w", err)
	}
	if err = migrationConn.Close(); err != nil {
		return SuiteConfig{}, fmt.Errorf("close suite template migration connection: %w", err)
	}

	pgxConfig, err := pgx.ParseConfig(templateDSN)
	if err != nil {
		return SuiteConfig{}, fmt.Errorf("parse suite template DSN: %w", err)
	}
	pgxConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	templateConn, err := pgx.ConnectConfig(ctx, pgxConfig)
	if err != nil {
		return SuiteConfig{}, fmt.Errorf("connect suite template: %w", err)
	}
	_, markErr := templateConn.Exec(ctx, markTablesAsUnloggedSQL)
	closeErr := templateConn.Close(context.Background())
	if markErr != nil {
		return SuiteConfig{}, fmt.Errorf("mark suite template tables unlogged: %w", markErr)
	}
	if closeErr != nil {
		return SuiteConfig{}, fmt.Errorf("close suite template connection: %w", closeErr)
	}
	if err = terminateDatabaseBackends(ctx, admin, config.TemplateName); err != nil {
		return SuiteConfig{}, fmt.Errorf("terminate suite template backends: %w", err)
	}
	return config, nil
}

func configuredAdminDSN() string {
	if dsn := os.Getenv("CONCOURSE_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return DefaultAdminDSN
}

func generatedRunCreation(runID string) (int64, error) {
	match := runIDPattern.FindStringSubmatch(runID)
	if match == nil {
		return 0, fmt.Errorf("invalid generated run ID %q", runID)
	}
	created, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse run creation time: %w", err)
	}
	if _, err := strconv.ParseInt(match[2], 10, 64); err != nil {
		return 0, fmt.Errorf("parse run PID: %w", err)
	}
	return created, nil
}

func (r *Runner) createTestDBFromTemplate(ctx context.Context) (err error) {
	return r.createDatabase(ctx, true)
}

func (r *Runner) createEmptyTestDB(ctx context.Context) (err error) {
	return r.createDatabase(ctx, false)
}

func (r *Runner) createDatabase(ctx context.Context, fromTemplate bool) (err error) {
	name, err := r.reserveDatabase()
	if err != nil {
		return err
	}
	success := false
	defer func() { r.finishDatabaseAllocation(name, success) }()

	r.state.mu.Lock()
	adminDSN := r.state.suite.AdminDSN
	templateName := r.state.suite.TemplateName
	r.state.mu.Unlock()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect shared PostgreSQL admin: %w", err)
	}
	defer admin.Close(context.Background())
	statement := "CREATE DATABASE " + name
	if fromTemplate {
		if err := validateIdentifier(templateName); err != nil {
			return err
		}
		statement += " TEMPLATE " + templateName
	}
	if _, err := admin.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create test database %s: %w", name, err)
	}
	success = true
	return nil
}

func (r *Runner) finishDatabaseAllocation(name string, success bool) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.allocating = false
	if success {
		r.state.currentDB = name
		r.state.ownedDBs[name] = struct{}{}
	}
}

func (r *Runner) dropTestDB(ctx context.Context) error {
	if r.state == nil {
		return fmt.Errorf("runner has no adopted suite configuration")
	}
	r.state.mu.Lock()
	name := r.state.currentDB
	adminDSN := r.state.suite.AdminDSN
	runID := r.state.suite.RunID
	r.state.mu.Unlock()
	if name == "" {
		return fmt.Errorf("runner has no active database")
	}
	if err := validateRunDatabaseName(name, runID); err != nil {
		return err
	}
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect shared PostgreSQL admin: %w", err)
	}
	defer admin.Close(context.Background())
	if err := dropGeneratedDatabaseOnConn(ctx, admin, name); err != nil {
		return err
	}
	r.state.mu.Lock()
	if r.state.currentDB == name {
		r.state.currentDB = ""
	}
	delete(r.state.ownedDBs, name)
	r.state.mu.Unlock()
	return nil
}

func (r *Runner) CleanupProcess(ctx context.Context) error {
	if r.state == nil {
		return nil
	}
	r.state.mu.Lock()
	names := make([]string, 0, len(r.state.ownedDBs))
	for name := range r.state.ownedDBs {
		names = append(names, name)
	}
	adminDSN := r.state.suite.AdminDSN
	runID := r.state.suite.RunID
	r.state.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		if err := validateRunDatabaseName(name, runID); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return nil
	}
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect shared PostgreSQL admin: %w", err)
	}
	defer admin.Close(context.Background())
	for _, name := range names {
		if err := dropGeneratedDatabaseOnConn(ctx, admin, name); err != nil {
			return err
		}
		r.state.mu.Lock()
		delete(r.state.ownedDBs, name)
		if r.state.currentDB == name {
			r.state.currentDB = ""
		}
		r.state.mu.Unlock()
	}
	return nil
}

func (r *Runner) CleanupSuite(ctx context.Context) error {
	if r.state == nil {
		return nil
	}
	state := r.state
	state.mu.Lock()
	config := state.suite
	state.mu.Unlock()
	prefix := "cc_db_" + config.RunID + "_"
	admin, err := pgx.Connect(ctx, config.AdminDSN)
	if err != nil {
		return fmt.Errorf("connect shared PostgreSQL admin: %w", err)
	}
	defer admin.Close(context.Background())
	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE left(datname, length($1)) = $1 ORDER BY datname`, prefix)
	if err != nil {
		return fmt.Errorf("discover suite clones: %w", err)
	}
	var clones []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan suite clone: %w", err)
		}
		clones = append(clones, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("discover suite clones: %w", err)
	}
	rows.Close()
	for _, name := range clones {
		if err := validateRunDatabaseName(name, config.RunID); err != nil {
			return err
		}
	}
	if err := validateRunTemplateName(config.TemplateName, config.RunID); err != nil {
		return err
	}
	for _, name := range clones {
		if err := dropGeneratedDatabaseOnConn(ctx, admin, name); err != nil {
			return err
		}
	}
	if err := dropGeneratedDatabaseOnConn(ctx, admin, config.TemplateName); err != nil {
		return err
	}
	r.state = nil
	return nil
}

func validateRunDatabaseName(name, runID string) error {
	parsedRunID, ok := generatedCloneRun(name)
	if !ok || parsedRunID != runID {
		return fmt.Errorf("database %q is outside run namespace %q", name, runID)
	}
	return nil
}

func validateRunTemplateName(name, runID string) error {
	if err := validateIdentifier(name); err != nil {
		return err
	}
	match := templatePattern.FindStringSubmatch(name)
	if match == nil || match[1] != runID {
		return fmt.Errorf("template %q is outside run namespace %q", name, runID)
	}
	return nil
}

func dropGeneratedDatabaseOnConn(ctx context.Context, admin *pgx.Conn, name string) error {
	if err := validateIdentifier(name); err != nil {
		return err
	}
	if clonePattern.FindStringSubmatch(name) == nil && templatePattern.FindStringSubmatch(name) == nil {
		return fmt.Errorf("database %q is outside the shared-test namespace", name)
	}
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return fmt.Errorf("check database %s: %w", name, err)
	}
	if !exists {
		return nil
	}
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+name+" WITH ALLOW_CONNECTIONS false"); err != nil {
		return fmt.Errorf("disable connections to database %s: %w", name, err)
	}
	if err := terminateDatabaseBackends(ctx, admin, name); err != nil {
		return fmt.Errorf("terminate database %s backends: %w", name, err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

func terminateDatabaseBackends(ctx context.Context, admin *pgx.Conn, name string) error {
	_, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE pid <> pg_backend_pid() AND datname = $1`, name)
	return err
}

func reapExpiredRuns(ctx context.Context, adminDSN string, now time.Time) error {
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect shared PostgreSQL admin: %w", err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock($1)`, reaperAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire shared PostgreSQL reaper lock: %w", err)
	}
	defer admin.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, reaperAdvisoryLockID)
	return reapExpiredRunsOnConn(ctx, admin, now)
}

func reapExpiredRunsOnConn(ctx context.Context, admin *pgx.Conn, now time.Time) error {
	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database ORDER BY datname`)
	if err != nil {
		return fmt.Errorf("scan shared PostgreSQL databases: %w", err)
	}
	type candidate struct {
		name       string
		isTemplate bool
	}
	var candidates []candidate
	cutoff := now.Add(-24 * time.Hour).Unix()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		runID, isTemplate, ok := generatedDatabaseRun(name)
		createdUnix, parseErr := generatedRunCreation(runID)
		if ok && parseErr == nil && createdUnix < cutoff {
			candidates = append(candidates, candidate{name: name, isTemplate: isTemplate})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].isTemplate != candidates[j].isTemplate {
			return !candidates[i].isTemplate
		}
		return candidates[i].name < candidates[j].name
	})
	for _, candidate := range candidates {
		if err := dropGeneratedDatabaseOnConn(ctx, admin, candidate.name); err != nil {
			return err
		}
	}
	return nil
}

func generatedDatabaseRun(name string) (runID string, isTemplate bool, ok bool) {
	if runID, ok := generatedCloneRun(name); ok {
		return runID, false, true
	}
	if match := templatePattern.FindStringSubmatch(name); match != nil {
		if _, err := generatedRunCreation(match[1]); err == nil {
			return match[1], true, true
		}
	}
	return "", false, false
}

func generatedCloneRun(name string) (string, bool) {
	if err := validateIdentifier(name); err != nil {
		return "", false
	}
	match := clonePattern.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}
	if _, err := generatedRunCreation(match[1]); err != nil {
		return "", false
	}
	node, err := strconv.ParseInt(match[2], 10, strconv.IntSize)
	if err != nil || node <= 0 {
		return "", false
	}
	serial, err := strconv.ParseUint(match[3], 10, 64)
	if err != nil || serial == 0 {
		return "", false
	}
	generated := fmt.Sprintf("cc_db_%s_n%d_s%d", match[1], node, serial)
	if generated != name {
		return "", false
	}
	if err := validateIdentifier(generated); err != nil {
		return "", false
	}
	return match[1], true
}

const markTablesAsUnloggedSQL = `
SET client_min_messages TO WARNING;
CREATE TEMP TABLE cc_unlogged_foreign_keys AS
SELECT n.nspname AS schema_name,
       c.relname AS table_name,
       con.conname AS constraint_name,
       pg_get_constraintdef(con.oid) AS definition
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.contype = 'f' AND n.nspname = 'public';

DO $$
DECLARE
    constraint_row record;
BEGIN
    FOR constraint_row IN SELECT * FROM cc_unlogged_foreign_keys LOOP
        EXECUTE format(
            'ALTER TABLE %I.%I DROP CONSTRAINT %I',
            constraint_row.schema_name,
            constraint_row.table_name,
            constraint_row.constraint_name
        );
    END LOOP;
END;
$$ LANGUAGE plpgsql;

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

DO $$
DECLARE
    constraint_row record;
BEGIN
    FOR constraint_row IN SELECT * FROM cc_unlogged_foreign_keys LOOP
        EXECUTE format(
            'ALTER TABLE %I.%I ADD CONSTRAINT %I %s',
            constraint_row.schema_name,
            constraint_row.table_name,
            constraint_row.constraint_name,
            constraint_row.definition
        );
    END LOOP;
END;
$$ LANGUAGE plpgsql;

DROP TABLE cc_unlogged_foreign_keys;
`

func dsnForDatabase(dsn, database string) (string, error) {
	if err := validateIdentifier(database); err != nil {
		return "", err
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse URL DSN: %w", err)
		}
		parsed.Path = "/" + database
		parsed.RawPath = ""
		query := parsed.Query()
		query.Del("dbname")
		query.Del("database")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}

	tokens, err := keywordDSNTokens(dsn)
	if err != nil {
		return "", err
	}
	databaseTokens := 0
	rewritten := dsn
	for index := len(tokens) - 1; index >= 0; index-- {
		token := tokens[index]
		if token.key == "dbname" || token.key == "database" {
			databaseTokens++
			rewritten = rewritten[:token.valueStart] + database + rewritten[token.valueEnd:]
		}
	}
	if databaseTokens == 0 {
		return "", fmt.Errorf("keyword DSN must contain a dbname or database token")
	}
	return rewritten, nil
}

type keywordDSNToken struct {
	key                  string
	valueStart, valueEnd int
}

func keywordDSNTokens(dsn string) ([]keywordDSNToken, error) {
	var tokens []keywordDSNToken
	for pos := 0; pos < len(dsn); {
		for pos < len(dsn) && isDSNSpace(dsn[pos]) {
			pos++
		}
		if pos == len(dsn) {
			break
		}
		keyStart := pos
		for pos < len(dsn) && dsn[pos] != '=' {
			pos++
		}
		if pos == len(dsn) {
			return nil, fmt.Errorf("invalid keyword DSN near %q", dsn[keyStart:])
		}
		keyEnd := pos
		for keyEnd > keyStart && isDSNSpace(dsn[keyEnd-1]) {
			keyEnd--
		}
		if keyEnd == keyStart {
			return nil, fmt.Errorf("empty keyword in DSN near %q", dsn[keyStart:])
		}
		key := dsn[keyStart:keyEnd]
		pos++
		for pos < len(dsn) && isDSNSpace(dsn[pos]) {
			pos++
		}
		valueStart := pos
		quoted := pos < len(dsn) && dsn[pos] == '\''
		if quoted {
			pos++
		}
		closed := !quoted
		for pos < len(dsn) {
			if dsn[pos] == '\\' {
				if pos+1 == len(dsn) {
					return nil, fmt.Errorf("invalid trailing backslash for %q", key)
				}
				pos += 2
				continue
			}
			if quoted && dsn[pos] == '\'' {
				pos++
				closed = true
				break
			}
			if !quoted && isDSNSpace(dsn[pos]) {
				break
			}
			pos++
		}
		if !closed {
			return nil, fmt.Errorf("unterminated quoted value for %q", key)
		}
		valueEnd := pos
		if quoted && pos < len(dsn) && !isDSNSpace(dsn[pos]) {
			return nil, fmt.Errorf("invalid characters after quoted value for %q", key)
		}
		tokens = append(tokens, keywordDSNToken{key: key, valueStart: valueStart, valueEnd: valueEnd})
	}
	return tokens, nil
}

func isDSNSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n' || char == '\v' || char == '\f'
}

func childSSLMode(dsn string) (string, error) {
	mode := connectionSetting(dsn, "sslmode", "PGSSLMODE")
	if mode == "" {
		return "", fmt.Errorf("sslmode must be explicit because PostgreSQL's prefer default cannot be represented by Concourse child configuration")
	}
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
		return mode, nil
	default:
		return "", fmt.Errorf("sslmode %q is not supported; use disable, require, verify-ca, or verify-full", mode)
	}
}

func connectionSetting(dsn, name, environmentName string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err == nil {
			if value := parsed.Query().Get(name); value != "" {
				return value
			}
		}
	} else {
		tokens, err := keywordDSNTokens(dsn)
		if err == nil {
			value := ""
			for _, token := range tokens {
				if token.key == name {
					value = unquoteKeywordValue(dsn[token.valueStart:token.valueEnd])
				}
			}
			if value != "" {
				return value
			}
		}
	}
	if environmentName != "" {
		return os.Getenv(environmentName)
	}
	return ""
}

func unquoteKeywordValue(value string) string {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = value[1 : len(value)-1]
	}
	var result strings.Builder
	for pos := 0; pos < len(value); pos++ {
		if value[pos] == '\\' && pos+1 < len(value) {
			pos++
		}
		result.WriteByte(value[pos])
	}
	return result.String()
}
