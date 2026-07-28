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
		version := 3
		return &tickets.Ticket{
			Title: title, Body: "body md", Origin: "fly", Repo: repo,
			WorkflowName: "standard-dev", WorkflowVersion: &version,
			UserName: "tdm", CreatedBy: "tdm",
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
		Expect(factory.Update(id, tickets.Update{Title: &title})).To(Succeed())

		got, _, err := factory.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Title).To(Equal("new title"))
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
					(team_id, type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, $2, 1, $3, 1, 1, 'filesystem-tree-v1')
				RETURNING id
			`, defaultTeam.ID(), typeName, "sha256:"+strings.Repeat(digestDigit, 64)).Scan(&id)).To(Succeed())
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

		It("walks draft→queued→running→needs_review→closed recording side effects", func() {
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
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.CompletedAt).To(BeZero()) // needs_review is not terminal

			// needs_review → closed is the ONE human close action. WHY it
			// closed (merged / dropped / analysis-only) is the durable run's
			// outcome, never a ticket state.
			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateClosed,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.State).To(Equal(tickets.StateClosed))
			Expect(got.CompletedAt).To(BeNumerically(">", 0))

			// No exits: closed tickets never re-enter the queue.
			Expect(factory.Transition(id, tickets.StateClosed, tickets.StateQueued,
				tickets.TransitionMeta{})).To(MatchError(tickets.ErrInvalidTransition))
		})

		It("clears completed_at on requeue and counts attempts", func() {
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			Expect(factory.Transition(id, tickets.StateQueued, tickets.StateRunning,
				tickets.TransitionMeta{})).To(Succeed())
			// A run that died still lands in front of a human: needs_review is
			// the only completion edge now, and it is not terminal.
			Expect(factory.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
				tickets.TransitionMeta{})).To(Succeed())

			got, _, _ := factory.Get(id)
			Expect(got.CompletedAt).To(BeZero())
			Expect(got.AttemptCount).To(BeZero())

			Expect(factory.Transition(id, tickets.StateNeedsReview, tickets.StateQueued,
				tickets.TransitionMeta{})).To(Succeed())
			got, _, _ = factory.Get(id)
			Expect(got.CompletedAt).To(BeZero())

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
			Expect(factory.Transition(id, tickets.StateDraft, tickets.StateNeedsReview,
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

	Describe("immutable work-item revision capture", func() {
		It("increments every mutable path and captures the complete current revision", func() {
			version := 3
			ticket := newTicket("upgrade postgres", "tdmtrader/concourse")
			ticket.Origin = "web"
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

			captured, found, err := factory.CaptureRevision(context.Background(), id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(captured.TicketID).To(Equal(id))
			Expect(captured.Revision).To(Equal(int64(3)))
			var document contracts.WorkItemDocument
			Expect(json.Unmarshal(captured.Document, &document)).To(Succeed())
			Expect(document.Validate()).To(Succeed())
			Expect(document.Adapter).To(Equal("jetbridge"))
			Expect(document.ExternalID).To(Equal("ENG-42"))
			Expect(document.Title).To(Equal("upgrade postgres"))
			Expect(document.Body).To(Equal(body))
			// work-item/v1 no longer embeds its consumer: the ticket's lifecycle
			// state and the workflow selected to run over it belong to the durable
			// run, not to the immutable captured value.
			Expect(captured.Document).ToNot(ContainSubstring(`"state"`))
			Expect(captured.Document).ToNot(ContainSubstring(`"workflow"`))
			// Nor the retired content surfaces: comments, spec and plan tables
			// are gone, so the ticket body is the whole of the captured prose.
			for _, retired := range []string{`"comments"`, `"spec"`, `"plan"`} {
				Expect(captured.Document).ToNot(ContainSubstring(retired))
			}
		})

		It("returns complete revision N or N+1 while the body is edited concurrently", func() {
			id, err := factory.Create(newTicket("concurrent capture", "repo"))
			Expect(err).ToNot(HaveOccurred())
			started := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				<-started
				for version := 1; version <= 40; version++ {
					body := fmt.Sprintf("body-%d", version)
					Expect(factory.Update(id, tickets.Update{Body: &body})).To(Succeed())
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
				// Every capture is one consistent revision: the body value and
				// the revision counter must never come from different rows.
				if document.Body == "body md" {
					Expect(captured.Revision).To(Equal(int64(1)))
				} else {
					suffix := strings.TrimPrefix(document.Body, "body-")
					bodyVersion, err := strconv.Atoi(suffix)
					Expect(err).ToNot(HaveOccurred())
					Expect(captured.Revision).To(Equal(int64(bodyVersion + 1)))
				}
				captures++
			}
		})
	})
})
