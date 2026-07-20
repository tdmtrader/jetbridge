package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/gitcheck"

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

	Describe("UpdateAgentTicket", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7"),
					ghttp.VerifyJSON(`{"workflow_name":"deploy","workflow_version":3}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, tickets.Ticket{
						ID: 7, WorkflowName: "deploy",
					}),
				),
			)
		})

		It("puts the update and decodes the updated ticket", func() {
			name := "deploy"
			ver := 3
			updated, err := client.UpdateAgentTicket(7, tickets.UpdateRequest{
				WorkflowName:    &name,
				WorkflowVersion: &ver,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.WorkflowName).To(Equal("deploy"))
		})
	})

	Describe("SetAgentTicketDisposition", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/12/disposition"),
					ghttp.VerifyJSON(`{"disposition":"sent_back","reason":"incomplete","notes":"missing tests"}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, outcomes.Outcome{
						TicketID:          12,
						MergeState:        outcomes.ClosedUnmerged,
						Disposition:       outcomes.DispositionSentBack,
						DispositionReason: "incomplete",
						DispositionNotes:  "missing tests",
						DisposedBy:        "tdm",
					}),
				),
			)
		})

		It("puts the disposition and decodes the outcome", func() {
			outcome, err := client.SetAgentTicketDisposition(12, outcomes.DispositionRequestFor(
				outcomes.DispositionSentBack, "incomplete", "missing tests",
			))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.TicketID).To(Equal(12))
			Expect(outcome.MergeState).To(Equal(outcomes.ClosedUnmerged))
			Expect(outcome.Disposition).To(Equal(outcomes.DispositionSentBack))
			Expect(outcome.DispositionReason).To(Equal("incomplete"))
			Expect(outcome.DisposedBy).To(Equal("tdm"))
		})
	})

	Describe("GetAgentTicketOutcome", func() {
		Context("when the outcome exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/12/outcome"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, outcomes.Outcome{
							TicketID:   12,
							Repo:       "tdmtrader/concourse",
							Branch:     "agent/ticket-12",
							MergeState: outcomes.Merged,
							MergedSha:  "abc123",
						}),
					),
				)
			})

			It("returns the outcome and found=true", func() {
				outcome, found, err := client.GetAgentTicketOutcome(12)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(outcome.TicketID).To(Equal(12))
				Expect(outcome.MergeState).To(Equal(outcomes.Merged))
				Expect(outcome.MergedSha).To(Equal("abc123"))
			})
		})

		Context("when there is no outcome row", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99/outcome"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns found=false without an error", func() {
				_, found, err := client.GetAgentTicketOutcome(99)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("GetAgentTicketDiff", func() {
		Context("when the diff exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/12/diff", "offset=0&limit=50"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, gitcheck.DiffPage{
							Files: []gitcheck.DiffFile{
								{Path: "atc/foo.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
							},
							Offset: 0, Limit: 50, TotalFiles: 1, HasMore: false,
						}),
					),
				)
			})

			It("returns the decoded diff page and found=true", func() {
				page, found, err := client.GetAgentTicketDiff(12, 0, 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(page.TotalFiles).To(Equal(1))
				Expect(page.Files).To(HaveLen(1))
				Expect(page.Files[0].Path).To(Equal("atc/foo.go"))
				Expect(page.Files[0].Patch).To(ContainSubstring("+new"))
			})
		})

		Context("when there is no diff for the ticket", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99/diff", "offset=0&limit=50"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns found=false without an error", func() {
				_, found, err := client.GetAgentTicketDiff(99, 0, 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("AgentRunTranscript", func() {
		Context("when a transcript exists", func() {
			const ndjson = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit"}]}}
{"type":"result","total_cost_usd":0.4}
`
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/12/runs/4242/transcript"),
						ghttp.RespondWith(http.StatusOK, ndjson, http.Header{"Content-Type": []string{"application/x-ndjson"}}),
					),
				)
			})

			It("returns the raw ndjson bytes verbatim", func() {
				raw, err := client.AgentRunTranscript(12, 4242)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(raw)).To(Equal(ndjson))
			})
		})

		Context("when there is no transcript", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99/runs/1/transcript"),
						ghttp.RespondWith(http.StatusNotFound, ""),
					),
				)
			})

			It("returns an error", func() {
				_, err := client.AgentRunTranscript(99, 1)
				Expect(err).To(HaveOccurred())
			})
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
