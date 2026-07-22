package migration_test

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

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
	)

	BeforeEach(func() {
		db, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())

		for i := range lock.FactoryCount {
			lockDB[i], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		lockFactory = lock.NewLockFactory(lockDB, fakeLogFunc, fakeLogFunc)
	})

	AfterEach(func() {
		_ = db.Close()
		for _, closer := range lockDB {
			closer.Close()
		}
	})

	Context("starting with unencrypted DB", func() {
		var (
			key *encryption.Key
		)
		BeforeEach(func() {
			key = createKey("AES256Key-32Characters1234567890")
		})

		It("encrypts the database", func() {
			migrator := migration.NewMigrator(db, lockFactory)

			err := migrator.Up(nil, nil)
			Expect(err).ToNot(HaveOccurred())

			insertIntoEncryptedColumn(db, encryption.NewNoEncryption(), "test")

			err = migrator.Up(key, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(isEncryptedWith(db, key, "test")).To(BeTrue())
		})

		It("encrypts every workflow-run provenance field with its dedicated nonce", func() {
			migrator := migration.NewMigrator(db, lockFactory)
			Expect(migrator.Up(nil, nil)).To(Succeed())
			insertWorkflowRunProvenance(db, encryption.NewNoEncryption(), "workflow-plaintext")

			Expect(migrator.Up(key, nil)).To(Succeed())
			Expect(isWorkflowRunProvenanceEncryptedWith(db, key, "workflow-plaintext")).To(BeTrue())
		})
	})

	Context("starting with encrypted DB", func() {
		var (
			key1 *encryption.Key
			key2 *encryption.Key
		)

		BeforeEach(func() {
			key1 = createKey("AES256Key-32Characters1234567890")
			key2 = createKey("AES256Key-32Characters0987654321")
		})

		Context("removing the encryption key", func() {
			It("decrypts the database", func() {
				migrator := migration.NewMigrator(db, lockFactory)

				err := migrator.Up(key1, nil)
				Expect(err).ToNot(HaveOccurred())

				insertIntoEncryptedColumn(db, key1, "test")

				err = migrator.Up(nil, key1)
				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, encryption.NewNoEncryption(), "test")).To(BeTrue())
			})

			It("decrypts every workflow-run provenance field through its dedicated nonce", func() {
				migrator := migration.NewMigrator(db, lockFactory)
				Expect(migrator.Up(key1, nil)).To(Succeed())
				insertWorkflowRunProvenance(db, key1, "workflow-decrypt")

				Expect(migrator.Up(nil, key1)).To(Succeed())
				Expect(isWorkflowRunProvenanceEncryptedWith(db, encryption.NewNoEncryption(), "workflow-decrypt")).To(BeTrue())
			})
		})

		Context("rotating the encryption key", func() {
			It("re-encrypts the database with the new key", func() {
				migrator := migration.NewMigrator(db, lockFactory)

				err := migrator.Up(key2, nil)
				Expect(err).ToNot(HaveOccurred())

				insertIntoEncryptedColumn(db, key2, "test")

				err = migrator.Up(key1, key2)
				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, key1, "test")).To(BeTrue())
			})

			It("rotates every workflow-run provenance field through its dedicated nonce", func() {
				migrator := migration.NewMigrator(db, lockFactory)
				Expect(migrator.Up(key1, nil)).To(Succeed())
				insertWorkflowRunProvenance(db, key1, "workflow-rotate")

				Expect(migrator.Up(key2, key1)).To(Succeed())
				Expect(isWorkflowRunProvenanceEncryptedWith(db, key2, "workflow-rotate")).To(BeTrue())
			})

			It("rotates the key while doing a migration", func() {
				migrator := migration.NewMigrator(db, lockFactory)

				// do all the necessary schema migrations to this particular version
				err = migrator.Migrate(nil, nil, 1513895878)
				Expect(err).ToNot(HaveOccurred())

				insertIntoEncryptedColumnLegacy(db, key1, "test")

				// the migration after this one, 1516643303 needs to re-encrypt the auth column
				err := migrator.Up(key2, key1)
				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, key2, "test")).To(BeTrue())
			})
		})
	})

	Context("migrating down past a migration that drops an encrypted table", func() {
		var (
			key *encryption.Key
		)

		BeforeEach(func() {
			key = createKey("AES256Key-32Characters1234567890")
		})

		It("skips encrypted tables that no longer exist", func() {
			migrator := migration.NewMigrator(db, lockFactory)

			err := migrator.Up(key, nil)
			Expect(err).ToNot(HaveOccurred())

			// 1773106011 predates 1773106020_create_agent_user_credentials,
			// so the downgrade drops agent_user_credentials; the
			// post-migration encryption pass must skip the absent table
			// rather than fail the whole downgrade.
			err = migrator.Migrate(key, nil, 1773106011)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("starting with partially encrypted DB", func() {
		var (
			key1     *encryption.Key
			key2     *encryption.Key
			migrator migration.Migrator
		)

		BeforeEach(func() {
			key1 = createKey("AES256Key-32Characters1234567890")
			key2 = createKey("AES256Key-32Characters0987654321")
			migrator = migration.NewMigrator(db, lockFactory)

			err := migrator.Up(nil, nil)
			Expect(err).ToNot(HaveOccurred())

			insertIntoEncryptedColumn(db, encryption.NewNoEncryption(), "row1")
			insertIntoEncryptedColumn(db, key1, "row2")
		})

		Context("adding the encryption key", func() {
			It("encrypts the database", func() {
				err = migrator.Up(key1, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, key1, "row1")).To(BeTrue())
				Expect(isEncryptedWith(db, key1, "row2")).To(BeTrue())
			})
		})

		Context("removing the encryption key", func() {
			It("decrypts the database", func() {
				err = migrator.Up(nil, key1)
				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, encryption.NewNoEncryption(), "row1")).To(BeTrue())
				Expect(isEncryptedWith(db, encryption.NewNoEncryption(), "row2")).To(BeTrue())
			})
		})

		Context("rotating the encryption key", func() {
			It("re-encrypts the database with the new key", func() {
				err = migrator.Up(key2, key1)

				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, key2, "row1")).To(BeTrue())
				Expect(isEncryptedWith(db, key2, "row2")).To(BeTrue())
			})
		})

		Context("rotating to the same key", func() {
			It("doesn't break", func() {
				err = migrator.Up(key1, key1)

				Expect(err).NotTo(HaveOccurred())
				Expect(isEncryptedWith(db, key1, "row1")).To(BeTrue())
				Expect(isEncryptedWith(db, key1, "row2")).To(BeTrue())
			})
		})
	})
})

