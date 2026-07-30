package db_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentChildExecutionsFactory", func() {
	var (
		factory db.AgentChildExecutionsFactory
		runID   int64
	)

	BeforeEach(func() {
		factory = db.NewAgentChildExecutionsFactory(dbConn)
		suffix := time.Now().UnixNano()
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'broker-test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("broker-%d", suffix), fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7, 'manual', '', 'broker-test', 'running')
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID,
			fmt.Sprintf("broker-%d", suffix), strings.Repeat("a", 64),
			fmt.Sprintf("run-%d", suffix), strings.Repeat("b", 64)).Scan(&runID)).To(Succeed())
	})

	It("creates idempotently, rejects identity drift, and advances monotonically", func() {
		identity := broker.ExecutionIdentity{
			TeamID: defaultTeam.ID(), WorkflowRunID: runID, NodePlanID: "review",
			ParentAttempt: 1, IdempotencyKey: "call-1", Tool: broker.ToolConsultAgent,
			Selector:  broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
			ProfileID: "profile", ProfileDigest: "sha256:" + strings.Repeat("c", 64),
			InputDigest: "sha256:" + strings.Repeat("d", 64), Attachments: []string{"design"},
		}
		created, err := factory.Create(context.Background(),
			"9ed04ef1-0db0-4d1f-a2c1-b7eeedce8f36", identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.State).To(Equal(broker.ExecutionPending))
		Expect(created.Sequence).To(BeZero())

		replayed, err := factory.Create(context.Background(),
			"d72433a5-f9d9-4f48-b785-c5f37d0b209b", identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.ID).To(Equal(created.ID))

		drift := identity
		drift.ProfileID = "different"
		_, err = factory.Create(context.Background(),
			"f994f0a8-6ee3-4bbc-b946-814fd1c1cf00", drift)
		Expect(err).To(MatchError(ContainSubstring("identity")))

		advanced, err := factory.Advance(context.Background(), db.AdvanceAgentChildExecution{
			ID: created.ID, TeamID: defaultTeam.ID(), ExpectedSequence: 0,
			State: broker.ExecutionAdmitted, Phase: "admitted",
			LeaseExpiresAt: time.Now().Add(time.Minute), BrokerInstance: "pod-1",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced.Sequence).To(Equal(int64(1)))
		Expect(advanced.State).To(Equal(broker.ExecutionAdmitted))

		_, err = factory.Advance(context.Background(), db.AdvanceAgentChildExecution{
			ID: created.ID, TeamID: defaultTeam.ID(), ExpectedSequence: 0,
			State: broker.ExecutionRunning, Phase: "running",
		})
		Expect(err).To(MatchError(ContainSubstring("sequence")))
	})

	It("converges concurrent creates on one durable execution", func() {
		identity := broker.ExecutionIdentity{
			TeamID: defaultTeam.ID(), WorkflowRunID: runID, NodePlanID: "consult",
			ParentAttempt: 1, IdempotencyKey: "concurrent-call", Tool: broker.ToolConsultAgent,
			Selector:  broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
			ProfileID: "profile", ProfileDigest: "sha256:" + strings.Repeat("c", 64),
			InputDigest: "sha256:" + strings.Repeat("d", 64), Attachments: []string{"design"},
		}
		ids := []string{
			"209a3552-4055-4cdf-aed1-c3f49c15de4c",
			"ee3ca478-7b5b-40ef-9566-cc9be0935295",
		}
		start := make(chan struct{})
		results := make(chan db.AgentChildExecution, 2)
		failures := make(chan error, 2)
		var wait sync.WaitGroup
		for _, id := range ids {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				execution, err := factory.Create(context.Background(), id, identity)
				results <- execution
				failures <- err
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		close(failures)
		for err := range failures {
			Expect(err).NotTo(HaveOccurred())
		}
		var durableID string
		for execution := range results {
			if durableID == "" {
				durableID = execution.ID
			}
			Expect(execution.ID).To(Equal(durableID))
		}
	})

	It("reconciles a bounded non-blocking claim of expired leases as broker_lost", func() {
		identity := broker.ExecutionIdentity{
			TeamID: defaultTeam.ID(), WorkflowRunID: runID, NodePlanID: "expired-first",
			ParentAttempt: 1, IdempotencyKey: "expired-first", Tool: broker.ToolConsultAgent,
			Selector:  broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
			ProfileID: "profile", ProfileDigest: "sha256:" + strings.Repeat("c", 64),
			InputDigest: "sha256:" + strings.Repeat("d", 64), Attachments: []string{"design"},
		}
		first, err := factory.Create(context.Background(),
			"8f6a6bfa-7410-4fca-a569-15a4cd9e983e", identity)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Advance(context.Background(), db.AdvanceAgentChildExecution{
			ID: first.ID, TeamID: defaultTeam.ID(), ExpectedSequence: first.Sequence,
			State: broker.ExecutionAdmitted, Phase: "admitted",
			LeaseExpiresAt: time.Now().Add(-2 * time.Minute), BrokerInstance: "broker-1",
		})
		Expect(err).NotTo(HaveOccurred())

		secondIdentity := identity
		secondIdentity.NodePlanID = "expired-second"
		secondIdentity.IdempotencyKey = "expired-second"
		second, err := factory.Create(context.Background(),
			"684cce96-8777-4b0f-8a74-a293482f9c72", secondIdentity)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Advance(context.Background(), db.AdvanceAgentChildExecution{
			ID: second.ID, TeamID: defaultTeam.ID(), ExpectedSequence: second.Sequence,
			State: broker.ExecutionAdmitted, Phase: "admitted",
			LeaseExpiresAt: time.Now().Add(-time.Minute), BrokerInstance: "broker-2",
		})
		Expect(err).NotTo(HaveOccurred())

		locked, err := dbConn.BeginTx(context.Background(), nil)
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(locked)
		var lockedID string
		Expect(locked.QueryRowContext(context.Background(), `
			SELECT id::text FROM agent_child_executions
			WHERE id = $1::uuid
			FOR UPDATE
		`, first.ID).Scan(&lockedID)).To(Succeed())
		Expect(lockedID).To(Equal(first.ID))

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		type result struct {
			executions []db.AgentChildExecution
			err        error
		}
		results := make(chan result, 1)
		go func() {
			executions, err := factory.ReconcileExpiredLeases(ctx, 1)
			results <- result{executions: executions, err: err}
		}()

		var reconciliation result
		Eventually(results).Should(Receive(&reconciliation))
		Expect(reconciliation.err).NotTo(HaveOccurred())
		Expect(reconciliation.executions).To(HaveLen(1))
		Expect(reconciliation.executions[0].ID).To(Equal(second.ID))
		Expect(reconciliation.executions[0].State).To(Equal(broker.ExecutionErrored))
		Expect(reconciliation.executions[0].Sequence).To(Equal(int64(2)))
		Expect(reconciliation.executions[0].LeaseExpiresAt).To(BeNil())
		Expect(reconciliation.executions[0].TerminalAt).NotTo(BeNil())
		Expect(reconciliation.executions[0].ErrorCode).To(Equal("broker_lost"))
		Expect(*reconciliation.executions[0].ErrorRetryable).To(BeTrue())

		var eventState, eventPhase string
		var eventSequence int64
		Expect(dbConn.QueryRow(`
			SELECT sequence, state, phase
			FROM agent_child_execution_events
			WHERE execution_id = $1::uuid AND sequence = 2
		`, second.ID).Scan(&eventSequence, &eventState, &eventPhase)).To(Succeed())
		Expect(eventSequence).To(Equal(int64(2)))
		Expect(eventState).To(Equal(string(broker.ExecutionErrored)))
		Expect(eventPhase).To(Equal("broker_lost"))

		Expect(locked.Rollback()).To(Succeed())
		reconciled, err := factory.ReconcileExpiredLeases(context.Background(), 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciled).To(HaveLen(1))
		Expect(reconciled[0].ID).To(Equal(first.ID))
	})
})
