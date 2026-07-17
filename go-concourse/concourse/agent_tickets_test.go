package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/tickets"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Agent Tickets", func() {
	Describe("ListAgentTickets", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets", "state=queued&limit=5"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []tickets.Ticket{
						{ID: 7, Title: "fix X", State: tickets.StateQueued, Repo: "tdmtrader/concourse"},
					}),
				),
			)
		})

		It("sends the filters and decodes the list", func() {
			list, err := client.ListAgentTickets(tickets.ListFilter{State: tickets.StateQueued, Limit: 5})
			Expect(err).NotTo(HaveOccurred())
			Expect(list).To(HaveLen(1))
			Expect(list[0].ID).To(Equal(7))
			Expect(list[0].State).To(Equal(tickets.StateQueued))
		})
	})

	Describe("CreateAgentTicket", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.VerifyJSON(`{"title":"fix X","body":"details","origin":"fly","repo":"tdmtrader/concourse","target_branch":"main"}`),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, tickets.Ticket{
						ID: 8, Title: "fix X", State: tickets.StateDraft,
					}),
				),
			)
		})

		It("posts the request body and decodes the created ticket", func() {
			created, err := client.CreateAgentTicket(tickets.CreateRequest{
				Title: "fix X", Body: "details", Origin: "fly",
				Repo: "tdmtrader/concourse", TargetBranch: "main",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created.ID).To(Equal(8))
			Expect(created.State).To(Equal(tickets.StateDraft))
		})
	})

	Describe("GetAgentTicket", func() {
		Context("when the ticket exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, tickets.TicketDetail{
							Ticket: tickets.Ticket{ID: 7, Title: "fix X", State: tickets.StateRunning},
							Tasks:  []tickets.Task{{Ordering: 1, Title: "one", Status: tickets.TaskDone}},
						}),
					),
				)
			})

			It("returns the detail and found=true", func() {
				detail, found, err := client.GetAgentTicket(7)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(detail.Ticket.ID).To(Equal(7))
				Expect(detail.Tasks).To(HaveLen(1))
			})
		})

		Context("when the ticket is missing", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns found=false without an error", func() {
				_, found, err := client.GetAgentTicket(99)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("TransitionAgentTicket", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, tickets.Ticket{
						ID: 7, State: tickets.StateQueued,
					}),
				),
			)
		})

		It("puts the transition and decodes the updated ticket", func() {
			updated, err := client.TransitionAgentTicket(7, tickets.TransitionRequest{
				From: tickets.StateDraft, To: tickets.StateQueued,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.State).To(Equal(tickets.StateQueued))
		})
	})

	Describe("DispatchAgentTicket", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, tickets.DispatchResponse{
						RunID: 321, PipelineName: "agent-ticket-7",
					}),
				),
			)
		})

		It("posts the dispatch and decodes the run", func() {
			res, err := client.DispatchAgentTicket(7)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RunID).To(Equal(321))
			Expect(res.PipelineName).To(Equal("agent-ticket-7"))
		})
	})
})
