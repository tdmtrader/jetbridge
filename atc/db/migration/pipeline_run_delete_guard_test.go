package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const pipelineRunDeleteGuardVersion = 1773105509

var _ = Describe("payload delete guard", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(pipelineRunDeleteGuardVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
		_, err := database.Exec(`INSERT INTO teams(name) VALUES ('guard-team')`)
		Expect(err).NotTo(HaveOccurred())
	})

	createRun := func(status string) (int, int, int) {
		GinkgoHelper()
		var templateID, runID, payloadID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines(team_id, name, template, secondary_ordering)
			SELECT id, 'guard-base', true, 1 FROM teams WHERE name = 'guard-team'
			RETURNING id
		`).Scan(&templateID)).To(Succeed())
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		completed := "NULL"
		if status != "running" {
			completed = "now()"
		}
		Expect(tx.QueryRow(`
			INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, completed_at, config_hash)
			VALUES ($1, 1, '{}', $2, 'creator', `+completed+`, 'hash') RETURNING id
		`, templateID, status).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`
			INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering)
			SELECT id, 'guard-base', '{"run":1}', $1, 1 FROM teams WHERE name = 'guard-team'
			RETURNING id
		`, runID).Scan(&payloadID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
		return templateID, runID, payloadID
	}

	createAttachedBuild := func(runID, payloadID int) int {
		GinkgoHelper()
		var jobID, buildID int
		Expect(database.QueryRow(`INSERT INTO jobs(pipeline_id, name, config, run_policy_key) VALUES ($1, 'entry', '', 'entry') RETURNING id`, payloadID).Scan(&jobID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds(name, status, team_id, job_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key)
			SELECT '1', 'succeeded', team_id, $1, id, $2, 'entry', 'entry' FROM pipelines WHERE id = $3
			RETURNING id
		`, jobID, runID, payloadID).Scan(&buildID)).To(Succeed())
		return buildID
	}

	It("rejects payload deletion until terminal retained builds are detached", func() {
		_, runID, payloadID := createRun("succeeded")
		buildID := createAttachedBuild(runID, payloadID)

		_, err := database.Exec(`DELETE FROM pipelines WHERE id = $1`, payloadID)
		Expect(err).To(MatchError(ContainSubstring("run payload cannot be deleted")))

		_, err = database.Exec(`UPDATE builds SET job_id = NULL, pipeline_id = NULL WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE id = $1`, payloadID)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects deletion of a running payload even without retained builds", func() {
		_, _, payloadID := createRun("running")
		_, err := database.Exec(`DELETE FROM pipelines WHERE id = $1`, payloadID)
		Expect(err).To(MatchError(ContainSubstring("run payload cannot be deleted")))
	})

	It("permits ordinary pipeline deletion and a transaction-local team purge bypass", func() {
		var ordinaryID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines(team_id, name, secondary_ordering)
			SELECT id, 'ordinary', 1 FROM teams WHERE name = 'guard-team' RETURNING id
		`).Scan(&ordinaryID)).To(Succeed())
		_, err := database.Exec(`DELETE FROM pipelines WHERE id = $1`, ordinaryID)
		Expect(err).NotTo(HaveOccurred())

		_, runID, payloadID := createRun("succeeded")
		createAttachedBuild(runID, payloadID)
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`SELECT set_config('concourse.pipeline_run_team_purge', 'on', true)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM pipelines WHERE id = $1`, payloadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM pipeline_runs WHERE id = $1`, runID)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM pipelines WHERE name = 'guard-base' AND template`)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		_, secondRunID, secondPayloadID := createRun("succeeded")
		createAttachedBuild(secondRunID, secondPayloadID)
		_, err = database.Exec(`DELETE FROM pipelines WHERE id = $1`, secondPayloadID)
		Expect(err).To(MatchError(ContainSubstring("run payload cannot be deleted")), "SET LOCAL bypass must not leak to the pooled session")
	})

	It("adds both independently useful terminal candidate indexes", func() {
		var numberIndex, ageIndex string
		Expect(database.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'pipeline_runs_terminal_number_idx'`).Scan(&numberIndex)).To(Succeed())
		Expect(database.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'pipeline_runs_terminal_completed_at_idx'`).Scan(&ageIndex)).To(Succeed())
		Expect(numberIndex).To(And(ContainSubstring("(template_pipeline_id, number)"), ContainSubstring("WHERE (status = ANY")))
		Expect(ageIndex).To(And(ContainSubstring("(template_pipeline_id, completed_at)"), ContainSubstring("WHERE ((status = ANY"), ContainSubstring("completed_at IS NOT NULL")))
	})

	It("refuses a populated down migration and permits an empty down migration", func() {
		createRun("running")
		Expect(database.Close()).To(Succeed())
		_, err := postgresRunner.TryOpenDBAtVersion(1773105508)
		Expect(err).To(MatchError(ContainSubstring("cannot roll back pipeline template runs")))

		database = postgresRunner.OpenDBAtVersion(pipelineRunDeleteGuardVersion)
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`SELECT set_config('concourse.pipeline_run_team_purge', 'on', true)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM builds WHERE pipeline_run_id IS NOT NULL`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM pipelines WHERE pipeline_run_id IS NOT NULL`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM pipeline_runs`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`DELETE FROM teams WHERE name = 'guard-team'`)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(1773105508)
		Expect(err).NotTo(HaveOccurred())
	})
})
