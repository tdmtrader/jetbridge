package integration_test

import (
	"os/exec"

	"github.com/concourse/concourse/agent/api/tickets"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent tickets", func() {
	Describe("list", func() {
		It("renders a table of tickets", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets", "state=queued&limit=50"),
					ghttp.RespondWithJSONEncoded(200, []tickets.Ticket{
						{ID: 7, Title: "fix X", State: tickets.StateQueued,
							Repo: "tdmtrader/concourse", UserName: "tdm"},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "list", "--state", "queued")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("7"))
			Expect(sess.Out).To(gbytes.Say("queued"))
			Expect(sess.Out).To(gbytes.Say("tdmtrader/concourse"))
			Expect(sess.Out).To(gbytes.Say("fix X"))
		})
	})

	Describe("create", func() {
		It("posts origin fly and prints the new ticket id", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.VerifyJSON(`{"title":"fix X","body":"details","origin":"fly","repo":"tdmtrader/concourse","target_branch":"main","budget_usd":5}`),
					ghttp.RespondWithJSONEncoded(201, tickets.Ticket{ID: 8, State: tickets.StateDraft}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "create",
				"--title", "fix X", "--body", "details",
				"--repo", "tdmtrader/concourse", "--budget", "5")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("created ticket #8"))
		})
	})

	Describe("show", func() {
		It("prints ticket, spec, and plan", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, Title: "fix X", State: tickets.StateRunning,
							Origin: "fly", Repo: "tdmtrader/concourse", TargetBranch: "main", Body: "details"},
						Spec: &tickets.Spec{Version: 2, Title: "the spec"},
						Tasks: []tickets.Task{
							{Ordering: 1, Title: "one", Status: tickets.TaskDone},
							{Ordering: 2, Title: "two", Status: tickets.TaskPending},
						},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`ticket #7: fix X`))
			Expect(sess.Out).To(gbytes.Say(`state: running`))
			Expect(sess.Out).To(gbytes.Say(`spec v2: the spec`))
			Expect(sess.Out).To(gbytes.Say(`1. \[done\] one`))
			Expect(sess.Out).To(gbytes.Say(`2. \[pending\] two`))
		})

		It("exits 1 when the ticket does not exist", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
					ghttp.RespondWith(404, ""),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "99")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 99 not found"))
		})
	})

	Describe("queue", func() {
		It("transitions draft to queued", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateQueued}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "queue", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now queued"))
		})
	})

	Describe("transition", func() {
		It("puts the requested edge with optional metadata", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"needs_review","to":"concluded"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateConcluded}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "transition",
				"--id", "7", "--from", "needs_review", "--to", "concluded")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now concluded"))
		})
	})

	Describe("dispatch", func() {
		It("posts the dispatch and prints the run", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "agent-ticket-7",
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #7 as run 321 \(pipeline agent-ticket-7\)`))
		})
	})
})
