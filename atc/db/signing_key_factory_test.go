package db_test

import (
	"encoding/json"
	"time"

	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/go-jose/go-jose/v4"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SigningKey factory", func() {
	var (
		factory db.SigningKeyFactory
	)

	BeforeEach(func() {
		factory = db.NewSigningKeyFactory(logger, dbConn)
	})

	It("can store and retrieve signing keys", func() {
		rsaKey1, err := idtoken.GenerateNewKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())

		rsaKey2, err := idtoken.GenerateNewKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())

		previousKeys, err := factory.GetAllKeys()
		Expect(err).ToNot(HaveOccurred())
		Expect(previousKeys).To(HaveLen(0))

		Expect(factory.CreateKey(*rsaKey1)).To(Succeed())
		// make sure there is a measurable difference in created_at of the two keys
		time.Sleep(1 * time.Second)
		Expect(factory.CreateKey(*rsaKey2)).To(Succeed())

		storedKeys, err := factory.GetAllKeys()
		Expect(err).ToNot(HaveOccurred())
		Expect(storedKeys).To(HaveLen(2))

		Expect(storedKeys[0].JWK().KeyID).To(Equal(rsaKey1.KeyID))
		Expect(storedKeys[1].JWK().KeyID).To(Equal(rsaKey2.KeyID))

		latest, err := factory.GetNewestKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())

		Expect(latest.JWK().KeyID).To(Equal(rsaKey2.KeyID))

		Expect(storedKeys[0].Delete()).To(Succeed())
		storedKeys, err = factory.GetAllKeys()
		Expect(err).ToNot(HaveOccurred())
		Expect(storedKeys).To(HaveLen(1))
		Expect(storedKeys[0].JWK().KeyID).To(Equal(rsaKey2.KeyID))

		_, err = factory.GetNewestKey(db.SigningKeyTypeEC)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("SigningKey factory at rest", func() {
	generate := func() jose.JSONWebKey {
		GinkgoHelper()
		key, err := idtoken.GenerateNewKey(db.SigningKeyTypeEC)
		Expect(err).ToNot(HaveOccurred())
		return *key
	}

	encoded := func(key jose.JSONWebKey) string {
		GinkgoHelper()
		raw, err := json.Marshal(key)
		Expect(err).ToNot(HaveOccurred())
		return string(raw)
	}

	privatePart := func(key jose.JSONWebKey) string {
		GinkgoHelper()
		var fields map[string]any
		Expect(json.Unmarshal([]byte(encoded(key)), &fields)).To(Succeed())
		d, ok := fields["d"].(string)
		Expect(ok).To(BeTrue(), "the key carries its private part")
		return d
	}

	stored := func(conn db.DbConn, kid string) (string, *string) {
		GinkgoHelper()
		var (
			jwk   string
			nonce *string
		)
		Expect(conn.QueryRow(`SELECT jwk, nonce FROM signing_keys WHERE kid = $1`, kid).Scan(&jwk, &nonce)).To(Succeed())
		return jwk, nonce
	}

	// decrypted is the plaintext of an encrypted row: its jwk is the
	// ciphertext as a JSON string.
	decrypted := func(conn db.DbConn, kid string) []byte {
		GinkgoHelper()
		jwk, nonce := stored(conn, kid)
		Expect(nonce).NotTo(BeNil())
		var ciphertext string
		Expect(json.Unmarshal([]byte(jwk), &ciphertext)).To(Succeed(), "an encrypted jwk is a JSON string")
		plaintext, err := conn.EncryptionStrategy().Decrypt(ciphertext, nonce)
		Expect(err).ToNot(HaveOccurred())
		return plaintext
	}

	// insertLegacy writes a key the way every web before the nonce did.
	insertLegacy := func(conn db.DbConn, key jose.JSONWebKey) {
		GinkgoHelper()
		_, err := conn.Exec(`INSERT INTO signing_keys (kid, kty, jwk) VALUES ($1, 'EC', $2)`, key.KeyID, encoded(key))
		Expect(err).ToNot(HaveOccurred())
	}

	// readLegacy reads a key the way every web before the nonce does: it
	// is what an older replica does with the row during a rolling upgrade.
	readLegacy := func(conn db.DbConn, kid string) jose.JSONWebKey {
		GinkgoHelper()
		var raw []byte
		Expect(conn.QueryRow(`SELECT jwk FROM signing_keys WHERE kid = $1`, kid).Scan(&raw)).To(Succeed())
		var key jose.JSONWebKey
		Expect(json.Unmarshal(raw, &key)).To(Succeed())
		return key
	}

	keyless := func() db.DbConn {
		GinkgoHelper()
		conn, err := db.Open(logger, "pgx", postgresRunner.DataSourceName(), nil, nil, "keyless", nil)
		Expect(err).ToNot(HaveOccurred())
		conn.SetMaxOpenConns(1)
		DeferCleanup(conn.Close)
		return conn
	}

	It("ships with signing keys stored in plaintext, while a replica may predate encrypted keys", func() {
		Expect(migration.SigningKeysEncryptedAtRest).To(BeFalse())
	})

	Context("as this release writes", func() {
		var factory db.SigningKeyFactory

		BeforeEach(func() {
			factory = db.NewSigningKeyFactory(logger, dbConn)
		})

		It("stores a key that a web predating encryption can read, even with an encryption key configured", func() {
			key := generate()
			Expect(factory.CreateKey(key)).To(Succeed())

			_, nonce := stored(dbConn, key.KeyID)
			Expect(nonce).To(BeNil())
			Expect(encoded(readLegacy(dbConn, key.KeyID))).To(MatchJSON(encoded(key)))
		})

		It("reads a key a later release stored encrypted", func() {
			key := generate()
			Expect(db.NewSigningKeyFactoryEncryptingWrites(logger, dbConn).CreateKey(key)).To(Succeed())

			all, err := factory.GetAllKeys()
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(1))
			Expect(encoded(all[0].JWK())).To(MatchJSON(encoded(key)))

			newest, err := factory.GetNewestKey(db.SigningKeyTypeEC)
			Expect(err).ToNot(HaveOccurred())
			Expect(encoded(newest.JWK())).To(MatchJSON(encoded(key)))
		})
	})

	Context("once every web can decrypt a signing key", func() {
		var factory db.SigningKeyFactory

		BeforeEach(func() {
			factory = db.NewSigningKeyFactoryEncryptingWrites(logger, dbConn)
		})

		It("stores the key encrypted and reads it back unchanged", func() {
			key := generate()
			Expect(factory.CreateKey(key)).To(Succeed())

			jwk, _ := stored(dbConn, key.KeyID)
			Expect(jwk).NotTo(ContainSubstring(privatePart(key)))
			Expect(decrypted(dbConn, key.KeyID)).To(MatchJSON(encoded(key)))

			all, err := factory.GetAllKeys()
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(1))
			Expect(encoded(all[0].JWK())).To(MatchJSON(encoded(key)))
			Expect(all[0].KeyType()).To(Equal(db.SigningKeyTypeEC))

			newest, err := factory.GetNewestKey(db.SigningKeyTypeEC)
			Expect(err).ToNot(HaveOccurred())
			Expect(encoded(newest.JWK())).To(MatchJSON(encoded(key)))
		})

		It("reads a plaintext key without rewriting it", func() {
			key := generate()
			insertLegacy(dbConn, key)

			all, err := factory.GetAllKeys()
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(1))
			Expect(encoded(all[0].JWK())).To(MatchJSON(encoded(key)))
			newest, err := factory.GetNewestKey(db.SigningKeyTypeEC)
			Expect(err).ToNot(HaveOccurred())
			Expect(encoded(newest.JWK())).To(MatchJSON(encoded(key)))

			_, nonce := stored(dbConn, key.KeyID)
			Expect(nonce).To(BeNil())
		})

		It("deletes an encrypted key", func() {
			key := generate()
			Expect(factory.CreateKey(key)).To(Succeed())
			all, err := factory.GetAllKeys()
			Expect(err).ToNot(HaveOccurred())
			Expect(all[0].Delete()).To(Succeed())

			all, err = factory.GetAllKeys()
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(BeEmpty())
		})

		Context("without an encryption key", func() {
			var conn db.DbConn

			BeforeEach(func() {
				conn = keyless()
				factory = db.NewSigningKeyFactoryEncryptingWrites(logger, conn)
			})

			It("stores and reads keys in plaintext, as before", func() {
				created := generate()
				Expect(factory.CreateKey(created)).To(Succeed())
				legacy := generate()
				insertLegacy(conn, legacy)

				all, err := factory.GetAllKeys()
				Expect(err).ToNot(HaveOccurred())
				Expect(all).To(HaveLen(2))

				for _, key := range []jose.JSONWebKey{created, legacy} {
					_, nonce := stored(conn, key.KeyID)
					Expect(nonce).To(BeNil())
					Expect(encoded(readLegacy(conn, key.KeyID))).To(MatchJSON(encoded(key)))
				}
			})

			It("leaves out and logs a key it cannot decrypt, and still serves the others", func() {
				unreadable := generate()
				Expect(db.NewSigningKeyFactoryEncryptingWrites(logger, dbConn).CreateKey(unreadable)).To(Succeed())
				readable := generate()
				insertLegacy(conn, readable)

				all, err := factory.GetAllKeys()
				Expect(err).ToNot(HaveOccurred())
				Expect(all).To(HaveLen(1))
				Expect(all[0].ID()).To(Equal(readable.KeyID))

				Expect(logger.Logs()).To(ContainElement(SatisfyAll(
					HaveField("Message", "test.signing-key-factory.skipped-unreadable-signing-key"),
					HaveField("Data", HaveKeyWithValue("kid", unreadable.KeyID)),
				)))
			})
		})
	})
})
