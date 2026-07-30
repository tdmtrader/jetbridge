package migration_test

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PR publication evidence migration", func() {
	const beforeVersion, targetVersion = 1773106151, 1773106152

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
		teamID   int
		defID    int
	)

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		migrator = migration.NewMigrator(database, lock.NewLockFactory(lockDB, noop, noop))
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		unique := GinkgoRandomSeed()
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("pr-evidence-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("pr-evidence-%d", unique), strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("backfills durable waits, enforces exclusive evidence, and refuses lossy downgrade", func() {
		runID, occurrenceID, _ := insertPRBindingMigrationEvidence(
			database, teamID, defID, "legacy-human-wait",
		)
		var buildID int64
		Expect(database.QueryRow(`
			SELECT planned_build_id FROM agent_workflow_runs WHERE id=$1
		`, runID).Scan(&buildID)).To(Succeed())

		var questionID, answerID, waitID int64
		questionDigest := "sha256:" + strings.Repeat("1", 64)
		answerDigest := "sha256:" + strings.Repeat("2", 64)
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'question', 1, $2, 1, 1, 'application/x-tar')
			RETURNING id
		`, teamID, questionDigest).Scan(&questionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'human-answer', 1, $2, 1, 1, 'application/x-tar')
			RETURNING id
		`, teamID, answerDigest).Scan(&answerID)).To(Succeed())
		resolvedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence,
				 plan_id, attempt, output_name, question_name, question_snapshot_id,
				 expected_type_name, expected_type_version, deadline, timeout_policy,
				 status, answer_snapshot_id, resolved_by, resolved_by_display_name,
				 resolution_source, resolved_at, resolution_intent_answer,
				 resolution_intent_actor, resolution_intent_display_name,
				 resolution_intent_at)
			VALUES ($1, $2, $3, $3, 'pr-approval', '1', 'approval',
			        'PR approval', $4, 'human-answer', 1, $5, 'fail',
			        'resolved', $6, 'alice', 'Alice', 'human', $7,
			        'approve', 'alice', 'Alice', $7)
			RETURNING id
		`, teamID, runID, buildID, questionID, resolvedAt.Add(time.Hour),
			answerID, resolvedAt).Scan(&waitID)).To(Succeed())
		_, err := database.Exec(`
			UPDATE agent_publication_occurrences
			SET approved_by='alice', approval_wait_id=$2,
			    approval_question_snapshot_id=$3,
			    approval_answer_snapshot_id=$4, approval_resolved_at=$5
			WHERE id=$1
		`, occurrenceID, waitID, questionID, answerID, resolvedAt)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var (
			kind       string
			storedTeam int
			storedWait int64
			storedBy   string
			storedAt   time.Time
		)
		Expect(database.QueryRow(`
			SELECT evidence_kind, team_id, human_wait_id, resolved_by, resolved_at
			FROM agent_publication_approval_evidence
			WHERE publication_id=$1
		`, occurrenceID).Scan(
			&kind, &storedTeam, &storedWait, &storedBy, &storedAt,
		)).To(Succeed())
		Expect(kind).To(Equal("human_wait"))
		Expect(storedTeam).To(Equal(teamID))
		Expect(storedWait).To(Equal(waitID))
		Expect(storedBy).To(Equal("alice"))
		Expect(storedAt).To(BeTemporally("==", resolvedAt))

		acceptedRunID, acceptedOccurrenceID, observationID := insertPRBindingMigrationEvidence(
			database, teamID, defID, "accepted-review",
		)
		var candidateID, reviewID, validationID int64
		for index, target := range []struct {
			typeName string
			id       *int64
		}{
			{typeName: "repository", id: &candidateID},
			{typeName: "review", id: &reviewID},
			{typeName: "validation", id: &validationID},
		} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(team_id, type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, $2, 1, $3, 1, 1, 'application/x-tar')
				RETURNING id
			`, teamID, target.typeName,
				"sha256:"+fmt.Sprintf("%064x", acceptedOccurrenceID+int64(index)+100),
			).Scan(target.id)).To(Succeed())
		}

		_, err = database.Exec(`
			INSERT INTO agent_publication_approval_evidence
				(publication_id, team_id, evidence_kind, review_snapshot_id)
			VALUES ($1, $2, 'accepted_review', $3)
		`, acceptedOccurrenceID, teamID, reviewID)
		Expect(err).To(HaveOccurred(), "partial accepted-review evidence must fail closed")

		_, err = database.Exec(`
			INSERT INTO agent_publication_approval_evidence
				(publication_id, team_id, evidence_kind,
				 review_snapshot_id, candidate_snapshot_id, validation_snapshot_id,
				 review_workflow_run_id, outcome_revision, accepted_by, accepted_at)
			VALUES ($1, $2, 'accepted_review', $3, $4, $5, $6, 3, 'alice', $7)
		`, acceptedOccurrenceID, teamID, reviewID, candidateID, validationID,
			acceptedRunID, resolvedAt)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_publication_inputs
				(publication_id, team_id, role, snapshot_id)
			VALUES ($1, $2, 'observation', $3), ($1, $2, 'validation', $4)
		`, acceptedOccurrenceID, teamID, observationID, validationID)
		Expect(err).NotTo(HaveOccurred())

		var otherTeamID int
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("pr-evidence-other-%d", GinkgoRandomSeed())).Scan(
			&otherTeamID,
		)).To(Succeed())
		var otherSnapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'validation', 1, $2, 1, 1, 'application/x-tar')
			RETURNING id
		`, otherTeamID, "sha256:"+strings.Repeat("e", 64)).Scan(
			&otherSnapshotID,
		)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_publication_inputs
				(publication_id, team_id, role, snapshot_id)
			VALUES ($1, $2, 'impact', $3)
		`, acceptedOccurrenceID, teamID, otherSnapshotID)
		Expect(err).To(HaveOccurred(), "cross-team evidence must fail at the database boundary")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).NotTo(Succeed(),
			"older binaries cannot represent accepted-review or additional-input evidence")
		_, err = database.Exec(`DELETE FROM agent_publication_inputs WHERE publication_id=$1`, acceptedOccurrenceID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			DELETE FROM agent_publication_approval_evidence WHERE publication_id=$1
		`, acceptedOccurrenceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var legacyWaitID int64
		Expect(database.QueryRow(`
			SELECT approval_wait_id FROM agent_publication_occurrences WHERE id=$1
		`, occurrenceID).Scan(&legacyWaitID)).To(Succeed())
		Expect(legacyWaitID).To(Equal(waitID))
	})
})
