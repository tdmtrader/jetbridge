package db_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The canonical outcome factory satisfies exactly the v3 store and authorizer
// contracts; the legacy resolver and disposition-selection interfaces are gone.
var (
	_ workflowoutcomes.Store      = db.NewAgentWorkflowOutcomesFactory(nil)
	_ workflowoutcomes.Authorizer = db.NewAgentWorkflowOutcomesFactory(nil)
)

type workflowOutcomeFixture struct {
	runID                 snapshot.WorkflowRunID
	firstOutput           snapshot.SnapshotID
	secondOutput          snapshot.SnapshotID
	modification          snapshot.SnapshotID
	wrongTypeModification snapshot.SnapshotID
	unrelatedModification snapshot.SnapshotID
	otherTeamModification snapshot.SnapshotID
	unboundOutput         snapshot.SnapshotID
}

func insertWorkflowOutcomeFixture(suffix string) workflowOutcomeFixture {
	definitionName := fmt.Sprintf("workflow-outcome-%s-%d", suffix, time.Now().UnixNano())
	definitionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(definitionName+"-definition")))
	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(definitionName+"-config")))
	var definitionID int
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'fixture', 3, 1)
		RETURNING id
	`, definitionName, definitionHash).Scan(&definitionID)).To(Succeed())

	var fixture workflowOutcomeFixture
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
			 schema_version, signature_version, definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
			 created_by, status)
		VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7, 'manual', '', 'fixture', 'admitting')
		RETURNING id
	`, defaultTeam.ID(), defaultTeam.Name(), definitionID, definitionName, definitionHash,
		definitionName+"-run", configHash).Scan(&fixture.runID)).To(Succeed())

	insertSnapshot := func(label, typeName string) snapshot.SnapshotID {
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(definitionName+"-"+label)))
		var id snapshot.SnapshotID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 1, $2, 1, 1, 'filesystem-tree-v1')
			RETURNING id
		`, typeName, digest).Scan(&id)).To(Succeed())
		return id
	}
	fixture.firstOutput = insertSnapshot("first", "review")
	fixture.secondOutput = insertSnapshot("second", "measurements")
	fixture.modification = insertSnapshot("modification", "review")
	fixture.wrongTypeModification = insertSnapshot("wrong-type-modification", "repository-change")
	fixture.unrelatedModification = insertSnapshot("unrelated-modification", "review")
	fixture.otherTeamModification = insertSnapshot("other-team-modification", "review")
	fixture.unboundOutput = insertSnapshot("unbound", "review")
	_, err := dbConn.Exec(`
		INSERT INTO agent_workflow_run_snapshots
			(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
		VALUES ($1, 'output', 'review', $2, now()), ($1, 'output', 'measurements', $3, now())
	`, int64(fixture.runID), int64(fixture.firstOutput), int64(fixture.secondOutput))
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`
		INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
		VALUES
			($1, $5, 'fixture', 'human modification'),
			($2, $5, 'fixture', 'wrong type modification'),
			($3, $5, 'fixture', 'unrelated modification'),
			($4, $5, 'fixture', 'other team modification')
	`, int64(fixture.modification), int64(fixture.wrongTypeModification),
		int64(fixture.unrelatedModification), int64(fixture.otherTeamModification), defaultTeam.ID())
	Expect(err).NotTo(HaveOccurred())

	insertProduction := func(output snapshot.SnapshotID, teamID int, teamName, label string, input *snapshot.SnapshotID) {
		var productionID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
				 step_kind, step_name, output_port, workflow_definition_id, workflow_run_id,
				 occurrence_kind)
			VALUES ($1, $2, $3, $4, 'fixture', $5, '1', 'task', 'human-modify', 'result', $6, $7,
			        'build')
			RETURNING id
		`, int64(output), time.Now().UnixNano(), teamID, teamName, "modify-"+label,
			definitionID, int64(fixture.runID)).Scan(&productionID)).To(Succeed())
		if input != nil {
			_, err := dbConn.Exec(`
				INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
				VALUES ($1, 0, 'original', $2)
			`, productionID, int64(*input))
			Expect(err).NotTo(HaveOccurred())
		}
	}
	insertProduction(fixture.modification, defaultTeam.ID(), defaultTeam.Name(), "valid", &fixture.firstOutput)
	insertProduction(fixture.wrongTypeModification, defaultTeam.ID(), defaultTeam.Name(), "wrong-type", &fixture.firstOutput)
	insertProduction(fixture.unrelatedModification, defaultTeam.ID(), defaultTeam.Name(), "unrelated", &fixture.secondOutput)
	insertProduction(fixture.otherTeamModification, defaultTeam.ID()+1000, "other-team", "other-team", &fixture.firstOutput)
	return fixture
}

