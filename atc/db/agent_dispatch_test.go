package db_test

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type liveOnlyWorkflows struct{ def *workflow.Definition }

func (f liveOnlyWorkflows) Live(string) (*workflow.Definition, bool, error) { return f.def, true, nil }
func (f liveOnlyWorkflows) Get(_ string, v int) (*workflow.Definition, bool, error) {
	if v != f.def.Version {
		return nil, false, nil
	}
	return f.def, true, nil
}

// DispatchOne over the REAL stores: ticket row, rendered template
// persisted via SavePipeline on a real team, materialized pipeline run
// with an entry build, and the queued→running transition — the
// manual-dispatch slice's DB-backed proof (plan 11 Task 12 shape).
var _ = Describe("dispatching a ticket end-to-end", func() {
	It("renders, persists, creates the run, and moves the ticket to running", func() {
		ticketsFactory := db.NewAgentTicketsFactory(dbConn)
		runFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

		deps := dispatch.Deps{
			Tickets:   ticketsFactory,
			Workflows: liveOnlyWorkflows{def: &workflow.Definition{
				Name: "smoke", Version: 2, ContentHash: "hash2", Live: true,
				Config: workflow.Config{
					Name:         "smoke",
					SpecDelivery: "files",
					Defaults:     workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 5},
					Prompts:      map[string]string{"do": "Read ticket/spec.md and do it."},
					Steps: []workflow.Step{
						{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
					},
				},
			}},
			Templates:      dispatch.NewTeamTemplateSaver(teamFactory, defaultTeam.Name()),
			Runs:           runFactory,
			ATCExternalURL: "http://concourse.home",
			RepoBaseURL:    "https://github.com",
		}

		ticketID, err := ticketsFactory.Create(&tickets.Ticket{
			Title: "dispatch me", Body: "prove the loop", Origin: "fly",
			Repo: "tdmtrader/jetbridge", UserName: "tdm", CreatedBy: "tdm",
			WorkflowName: "smoke",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(ticketsFactory.Transition(ticketID, tickets.StateDraft, tickets.StateQueued,
			tickets.TransitionMeta{})).To(Succeed())

		res, err := dispatch.DispatchOne(context.Background(), deps, ticketID, "admin")
		Expect(err).ToNot(HaveOccurred())
		Expect(res.RunID).To(BeNumerically(">", 0))
		Expect(res.PipelineName).To(Equal(fmt.Sprintf("agent-ticket-%d", ticketID)))

		// the template pipeline exists on the team, unpaused
		template, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: res.PipelineName})
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(template.Paused()).To(BeFalse())

		// the ticket is running, attributed to the run — and the run
		// passes the exec's linkage gate for AGENT_TICKET_ID claims
		got, _, err := ticketsFactory.Get(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(tickets.StateRunning))
		Expect(*got.PipelineRunID).To(Equal(res.RunID))
		Expect(*got.WorkflowVersion).To(Equal(2)) // live version frozen at dispatch

		linked, err := runFactory.TicketBelongsToRun(ticketID, res.RunID)
		Expect(err).ToNot(HaveOccurred())
		Expect(linked).To(BeTrue())

		// dispatching again while running is refused; a second dispatch
		// after a manual requeue re-renders in place (SavePipeline update)
		_, err = dispatch.DispatchOne(context.Background(), deps, ticketID, "admin")
		Expect(err).To(MatchError(dispatch.ErrNotQueued))

		Expect(ticketsFactory.Transition(ticketID, tickets.StateRunning, tickets.StateQueued,
			tickets.TransitionMeta{})).To(Succeed())
		res2, err := dispatch.DispatchOne(context.Background(), deps, ticketID, "admin")
		Expect(err).ToNot(HaveOccurred())
		Expect(res2.RunID).ToNot(Equal(res.RunID))

		got, _, _ = ticketsFactory.Get(ticketID)
		Expect(got.AttemptCount).To(Equal(1)) // the requeue counted
		Expect(*got.PipelineRunID).To(Equal(res2.RunID))
	})
})
