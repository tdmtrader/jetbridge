package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workitem"
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

	It("updates user_id via the non-state writer (dispatch user resolution, 2026-07-17)", func() {
		id, err := factory.Create(newTicket("user-id round trip", "tdmtrader/concourse"))
		Expect(err).ToNot(HaveOccurred())

		uid := 4242
		Expect(factory.Update(id, tickets.Update{UserID: &uid})).To(Succeed())

		got, found, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.UserID).ToNot(BeNil())
		Expect(*got.UserID).To(Equal(4242))
	})

	It("creates a draft ticket and round-trips every column", func() {
		id, err := factory.Create(newTicket("fix flaky spec", "tdmtrader/concourse"))
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(BeNumerically(">", 0))

		got, found, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateDraft))
		Expect(got.Revision).To(Equal(int64(1)))
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

	It("atomically reserves schema-v3 dispatch and records immutable snapshot/run links", func() {
		var definitionID int
		definitionName := fmt.Sprintf("ticket-dispatch-%d", time.Now().UnixNano())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 7, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, definitionName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		insertSnapshot := func(typeName, digestDigit string) snapshot.SnapshotID {
			var id snapshot.SnapshotID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, 1, $2, 1, 1, 'filesystem-tree-v1')
				RETURNING id
			`, typeName, "sha256:"+strings.Repeat(digestDigit, 64)).Scan(&id)).To(Succeed())
			return id
		}
		repositoryID := insertSnapshot("repository", "b")
		workItemID := insertSnapshot("work-item", "c")
		ticketID, err := factory.Create(&tickets.Ticket{
			Title: "dispatch", Body: "captured", Repo: "example/repo", WorkflowName: definitionName,
			RepositorySnapshotID: &repositoryID,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Transition(ticketID, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
		before, found, err := factory.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		const callers = 12
		results := make(chan tickets.DispatchReservation, callers)
		errors := make(chan error, callers)
		var wait sync.WaitGroup
		for index := 0; index < callers; index++ {
			wait.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wait.Done()
				result, reserveErr := factory.ReserveDispatch(context.Background(), ticketID, tickets.DispatchReservationRequest{
					ExpectedRevision: before.Revision, WorkflowVersion: 7, WorkflowDefinitionID: definitionID,
				})
				results <- result
				errors <- reserveErr
			}()
		}
		wait.Wait()
		close(results)
		close(errors)
		for reserveErr := range errors {
			Expect(reserveErr).NotTo(HaveOccurred())
		}
		reservationKey := ""
		for result := range results {
			Expect(result.Key).NotTo(BeEmpty())
			if reservationKey == "" {
				reservationKey = result.Key
			} else {
				Expect(result.Key).To(Equal(reservationKey))
			}
		}

		reserved, _, err := factory.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordDispatchWorkItem(context.Background(), ticketID, reservationKey, reserved.Revision, workItemID)).To(Succeed())
		Expect(factory.RecordDispatchWorkItem(context.Background(), ticketID, reservationKey, reserved.Revision, workItemID)).To(Succeed())
		otherRepository := insertSnapshot("repository", "d")
		Expect(factory.Update(ticketID, tickets.Update{RepositorySnapshotID: &otherRepository})).To(MatchError(tickets.ErrDispatchConflict))

		var workflowRunID snapshot.WorkflowRunID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $4, 7, 3, 1, $5, $6, '{}', $7, 'ticket', $8, 'alice', 'admitting')
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, definitionName,
			strings.Repeat("a", 64), reservationKey, strings.Repeat("e", 64), strconv.Itoa(ticketID)).Scan(&workflowRunID)).To(Succeed())
		var pipelineID, pipelineRunID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, fmt.Sprintf("ticket-dispatch-pipeline-%d", time.Now().UnixNano()), defaultTeam.ID()).Scan(&pipelineID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $1, 1) RETURNING id
		`, pipelineID).Scan(&pipelineRunID)).To(Succeed())
		Expect(factory.RecordDispatchRun(context.Background(), ticketID, reservationKey, workflowRunID, pipelineRunID)).To(Succeed())
		Expect(factory.RecordDispatchRun(context.Background(), ticketID, reservationKey, workflowRunID, pipelineRunID)).To(Succeed())
		Expect(factory.Transition(ticketID, tickets.StateQueued, tickets.StateRunning,
			tickets.TransitionMeta{PipelineRunID: &pipelineRunID})).To(Succeed())

		got, found, err := factory.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.State).To(Equal(tickets.StateRunning))
		Expect(got.WorkflowDefinitionID).NotTo(BeNil())
		Expect(*got.WorkflowDefinitionID).To(Equal(definitionID))
		Expect(got.WorkflowVersion).NotTo(BeNil())
		Expect(*got.WorkflowVersion).To(Equal(7))
		Expect(got.RepositorySnapshotID).NotTo(BeNil())
		Expect(*got.RepositorySnapshotID).To(Equal(repositoryID))
		Expect(got.WorkItemSnapshotID).NotTo(BeNil())
		Expect(*got.WorkItemSnapshotID).To(Equal(workItemID))
		Expect(got.WorkflowRunID).NotTo(BeNil())
		Expect(*got.WorkflowRunID).To(Equal(workflowRunID))
		Expect(got.PipelineRunID).NotTo(BeNil())
		Expect(*got.PipelineRunID).To(Equal(pipelineRunID))
		Expect(got.DispatchReservationKey).To(Equal(reservationKey))
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
			// running→queued is the generic explicit/manual retry edge.
			// TransitionMeta remains empty; these assertions pin the retry
			// side effects and must not narrow the state-machine edge.
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.AttemptCount).To(Equal(1)) // running→queued increments
			Expect(got.CompletedAt).To(BeZero())  // stays cleared for explicit retry
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

	// seedSpec / seedPlan write the rows the deleted SubmitSpec/SubmitPlan
	// factory methods used to write. The platform has no ticket spec/plan WRITE
	// surface any more (POST .../spec, POST .../plan and the Store methods are
	// gone), but LatestSpec/ActivePlan/CaptureRevision still read rows written
	// before it was removed — so the read side is exercised over seeded rows.
	//
	// Each seed runs in ONE transaction holding the ticket's FOR UPDATE lock,
	// exactly as the deleted writers did: the row insert and the enclosing
	// ticket's revision bump must be a single atomic step, or a concurrent
	// CaptureRevision can observe spec version N under revision N-1.
	seedInTx := func(ticketID int, write func(tx db.Tx) error) {
		tx, err := dbConn.Begin()
		ExpectWithOffset(2, err).ToNot(HaveOccurred())
		defer tx.Rollback()

		var one int
		ExpectWithOffset(2, tx.QueryRow(
			`SELECT 1 FROM agent_tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&one)).To(Succeed())
		ExpectWithOffset(2, write(tx)).To(Succeed())
		_, err = tx.Exec(
			`UPDATE agent_tickets SET revision = revision + 1, updated_at = now() WHERE id = $1`, ticketID)
		ExpectWithOffset(2, err).ToNot(HaveOccurred())
		ExpectWithOffset(2, tx.Commit()).To(Succeed())
	}

	seedSpec := func(ticketID int, spec tickets.Spec) int {
		criteria := spec.AcceptanceCriteria
		if criteria == nil {
			criteria = []string{}
		}
		links := spec.Links
		if links == nil {
			links = []tickets.Link{}
		}
		criteriaJSON, err := json.Marshal(criteria)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		linksJSON, err := json.Marshal(links)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		var version int
		seedInTx(ticketID, func(tx db.Tx) error {
			return tx.QueryRow(`
				INSERT INTO agent_ticket_specs
					(ticket_id, version, title, body, acceptance_criteria, links, submitted_by)
				SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4, $5, $6
				FROM agent_ticket_specs WHERE ticket_id = $1
				RETURNING version
			`, ticketID, spec.Title, spec.Body, criteriaJSON, linksJSON, spec.SubmittedBy).Scan(&version)
		})
		return version
	}

	seedPlan := func(ticketID int, ts []tickets.Task) int {
		var planVersion int
		seedInTx(ticketID, func(tx db.Tx) error {
			if err := tx.QueryRow(
				`SELECT COALESCE(MAX(plan_version), 0) + 1 FROM agent_ticket_tasks WHERE ticket_id = $1`,
				ticketID).Scan(&planVersion); err != nil {
				return err
			}
			for i, task := range ts {
				status := task.Status
				if status == "" {
					status = tickets.TaskPending
				}
				if _, err := tx.Exec(`
					INSERT INTO agent_ticket_tasks (ticket_id, plan_version, ordering, title, detail, status)
					VALUES ($1, $2, $3, $4, $5, $6)
				`, ticketID, planVersion, i+1, task.Title, task.Detail, string(status)); err != nil {
					return err
				}
			}
			return nil
		})
		return planVersion
	}

	Describe("specs and plans (read side)", func() {
		var id int

		BeforeEach(func() {
			var err error
			id, err = factory.Create(newTicket("spec'd", "tdmtrader/concourse"))
			Expect(err).ToNot(HaveOccurred())
		})

		It("keeps old spec rows and returns the latest", func() {
			Expect(seedSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"tests pass"},
				Links:              []tickets.Link{{Title: "design", URL: "https://x"}},
				SubmittedBy:        "run-42-platform",
			})).To(Equal(1))
			Expect(seedSpec(id, tickets.Spec{Title: "spec2", Body: "prose2"})).To(Equal(2))

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
			seedSpec(id, tickets.Spec{
				Title: "spec", Body: "prose",
				AcceptanceCriteria: []string{"a", "b"},
				Links:              []tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}},
			})

			latest, _, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(latest.AcceptanceCriteria).To(Equal([]string{"a", "b"}))
			Expect(latest.Links).To(Equal([]tickets.Link{{Title: "l1", URL: "u1"}, {Title: "l2", URL: "u2"}}))
		})

		It("LatestSpec is found=false without specs", func() {
			_, found, err := factory.LatestSpec(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("ActivePlan returns only the newest plan version", func() {
			Expect(seedPlan(id, []tickets.Task{{Title: "one"}, {Title: "two", Detail: "d"}})).To(Equal(1))
			Expect(seedPlan(id, []tickets.Task{{Title: "redone"}})).To(Equal(2))

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
		})
	})

	Describe("immutable work-item revision capture", func() {
		It("increments every mutable path and captures the complete current revision", func() {
			version := 3
			ticket := newTicket("upgrade postgres", "tdmtrader/concourse")
			ticket.Origin = "jira"
			ticket.ExternalRef = "ENG-42"
			ticket.WorkflowName = "version-upgrade"
			ticket.WorkflowVersion = &version
			id, err := factory.Create(ticket)
			Expect(err).ToNot(HaveOccurred())

			expectRevision := func(revision int64) {
				got, found, err := factory.Get(id)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(got.Revision).To(Equal(revision))
			}
			expectRevision(1)

			body := "Upgrade PostgreSQL to 18."
			Expect(factory.Update(id, tickets.Update{Body: &body})).To(Succeed())
			expectRevision(2)
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())
			expectRevision(3)
			seedSpec(id, tickets.Spec{Title: "old", Body: "old", SubmittedBy: "alice"})
			expectRevision(4)
			seedSpec(id, tickets.Spec{
				Title: "current", Body: "preserve extensions", AcceptanceCriteria: []string{"tests pass"},
				Links: []tickets.Link{{Title: "release", URL: "https://example.test/release"}}, SubmittedBy: "bob",
			})
			expectRevision(5)
			seedPlan(id, []tickets.Task{{Title: "old plan"}})
			expectRevision(6)
			planVersion := seedPlan(id, []tickets.Task{
				{Title: "bump", Detail: "> editing go.mod", Status: tickets.TaskDone},
				{Title: "test"},
			})
			expectRevision(7)

			captured, found, err := factory.CaptureRevision(context.Background(), id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(captured.TicketID).To(Equal(id))
			Expect(captured.Revision).To(Equal(int64(7)))
			var document contracts.WorkItemDocument
			Expect(json.Unmarshal(captured.Document, &document)).To(Succeed())
			Expect(document.Validate()).To(Succeed())
			Expect(document.Adapter).To(Equal("jira"))
			Expect(document.ExternalID).To(Equal("ENG-42"))
			Expect(document.Body).To(Equal(body))
			Expect(document.State).To(Equal("queued"))
			Expect(document.Workflow).ToNot(BeNil())
			Expect(document.Workflow.Name).To(Equal("version-upgrade"))
			Expect(document.Workflow.Version).ToNot(BeNil())
			Expect(*document.Workflow.Version).To(Equal(3))
			Expect(document.Spec).ToNot(BeNil())
			Expect(document.Spec.Revision).To(Equal("2"))
			Expect(document.Plan).ToNot(BeNil())
			Expect(document.Plan.Revision).To(Equal(strconv.Itoa(planVersion)))
			// work-item/v1 carries no comments key any more.
			Expect(captured.Document).ToNot(ContainSubstring(`"comments"`))

			var spec workitem.SpecRevision
			Expect(json.Unmarshal([]byte(document.Spec.Content), &spec)).To(Succeed())
			Expect(spec.Body).To(Equal("preserve extensions"))
			Expect(spec.AcceptanceCriteria).To(Equal([]string{"tests pass"}))
			var plan workitem.PlanRevision
			Expect(json.Unmarshal([]byte(document.Plan.Content), &plan)).To(Succeed())
			Expect(plan.Tasks).To(HaveLen(2))
			Expect(plan.Tasks[0].Status).To(Equal("done"))
			Expect(plan.Tasks[0].Detail).To(Equal("> editing go.mod"))
		})

		It("returns complete revision N or N+1 while specs are edited concurrently", func() {
			id, err := factory.Create(newTicket("concurrent capture", "repo"))
			Expect(err).ToNot(HaveOccurred())
			started := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				<-started
				for version := 1; version <= 40; version++ {
					seedSpec(id, tickets.Spec{
						Title: fmt.Sprintf("spec-%d", version), Body: fmt.Sprintf("body-%d", version), SubmittedBy: "writer",
					})
					time.Sleep(time.Millisecond)
				}
				finished <- nil
			}()
			close(started)

			captures := 0
			for {
				select {
				case err := <-finished:
					Expect(err).ToNot(HaveOccurred())
					Expect(captures).To(BeNumerically(">", 0))
					return
				default:
				}
				captured, found, err := factory.CaptureRevision(context.Background(), id)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				var document contracts.WorkItemDocument
				Expect(json.Unmarshal(captured.Document, &document)).To(Succeed())
				if document.Spec == nil {
					Expect(captured.Revision).To(Equal(int64(1)))
				} else {
					var spec workitem.SpecRevision
					Expect(json.Unmarshal([]byte(document.Spec.Content), &spec)).To(Succeed())
					suffix := strings.TrimPrefix(spec.Title, "spec-")
					titleVersion, err := strconv.Atoi(suffix)
					Expect(err).ToNot(HaveOccurred())
					Expect(spec.Version).To(Equal(titleVersion))
					Expect(spec.Body).To(Equal(fmt.Sprintf("body-%d", titleVersion)))
					Expect(captured.Revision).To(Equal(int64(titleVersion + 1)))
				}
				captures++
			}
		})
	})
})