// used to test database versions before the column got renamed
func insertIntoEncryptedColumnLegacy(db *sql.DB, strategy encryption.Strategy, name string) {
	ciphertext, nonce, err := strategy.Encrypt([]byte("{}"))
	Expect(err).ToNot(HaveOccurred())
	var teamID int
	err = db.QueryRow(`INSERT INTO teams(name, auth, nonce) VALUES($1, $2, $3) RETURNING id`, name, ciphertext, nonce).Scan(&teamID)
	Expect(err).ToNot(HaveOccurred())

	_, err = db.Exec(fmt.Sprintf(`CREATE TABLE team_build_events_%d () INHERITS (build_events)`, teamID))
	Expect(err).ToNot(HaveOccurred())
}

func insertIntoEncryptedColumn(db *sql.DB, strategy encryption.Strategy, name string) {
	ciphertext, nonce, err := strategy.Encrypt([]byte("{}"))
	Expect(err).ToNot(HaveOccurred())
	_, err = db.Exec(`INSERT INTO teams(name, legacy_auth, nonce) VALUES($1, $2, $3)`, name, ciphertext, nonce)
	Expect(err).ToNot(HaveOccurred())
}

func isEncryptedWith(db *sql.DB, strategy encryption.Strategy, name string) bool {
	var (
		ciphertext string
		nonce      *string
	)
	row := db.QueryRow(`SELECT legacy_auth, nonce FROM teams WHERE name = $1`, name)
	err := row.Scan(&ciphertext, &nonce)
	Expect(err).ToNot(HaveOccurred())

	_, err = strategy.Decrypt(ciphertext, nonce)
	return err == nil
}

