package idtoken_test

import (
	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ManagerFactory", func() {
	var factory *idtoken.ManagerFactory
	var signingKeyFactory db.SigningKeyFactory
	var config map[string]interface{}

	BeforeEach(func() {
		factory = idtoken.NewManagerFactory().(*idtoken.ManagerFactory)
		signingKeyFactory = db.NewSigningKeyFactory(dbConn)
		factory.SetSigningKeyFactory(signingKeyFactory)

		config = map[string]interface{}{
			"audience": []interface{}{"sts.amazonaws.com"},
		}
	})

	Context("when issuer is set", func() {
		BeforeEach(func() {
			factory.SetIssuer("https://concourse.example.com")
		})

		It("uses issuer for token generation", func() {
			manager, err := factory.NewInstance(config)
			Expect(err).ToNot(HaveOccurred())
			Expect(manager).ToNot(BeNil())

			gen := manager.(*idtoken.Manager).GetTokenGenerator()
			Expect(gen.Issuer).To(Equal("https://concourse.example.com"))
		})
	})

	Context("when issuer is not set", func() {
		It("returns an error", func() {
			_, err := factory.NewInstance(config)
			Expect(err).To(MatchError(ContainSubstring("issuer not set")))
		})
	})
})
