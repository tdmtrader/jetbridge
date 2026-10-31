package migration_test

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption", func() {
	var (
		err         error
		db          *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}
		key         *encryption.Key
	)

	BeforeEach(func() {
		db, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())

		for i := range lock.FactoryCount {
			lockDB[i], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		lockFactory = lock.NewLockFactory(lockDB, fakeLogFunc, fakeLogFunc)
		key = createKey("AES256Key-32Characters1234567890")
	})

	AfterEach(func() {
		_ = db.Close()
		for _, closer := range lockDB {
			closer.Close()
		}
	})

	It("refuses to migrate without a key", func() {
		migrator := migration.NewMigrator(testLogger, db, lockFactory)

		Expect(migrator.Up(nil)).To(MatchError(encryption.ErrNoKey))
	})

	It("runs at a schema version that predates some of the encrypted columns", func() {
		migrator := migration.NewMigrator(testLogger, db, lockFactory)

		// At the baseline, pipeline_run_definitions and mcp_oauth_state do
		// not exist yet; the plaintext pass skips what the schema does not have.
		Expect(migrator.Migrate(key, 1765921815)).To(Succeed())
	})

	Context("starting with a database that holds plaintext rows", func() {
		It("encrypts them on the first migration under the key", func() {
			migrator := migration.NewMigrator(testLogger, db, lockFactory)

			err := migrator.Up(key)
			Expect(err).ToNot(HaveOccurred())

			insertPlaintextIntoEncryptedColumn(db, "test")

			err = migrator.Up(key)
			Expect(err).NotTo(HaveOccurred())
			Expect(isEncryptedWith(db, key, "test")).To(BeTrue())
		})
	})

	It("logs the encryption pass to the caller's logger", func() {
		Expect(migration.NewMigrator(testLogger, db, lockFactory).Up(key)).To(Succeed())
		insertPlaintextIntoEncryptedColumn(db, "test")

		logger := lagertest.NewTestLogger("web")
		conn, err := migration.NewOpenHelper(logger, "pgx", postgresRunner.DataSourceName(), lockFactory, key).Open()
		Expect(err).NotTo(HaveOccurred())
		_ = conn.Close()

		Expect(logger.LogMessages()).To(ContainElement("web.migrations.encrypt.table.encrypted-existing-plaintext-data"))
	})

	Context("starting with a partially encrypted database", func() {
		It("encrypts the plaintext rows and leaves the encrypted ones readable", func() {
			migrator := migration.NewMigrator(testLogger, db, lockFactory)

			err := migrator.Up(key)
			Expect(err).ToNot(HaveOccurred())

			insertPlaintextIntoEncryptedColumn(db, "row1")
			insertIntoEncryptedColumn(db, key, "row2")

			err = migrator.Up(key)
			Expect(err).NotTo(HaveOccurred())
			Expect(isEncryptedWith(db, key, "row1")).To(BeTrue())
			Expect(isEncryptedWith(db, key, "row2")).To(BeTrue())
		})
	})

	Context("starting with a database encrypted under another key", func() {
		It("cannot read it: a different key means a restore from backup", func() {
			migrator := migration.NewMigrator(testLogger, db, lockFactory)
			other := createKey("AES256Key-32Characters0987654321")

			err := migrator.Up(other)
			Expect(err).ToNot(HaveOccurred())

			insertIntoEncryptedColumn(db, other, "test")

			err = migrator.Up(key)
			Expect(err).NotTo(HaveOccurred(), "the encrypted row is not plaintext, so the pass leaves it alone")
			Expect(isEncryptedWith(db, key, "test")).To(BeFalse())
			Expect(isEncryptedWith(db, other, "test")).To(BeTrue())
		})
	})
})

func insertPlaintextIntoEncryptedColumn(db *sql.DB, name string) {
	GinkgoHelper()
	_, err := db.Exec(`INSERT INTO teams(name, legacy_auth, nonce) VALUES($1, $2, NULL)`, name, "{}")
	Expect(err).ToNot(HaveOccurred())
}

func insertIntoEncryptedColumn(db *sql.DB, key *encryption.Key, name string) {
	GinkgoHelper()
	ciphertext, nonce, err := key.Encrypt([]byte("{}"))
	Expect(err).ToNot(HaveOccurred())
	_, err = db.Exec(`INSERT INTO teams(name, legacy_auth, nonce) VALUES($1, $2, $3)`, name, ciphertext, nonce)
	Expect(err).ToNot(HaveOccurred())
}

func isEncryptedWith(db *sql.DB, key *encryption.Key, name string) bool {
	GinkgoHelper()
	var (
		ciphertext string
		nonce      *string
	)
	row := db.QueryRow(`SELECT legacy_auth, nonce FROM teams WHERE name = $1`, name)
	err := row.Scan(&ciphertext, &nonce)
	Expect(err).ToNot(HaveOccurred())

	_, err = key.Decrypt(ciphertext, nonce)
	return err == nil
}

// createKey generates an encryption.Key from a 32 characters key
func createKey(key string) *encryption.Key {
	GinkgoHelper()
	k := []byte(key)

	block, err := aes.NewCipher(k)
	Expect(err).ToNot(HaveOccurred())

	aesgcm, err := cipher.NewGCM(block)
	Expect(err).ToNot(HaveOccurred())

	return encryption.NewKey(aesgcm)
}

var _ = Describe("Encryption on the test helpers' one-connection pool", func() {
	// postgresrunner.OpenDB caps the pool at one connection. Reading a
	// plaintext row and updating it on the same pool used to wait forever
	// for the connection the cursor held.
	It("encrypts a plaintext row without deadlocking", func(ctx SpecContext) {
		db := postgresRunner.OpenDB()
		defer db.Close()

		_, err := db.Exec(`INSERT INTO teams(name, legacy_auth, nonce) VALUES('one-conn', '{}', NULL)`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migration.NewMigrator(testLogger, db, nil).Up(postgresrunner.EncryptionKey())).To(Succeed())
		Expect(isEncryptedWith(db, postgresrunner.EncryptionKey(), "one-conn")).To(BeTrue())
	}, SpecTimeout(time.Minute))

	// More plaintext rows than one batch holds, so the pass loops: every row
	// ends up encrypted and none is visited twice.
	It("encrypts a column larger than one batch", func(ctx SpecContext) {
		db := postgresRunner.OpenDB()
		defer db.Close()

		const rows = 2500
		_, err := db.Exec(`INSERT INTO teams(name, legacy_auth, nonce)
			SELECT 'bulk-' || i, '{}', NULL FROM generate_series(1, ` + fmt.Sprint(rows) + `) AS i`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migration.NewMigrator(testLogger, db, nil).Up(postgresrunner.EncryptionKey())).To(Succeed())

		var plaintextLeft, encrypted int
		Expect(db.QueryRow(`SELECT count(*) FROM teams WHERE name LIKE 'bulk-%' AND nonce IS NULL`).Scan(&plaintextLeft)).To(Succeed())
		Expect(plaintextLeft).To(BeZero())
		Expect(db.QueryRow(`SELECT count(*) FROM teams WHERE name LIKE 'bulk-%' AND nonce IS NOT NULL`).Scan(&encrypted)).To(Succeed())
		Expect(encrypted).To(Equal(rows))
		Expect(isEncryptedWith(db, postgresrunner.EncryptionKey(), "bulk-1")).To(BeTrue())
		Expect(isEncryptedWith(db, postgresrunner.EncryptionKey(), "bulk-"+fmt.Sprint(rows))).To(BeTrue())
	}, SpecTimeout(2*time.Minute))
})
