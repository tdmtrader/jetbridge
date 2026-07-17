package db_test

import (
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The AGENT_TICKET_ID attribution chain, end-to-end over the REAL tables.
//
// The exec side is pinned in atc/exec/agent_step_test.go with fakes: a
// plan-env AGENT_TICKET_ID claim reaches agent_run_metrics tagging ONLY
// after runVerifier.RunBelongsToPipeline + TicketBelongsToRun both pass
// (attacker-writable YAML is never trusted raw). Until ticket-core landed,
// TicketBelongsToRun failed closed unconditionally — no agent_tickets
// table, no legitimate claim, every metrics row had ticket_id NULL ("CI").
//
// This spec is the definition-of-done for the "tickets exist" slice: a
// factory-created ticket, dispatched as a real pipeline run via the
// single-writer Transition, satisfies the exact linkage query the exec
// consults, and the metrics row written with that verified id lists under
// the ticket (GET /api/v1/agent/tickets/:ticket_id/metrics, fly agent runs).
var _ = Describe("agent ticket attribution (AGENT_TICKET_ID end-to-end)", func() {
	var (
		runFactory     db.PipelineRunFactory
		ticketsFactory db.AgentTicketsFactory
		metricsFactory db.AgentRunMetricsFactory
		template       db.Pipeline
	)

	templateConfig := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{
			{
				Name: "entry",
				PlanSequence: []atc.Step{
					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
				},
			},
		},
	}

	BeforeEach(func() {
		runFactory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
		ticketsFactory = db.NewAgentTicketsFactory(dbConn)
		metricsFactory = db.NewAgentRunMetricsFactory(dbConn)

		var err error
		template, _, err = defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "ticket-run-template"}, templateConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
	})

	It("attributes a metrics row to a ticket dispatched as a real pipeline run", func() {
		// The ticket exists (this slice's whole point)...
		ticketID, err := ticketsFactory.Create(&tickets.Ticket{
			Title: "prove attribution", Body: "run a step against me",
			Origin: "fly", Repo: "tdmtrader/concourse", UserName: "tdm", CreatedBy: "tdm",
		})
		Expect(err).ToNot(HaveOccurred())

		// ...and is dispatched as a pipeline run through the single writer.
		run, err := runFactory.CreateRun(template.ID(), nil, "tdm")
		Expect(err).ToNot(HaveOccurred())
		runID := run.ID()

		Expect(ticketsFactory.Transition(ticketID, tickets.StateDraft, tickets.StateQueued,
			tickets.TransitionMeta{})).To(Succeed())
		Expect(ticketsFactory.Transition(ticketID, tickets.StateQueued, tickets.StateRunning,
			tickets.TransitionMeta{PipelineRunID: &runID})).To(Succeed())

		// The exec's admission gate (agent_step.go run()): the claimed run
		// must belong to the build's pipeline, and the claimed ticket must
		// have been dispatched as that verified run.
		instanceID, ok := run.InstancePipelineID()
		Expect(ok).To(BeTrue())
		owned, err := runFactory.RunBelongsToPipeline(runID, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(owned).To(BeTrue())

		linked, err := runFactory.TicketBelongsToRun(ticketID, runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(linked).To(BeTrue(), "the exec's TicketBelongsToRun gate must admit the dispatched ticket")

		// A ticket NOT dispatched as this run still fails closed — someone
		// else's AGENT_TICKET_ID claim never admits or misattributes.
		otherID, err := ticketsFactory.Create(&tickets.Ticket{
			Title: "innocent bystander", Repo: "tdmtrader/concourse",
		})
		Expect(err).ToNot(HaveOccurred())
		linked, err = runFactory.TicketBelongsToRun(otherID, runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(linked).To(BeFalse())

		// The step's flight-recorder ingest writes the metrics row with the
		// SERVER-VERIFIED ids (agent_step.go ingestFlightRecorder)...
		Expect(metricsFactory.Upsert(&schema.RunMetrics{
			TicketID: &ticketID, PipelineRunID: &runID,
			BuildID: 424242, PlanID: "aa11", StepName: "implement",
			Status: "ok", Summary: "did the thing", Model: "claude-fable-5",
			Turns: 12, CostUSD: 1.25,
		})).To(Succeed())

		// ...and the row lists under the ticket — the payload of
		// GET /api/v1/agent/tickets/:ticket_id/metrics, where a ticket
		// number (not "CI") finally shows up.
		rows, err := metricsFactory.ListByTicket(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(*rows[0].TicketID).To(Equal(ticketID))
		Expect(*rows[0].PipelineRunID).To(Equal(runID))
		Expect(rows[0].StepName).To(Equal("implement"))
		Expect(rows[0].CostUSD).To(Equal(1.25))

		// The bystander ticket accrues nothing.
		rows, err = metricsFactory.ListByTicket(otherID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})
})