var _ = Describe("AgentWorkflowOutcomesFactory", func() {
	var factory db.AgentWorkflowOutcomesFactory
	var fixture workflowOutcomeFixture

	BeforeEach(func() {
		factory = db.NewAgentWorkflowOutcomesFactory(dbConn)
		fixture = insertWorkflowOutcomeFixture("factory")
	})

	request := func(output snapshot.SnapshotID) workflowoutcomes.RecordRequest {
		return workflowoutcomes.RecordRequest{
			WorkflowRunID: fixture.runID, OutputSnapshotID: output,
			Disposition:      workflowoutcomes.DispositionAccepted,
			PublicationState: workflowoutcomes.PublicationNotRequested,
			Labels:           []string{"quality", "dogfood"}, Actor: "watcher",
		}
	}

	It("records each exact output, replays idempotently, and revisions semantic updates", func() {
		first, created, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(first.Revision).To(Equal(int64(1)))
		Expect(first.Labels).To(Equal([]string{"dogfood", "quality"}))

		replayed, created, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replayed).To(Equal(first))

		second, created, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.secondOutput))
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(second.OutputSnapshotID).To(Equal(fixture.secondOutput))

		updatedRequest := request(fixture.firstOutput)
		updatedRequest.Disposition = workflowoutcomes.DispositionMerged
		updatedRequest.HumanModified = true
		updatedRequest.ModificationSnapshotID = &fixture.modification
		updatedRequest.InterventionCount = 1
		updatedRequest.Actor = "alice"
		updated, created, err := factory.Record(context.Background(), defaultTeam.ID(), updatedRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(updated.Revision).To(Equal(int64(2)))
		Expect(updated.AuditedAt).To(BeTemporally(">=", first.AuditedAt))

		listed, err := factory.ListByRun(context.Background(), defaultTeam.ID(), fixture.runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(2))
		Expect(listed[0].OutputSnapshotID).To(BeNumerically("<", listed[1].OutputSnapshotID))
		stored, found, err := factory.Get(context.Background(), defaultTeam.ID(), fixture.runID, fixture.firstOutput)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored).To(Equal(updated))
	})

	It("conceals other teams, unbound outputs, and incompatible modification snapshots", func() {
		_, _, err := factory.Record(context.Background(), defaultTeam.ID()+1000, request(fixture.firstOutput))
		Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))
		_, _, err = factory.Record(context.Background(), defaultTeam.ID(), request(fixture.unboundOutput))
		Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))

		ungranted := request(fixture.firstOutput)
		ungranted.HumanModified = true
		ungranted.ModificationSnapshotID = &fixture.unboundOutput
		_, _, err = factory.Record(context.Background(), defaultTeam.ID(), ungranted)
		Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))

		for _, incompatible := range []snapshot.SnapshotID{
			fixture.wrongTypeModification,
			fixture.unrelatedModification,
			fixture.otherTeamModification,
			fixture.firstOutput,
		} {
			update := request(fixture.firstOutput)
			update.HumanModified = true
			update.ModificationSnapshotID = &incompatible
			_, _, err = factory.Record(context.Background(), defaultTeam.ID(), update)
			Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))
		}

		_, found, err := factory.Get(context.Background(), defaultTeam.ID()+1000, fixture.runID, fixture.firstOutput)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("conceals staged outputs until successful run finalization promotes them", func() {
		_, _, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factory.Record(context.Background(), defaultTeam.ID(), request(fixture.secondOutput))
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_run_snapshots
			SET promoted_at = NULL
			WHERE workflow_run_id = $1 AND direction = 'output' AND snapshot_id = $2
		`, int64(fixture.runID), int64(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())

		authorized, err := factory.AuthorizeOutput(
			context.Background(), defaultTeam.ID(), fixture.runID, fixture.firstOutput,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(authorized).To(BeFalse())
		_, found, err := factory.Get(
			context.Background(), defaultTeam.ID(), fixture.runID, fixture.firstOutput,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		listed, err := factory.ListByRun(context.Background(), defaultTeam.ID(), fixture.runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].OutputSnapshotID).To(Equal(fixture.secondOutput))
	})

	It("serializes concurrent first writes into one durable row", func() {
		const writers = 12
		var wait sync.WaitGroup
		wait.Add(writers)
		created := make(chan bool, writers)
		errs := make(chan error, writers)
		for range writers {
			go func() {
				defer GinkgoRecover()
				defer wait.Done()
				_, wasCreated, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.firstOutput))
				created <- wasCreated
				errs <- err
			}()
		}
		wait.Wait()
		close(created)
		close(errs)
		createdCount := 0
		for value := range created {
			if value {
				createdCount++
			}
		}
		Expect(createdCount).To(Equal(1))
		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("serializes concurrent member modifications without losing intervention increments", func() {
		_, _, err := factory.Record(context.Background(), defaultTeam.ID(), request(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())

		modifications := []workflowoutcomes.ModifyRequest{
			{
				WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
				Disposition: workflowoutcomes.DispositionRejected,
				Labels:      []string{"first"}, Actor: "alice",
			},
			{
				WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
				Disposition: workflowoutcomes.DispositionAbandoned,
				Labels:      []string{"second"}, Actor: "bob",
			},
		}
		var wait sync.WaitGroup
		wait.Add(len(modifications))
		errs := make(chan error, len(modifications))
		for _, modification := range modifications {
			modification := modification
			go func() {
				defer GinkgoRecover()
				defer wait.Done()
				_, _, err := factory.Modify(context.Background(), defaultTeam.ID(), modification)
				errs <- err
			}()
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}

		stored, found, err := factory.Get(context.Background(), defaultTeam.ID(), fixture.runID, fixture.firstOutput)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.InterventionCount).To(Equal(2))
		Expect(stored.Revision).To(Equal(int64(3)))
	})

	It("rejects canceled and structurally invalid records before persistence", func() {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := factory.Record(canceled, defaultTeam.ID(), request(fixture.firstOutput))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		invalid := request(fixture.firstOutput)
		invalid.Actor = " "
		_, _, err = factory.Record(context.Background(), defaultTeam.ID(), invalid)
		Expect(errors.Is(err, workflowoutcomes.ErrInvalidOutcome)).To(BeTrue())

		forgedPublicationID := snapshot.DatabaseID(41)
		forged := request(fixture.firstOutput)
		forged.PublicationState = workflowoutcomes.PublicationPublished
		forged.PublicationID = &forgedPublicationID
		_, _, err = factory.Record(context.Background(), defaultTeam.ID(), forged)
		Expect(errors.Is(err, workflowoutcomes.ErrInvalidOutcome)).To(BeTrue())
	})

	It("authorizes only same-type same-team descendants of the exact output", func() {
		authorized, err := factory.AuthorizeRun(context.Background(), defaultTeam.ID(), fixtureWorkflowName(fixture.runID), fixture.runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(authorized).To(BeTrue())
		authorized, err = factory.AuthorizeRun(context.Background(), defaultTeam.ID(), "other-workflow", fixture.runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(authorized).To(BeFalse())
		authorized, err = factory.AuthorizeOutput(context.Background(), defaultTeam.ID(), fixture.runID, fixture.firstOutput)
		Expect(err).NotTo(HaveOccurred())
		Expect(authorized).To(BeTrue())
		authorized, err = factory.AuthorizeOutput(context.Background(), defaultTeam.ID(), fixture.runID, fixture.unboundOutput)
		Expect(err).NotTo(HaveOccurred())
		Expect(authorized).To(BeFalse())
		_, _, err = factory.Modify(context.Background(), defaultTeam.ID(), workflowoutcomes.ModifyRequest{
			WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
			Disposition: workflowoutcomes.DispositionAccepted, HumanModified: true,
			ModificationSnapshotID: &fixture.modification, Actor: "alice",
		})
		Expect(err).NotTo(HaveOccurred())
		for _, incompatible := range []snapshot.SnapshotID{
			fixture.wrongTypeModification,
			fixture.unrelatedModification,
			fixture.otherTeamModification,
			fixture.firstOutput,
			fixture.unboundOutput,
		} {
			_, _, err = factory.Modify(context.Background(), defaultTeam.ID(), workflowoutcomes.ModifyRequest{
				WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
				Disposition: workflowoutcomes.DispositionAccepted, HumanModified: true,
				ModificationSnapshotID: &incompatible, Actor: "alice",
			})
			Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))
		}
	})

	It("fails closed when modification lineage exceeds the hard depth bound", func() {
		previous := fixture.firstOutput
		var deepest snapshot.SnapshotID
		for depth := 0; depth < 65; depth++ {
			label := fmt.Sprintf("deep-lineage-%d-%d", depth, time.Now().UnixNano())
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(label)))
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1')
				RETURNING id
			`, digest).Scan(&deepest)).To(Succeed())
			_, err := dbConn.Exec(`
				INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
				VALUES ($1, $2, 'fixture', 'depth bound test')
			`, int64(deepest), defaultTeam.ID())
			Expect(err).NotTo(HaveOccurred())
			var productionID int64
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshot_productions
					(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
					 step_kind, step_name, output_port, workflow_run_id, occurrence_kind)
				VALUES ($1, $2, $3, $4, 'fixture', $5, '1', 'task', 'human-modify',
				        'result', $6, 'build')
				RETURNING id
			`, int64(deepest), time.Now().UnixNano(), defaultTeam.ID(), defaultTeam.Name(),
				fmt.Sprintf("deep-%d", depth), int64(fixture.runID)).Scan(&productionID)).To(Succeed())
			_, err = dbConn.Exec(`
				INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
				VALUES ($1, 0, 'previous', $2)
			`, productionID, int64(previous))
			Expect(err).NotTo(HaveOccurred())
			previous = deepest
		}

		_, _, err := factory.Modify(context.Background(), defaultTeam.ID(), workflowoutcomes.ModifyRequest{
			WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
			Disposition: workflowoutcomes.DispositionAccepted, HumanModified: true,
			ModificationSnapshotID: &deepest, Actor: "alice",
		})
		Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))
	})

	It("fails closed when modification lineage exceeds the hard node bound", func() {
		label := fmt.Sprintf("wide-lineage-%d", time.Now().UnixNano())
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(label)))
		var modification snapshot.SnapshotID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1')
			RETURNING id
		`, digest).Scan(&modification)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'fixture', 'node bound test')
		`, int64(modification), defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		var productionID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, build_id, team_id, team_name, created_by, plan_id, attempt,
				 step_kind, step_name, output_port, workflow_run_id, occurrence_kind)
			VALUES ($1, $2, $3, $4, 'fixture', 'wide-lineage', '1', 'task',
			        'human-modify', 'result', $5, 'build')
			RETURNING id
		`, int64(modification), time.Now().UnixNano(), defaultTeam.ID(), defaultTeam.Name(),
			int64(fixture.runID)).Scan(&productionID)).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
			VALUES ($1, 0, 'original', $2)
		`, productionID, int64(fixture.firstOutput))
		Expect(err).NotTo(HaveOccurred())
		for position := 1; position <= 1024; position++ {
			fillerDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fmt.Sprintf("%s-%d", label, position))))
			var filler snapshot.SnapshotID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1')
				RETURNING id
			`, fillerDigest).Scan(&filler)).To(Succeed())
			_, err = dbConn.Exec(`
				INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
				VALUES ($1, $2, $3, $4)
			`, productionID, position, fmt.Sprintf("input-%d", position), int64(filler))
			Expect(err).NotTo(HaveOccurred())
		}

		_, _, err = factory.Modify(context.Background(), defaultTeam.ID(), workflowoutcomes.ModifyRequest{
			WorkflowRunID: fixture.runID, OutputSnapshotID: fixture.firstOutput,
			Disposition: workflowoutcomes.DispositionAccepted, HumanModified: true,
			ModificationSnapshotID: &modification, Actor: "alice",
		})
		Expect(err).To(MatchError(workflowoutcomes.ErrOutcomeNotFound))
	})
})

func fixtureWorkflowName(runID snapshot.WorkflowRunID) string {
	var name string
	Expect(dbConn.QueryRow(`SELECT workflow_name FROM agent_workflow_runs WHERE id = $1`, int64(runID)).Scan(&name)).To(Succeed())
	return name
}
