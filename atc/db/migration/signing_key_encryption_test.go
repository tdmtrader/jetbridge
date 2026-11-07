package migration_test

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/concourse/concourse/atc/db/migration"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	signingKeyNonceVersion = 1789793169
	signingKeyJWK          = `{"kty":"oct","kid":"k1","k":"c2lnbmluZy1rZXktc2VjcmV0"}`
)

var _ = Describe("idtoken signing key encryption", func() {
	var conn *sql.DB

	BeforeEach(func() {
		var err error
		conn, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
	})

	stored := func(kid string) (string, *string) {
		GinkgoHelper()
		var (
			jwk   string
			nonce *string
		)
		Expect(conn.QueryRow(`SELECT jwk, nonce FROM signing_keys WHERE kid = $1`, kid).Scan(&jwk, &nonce)).To(Succeed())
		return jwk, nonce
	}

	It("adds a nullable nonce, so a web that predates it still inserts and reads keys", func() {
		Expect(migration.NewMigrator(testLogger, conn, nil).Up(nil, nil)).To(Succeed())

		_, err := conn.Exec(`INSERT INTO signing_keys (kid, kty, jwk) VALUES ('k1', 'RSA', $1)`, signingKeyJWK)
		Expect(err).NotTo(HaveOccurred())

		var raw []byte
		Expect(conn.QueryRow(`SELECT jwk FROM signing_keys WHERE kid = 'k1'`).Scan(&raw)).To(Succeed())
		Expect(raw).To(MatchJSON(signingKeyJWK))
		_, nonce := stored("k1")
		Expect(nonce).To(BeNil())
	})

	// A 0.4.4 web keeps running through the rolling upgrade. pgx caches its
	// statements as prepared statements per connection, and Postgres refuses
	// a cached plan whose result type the migration changed.
	It("keeps a running web's cached signing key statements working across the upgrade", func() {
		previous := postgresRunner.OpenDBAtVersion(signingKeyNonceVersion - 1)
		DeferCleanup(previous.Close)
		previous.SetMaxIdleConns(1)

		// The statements signing_key_factory.go and signing_key.go issued
		// before the nonce, verbatim.
		insert := `INSERT INTO signing_keys (kid,kty,jwk) VALUES ($1,$2,$3)`
		all := `SELECT jwk, created_at FROM signing_keys ORDER BY created_at ASC`
		newest := `SELECT jwk, created_at FROM signing_keys WHERE kty = $1 ORDER BY created_at DESC LIMIT 1`
		remove := `DELETE FROM signing_keys WHERE kid = $1`
		exercise := func(kid string) {
			GinkgoHelper()
			_, err := previous.Exec(insert, kid, "RSA", signingKeyJWK)
			Expect(err).NotTo(HaveOccurred())
			var (
				raw       []byte
				createdAt time.Time
			)
			rows, err := previous.Query(all)
			Expect(err).NotTo(HaveOccurred())
			for rows.Next() {
				Expect(rows.Scan(&raw, &createdAt)).To(Succeed())
				Expect(raw).To(MatchJSON(signingKeyJWK))
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			Expect(rows.Close()).To(Succeed())
			Expect(previous.QueryRow(newest, "RSA").Scan(&raw, &createdAt)).To(Succeed())
			Expect(raw).To(MatchJSON(signingKeyJWK))
			_, err = previous.Exec(remove, kid)
			Expect(err).NotTo(HaveOccurred())
		}

		exercise("before")
		Expect(migration.NewMigrator(testLogger, conn, nil).Up(nil, nil)).To(Succeed())
		exercise("after")
	})

	It("rotates an encrypted signing key with the rest of the database, and decrypts it when the key is removed", func() {
		migrator := migration.NewMigrator(testLogger, conn, nil)
		Expect(migrator.Up(nil, nil)).To(Succeed())
		first := createKey("AES256Key-32Characters1234567890")
		second := createKey("AES256Key-32Characters0987654321")

		ciphertext, nonce, err := first.Encrypt([]byte(signingKeyJWK))
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Exec(`INSERT INTO signing_keys (kid, kty, jwk, nonce) VALUES ('k1', 'RSA', $1, $2)`, storedCiphertext(ciphertext), nonce)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Up(second, first)).To(Succeed())
		jwk, nonce := stored("k1")
		Expect(nonce).NotTo(BeNil())
		Expect(jwk).NotTo(ContainSubstring("c2lnbmluZy1rZXktc2VjcmV0"))
		plaintext, err := second.Decrypt(ciphertextOf(jwk), nonce)
		Expect(err).NotTo(HaveOccurred())
		Expect(plaintext).To(MatchJSON(signingKeyJWK))

		Expect(migrator.Up(nil, second)).To(Succeed())
		jwk, nonce = stored("k1")
		Expect(nonce).To(BeNil())
		Expect(jwk).To(MatchJSON(signingKeyJWK))
	})

	// A web that predates the nonce cannot decrypt a signing key, so the
	// start-up pass must not encrypt one behind its back during a rolling
	// upgrade or after a rollback.
	It("leaves a plaintext signing key alone when a key is configured", func() {
		migrator := migration.NewMigrator(testLogger, conn, nil)
		Expect(migrator.Up(nil, nil)).To(Succeed())
		_, err := conn.Exec(`INSERT INTO signing_keys (kid, kty, jwk) VALUES ('k1', 'RSA', $1)`, signingKeyJWK)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Up(createKey("AES256Key-32Characters1234567890"), nil)).To(Succeed())

		jwk, nonce := stored("k1")
		Expect(nonce).To(BeNil())
		Expect(jwk).To(MatchJSON(signingKeyJWK))
	})

	It("encrypts a plaintext signing key on start once signing keys are encrypted at rest, keeping the key", func() {
		DeferCleanup(migration.EncryptSigningKeysOnStart())
		migrator := migration.NewMigrator(testLogger, conn, nil)
		Expect(migrator.Up(nil, nil)).To(Succeed())
		_, err := conn.Exec(`INSERT INTO signing_keys (kid, kty, jwk) VALUES ('k1', 'RSA', $1)`, signingKeyJWK)
		Expect(err).NotTo(HaveOccurred())
		key := createKey("AES256Key-32Characters1234567890")

		Expect(migrator.Up(key, nil)).To(Succeed())

		jwk, nonce := stored("k1")
		Expect(nonce).NotTo(BeNil())
		Expect(jwk).NotTo(ContainSubstring("c2lnbmluZy1rZXktc2VjcmV0"))
		plaintext, err := key.Decrypt(ciphertextOf(jwk), nonce)
		Expect(err).NotTo(HaveOccurred())
		Expect(plaintext).To(MatchJSON(signingKeyJWK))
	})

	It("rotates a database whose schema predates the nonce", func() {
		database := postgresRunner.OpenDBAtVersion(signingKeyNonceVersion - 1)
		DeferCleanup(database.Close)
		first := createKey("AES256Key-32Characters1234567890")
		second := createKey("AES256Key-32Characters0987654321")
		migrator := migration.NewMigrator(testLogger, database, nil)

		Expect(migrator.Migrate(first, nil, signingKeyNonceVersion-1)).To(Succeed())
		Expect(migrator.Migrate(second, first, signingKeyNonceVersion-1)).To(Succeed())
		Expect(migrator.Migrate(nil, second, signingKeyNonceVersion-1)).To(Succeed())
	})
})

// storedCiphertext is ciphertext as a JSON string, the form jwk keeps it in.
func storedCiphertext(ciphertext string) string {
	GinkgoHelper()
	encoded, err := json.Marshal(ciphertext)
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}

func ciphertextOf(stored string) string {
	GinkgoHelper()
	var ciphertext string
	Expect(json.Unmarshal([]byte(stored), &ciphertext)).To(Succeed(), "an encrypted jwk is a JSON string")
	return ciphertext
}
