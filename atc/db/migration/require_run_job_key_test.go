package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("requiring a run job key", func() {
	const preMigrationVersion = 1789607907
	const postMigrationVersion = 1789792877

	var database *sql.DB
	var runID, payloadID int

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(preMigrationVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		var teamID, templateID int
		Expect(database.QueryRow(`INSERT INTO teams(name, auth) VALUES ('key-team', '{}') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, 'key-base', true, 1) RETURNING id
		`, teamID).Scan(&templateID)).To(Succeed())
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`
			INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash)
			VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id
		`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`
			INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering)
			VALUES ($1, 'key-base', '{"run":1}', $2, 1) RETURNING id
		`, teamID, runID).Scan(&payloadID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
	})

	insertJob := func(name string, key sql.NullString) int {
		GinkgoHelper()
		var jobID int
		Expect(database.QueryRow(`
			INSERT INTO jobs(pipeline_id, name, config, run_policy_key) VALUES ($1, $2, '', $3) RETURNING id
		`, payloadID, name, key).Scan(&jobID)).To(Succeed())
		return jobID
	}

	insertBuild := func(jobID int, name, key string) int {
		GinkgoHelper()
		var buildID int
		Expect(database.QueryRow(`
			INSERT INTO builds(name, status, team_id, job_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key)
			SELECT '1', 'succeeded', team_id, $1, id, $2, $3, $4 FROM pipelines WHERE id = $5
			RETURNING id
		`, jobID, runID, name, key, payloadID).Scan(&buildID)).To(Succeed())
		return buildID
	}

	// Before this migration an uninterpolated job had no key and its builds
	// carried the empty string, which is what ChronoRunBuilds is asked for
	// after the payload is gone. The backfill has to give both the source name
	// and leave a renamed job's placeholder key alone.
	It("backfills missing keys from the job name and refuses empty ones afterwards", func() {
		plainJob := insertJob("entry", sql.NullString{})
		renamedJob := insertJob("deploy-staging", sql.NullString{String: "deploy-((environment))", Valid: true})
		plainBuild := insertBuild(plainJob, "entry", "")
		renamedBuild := insertBuild(renamedJob, "deploy-staging", "deploy-((environment))")

		postgresRunner.MigrateToVersion(postMigrationVersion)

		var key string
		Expect(database.QueryRow(`SELECT run_job_key FROM jobs WHERE id = $1`, plainJob).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("entry"))
		Expect(database.QueryRow(`SELECT run_job_key FROM jobs WHERE id = $1`, renamedJob).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("deploy-((environment))"))
		Expect(database.QueryRow(`SELECT run_job_key FROM builds WHERE id = $1`, plainBuild).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("entry"))
		Expect(database.QueryRow(`SELECT run_job_key FROM builds WHERE id = $1`, renamedBuild).Scan(&key)).To(Succeed())
		Expect(key).To(Equal("deploy-((environment))"))

		var columns int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'run_policy_key'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(0), "run_policy_key outlived the rename")

		_, err := database.Exec(`
			INSERT INTO builds(name, status, team_id, job_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key)
			SELECT '2', 'succeeded', team_id, $1, id, $2, 'entry', '' FROM pipelines WHERE id = $3
		`, plainJob, runID, payloadID)
		Expect(err).To(MatchError(ContainSubstring("builds_pipeline_run_identity_complete")))

		_, err = database.Exec(`INSERT INTO jobs(pipeline_id, name, config, run_job_key) VALUES ($1, 'blank', '', '')`, payloadID)
		Expect(err).To(MatchError(ContainSubstring("jobs_run_job_key_nonempty")))

		// Both triggers are lifted only for the backfill.
		_, err = database.Exec(`UPDATE builds SET run_job_key = 'other' WHERE id = $1`, plainBuild)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
		_, err = database.Exec(`UPDATE jobs SET run_job_key = 'other' WHERE id = $1`, plainJob)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
	})

	It("migrates back down", func() {
		insertJob("entry", sql.NullString{})
		postgresRunner.MigrateToVersion(postMigrationVersion)
		Expect(database.Close()).To(Succeed())

		database = postgresRunner.OpenDBAtVersion(preMigrationVersion)
		var key sql.NullString
		Expect(database.QueryRow(`SELECT run_policy_key FROM jobs WHERE pipeline_id = $1`, payloadID).Scan(&key)).To(Succeed())
		Expect(key.String).To(Equal("entry"))

		// The restored function names the old column again and the trigger
		// still guards it.
		_, err := database.Exec(`UPDATE jobs SET run_policy_key = 'other' WHERE pipeline_id = $1`, payloadID)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
	})
})
