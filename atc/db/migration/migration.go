package migration

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration/migrations"
	multierror "github.com/hashicorp/go-multierror"
)

func NewOpenHelper(driver, name string, lockFactory lock.LockFactory, newKey *encryption.Key, oldKey *encryption.Key) *OpenHelper {
	return &OpenHelper{
		driver,
		name,
		lockFactory,
		newKey,
		oldKey,
	}
}

type OpenHelper struct {
	driver         string
	dataSourceName string
	lockFactory    lock.LockFactory
	newKey         *encryption.Key
	oldKey         *encryption.Key
}

func (helper *OpenHelper) CurrentVersion() (int, error) {
	db, err := sql.Open(helper.driver, helper.dataSourceName)
	if err != nil {
		return -1, err
	}

	defer db.Close()

	return NewMigrator(db, helper.lockFactory).CurrentVersion()
}

func (helper *OpenHelper) SupportedVersion() (int, error) {
	db, err := sql.Open(helper.driver, helper.dataSourceName)
	if err != nil {
		return -1, err
	}

	defer db.Close()

	return NewMigrator(db, helper.lockFactory).SupportedVersion()
}

func (helper *OpenHelper) Open() (*sql.DB, error) {
	db, err := sql.Open(helper.driver, helper.dataSourceName)
	if err != nil {
		return nil, err
	}

	if err := NewMigrator(db, helper.lockFactory).Up(helper.newKey, helper.oldKey); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func (helper *OpenHelper) OpenAtVersion(version int) (*sql.DB, error) {
	db, err := sql.Open(helper.driver, helper.dataSourceName)
	if err != nil {
		return nil, err
	}

	if err := NewMigrator(db, helper.lockFactory).Migrate(helper.newKey, helper.oldKey, version); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func (helper *OpenHelper) MigrateToVersion(version int) error {
	db, err := sql.Open(helper.driver, helper.dataSourceName)
	if err != nil {
		return err
	}

	defer db.Close()

	return NewMigrator(db, helper.lockFactory).Migrate(helper.newKey, helper.oldKey, version)
}

type Migrator interface {
	CurrentVersion() (int, error)
	SupportedVersion() (int, error)
	Migrate(newKey, oldKey *encryption.Key, version int) error
	Up(newKey, oldKey *encryption.Key) error
	Migrations() ([]migration, error)
}

//go:embed migrations
var migrationsEmbed embed.FS

// The embedded migration files are immutable for the lifetime of the binary.
// Parse them once, while still checking the database and taking migration locks
// on every open. Caller-supplied filesystems are deliberately not cached.
var embeddedMigrations = sync.OnceValues(func() ([]migration, error) {
	migrationsFS, err := fs.Sub(migrationsEmbed, "migrations")
	if err != nil {
		panic(err) // impossible for this constant path
	}
	return readMigrations(migrationsFS)
})

func NewMigrator(db *sql.DB, lockFactory lock.LockFactory) Migrator {
	return &migrator{
		db,
		lockFactory,
		lager.NewLogger("migrations"),
		func() ([]migration, error) {
			migrations, err := embeddedMigrations()
			// Migrations historically returned a fresh slice. Keep callers from
			// changing the definitions seen by later opens or other migrators.
			return slices.Clone(migrations), err
		},
	}
}

func NewMigratorForMigrations(db *sql.DB, lockFactory lock.LockFactory, migrationsFS fs.FS) Migrator {
	return &migrator{
		db,
		lockFactory,
		lager.NewLogger("migrations"),
		func() ([]migration, error) { return readMigrations(migrationsFS) },
	}
}

type migrator struct {
	db             *sql.DB
	lockFactory    lock.LockFactory
	logger         lager.Logger
	loadMigrations func() ([]migration, error)
}

func (helper *migrator) Migrations() ([]migration, error) {
	return helper.loadMigrations()
}

func readMigrations(migrationsFS fs.FS) ([]migration, error) {
	migrationList := []migration{}

	assets, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return nil, err
	}

	var parser = NewParser(migrationsFS)
	for _, asset := range assets {
		if asset.Name() == "migrations.go" {
			// special file declaring type for Go migrations
			continue
		}

		parsedMigration, err := parser.ParseFileToMigration(asset.Name())
		if err != nil {
			return nil, fmt.Errorf("parse migration filename %s: %w", asset.Name(), err)
		}

		migrationList = append(migrationList, parsedMigration)
	}

	sortMigrations(migrationList)

	return migrationList, nil
}

func (m *migrator) SupportedVersion() (int, error) {
	migrations, err := m.Migrations()
	if err != nil {
		return 0, fmt.Errorf("list migrations: %w", err)
	}

	if len(migrations) == 0 {
		return 0, errors.New("no migrations")
	}

	return migrations[len(migrations)-1].Version, nil
}

func (helper *migrator) CurrentVersion() (int, error) {
	var currentVersion int
	var direction string
	err := helper.db.QueryRow("SELECT version, direction FROM migrations_history WHERE status!='failed' ORDER BY tstamp DESC LIMIT 1").Scan(&currentVersion, &direction)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return -1, err
	}
	if direction != "down" {
		return currentVersion, nil
	}

	// A passed down leaves the database at the version before it. That is the
	// nearest migration this binary carries below it or, when it carries none
	// (the down reversed its baseline or something older), the nearest version
	// the history recorded as applied.
	migrations, err := helper.Migrations()
	if err != nil {
		return -1, err
	}
	previous := 0
	for _, m := range migrations {
		if m.Version < currentVersion {
			previous = m.Version
		}
	}
	if previous == 0 {
		err = helper.db.QueryRow("SELECT coalesce(max(version), 0) FROM migrations_history WHERE version < $1 AND direction = 'up' AND status = 'passed'", currentVersion).Scan(&previous)
		if err != nil {
			return -1, err
		}
	}
	return previous, nil
}

