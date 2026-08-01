package db_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
)

var _ = Describe("AgentWorkflowRunNodeOccurrencesFactory", func() {
	var (
		factory db.AgentWorkflowRunNodeOccurrencesFactory
		ctx     context.Context
	)

	BeforeEach(func() {
		factory = db.NewAgentWorkflowRunNodeOccurrencesFactory(dbConn)
		ctx = context.Background()
	})

	// createAgentWorkflowRunForOccurrences reuses the suite's existing run
	// fixture, which inserts a real definition and a real run under
	// defaultTeam bound to a real build.
	createRun := func() (int64, string) {
		name := fmt.Sprintf("occurrences-%d", time.Now().UnixNano())
		runID := createWorkflowRun(name, 3, createBuildWithStatus(db.BuildStatusSucceeded))
		return int64(runID), name
	}

	occurrence := func(runID int64, name, nodeID, kind, planID, status string) db.AgentWorkflowRunNodeOccurrence {
		return db.AgentWorkflowRunNodeOccurrence{
			WorkflowRunID: runID, NodeID: nodeID, RetryAttempt: 1, Attempt: 1,
			TeamID: defaultTeam.ID(), WorkflowName: name,
			WorkflowDefinitionID: 41, WorkflowVersion: 3,
			NodeKind: kind, PlanID: planID, Status: status,
		}
	}

	Describe("Freeze", func() {
		It("persists one row per node attempt and reads them back in plan order", func() {
			runID, name := createRun()

			implement := occurrence(runID, name, "implement", "agent", "1/1", "succeeded")
			implement.CostUSD = 1.25
			approval := occurrence(runID, name, "approval", "await", "1/2", "waiting")

			// Written out of plan order on purpose, so the read proves an
			// ordering rather than echoing the insertion sequence.
			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{approval, implement})).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(2))
			Expect(stored[0].NodeID).To(Equal("implement"))
			Expect(stored[0].Status).To(Equal("succeeded"))
			Expect(stored[0].NodeKind).To(Equal("agent"))
			Expect(stored[0].CostUSD).To(Equal(1.25))
			Expect(stored[0].FrozenAt).ToNot(BeZero())
			Expect(stored[1].NodeID).To(Equal("approval"))
			Expect(stored[1].Status).To(Equal("waiting"))
		})

		It("carries every projected field through the round trip", func() {
			runID, name := createRun()

			started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
			completed := started.Add(4 * time.Minute)
			publicationID := int64(97)
			version := 4

			row := occurrence(runID, name, "review", "agent", "1/3", "failed")
			row.RetryAttempt = 2
			row.Attempt = 3
			row.ReusableNodeName = "code-review-node"
			row.ReusableNodeVersion = &version
			row.PublicationID = &publicationID
			row.StartedAt = &started
			row.CompletedAt = &completed
			row.DurationSeconds = 240
			row.CostUSD = 2.5

			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{row})).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(1))
			Expect(stored[0].RetryAttempt).To(Equal(2))
			Expect(stored[0].Attempt).To(Equal(3))
			Expect(stored[0].TeamID).To(Equal(defaultTeam.ID()))
			Expect(stored[0].WorkflowName).To(Equal(name))
			Expect(stored[0].WorkflowDefinitionID).To(Equal(41))
			Expect(stored[0].WorkflowVersion).To(Equal(3))
			Expect(stored[0].PlanID).To(Equal("1/3"))
			Expect(stored[0].ReusableNodeName).To(Equal("code-review-node"))
			Expect(stored[0].ReusableNodeVersion).ToNot(BeNil())
			Expect(*stored[0].ReusableNodeVersion).To(Equal(4))
			Expect(stored[0].WaitID).To(BeNil())
			Expect(stored[0].PublicationID).ToNot(BeNil())
			Expect(*stored[0].PublicationID).To(Equal(int64(97)))
			Expect(stored[0].StartedAt.UTC()).To(Equal(started))
			Expect(stored[0].CompletedAt.UTC()).To(Equal(completed))
			Expect(stored[0].DurationSeconds).To(Equal(240))
			Expect(stored[0].CostUSD).To(Equal(2.5))
		})

		It("is idempotent so a retried finalization cannot double-write", func() {
			runID, name := createRun()
			rows := []db.AgentWorkflowRunNodeOccurrence{
				occurrence(runID, name, "implement", "agent", "1/1", "succeeded"),
			}

			Expect(factory.Freeze(ctx, rows)).To(Succeed())
			Expect(factory.Freeze(ctx, rows)).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(1))
		})

		It("keeps the first freeze when a retried one disagrees, because frozen history is immutable", func() {
			runID, name := createRun()

			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{
				occurrence(runID, name, "implement", "agent", "1/1", "succeeded"),
			})).To(Succeed())
			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{
				occurrence(runID, name, "implement", "agent", "1/1", "failed"),
			})).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(1))
			Expect(stored[0].Status).To(Equal("succeeded"))
		})

		It("keeps the retry copies of one node apart", func() {
			runID, name := createRun()

			first := occurrence(runID, name, "implement", "agent", "1/1", "failed")
			second := occurrence(runID, name, "implement", "agent", "1/2", "succeeded")
			second.RetryAttempt = 2

			// Both copies are recovery attempt 1 of their own copy, so a key
			// without retry_attempt would silently drop the second one under
			// ON CONFLICT DO NOTHING.
			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{second, first})).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(2))
			Expect(stored[0].RetryAttempt).To(Equal(1))
			Expect(stored[0].Status).To(Equal("failed"))
			Expect(stored[1].RetryAttempt).To(Equal(2))
			Expect(stored[1].Status).To(Equal("succeeded"))
		})

		It("writes nothing at all when one row is invalid", func() {
			runID, name := createRun()

			good := occurrence(runID, name, "implement", "agent", "1/1", "succeeded")
			bad := occurrence(runID, name, "review", "agent", "1/2", "not-a-status")

			err := factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{good, bad})
			Expect(err).To(HaveOccurred())
			// The error must name the row that could not be frozen. Letting the
			// transaction's own "current transaction is aborted" surface
			// instead would say a freeze failed without saying which node's
			// projection was wrong, which is the only actionable part.
			Expect(err.Error()).To(ContainSubstring("review"))
			Expect(err.Error()).To(ContainSubstring("1/2"))

			// A half-written projection would be indistinguishable from a run
			// that never reached the remaining nodes.
			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(BeEmpty())
		})

		It("accepts an empty projection without touching the database", func() {
			runID, _ := createRun()
			Expect(factory.Freeze(ctx, nil)).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(BeEmpty())
		})

		It("rejects an in-place update of frozen history", func() {
			runID, name := createRun()
			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{
				occurrence(runID, name, "implement", "agent", "1/1", "succeeded"),
			})).To(Succeed())

			_, err := dbConn.ExecContext(ctx,
				`UPDATE agent_workflow_run_node_occurrences SET status = 'failed' WHERE workflow_run_id = $1`,
				runID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable once frozen"))
		})
	})

	Describe("ForRun", func() {
		It("returns only the requested run's occurrences", func() {
			runID, name := createRun()
			otherID, otherName := createRun()

			Expect(factory.Freeze(ctx, []db.AgentWorkflowRunNodeOccurrence{
				occurrence(runID, name, "implement", "agent", "1/1", "succeeded"),
				occurrence(otherID, otherName, "implement", "agent", "1/1", "failed"),
			})).To(Succeed())

			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveLen(1))
			Expect(stored[0].WorkflowRunID).To(Equal(runID))
			Expect(stored[0].Status).To(Equal("succeeded"))
		})

		It("returns nothing for a run that was never frozen", func() {
			runID, _ := createRun()
			stored, err := factory.ForRun(ctx, runID)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(BeEmpty())
		})
	})
})
