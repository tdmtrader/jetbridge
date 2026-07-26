package integration_test

import (
	"net/http"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent actions", func() {
	Describe("status (no subcommand)", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/actions"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode": "active", "source": "default",
						"updated_at": nil, "updated_by": nil,
					}),
				),
			)
		})

		It("prints the current mode and source", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "actions")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`actions: active`))
			Expect(sess.Out).To(gbytes.Say(`source:\s+default`))
		})
	})

	Describe("suppress", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/actions"),
					ghttp.VerifyJSONRepresenting(map[string]any{"mode": "suppressed"}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode": "suppressed", "source": "setting",
						"updated_at": "2026-07-25T12:00:00Z", "updated_by": "tdm",
					}),
				),
			)
		})

		It("PUTs mode=suppressed and prints the new state", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "actions", "suppress")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`actions: suppressed`))
		})
	})

	Describe("resume", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/actions"),
					ghttp.VerifyJSONRepresenting(map[string]any{"mode": "active"}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode": "active", "source": "setting",
						"updated_at": "2026-07-25T12:00:00Z", "updated_by": "tdm",
					}),
				),
			)
		})

		It("PUTs mode=active", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "actions", "resume")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`actions: active`))
		})
	})

	Describe("server error", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/actions"),
					ghttp.RespondWith(http.StatusForbidden, "admin only"),
				),
			)
		})

		It("exits non-zero with the server message", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "actions", "suppress")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`admin only`))
		})
	})
})
