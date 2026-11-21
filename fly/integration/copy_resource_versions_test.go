package integration_test

import (
	"net/http"
	"os/exec"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("copy-resource-versions", func() {
	var (
		args []string
		sess *gexec.Session
	)

	BeforeEach(func() {
		args = []string{}
	})

	JustBeforeEach(func() {
		var err error

		flyCmd := exec.Command(flyPath, append([]string{"-t", targetName, "copy-resource-versions"}, args...)...)
		sess, err = gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())
	})

	Context("when --resource is not specified", func() {
		It("returns an error", func() {
			Eventually(sess).Should(gexec.Exit(1))
			Expect(sess.Err).To(gbytes.Say("required"))
		})
	})

	Context("when listing previous version histories (no --from-scope)", func() {
		var expectedURL = "/api/v1/teams/main/pipelines/some-pipeline/resources/some-resource/deprecated-scopes"

		BeforeEach(func() {
			args = []string{"-r", "some-pipeline/some-resource"}
		})

		Context("when there are previous histories", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL),
						ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.DeprecatedScope{
							{
								ID:           42,
								DeprecatedAt: "2026-04-10T12:00:00Z",
								ConfigID:     17,
							},
							{
								ID:           38,
								DeprecatedAt: "2026-04-08T09:30:00Z",
								ConfigID:     15,
							},
							{
								ID:          51,
								ConfigID:    19,
								Versions:    150,
								LastChecked: "2026-11-01T10:00:00Z",
							},
						}),
					),
				)
			})

			It("lists the available histories, deprecated and kept, with their version counts", func() {
				Eventually(sess).Should(gexec.Exit(0))
				Expect(sess.Out).To(gbytes.Say("previous version histories available to copy"))
				Expect(sess.Out).To(gbytes.Say(`scope 42  \(deprecated at: 2026-04-10T12:00:00Z, 0 versions, config id: 17\)`))
				Expect(sess.Out).To(gbytes.Say("scope 38"))
				Expect(sess.Out).To(gbytes.Say(`scope 51  \(kept, 150 versions, last checked: 2026-11-01T10:00:00Z, config id: 19\)`))
				Expect(sess.Out).To(gbytes.Say("re-run with --from-scope"))
			})
		})

		Context("when there are no previous histories", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL),
						ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.DeprecatedScope{}),
					),
				)
			})

			It("reports no histories found", func() {
				Eventually(sess).Should(gexec.Exit(0))
				Expect(sess.Out).To(gbytes.Say("no previous version histories found"))
			})
		})
	})

	Context("when copying versions with --from-scope", func() {
		var expectedURL = "/api/v1/teams/main/pipelines/some-pipeline/resources/some-resource/copy-versions"

		BeforeEach(func() {
			args = []string{"-r", "some-pipeline/some-resource", "--from-scope", "42"}
		})

		Context("when the copy succeeds", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("PUT", expectedURL),
						ghttp.VerifyJSON(`{"from_scope_id":42}`),
						ghttp.RespondWithJSONEncoded(http.StatusOK, atc.CopyVersionsResponse{
							VersionsCopied: 150,
						}),
					),
				)
			})

			It("reports the number of versions copied", func() {
				Eventually(sess).Should(gexec.Exit(0))
				Expect(sess.Out).To(gbytes.Say("copied 150 versions from scope 42"))
			})
		})

		Context("when the scope is not found", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("PUT", expectedURL),
						ghttp.RespondWith(http.StatusNotFound, `{"error":"source scope not found or does not belong to this resource"}`),
					),
				)
			})

			It("returns an error", func() {
				Eventually(sess).Should(gexec.Exit(1))
			})
		})

		Context("when the copy is refused because the current scope is shared", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("PUT", expectedURL),
						ghttp.RespondWith(http.StatusConflict, `{"error":"the resource's current version history is shared with other resources"}`),
					),
				)
			})

			It("prints the reason and fails", func() {
				Eventually(sess).Should(gexec.Exit(1))
				Expect(sess.Err).To(gbytes.Say("409"))
				Expect(sess.Err).To(gbytes.Say("shared with other resources"))
			})
		})
	})
})
