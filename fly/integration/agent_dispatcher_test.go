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

var _ = Describe("fly agent dispatcher", func() {
	Describe("status (no subcommand)", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/dispatcher"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode": "off", "updated_at": "2026-07-26T09:00:00Z", "updated_by": "migration",
					}),
				),
			)
		})

		It("prints the current mode and who set it", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "dispatcher")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatcher mode: off`))
			Expect(sess.Out).To(gbytes.Say(`last updated:\s+2026-07-26T09:00:00Z by migration`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("boot default"))
		})
	})

	Describe("pause", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/dispatcher"),
					ghttp.VerifyJSONRepresenting(map[string]any{"mode": "paused"}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode":       "paused",
						"updated_at": "2026-07-19T12:00:00Z", "updated_by": "tdm",
					}),
				),
			)
		})

		It("PUTs mode=paused and prints the new state", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "dispatcher", "pause")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatcher mode: paused`))
		})
	})

	Describe("resume", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/dispatcher"),
					ghttp.VerifyJSONRepresenting(map[string]any{"mode": "active"}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode":       "active",
						"updated_at": "2026-07-19T12:00:00Z", "updated_by": "tdm",
					}),
				),
			)
		})

		It("PUTs mode=active", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "dispatcher", "resume")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatcher mode: active`))
		})
	})

	Describe("off", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/dispatcher"),
					ghttp.VerifyJSONRepresenting(map[string]any{"mode": "off"}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"mode":       "off",
						"updated_at": "2026-07-19T12:00:00Z", "updated_by": "tdm",
					}),
				),
			)
		})

		It("PUTs mode=off", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "dispatcher", "off")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatcher mode: off`))
		})
	})

	Describe("server error", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/dispatcher"),
					ghttp.RespondWith(http.StatusForbidden, "admin only"),
				),
			)
		})

		It("exits non-zero with the server message", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "dispatcher", "pause")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`admin only`))
		})
	})
})