func insertWorkflowRunProvenance(db *sql.DB, strategy encryption.Strategy, name string) {
	const planPlaintext = `{"id":"secret-plan","private":"top-secret-api-token","task":"review"}`
	hashPlaintext := name + "-actual-plan-hash"
	dependenciesPlaintext := fmt.Sprintf(`{"version":1,"resources":[{"source_identity_hash":%q}],"images":[],"platform_resource_types":[]}`, name+"-source-hash")
	planCiphertext, planNonce, err := strategy.Encrypt([]byte(planPlaintext))
	Expect(err).NotTo(HaveOccurred())
	hashCiphertext, hashNonce, err := strategy.Encrypt([]byte(hashPlaintext))
	Expect(err).NotTo(HaveOccurred())
	dependenciesCiphertext, dependenciesNonce, err := strategy.Encrypt([]byte(dependenciesPlaintext))
	Expect(err).NotTo(HaveOccurred())

	var definitionID int
	Expect(db.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'migration-test', 3, 1)
		RETURNING id
	`, name, name+"-definition-hash").Scan(&definitionID)).To(Succeed())
	_, err = db.Exec(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name,
			 workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 actual_plan, actual_plan_nonce,
			 actual_plan_hash, actual_plan_hash_nonce,
			 resolved_dependencies, resolved_dependencies_nonce,
			 origin_kind, origin_reference, created_by, status, planned_build_id)
		VALUES
			(1, 'migration-team', $1, $2, 1, 3, 1,
			 $3, $2, '{}', $4, $5, $6, $7, $8, $9, $10,
			 'migration', $2, 'migration-test', 'admitting', 1)
	`, definitionID, name, name+"-definition-hash", name+"-parameterized-hash",
		planCiphertext, planNonce, hashCiphertext, hashNonce, dependenciesCiphertext, dependenciesNonce)
	Expect(err).NotTo(HaveOccurred())
}

func isWorkflowRunProvenanceEncryptedWith(db *sql.DB, strategy encryption.Strategy, name string) bool {
	var planCiphertext, hashCiphertext, dependenciesCiphertext string
	var planNonce, hashNonce, dependenciesNonce *string
	Expect(db.QueryRow(`
		SELECT actual_plan, actual_plan_nonce,
		       actual_plan_hash, actual_plan_hash_nonce,
		       resolved_dependencies, resolved_dependencies_nonce
		FROM agent_workflow_runs
		WHERE idempotency_key = $1
	`, name).Scan(
		&planCiphertext, &planNonce, &hashCiphertext, &hashNonce,
		&dependenciesCiphertext, &dependenciesNonce,
	)).To(Succeed())
	planPlaintext, err := strategy.Decrypt(planCiphertext, planNonce)
	if err != nil || string(planPlaintext) != `{"id":"secret-plan","private":"top-secret-api-token","task":"review"}` {
		return false
	}
	hashPlaintext, err := strategy.Decrypt(hashCiphertext, hashNonce)
	if err != nil || string(hashPlaintext) != name+"-actual-plan-hash" {
		return false
	}
	dependenciesPlaintext, err := strategy.Decrypt(dependenciesCiphertext, dependenciesNonce)
	expectedDependencies := fmt.Sprintf(`{"version":1,"resources":[{"source_identity_hash":%q}],"images":[],"platform_resource_types":[]}`, name+"-source-hash")
	return err == nil && string(dependenciesPlaintext) == expectedDependencies
}

// createKey generates an encryption.Key from a 32 characters key
func createKey(key string) *encryption.Key {
	k := []byte(key)

	block, err := aes.NewCipher(k)
	Expect(err).ToNot(HaveOccurred())

	aesgcm, err := cipher.NewGCM(block)
	Expect(err).ToNot(HaveOccurred())

	return encryption.NewKey(aesgcm)
}
