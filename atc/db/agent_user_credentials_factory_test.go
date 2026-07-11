package db_test

import (
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentUserCredentialsFactory", func() {
	var factory db.AgentUserCredentialsFactory

	BeforeEach(func() {
		factory = db.NewAgentUserCredentialsFactory(dbConn)
	})

	createUser := func(sub, name string) int {
		Expect(db.NewUserFactory(dbConn).CreateOrUpdateUser(name, "local", sub)).To(Succeed())
		var id int
		Expect(dbConn.QueryRow(`SELECT id FROM users WHERE sub = $1`, sub).Scan(&id)).To(Succeed())
		return id
	}

	It("resolves users by sub", func() {
		id := createUser("cred-sub-a", "alice")
		gotID, gotName, found, err := factory.UserBySub("cred-sub-a")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotID).To(Equal(id))
		Expect(gotName).To(Equal("alice"))

		_, _, found, err = factory.UserBySub("cred-sub-missing")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("round-trips a credential encrypted with the connection strategy", func() {
		id := createUser("cred-sub-b", "bob")
		exp := time.Now().Add(365 * 24 * time.Hour).Truncate(time.Second)
		Expect(factory.Put(id, "bob", "anthropic_oauth", "sk-live-token", exp)).To(Succeed())

		By("never returning the token from Status")
		status, err := factory.Status(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(status).To(HaveLen(1))
		Expect(status[0].Token).To(BeEmpty())
		Expect(status[0].Kind).To(Equal("anthropic_oauth"))
		Expect(status[0].UserName).To(Equal("bob"))
		Expect(status[0].ExpiresAt).To(Equal(exp.Unix()))

		By("decrypting via Resolve")
		cred, found, err := factory.Resolve(id, "anthropic_oauth")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cred.Token).To(Equal("sk-live-token"))

		By("storing ciphertext consistent with the connection strategy")
		var enc string
		var nonce sql.NullString
		Expect(dbConn.QueryRow(
			`SELECT encrypted_token, nonce FROM agent_user_credentials WHERE user_id = $1 AND kind = 'anthropic_oauth'`, id,
		).Scan(&enc, &nonce)).To(Succeed())
		var noncePtr *string
		if nonce.Valid {
			noncePtr = &nonce.String
		}
		plain, err := dbConn.EncryptionStrategy().Decrypt(enc, noncePtr)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(plain)).To(Equal("sk-live-token"))
	})

	It("upserts on (user_id, kind)", func() {
		id := createUser("cred-sub-c", "carol")
		Expect(factory.Put(id, "carol", "anthropic_oauth", "tok-1", time.Time{})).To(Succeed())
		Expect(factory.Put(id, "carol", "anthropic_oauth", "tok-2", time.Time{})).To(Succeed())

		cred, found, err := factory.Resolve(id, "anthropic_oauth")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cred.Token).To(Equal("tok-2"))
		Expect(cred.ExpiresAt).To(BeZero())

		status, _ := factory.Status(id)
		Expect(status).To(HaveLen(1))
	})

	It("lists credentials expiring within a horizon", func() {
		idSoon := createUser("cred-sub-d", "dana")
		idLater := createUser("cred-sub-e", "erin")
		Expect(factory.Put(idSoon, "dana", "anthropic_oauth", "t", time.Now().Add(24*time.Hour))).To(Succeed())
		Expect(factory.Put(idLater, "erin", "anthropic_oauth", "t", time.Now().Add(90*24*time.Hour))).To(Succeed())

		expiring, err := factory.ExpiringWithin(30 * 24 * time.Hour)
		Expect(err).ToNot(HaveOccurred())
		names := []string{}
		for _, c := range expiring {
			names = append(names, c.UserName)
		}
		Expect(names).To(ContainElement("dana"))
		Expect(names).ToNot(ContainElement("erin"))
	})

	It("deletes by kind and records the jira seam", func() {
		id := createUser("cred-sub-f", "finn")
		Expect(factory.Put(id, "finn", "anthropic_api_key", "key", time.Time{})).To(Succeed())
		Expect(factory.SetJiraAccountID(id, "acct-9")).To(Succeed())

		status, _ := factory.Status(id)
		Expect(status[0].JiraAccountID).To(Equal("acct-9"))

		Expect(factory.Delete(id, "anthropic_api_key")).To(Succeed())
		_, found, err := factory.Resolve(id, "anthropic_api_key")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