// LastReleaseBeforeBaseline is the last release that carries the migrations
// the baseline replaced, and so the one that can bring an older database up to
// it.
const LastReleaseBeforeBaseline = "JetBridge 0.3.1"

func belowBaseline(version, baseline int) error {
	return fmt.Errorf("database is at migration %d, older than this release's baseline migration %d: upgrade it with %s first", version, baseline, LastReleaseBeforeBaseline)
}

func forwardOnly(version int) error {
	return fmt.Errorf("migration %d is forward-only: restore the pre-upgrade database backup instead", version)
}

func (helper *migrator) Migrate(newKey, oldKey *encryption.Key, toVersion int) error {
	var strategy encryption.Strategy
	if oldKey != nil {
		strategy = oldKey
	} else if newKey != nil {
		// special case - if the old key is not provided but the new key is,
		// this might mean the data was not encrypted, or that it was encrypted with newKey
		strategy = encryption.NewFallbackStrategy(newKey, encryption.NewNoEncryption())
	} else {
		strategy = encryption.NewNoEncryption()
	}

	migrations, err := helper.Migrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("no migrations")
	}
	// The earliest migration is the baseline: it builds the schema from
	// nothing, and nothing older than it can be applied or reversed to.
	baseline := migrations[0].Version
	if toVersion > 0 && toVersion < baseline {
		return fmt.Errorf("cannot migrate to %d: it predates this release's baseline migration %d", toVersion, baseline)
	}

	lock, err := helper.acquireLock()
	if err != nil {
		return err
	}

	if lock != nil {
		defer lock.Release()
	}

	err = helper.refuseLegacyTracking(baseline)
	if err != nil {
		return err
	}

	_, err = helper.db.Exec("CREATE TABLE IF NOT EXISTS migrations_history (version bigint, tstamp timestamp with time zone, direction varchar, status varchar, dirty boolean)")
	if err != nil {
		return err
	}

	currentVersion, err := helper.CurrentVersion()
	if err != nil {
		return err
	}
	if currentVersion > 0 && currentVersion < baseline {
		return belowBaseline(currentVersion, baseline)
	}

	if currentVersion <= toVersion {
		for _, m := range migrations {
			if currentVersion < m.Version && m.Version <= toVersion && m.Direction == "up" {
				err = helper.runMigration(m, strategy)
				if err != nil {
					return err
				}
			}
		}
	} else {
		// A migration without a down is forward-only. Refuse before reversing
		// anything, so a refused downgrade leaves the schema where it was.
		downs := map[int]migration{}
		for _, m := range migrations {
			if m.Direction == "down" {
				downs[m.Version] = m
			}
		}
		var reversals []migration
		for i := len(migrations) - 1; i >= 0; i-- {
			m := migrations[i]
			if m.Direction != "up" || m.Version > currentVersion || m.Version <= toVersion {
				continue
			}
			down, ok := downs[m.Version]
			if !ok {
				return forwardOnly(m.Version)
			}
			reversals = append(reversals, down)
		}
		for _, down := range reversals {
			err = helper.runMigration(down, strategy)
			if err != nil {
				return err
			}
		}
	}

	switch {
	case oldKey != nil && newKey == nil:
		err = helper.decryptToPlaintext(oldKey)
	case oldKey != nil && newKey != nil:
		err = helper.encryptWithNewKey(newKey, oldKey)
	}
	if err != nil {
		return err
	}

	if newKey != nil {
		err = helper.encryptPlaintext(newKey)
		if err != nil {
			return err
		}
	}

	return nil
}

