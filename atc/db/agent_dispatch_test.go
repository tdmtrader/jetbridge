package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dispatchRuntimeImage = "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type dispatchWorkItemCapturer struct {
	tickets  tickets.Store
	snapshot snapshot.Snapshot
}

func (capturer dispatchWorkItemCapturer) CaptureRevision(
	_ context.Context,
	ticketID int,
) (workitem.CaptureResult, bool, error) {
	ticket, found, err := capturer.tickets.Get(ticketID)
	if err != nil || !found {
		return workitem.CaptureResult{}, found, err
	}
	return workitem.CaptureResult{
		TicketID: ticketID,
		Revision: ticket.Revision,
		Snapshot: capturer.snapshot.Clone(),
	}, true, nil
}

type agentDispatchFixture struct {
	tickets            tickets.Store
	workflows          db.AgentWorkflowsFactory
	workflowRuns       db.AgentWorkflowRunsFactory
	pipelineRuns       db.PipelineRunFactory
	deps               dispatch.Deps
	definition         workflow.Definition
	workItemSnapshot   snapshot.Snapshot
	repositorySnapshot snapshot.Snapshot
	secondRepository   snapshot.Snapshot
}

func newAgentDispatchFixture() *agentDispatchFixture {
	renderer := workflowrun.WorkflowTargetRenderer{RuntimeImage: dispatchRuntimeImage}
	workflows := db.NewAgentWorkflowsFactory(dbConn, renderer)
	definition, err := workflows.ImportManifest("smoke", workflow.Manifest{
		"workflow.yml": `schema_version: 3
name: smoke
signature_version: 1
inputs:
  - name: work-item
    type: work-item/v1
  - name: repository
    type: repository/v1
outputs:
  - name: report
    type: opaque/v1
    from: report
plan:
  - agent: implement
    function_id: implement
    prompt: Apply the captured work item to the exact repository snapshot.
    inputs: [work-item, repository]
    outputs: [report]
    input_types:
      work-item: {type: work-item/v1}
      repository: {type: repository/v1}
    output_types:
      report: opaque/v1
`,
	}, "alice")
	Expect(err).NotTo(HaveOccurred())
	_, err = workflows.Promote(definition.Name, definition.Version, "alice")
	Expect(err).NotTo(HaveOccurred())

	ticketsFactory := db.NewAgentTicketsFactory(dbConn)
	workflowRuns := db.NewAgentWorkflowRunsFactory(dbConn)
	pipelineRuns := db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
	templateSaver, err := workflowrun.NewTemplateSaver(
		teamFactory,
		db.NewWorkflowRunTemplateFactory(dbConn, lockFactory),
	)
	Expect(err).NotTo(HaveOccurred())
	binder, err := workflowrun.NewBinder(
		workflowrun.WorkflowDefinitionStoreResolver{Store: workflows},
		renderer,
		db.NewAgentSnapshotsFactory(dbConn),
		workflowRuns,
		workflowrun.AllowAllBudgetAdmitter{},
		templateSaver,
		pipelineRuns,
		workflowRunNoopSecretPreparer{},
	)
	Expect(err).NotTo(HaveOccurred())
	canceler, err := workflowrun.NewCanceler(workflowRuns, buildFactory)
	Expect(err).NotTo(HaveOccurred())

	workItemSnapshot := insertDispatchSnapshot("work-item", 'b')
	repositorySnapshot := insertDispatchSnapshot("repository", 'c')
	secondRepository := insertDispatchSnapshot("repository", 'd')
	deps := dispatch.Deps{
		Tickets: ticketsFactory, Workflows: workflows,
		TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
		WorkItems: dispatchWorkItemCapturer{
			tickets: ticketsFactory, snapshot: workItemSnapshot,
		},
		WorkflowBinder: binder, WorkflowCanceler: canceler,
		Budget: budget.NewChecker(
			db.NewAgentCostLedgerFactory(dbConn),
			dispatch.NewTicketBudgets(ticketsFactory),
			budget.Config{},
		),
	}
	return &agentDispatchFixture{
		tickets: ticketsFactory, workflows: workflows, workflowRuns: workflowRuns,
		pipelineRuns: pipelineRuns, deps: deps, definition: *definition,
		workItemSnapshot: workItemSnapshot, repositorySnapshot: repositorySnapshot,
		secondRepository: secondRepository,
	}
}

