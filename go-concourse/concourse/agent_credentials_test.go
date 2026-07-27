package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Agent user credentials", func() {
	Describe("SetAgentUserCredential", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					ghttp.VerifyJSON(`{"kind":"anthropic_oauth","token":"sk-tok","expires_at":1783891200}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("PUTs the credential body", func() {
			err := client.SetAgentUserCredential(credentials.PutRequest{
				Kind: "anthropic_oauth", Token: "sk-tok", ExpiresAt: 1783891200,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("AgentUserCredentialStatus", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []credentials.Credential{
						{UserID: 7, UserName: "alice", Kind: "anthropic_oauth", ExpiresAt: 1783891200},
					}),
				),
			)
		})

		It("returns the caller's credentials", func() {
			creds, err := client.AgentUserCredentialStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(creds).To(HaveLen(1))
			Expect(creds[0].Kind).To(Equal("anthropic_oauth"))
			Expect(creds[0].ExpiresAt).To(Equal(int64(1783891200)))
		})
	})

	Describe("DeleteAgentUserCredential", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("deletes by kind", func() {
			Expect(client.DeleteAgentUserCredential("anthropic_oauth", false)).To(Succeed())
		})
	})

	Describe("DeleteAgentUserCredential for the platform user", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth", "user=platform"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("adds the user=platform query param", func() {
			Expect(client.DeleteAgentUserCredential("anthropic_oauth", true)).To(Succeed())
		})
	})
})
