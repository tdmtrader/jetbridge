// The `concourse migrate` command: the one-shot Migration subcommand that opens
// its own lock connections, builds a stderr logger and migrates the database to
// the latest version with the --encryption-key it was given. It shares nothing
// with RunCommand but constructLockConns and the driver names.

package atccmd

import (
	"errors"
	"fmt"
	"os"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/flag/v2"
)

type Migration struct {
	lockFactory lock.LockFactory
	logger      lager.Logger

	Postgres               flag.PostgresConfig `group:"PostgreSQL Configuration" namespace:"postgres"`
	EncryptionKey          flag.Cipher         `long:"encryption-key"     description:"A 16 or 32 length key used to encrypt sensitive information before storing it in the database. Required to run migrations."`
	MigrateToLatestVersion bool                `long:"migrate-to-latest-version" description:"Migrate to the latest migration version and exit"`
}

func (m *Migration) Execute(args []string) error {
	db.SetupConnectionRetryingDriver(
		defaultDriverName,
		m.Postgres.ConnectionString(),
		retryingDriverName,
	)

	lockConns, err := constructLockConns(retryingDriverName, m.Postgres.ConnectionString())
	if err != nil {
		return err
	}
	defer func() {
		for _, conn := range lockConns {
			conn.Close()
		}
	}()

	m.lockFactory = lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)

	// Logs go to stderr, as a one-shot command's diagnostics should, leaving
	// stdout to whatever the command itself reports.
	logs := flag.Lager{LogLevel: flag.LogLevelInfo}
	logs.SetWriterSink(os.Stderr)
	m.logger, _ = logs.Logger("migrate")

	if m.MigrateToLatestVersion {
		return m.migrateToLatestVersion()
	}
	return errors.New("must specify `--migrate-to-latest-version`")
}

// encryptionKey is the key the migrate command was started with, or
// encryption.ErrNoKey naming the flag when it was not.
func (cmd *Migration) encryptionKey() (*encryption.Key, error) {
	if cmd.EncryptionKey.AEAD == nil {
		return nil, fmt.Errorf("--encryption-key is required to run migrations: %w", encryption.ErrNoKey)
	}
	return encryption.NewKey(cmd.EncryptionKey.AEAD), nil
}

func (cmd *Migration) migrateToLatestVersion() error {
	key, err := cmd.encryptionKey()
	if err != nil {
		return err
	}

	helper := migration.NewOpenHelper(
		cmd.logger,
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		cmd.lockFactory,
		key,
	)

	version, err := helper.SupportedVersion()
	if err != nil {
		return err
	}

	return helper.MigrateToVersion(version)
}
