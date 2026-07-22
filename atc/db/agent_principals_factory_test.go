package db_test

import (
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPrincipalsFactory", func() {
	var factory db.AgentPrincipalsFactory

	BeforeEach(func() {
		factory = db.NewAgentPrincipalsFactory(dbConn)
	})

	It("mints a principal whose token round-trips through Get", func() {
		created, token, err := factory.Create(principals.CreateSpec{
			Name:        "ci-agent-review",
			Description: "theborg publisher",
			Scopes:      []string{principals.ScopeReviewsWrite},
			CreatedBy:   "admin",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(token).To(HavePrefix("cap1."))

		id, ok := principals.ParseTokenID(token)
		Expect(ok).To(BeTrue())
		Expect(id).To(Equal(created.ID))

		got, found, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Name).To(Equal("ci-agent-review"))
		Expect(got.Description).To(Equal("theborg publisher"))
		Expect(got.Scopes).To(Equal([]string{principals.ScopeReviewsWrite}))
		Expect(got.TeamName).To(Equal("main"))
		Expect(got.CreatedBy).To(Equal("admin"))
		Expect(got.TokenPrefix).To(Equal(token[:12]))
		Expect(got.TokenHash).To(Equal(principals.HashToken(token)))
		Expect(got.CreatedAt).To(BeNumerically(">", 0))
		Expect(got.RevokedAt).To(BeNil())
	})

	It("stores expiry when given", func() {
		expires := time.Now().Add(time.Hour).Unix()
		created, _, err := factory.Create(principals.CreateSpec{
			Name:      "run-42-platform",
			Scopes:    []string{principals.ScopeTicketsWrite},
			ExpiresAt: &expires,
		})
		Expect(err).NotTo(HaveOccurred())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ExpiresAt).NotTo(BeNil())
		Expect(*got.ExpiresAt).To(Equal(expires))
	})

	It("lists principals including the backfilled legacy-publish row", func() {
		_, _, err := factory.Create(principals.CreateSpec{
			Name: "gateway", Scopes: []string{principals.ScopeCostsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		list, err := factory.List()
		Expect(err).NotTo(HaveOccurred())

		names := []string{}
		for _, p := range list {
			names = append(names, p.Name)
		}
		Expect(names).To(ContainElement(principals.LegacyPublishPrincipalName))
		Expect(names).To(ContainElement("gateway"))
	})

	It("revokes idempotently and reports missing ids", func() {
		created, _, err := factory.Create(principals.CreateSpec{
			Name: "harvest", Scopes: []string{principals.ScopeTicketsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		found, err := factory.Revoke(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.RevokedAt).NotTo(BeNil())
		firstRevokedAt := *got.RevokedAt

		// second revoke keeps the original timestamp
		found, err = factory.Revoke(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		got, _, _ = factory.Get(created.ID)
		Expect(*got.RevokedAt).To(Equal(firstRevokedAt))

		found, err = factory.Revoke(999999)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("revokes every exact per-run principal name durably", func() {
		first, _, err := factory.Create(principals.CreateSpec{
			Name: "agent-run-42", Scopes: []string{principals.ScopeTicketsWrite},
		})
		Expect(err).NotTo(HaveOccurred())
		second, _, err := factory.Create(principals.CreateSpec{
			Name: "agent-run-42", Scopes: []string{principals.ScopeMetricsWrite},
		})
		Expect(err).NotTo(HaveOccurred())
		other, _, err := factory.Create(principals.CreateSpec{
			Name: "agent-run-420", Scopes: []string{principals.ScopeMetricsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(factory.RevokeByName("agent-run-42")).To(Succeed())
		Expect(factory.RevokeByName("agent-run-42")).To(Succeed())

		for _, id := range []int{first.ID, second.ID} {
			stored, found, err := factory.Get(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(stored.RevokedAt).NotTo(BeNil())
		}
		stored, found, err := factory.Get(other.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.RevokedAt).To(BeNil())
	})

	It("rejects an empty revoke-by-name target", func() {
		Expect(factory.RevokeByName(" ")).To(MatchError("db: agent principal name is required"))
	})

	It("records usage", func() {
		created, _, err := factory.Create(principals.CreateSpec{
			Name: "agent-step", Scopes: []string{principals.ScopeMetricsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(factory.RecordUse(created.ID, time.Now())).To(Succeed())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.LastUsedAt).NotTo(BeNil())
	})
})
