package migration_test

import (
	"database/sql"

	"github.com/concourse/concourse/atc/db/migration"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MCP authorization encryption", func() {
	It("encrypts, rotates and decrypts retained provider credentials", func() {
		conn, err := sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close()
		migrator := migration.NewMigrator(conn, nil)
		Expect(migrator.Up(nil, nil)).To(Succeed())
		plain := `{"identity":{"refresh_token":"provider-refresh-secret"}}`
		_, err = conn.Exec(`INSERT INTO mcp_oauth_state(id,kind,data,expires_at) VALUES ('identity:test','identity',$1,CURRENT_TIMESTAMP + interval '90 days')`, plain)
		Expect(err).NotTo(HaveOccurred())
		first := createKey("AES256Key-32Characters1234567890")
		second := createKey("AES256Key-32Characters0987654321")
		Expect(migrator.Up(first, nil)).To(Succeed())
		var cipher string
		var nonce *string
		Expect(conn.QueryRow("SELECT data,nonce FROM mcp_oauth_state WHERE id='identity:test'").Scan(&cipher, &nonce)).To(Succeed())
		Expect(cipher).NotTo(ContainSubstring("provider-refresh-secret"))
		Expect(nonce).NotTo(BeNil())
		Expect(migrator.Up(second, first)).To(Succeed())
		Expect(conn.QueryRow("SELECT data,nonce FROM mcp_oauth_state WHERE id='identity:test'").Scan(&cipher, &nonce)).To(Succeed())
		decoded, err := second.Decrypt(cipher, nonce)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(decoded)).To(Equal(plain))
		Expect(migrator.Up(nil, second)).To(Succeed())
		Expect(conn.QueryRow("SELECT data,nonce FROM mcp_oauth_state WHERE id='identity:test'").Scan(&cipher, &nonce)).To(Succeed())
		Expect(nonce).To(BeNil())
		Expect(cipher).To(Equal(plain))
	})
})
