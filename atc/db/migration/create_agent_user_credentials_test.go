package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Create agent user credentials", func() {
	const postMigrationVersion = 1773106020

	It("creates the vault table with FK cascade, kind check, and (user_id, kind) uniqueness", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		_, err := db.Exec(`INSERT INTO users(sub, username, connector) VALUES('cred-mig-sub','alice','local')`)
		Expect(err).NotTo(HaveOccurred())
		var userID int
		Expect(db.QueryRow(`SELECT id FROM users WHERE sub='cred-mig-sub'`).Scan(&userID)).To(Succeed())

		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'anthropic_oauth', 'ciphertext')`, userID)
		Expect(err).NotTo(HaveOccurred())

		By("rejecting a duplicate (user_id, kind)")
		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'anthropic_oauth', 'other')`, userID)
		Expect(err).To(HaveOccurred())

		By("rejecting an unknown kind via CHECK")
		_, err = db.Exec(`INSERT INTO agent_user_credentials(user_id, user_name, kind, encrypted_token)
			VALUES($1, 'alice', 'openai', 'x')`, userID)
		Expect(err).To(HaveOccurred())

		By("allowing NULL nonce (encryption disabled) and NULL expiry")
		var nonce, expires any
		Expect(db.QueryRow(`SELECT nonce, expires_at FROM agent_user_credentials WHERE user_id=$1`, userID).
			Scan(&nonce, &expires)).To(Succeed())
		Expect(nonce).To(BeNil())
		Expect(expires).To(BeNil())

		By("cascading on user deletion")
		_, err = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
		Expect(err).NotTo(HaveOccurred())
		var n int
		Expect(db.QueryRow(`SELECT COUNT(*) FROM agent_user_credentials`).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(0))
	})
})
