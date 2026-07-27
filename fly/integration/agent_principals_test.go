package integration_test

import (
	"io"
	"net/http"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

func principalTimePtr(v int64) *int64 { return &v }

// NOTE: the list fixtures below deliberately carry retired scopes
// ("reviews:write", "metrics:write"). Those are no longer in
// principals.ValidScopes and can no longer be minted, but rows granted before
// the publishing routes were removed still exist in deployed databases, so the
// list renderer must keep printing them verbatim. Do not "fix" these to the
// current vocabulary — the mint path is covered separately and validates
// against principals.ValidScopes.
var _ = Describe("fly agent principals", func() {
	Describe("list", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/principals"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []principals.Principal{
						{
							ID:       1,
							Name:     "reviewer",
							Scopes:   []string{"reviews:write", "tickets:read"},
							TeamName: "main",
							// 2024-01-01, renders as a fixed date (>30d old)
							CreatedAt: 1704067200,
						},
						{
							ID:        2,
							Name:      "old-bot",
							Scopes:    []string{"metrics:write"},
							TeamName:  "other-team",
							CreatedAt: 1700000000,
							// revoked 2023-11-14/15 depending on tz
							RevokedAt: principalTimePtr(1700000000),
						},
					}),
				),
			)
		})

		It("renders id, name, scopes, team and a revoked marker", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "list")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`reviewer\s+reviews:write,tickets:read\s+main`))
			Expect(sess.Out).To(gbytes.Say(`old-bot\s+metrics:write\s+other-team`))
			// the revoked principal shows a revocation date, not "-"
			Expect(sess.Out).To(gbytes.Say(`2023-11-1`))
		})
	})

	Describe("list with ephemeral run principals mixed in", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/principals"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []principals.Principal{
						{
							ID:        7,
							Name:      "agent-run-482",
							Scopes:    []string{"tickets:read", "tickets:write"},
							TeamName:  "main",
							CreatedAt: 1704067200,
						},
						{
							ID:        1,
							Name:      "reviewer",
							Scopes:    []string{"reviews:write", "tickets:read"},
							TeamName:  "main",
							CreatedAt: 1704067200,
						},
						{
							ID:        8,
							Name:      "agent-run-501",
							Scopes:    []string{"tickets:read", "tickets:write"},
							TeamName:  "main",
							CreatedAt: 1704067200,
						},
					}),
				),
			)
		})

		It("prints operator principals first and collapses run principals to a summary line", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "list")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).NotTo(gbytes.Say(`agent-run-482`))
			Expect(sess.Out).NotTo(gbytes.Say(`agent-run-501`))
			Expect(sess.Out).To(gbytes.Say(`reviewer\s+reviews:write,tickets:read\s+main`))
			Expect(sess.Out).To(gbytes.Say(`2 ephemeral run principals \(--all to list\)`))
		})

		It("expands every run principal individually with --all", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "list", "--all")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).To(gbytes.Say(`reviewer\s+reviews:write,tickets:read\s+main`))
			Expect(sess.Out).To(gbytes.Say(`agent-run-482`))
			Expect(sess.Out).To(gbytes.Say(`agent-run-501`))
			Expect(sess.Out).NotTo(gbytes.Say(`ephemeral run principals`))
		})
	})

	Describe("mint", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/principals"),
					func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						Expect(string(body)).To(ContainSubstring(`"name":"ci-bot"`))
						Expect(string(body)).To(ContainSubstring(`"scopes":["tickets:write"]`))
						Expect(string(body)).To(ContainSubstring(`"team_name":"main"`))
					},
					ghttp.RespondWithJSONEncoded(http.StatusCreated, principals.CreatedResponse{
						Principal: principals.Principal{
							ID:        5,
							Name:      "ci-bot",
							Scopes:    []string{"tickets:write"},
							TeamName:  "main",
							CreatedAt: 1704067200,
						},
						Token: "sk-agent-secret-123",
					}),
				),
			)
		})

		It("prints the principal and the one-time token prominently", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "mint",
				"--name", "ci-bot", "--scope", "tickets:write")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`minted principal 5 \(ci-bot\)`))
			Expect(sess.Out).To(gbytes.Say(`STORE THIS TOKEN NOW`))
			Expect(sess.Out).To(gbytes.Say(`it will not be shown again`))
			Expect(sess.Out).To(gbytes.Say(`sk-agent-secret-123`))
		})
	})

	Describe("mint with a non-positive expiry", func() {
		It("rejects an explicit zero expiry client-side, before any API call", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "mint",
				"--name", "zero-bot", "--scope", "tickets:write", "--expires-in", "0s")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`--expires-in must be a positive duration`))
		})

		It("rejects a negative expiry client-side, before any API call", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "mint",
				"--name", "negative-bot", "--scope", "tickets:write", "--expires-in", "-1h")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`--expires-in must be a positive duration`))
		})
	})

	Describe("mint with an invalid scope", func() {
		It("rejects the scope client-side, before any API call", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "mint",
				"--name", "bad-bot", "--scope", "bogus:scope")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`unknown scope\(s\): bogus:scope`))
			Expect(sess.Err).To(gbytes.Say(`valid scopes are:`))
		})
	})

	Describe("revoke", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/principals/2"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("deletes the principal and confirms", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "revoke", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`revoked principal 2`))
		})
	})

	Describe("revoke a principal that does not exist", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("DELETE", "/api/v1/agent/principals/99"),
					ghttp.RespondWith(http.StatusNotFound, "principal not found"),
				),
			)
		})

		It("reports a friendly no-such-principal error", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "principals", "revoke", "--id", "99")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`no such principal: 99`))
		})
	})
})
