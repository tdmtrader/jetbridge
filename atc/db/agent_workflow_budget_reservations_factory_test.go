package db_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent workflow budget reservations", func() {
	var now time.Time
	var factory db.AgentWorkflowBudgetReservationsFactory

	BeforeEach(func() {
		now = time.Now().UTC()
		factory = db.NewAgentWorkflowBudgetReservationsFactory(dbConn, db.AgentWorkflowBudgetConfig{
			GlobalDailyCapUSD: 2,
			Location:          time.UTC,
			Now:               func() time.Time { return now },
		})
	})

	It("is idempotent, enforces shared liability, and retains terminal slack for delayed ledger ingestion", func() {
		first := insertAdmittingBudgetWorkflowRun("first")
		second := insertAdmittingBudgetWorkflowRun("second")

		reserved, err := factory.ReserveWorkflowBudget(context.Background(), first, 1.25)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		reserved, err = factory.ReserveWorkflowBudget(context.Background(), first, 1.25)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
		_, err = factory.ReserveWorkflowBudget(context.Background(), first, 1.5)
		Expect(err).To(MatchError(ContainSubstring("conflicts with durable amount")))

		reserved, err = factory.ReserveWorkflowBudget(context.Background(), second, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeFalse())

		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		transitioned, err := runs.Transition(
			context.Background(),
			first,
			db.AgentWorkflowRunStatusAdmitting,
			db.AgentWorkflowRunStatusErrored,
			"test admission failure",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		reserved, err = factory.ReserveWorkflowBudget(context.Background(), second, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeFalse(), "a just-completed reservation remains liable until ledger ingestion is safe")

		now = now.Add(24 * time.Hour)
		reserved, err = factory.ReserveWorkflowBudget(context.Background(), second, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reserved).To(BeTrue())
	})

	It("serializes concurrent workflow admissions across web nodes", func() {
		first := insertAdmittingBudgetWorkflowRun("concurrent-first")
		second := insertAdmittingBudgetWorkflowRun("concurrent-second")
		factories := []db.AgentWorkflowBudgetReservationsFactory{
			db.NewAgentWorkflowBudgetReservationsFactory(dbConn, db.AgentWorkflowBudgetConfig{
				GlobalDailyCapUSD: 1, Location: time.UTC, Now: func() time.Time { return now },
			}),
			db.NewAgentWorkflowBudgetReservationsFactory(dbConn, db.AgentWorkflowBudgetConfig{
				GlobalDailyCapUSD: 1, Location: time.UTC, Now: func() time.Time { return now },
			}),
		}
		ids := []snapshot.WorkflowRunID{first, second}
		results := make(chan bool, 2)
		errors := make(chan error, 2)
		var wait sync.WaitGroup
		for index := range ids {
			wait.Add(1)
			go func(index int) {
				defer GinkgoRecover()
				defer wait.Done()
				accepted, err := factories[index].ReserveWorkflowBudget(context.Background(), ids[index], 0.75)
				results <- accepted
				errors <- err
			}(index)
		}
		wait.Wait()
		close(results)
		close(errors)
		for err := range errors {
			Expect(err).NotTo(HaveOccurred())
		}
		accepted := 0
		for result := range results {
			if result {
				accepted++
			}
		}
		Expect(accepted).To(Equal(1))
	})
})

func insertAdmittingBudgetWorkflowRun(label string) snapshot.WorkflowRunID {
	suffix := time.Now().UnixNano()
	name := fmt.Sprintf("budget-%s-%d", label, suffix)
	var definitionID int
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'test', 3, 1)
		RETURNING id
	`, name, fmt.Sprintf("definition-%d", suffix)).Scan(&definitionID)).To(Succeed())
	var runID snapshot.WorkflowRunID
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
			 schema_version, signature_version, definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 origin_kind, origin_reference, created_by, status)
		VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6,
		        '{}', $7, 'test', $8, 'test', 'admitting')
		RETURNING id
	`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name,
		fmt.Sprintf("definition-%d", suffix), fmt.Sprintf("request-%d", suffix),
		fmt.Sprintf("target-%d", suffix), label).Scan(&runID)).To(Succeed())
	return runID
}
