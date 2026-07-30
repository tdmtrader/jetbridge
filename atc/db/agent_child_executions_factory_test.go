package db_test

import (
	"context"
	"fmt"
	"strings"
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
})
