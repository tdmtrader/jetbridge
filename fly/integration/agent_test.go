package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent", func() {
	Describe("agent auth --token", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("stores the token and prints the expiry", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--token", "sk-tok")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("stored your anthropic_oauth credential; expires"))
		})
	})

	Describe("agent auth with the token piped to stdin", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						Expect(string(body)).To(ContainSubstring(`"token":"sk-piped"`))
					},
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("accepts a piped token without a trailing newline", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth")
			flyCmd.Stdin = strings.NewReader("sk-piped") // no trailing newline: ReadString returns io.EOF WITH the text
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("stored your anthropic_oauth credential; expires"))
		})
	})

	Describe("agent auth --platform --token", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/user-credentials"),
					func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						Expect(string(body)).To(ContainSubstring(`"user":"platform"`))
					},
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{"status": "saved"}),
				),
			)
		})

		It("targets the platform service user", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--platform", "--token", "sk-plat")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("stored the platform anthropic_oauth credential; expires"))
		})
	})

	Describe("agent auth --delete", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("deletes the stored credential", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "auth", "--delete")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("deleted your anthropic_oauth credential"))
		})
	})

	Describe("agent costs", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/costs", "group_by=day"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, costs.RollupResponse{
						GroupBy: "day",
						Summary: costs.DailySummary{CapUSD: 50, SpentUSD: 2.5, RemainingUSD: 47.5},
						Rows: []budget.RollupRow{
							{Key: "2026-07-08", Entries: 3, Turns: 12, CostUSD: 2.5},
						},
					}),
				),
			)
		})

		It("renders the rollup table and daily summary", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "costs")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("2026-07-08"))
			Expect(sess.Out).To(gbytes.Say(`daily cap \$50\.00`))
		})
	})

	Describe("fly status expiry nag", func() {
		BeforeEach(func() {
			// fly status does not call target.Validate(), so the suite-level
			// queued infoHandler would shadow the first request — clear it
			// (same idiom as status_test.go).
			atcServer.Reset()
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/user"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{"user_name": "test"}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/user-credentials"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []credentials.Credential{
						{UserID: 7, Kind: "anthropic_oauth", ExpiresAt: time.Now().Add(10 * 24 * time.Hour).Unix()},
					}),
				),
			)
		})

		It("warns when a credential expires within 30 days", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "status")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say(fmt.Sprintf("WARNING: your agent anthropic_oauth credential expires in 9 days")))
			Expect(sess.Out).To(gbytes.Say("logged in successfully"))
		})
	})
})