func insertDispatchSnapshot(typeName string, digestByte byte) snapshot.Snapshot {
	digest := snapshot.Digest("sha256:" + strings.Repeat(string(digestByte), 64))
	var id int64
	err := dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(type_name, type_version, digest, byte_size, file_count, representation)
		VALUES ($1, 1, $2, 10, 1, 'application/vnd.jetbridge.snapshot.tar.v1')
		RETURNING id
	`, typeName, digest.String()).Scan(&id)
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`
		INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
		VALUES ($1, $2, 'alice', 'ticket dispatch input')
	`, id, defaultTeam.ID())
	Expect(err).NotTo(HaveOccurred())
	return snapshot.Snapshot{
		ID: snapshot.SnapshotID(id), Type: snapshot.TypeRef(typeName + "/v1"),
		Digest: digest, ByteSize: 10, FileCount: 1,
		Representation: "application/vnd.jetbridge.snapshot.tar.v1",
		ContentState:   snapshot.ContentStateAvailable,
	}
}

func (fixture *agentDispatchFixture) queueTicket(ticketBudget *float64) int {
	id, err := fixture.tickets.Create(&tickets.Ticket{
		Title: "dispatch me", Body: "prove binder dispatch", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "main",
		WorkflowName: fixture.definition.Name, BudgetUSD: ticketBudget,
		UserName: "tdm", CreatedBy: "tdm",
	})
	Expect(err).NotTo(HaveOccurred())
	repositoryID := fixture.repositorySnapshot.ID
	Expect(fixture.tickets.Update(id, tickets.Update{
		RepositorySnapshotID: &repositoryID,
	})).To(Succeed())
	selected, found, err := fixture.tickets.Get(id)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(selected.RepositorySnapshotID).ToNot(BeNil())
	Expect(*selected.RepositorySnapshotID).To(Equal(repositoryID))
	Expect(fixture.tickets.Transition(
		id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{},
	)).To(Succeed())
	return id
}

func (fixture *agentDispatchFixture) inputBindings(
	runID snapshot.WorkflowRunID,
) map[string]snapshot.SnapshotID {
	bindings, err := fixture.workflowRuns.Snapshots(context.Background(), runID)
	Expect(err).NotTo(HaveOccurred())
	result := map[string]snapshot.SnapshotID{}
	for _, binding := range bindings {
		if binding.Direction == db.AgentWorkflowRunSnapshotInput {
			result[binding.PortName] = binding.Snapshot.ID
		}
	}
	return result
}

var _ = Describe("dispatching a ticket end-to-end", func() {
	It("binds exact immutable ticket snapshots through a durable workflow run", func() {
		fixture := newAgentDispatchFixture()
		ticketID := fixture.queueTicket(nil)

		result, err := dispatch.DispatchOne(context.Background(), fixture.deps, ticketID, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RunID).To(BeNumerically(">", 0))
		Expect(result.WorkflowRunID).ToNot(BeNil())
		Expect(result.PipelineName).To(HavePrefix("agent-workflow-smoke-v1-"))

		run, found, err := fixture.workflowRuns.Get(
			context.Background(), defaultTeam.ID(), *result.WorkflowRunID,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(run.PipelineRunID).ToNot(BeNil())
		Expect(*run.PipelineRunID).To(Equal(result.RunID))
		Expect(run.WorkflowDefinitionID).To(Equal(fixture.definition.ID))
		Expect(run.WorkflowVersion).To(Equal(fixture.definition.Version))
		Expect(run.Status).To(Equal(db.AgentWorkflowRunStatusRunning))
		expectedTemplateName, err := workflow.TemplateName(
			workflow.TargetWorkflow,
			fixture.definition.Name,
			fixture.definition.Version,
			"",
			run.ParameterizedConfigHash,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.PipelineName).To(Equal(expectedTemplateName))

		got, found, err := fixture.tickets.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateRunning))
		Expect(got.WorkflowRunID).To(Equal(result.WorkflowRunID))
		Expect(got.PipelineRunID).ToNot(BeNil())
		Expect(*got.PipelineRunID).To(Equal(result.RunID))
		Expect(got.WorkflowDefinitionID).ToNot(BeNil())
		Expect(*got.WorkflowDefinitionID).To(Equal(fixture.definition.ID))
		Expect(got.WorkflowVersion).ToNot(BeNil())
		Expect(*got.WorkflowVersion).To(Equal(fixture.definition.Version))

		bindings := fixture.inputBindings(*result.WorkflowRunID)
		Expect(bindings).To(HaveLen(2))
		Expect(bindings["repository"]).To(Equal(fixture.repositorySnapshot.ID))
		Expect(bindings["work-item"]).To(Equal(fixture.workItemSnapshot.ID))

		template, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: result.PipelineName})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(template.Paused()).To(BeFalse())
		Expect(run.TemplatePipelineID).ToNot(BeNil())
		Expect(*run.TemplatePipelineID).To(Equal(template.ID()))
		_, legacyFound, err := defaultTeam.Pipeline(atc.PipelineRef{
			Name: fmt.Sprintf("agent-ticket-%d", ticketID),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(legacyFound).To(BeFalse())

		replay, err := dispatch.DispatchOne(context.Background(), fixture.deps, ticketID, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.RunID).To(Equal(result.RunID))
		Expect(replay.WorkflowRunID).To(Equal(result.WorkflowRunID))

		editedTitle := "edited after immutable binding"
		Expect(fixture.tickets.Update(ticketID, tickets.Update{Title: &editedTitle})).To(Succeed())
		Expect(fixture.inputBindings(*result.WorkflowRunID)["repository"]).To(
			Equal(fixture.repositorySnapshot.ID),
		)
		replacementID := fixture.secondRepository.ID
		err = fixture.tickets.Update(ticketID, tickets.Update{
			RepositorySnapshotID: &replacementID,
		})
		Expect(errors.Is(err, tickets.ErrDispatchConflict)).To(BeTrue())
		got, found, err = fixture.tickets.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Title).To(Equal(editedTitle))
		Expect(got.RepositorySnapshotID).ToNot(BeNil())
		Expect(*got.RepositorySnapshotID).To(Equal(fixture.repositorySnapshot.ID))
		Expect(fixture.inputBindings(*result.WorkflowRunID)["repository"]).To(
			Equal(fixture.repositorySnapshot.ID),
		)

		Expect(fixture.tickets.Transition(
			ticketID, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{},
		)).To(Succeed())
		second, err := dispatch.DispatchOne(context.Background(), fixture.deps, ticketID, "admin")
		Expect(err).NotTo(HaveOccurred())
		Expect(second.WorkflowRunID).ToNot(BeNil())
		Expect(*second.WorkflowRunID).ToNot(Equal(*result.WorkflowRunID))
		Expect(second.RunID).ToNot(Equal(result.RunID))
		Expect(second.PipelineName).To(Equal(result.PipelineName))
		Expect(fixture.inputBindings(*second.WorkflowRunID)["repository"]).To(
			Equal(fixture.repositorySnapshot.ID),
		)
		got, found, err = fixture.tickets.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.AttemptCount).To(Equal(1))
		Expect(got.RepositorySnapshotID).ToNot(BeNil())
		Expect(*got.RepositorySnapshotID).To(Equal(fixture.repositorySnapshot.ID))
	})
})

var _ = Describe("the dispatcher loop over real stores", func() {
	It("dispatches every queued ticket in one pass", func() {
		fixture := newAgentDispatchFixture()
		first, second := fixture.queueTicket(nil), fixture.queueTicket(nil)

		Expect(dispatch.NewDispatcher(fixture.deps, dispatch.LoopConfig{}).
			Run(context.Background())).To(Succeed())

		for _, id := range []int{first, second} {
			got, found, err := fixture.tickets.Get(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.State).To(Equal(tickets.StateRunning))
			Expect(got.WorkflowRunID).ToNot(BeNil())
			Expect(got.PipelineRunID).ToNot(BeNil())
		}
	})

	It("defers an over-budget ticket, leaving it queued", func() {
		fixture := newAgentDispatchFixture()
		one := 1.0
		id := fixture.queueTicket(&one)
		Expect(db.NewAgentCostLedgerFactory(dbConn).Insert(budget.LedgerEntry{
			TicketID: &id, Source: budget.SourceAgentStep, CostUSD: 2.0,
		})).To(Succeed())

		Expect(dispatch.NewDispatcher(fixture.deps, dispatch.LoopConfig{}).
			Run(context.Background())).To(Succeed())

		got, found, err := fixture.tickets.Get(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateQueued))
		Expect(got.WorkflowRunID).To(BeNil())
		Expect(got.PipelineRunID).To(BeNil())
	})

	It("reconciles a run that died before harvest to needs_review", func() {
		fixture := newAgentDispatchFixture()
		id := fixture.queueTicket(nil)
		dispatcher := dispatch.NewDispatcher(fixture.deps, dispatch.LoopConfig{
			RunReader: fixture.pipelineRuns,
		})
		Expect(dispatcher.Run(context.Background())).To(Succeed())

		got, found, err := fixture.tickets.Get(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateRunning))
		run, found, err := fixture.pipelineRuns.GetRunByID(*got.PipelineRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(run.Finish(db.PipelineRunFailed)).To(Succeed())

		Expect(dispatcher.Run(context.Background())).To(Succeed())
		got, found, err = fixture.tickets.Get(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateNeedsReview))
	})
})