type Strategy int

const (
	GoMigration Strategy = iota
	SQLMigration
)

type migration struct {
	Name       string
	Version    int
	Direction  string
	Statements string
	Strategy   Strategy
}

func (m *migrator) recordMigrationFailure(migration migration, migrationErr error, dirty bool) error {
	_, recordErr := m.db.Exec("INSERT INTO migrations_history (version, tstamp, direction, status, dirty) VALUES ($1, current_timestamp, $2, 'failed', $3)", migration.Version, migration.Direction, dirty)
	if recordErr != nil {
		return multierror.Append(
			migrationErr,
			fmt.Errorf("record failure to migration history: %w", recordErr),
		)
	}

	return migrationErr
}

func (m *migrator) runMigration(migration migration, strategy encryption.Strategy) (err error) {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			err = m.recordMigrationFailure(
				migration,
				fmt.Errorf("migration '%s' failed and was rolled back: %w", migration.Name, err),
				false,
			)

			rbErr := tx.Rollback()
			if rbErr != nil {
				err = multierror.Append(err, fmt.Errorf("rollback failed: %w", rbErr))
			}
		}
	}()

	switch migration.Strategy {
	case GoMigration:
		err = migrations.NewMigrations(tx, strategy).Run(migration.Name)
		if err != nil {
			return err
		}
	case SQLMigration:
		_, err = tx.Exec(migration.Statements)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec("INSERT INTO migrations_history (version, tstamp, direction, status, dirty) VALUES ($1, current_timestamp, $2, 'passed', false)", migration.Version, migration.Direction)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (helper *migrator) Up(newKey, oldKey *encryption.Key) error {
	migrations, err := helper.Migrations()
	if err != nil {
		return err
	}
	return helper.Migrate(newKey, oldKey, migrations[len(migrations)-1].Version)
}

func (helper *migrator) acquireLock() (lock.Lock, error) {

	var err error
	var acquired bool
	var newLock lock.Lock

	if helper.lockFactory != nil {
		for {
			newLock, acquired, err = helper.lockFactory.Acquire(helper.logger, lock.NewDatabaseMigrationLockID())

			if err != nil {
				return nil, err
			}

			if acquired {
				break
			}

			time.Sleep(1 * time.Second)
		}
	}

	return newLock, err
}

func checkTableExist(db *sql.DB, tableName string) (bool, error) {
	var existingTable sql.NullString
	err := db.QueryRow("SELECT to_regclass($1)", tableName).Scan(&existingTable)
	if err != nil {
		return false, err
	}

	return existingTable.Valid, nil
}

// refuseLegacyTracking refuses a database that records its version only in a
// tracking table older than migrations_history: migration_version (before
// Concourse 3.7) or schema_migrations (before migration 1532706545). Both
// predate the baseline, so the database is refused before anything changes.
func (helper *migrator) refuseLegacyTracking(baseline int) error {
	historyExists, err := checkTableExist(helper.db, "migrations_history")
	if err != nil || historyExists {
		return err
	}
	for _, table := range []string{"migration_version", "schema_migrations"} {
		exists, err := checkTableExist(helper.db, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		var version int
		err = helper.db.QueryRow("SELECT coalesce(max(version), 0) FROM " + table).Scan(&version)
		if err != nil {
			return err
		}
		return fmt.Errorf("database records migration %d in %s, which predates this release's baseline migration %d: upgrade it with %s first", version, table, baseline, LastReleaseBeforeBaseline)
	}
	return nil
}

func sortMigrations(migrationList []migration) {
	sort.Slice(migrationList, func(i, j int) bool {
		return migrationList[i].Version < migrationList[j].Version
	})
}
