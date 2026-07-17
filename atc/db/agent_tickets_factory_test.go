package db_test

import (
	"database/sql"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentTicketsFactory", func() {
	var factory db.AgentTicketsFactory

	BeforeEach(func() {
		factory = db.NewAgentTicketsFactory(dbConn)
	})

	newTicket := func(title, repo string) *tickets.Ticket {
		budget := 12.5
		version := 3
		return &tickets.Ticket{
			Title: title, Body: "body md", Origin: "fly", Repo: repo,
			WorkflowName: "standard-dev", WorkflowVersion: &version,
			BudgetUSD: &budget, UserName: "tdm", CreatedBy: "tdm",
		}
	}

	It("creates a draft ticket and round-trips every column", func() {
		id, err := factory.Create(newTicket("fix flaky spec", "tdmtrader/concourse"))
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(BeNumerically(">", 0))

		got, found, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateDraft))
		Expect(got.Origin).To(Equal("fly"))
		Expect(got.TargetBranch).To(Equal("main")) // defaulted
		Expect(got.WorkflowName).To(Equal("standard-dev"))
		Expect(*got.WorkflowVersion).To(Equal(3))
		Expect(got.WorkflowDefinitionID).To(BeNil())
		Expect(*got.BudgetUSD).To(Equal(12.5))
		Expect(got.UserID).To(BeNil())
		Expect(got.UserName).To(Equal("tdm"))
		Expect(got.CreatedBy).To(Equal("tdm"))
		Expect(got.ExternalRef).To(Equal(""))
		Expect(got.PipelineRunID).To(BeNil())
		Expect(got.AttemptCount).To(BeZero())
		Expect(got.CreatedAt).To(BeNumerically(">", 0))
		Expect(got.UpdatedAt).To(BeNumerically(">", 0))
		Expect(got.CompletedAt).To(BeZero())
	})

	It("Get returns found=false for a missing id", func() {
		_, found, err := factory.Get(999999)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("lists newest-first and honors filters", func() {
		id1, err := factory.Create(newTicket("a", "repo/one"))
		Expect(err).ToNot(HaveOccurred())
		id2, err := factory.Create(newTicket("b", "repo/two"))
		Expect(err).ToNot(HaveOccurred())

		all, err := factory.List(tickets.ListFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0].ID).To(Equal(id2)) // newest first
		Expect(all[1].ID).To(Equal(id1))

		byRepo, err := factory.List(tickets.ListFilter{Repo: "repo/one"})
		Expect(err).ToNot(HaveOccurred())
		Expect(byRepo).To(HaveLen(1))
		Expect(byRepo[0].ID).To(Equal(id1))

		byState, err := factory.List(tickets.ListFilter{State: tickets.StateQueued})
		Expect(err).ToNot(HaveOccurred())
		Expect(byState).To(BeEmpty())

		limited, err := factory.List(tickets.ListFilter{Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(HaveLen(1))
		Expect(limited[0].ID).To(Equal(id2))
	})

	It("updates only the provided fields and bumps updated_at", func() {
		id, err := factory.Create(newTicket("t", "r"))
		Expect(err).ToNot(HaveOccurred())

		var before sql.NullTime
		Expect(dbConn.QueryRow(`SELECT updated_at FROM agent_tickets WHERE id = $1`, id).
			Scan(&before)).To(Succeed())

		title := "new title"
		budget := 3.25
		Expect(factory.Update(id, tickets.Update{Title: &title, BudgetUSD: &budget})).To(Succeed())

		got, _, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Title).To(Equal("new title"))
		Expect(*got.BudgetUSD).To(Equal(3.25))
		Expect(got.Body).To(Equal("body md"))              // untouched
		Expect(got.WorkflowName).To(Equal("standard-dev")) // untouched
	})

	It("Update returns ErrTicketNotFound for a missing id", func() {
		title := "x"
		Expect(factory.Update(424242, tickets.Update{Title: &title})).
			To(MatchError(tickets.ErrTicketNotFound))
	})

	Describe("Transition (the single writer)", func() {
		var id int

		BeforeEach(func() {
			var err error
			id, err = factory.Create(newTicket("lifecycle", "tdmtrader/concourse"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("walks draft→queued→running→needs_review→merged recording side effects", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			var queuedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT queued_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&queuedAt)).To(Succeed())
			Expect(queuedAt.Valid).To(BeTrue())

			runID := 42
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{PipelineRunID: &runID})).To(Succeed())
			got, _, err := factory.Get(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(*got.PipelineRunID).To(Equal(42))
			var dispatchedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT dispatched_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&dispatchedAt)).To(Succeed())
			Expect(dispatchedAt.Valid).To(BeTrue())

			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
				tickets.TransitionMeta{Branch: "agent/ticket-7"})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.Branch).To(Equal("agent/ticket-7"))
			Expect(got.CompletedAt).To(BeZero()) // needs_review is not terminal

			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateMerged,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateMerged))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))
		})

		It("stamps completed_at on needs_review→concluded (spike disposition, terminal)", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
				tickets.TransitionMeta{})).To(Succeed())

			// needs_review → concluded: explicit human disposition — "run
			// finished, human reviewed, no merge intended" (FLOWS.md §3
			// spike-research). Positive sibling of abandoned; TERMINAL.
			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateConcluded,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ := factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateConcluded))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))

			// No exits: concluded tickets never re-enter the queue.
			Expect(factory.Transition(id, tickets.StateConcluded, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrInvalidTransition))
		})

		It("records error_detail on errored, clears completed_at on requeue, and counts attempts", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateErrored,
				tickets.TransitionMeta{ErrorDetail: "web node died"})).To(Succeed())

			got, _, _ := factory.Get(id)
			Expect(got.ErrorDetail).To(Equal("web node died"))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))
			Expect(got.AttemptCount).To(BeZero()) // errored, not requeued

			Expect(factory.Transition(id, tickets.StateErrored, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.CompletedAt).To(BeZero()) // cleared on requeue

			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			// running→queued (retryable platform error OR rejected send_back
			// checkpoint re-dispatch; attempt_count++). Second legitimate
			// caller: dispatch's run-completion reconciler (checkpoint-seam
			// delta §6, 2026-07-09), which requeues with TransitionMeta{}
			// exactly as below — these side-effect assertions are its
			// contract; do not narrow them.
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.AttemptCount).To(Equal(1)) // running→queued increments
			Expect(got.CompletedAt).To(BeZero())  // stays cleared for re-dispatch
			var requeuedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT queued_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&requeuedAt)).To(Succeed())
			Expect(requeuedAt.Valid).To(BeTrue()) // queued_at re-stamped
		})

		It("rejects illegal edges without touching the row", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateMerged,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrInvalidTransition))
			got, _, _ := factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateDraft))
		})

		It("returns ErrStaleTransition when the from-state no longer matches", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrStaleTransition))
		})

		It("returns ErrTicketNotFound for a missing ticket", func() {
			Expect(factory.Transition(987654, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrTicketNotFound))
		})
	})

	Describe("specs and plans", func() {
		var id int

		BeforeEach(func() {
			var err error
			id, err = factory.Create(newTicket("spec'd", "tdmtrader/concourse"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("versions specs, keeps old rows, and returns the latest", func() {
			v, err := factory.SubmitSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"tests pass"},
				Links:              []tickets.Link{{Title: "design", URL: "https://x"}},
				SubmittedBy:        "run-42-platform",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(v).To(Equal(1))

			v, err = factory.SubmitSpec(id, tickets.Spec{Title: "spec2", Body: "prose2"})
			Expect(err).ToNot(HaveOccurred())
			Expect(v).To(Equal(2))

			latest, found, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(latest.Version).To(Equal(2))
			Expect(latest.Title).To(Equal("spec2"))
			Expect(latest.AcceptanceCriteria).To(BeEmpty())
			Expect(latest.Links).To(BeEmpty())
			Expect(latest.CreatedAt).To(BeNumerically(">", 0))

			var count int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_ticket_specs WHERE ticket_id = $1`, id).
				Scan(&count)).To(Succeed())
			Expect(count).To(Equal(2)) // v1 retained for process intelligence
		})

		It("round-trips acceptance criteria and links JSON", func() {
			_, err := factory.SubmitSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"a", "b"},
				Links:              []tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}},
			})
			Expect(err).ToNot(HaveOccurred())

			latest, _, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(latest.AcceptanceCriteria).To(Equal([]string{"a", "b"}))
			Expect(latest.Links).To(Equal([]tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}}))
		})

		It("LatestSpec is found=false without specs; SubmitSpec 404s a missing ticket", func() {
			_, found, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			_, err = factory.SubmitSpec(313131, tickets.Spec{Title: "t", Body: "b"})
			Expect(err).To(MatchError(tickets.ErrTicketNotFound))
		})

		It("replaces the active plan by bumping plan_version", func() {
			pv, err := factory.SubmitPlan(id, []tickets.Task{{Title: "one"}, {Title: "two", Detail: "d"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(pv).To(Equal(1))

			pv, err = factory.SubmitPlan(id, []tickets.Task{{Title: "redone"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(pv).To(Equal(2))

			active, err := factory.ActivePlan(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(active).To(HaveLen(1))
			Expect(active[0].Title).To(Equal("redone"))
			Expect(active[0].Ordering).To(Equal(1))
			Expect(active[0].PlanVersion).To(Equal(2))
			Expect(active[0].Status).To(Equal(tickets.TaskPending))

			var total int
			Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM agent_ticket_tasks WHERE ticket_id = $1`, id).
				Scan(&total)).To(Succeed())
			Expect(total).To(Equal(3)) // v1's two tasks retained

			_, err = factory.SubmitPlan(313131, []tickets.Task{{Title: "x"}})
			Expect(err).To(MatchError(tickets.ErrTicketNotFound))
		})

		It("updates task status and appends notes as blockquotes", func() {
			_, err := factory.SubmitPlan(id, []tickets.Task{{Title: "one"}})
			Expect(err).ToNot(HaveOccurred())

			Expect(factory.UpdateTaskStatus(id, 1, 1, tickets.TaskInProgress)).To(Succeed())
			Expect(factory.AppendTaskNote(id, 1, 1, "halfway")).To(Succeed())
			Expect(factory.AppendTaskNote(id, 1, 1, "done now")).To(Succeed())

			active, err := factory.ActivePlan(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(active[0].Status).To(Equal(tickets.TaskInProgress))
			Expect(active[0].Detail).To(Equal("> halfway\n\n> done now"))
			Expect(active[0].UpdatedAt).To(BeNumerically(">", 0))

			Expect(factory.UpdateTaskStatus(id, 1, 99, tickets.TaskDone)).
				To(MatchError(tickets.ErrTaskNotFound))
			Expect(factory.AppendTaskNote(id, 9, 1, "x")).
				To(MatchError(tickets.ErrTaskNotFound))
		})
	})
})
