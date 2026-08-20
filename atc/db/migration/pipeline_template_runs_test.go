package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const pipelineTemplateRunsVersion = 1773105507

var _ = Describe("Pipeline template run schema", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(pipelineTemplateRunsVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
		_, err := database.Exec(`INSERT INTO teams(name) VALUES ('template-runs')`)
		Expect(err).NotTo(HaveOccurred())
	})

	It("commits a running header with exactly one payload child", func() {
		var templateID, runID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())

		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'a-user', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		_, err = tx.Exec(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) SELECT id, 'base', '{"run":1}', $1, 1 FROM teams WHERE name = 'template-runs'`, runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
	})

	It("rejects a running orphan at deferred commit", func() {
		var templateID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'orphan-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())

		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'a-user', 'hash')`, templateID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(MatchError(ContainSubstring("exactly one payload pipeline")))
	})

	It("stores a nullable completion timestamp on run headers", func() {
		var templateID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'completed-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())

		var completedAt sql.NullTime
		Expect(database.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'succeeded', 'a-user', 'hash') RETURNING completed_at`, templateID).Scan(&completedAt)).To(Succeed())
		Expect(completedAt.Valid).To(BeFalse())

		Expect(database.QueryRow(`UPDATE pipeline_runs SET completed_at = now() WHERE template_pipeline_id = $1 RETURNING completed_at`, templateID).Scan(&completedAt)).To(Succeed())
		Expect(completedAt.Valid).To(BeTrue())
	})

	It("rejects a second payload child and permits deleting a terminal child", func() {
		var templateID, runID, childID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'children-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'a-user', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) SELECT id, 'children-base', '{"run":1}', $1, 1 FROM teams WHERE name = 'template-runs' RETURNING id`, runID).Scan(&childID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		_, err = database.Exec(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) SELECT id, 'children-base', '{"run":2}', $1, 2 FROM teams WHERE name = 'template-runs'`, runID)
		Expect(err).To(MatchError(ContainSubstring("pipelines_pipeline_run_id_unique")))
		_, err = database.Exec(`UPDATE pipeline_runs SET status = 'succeeded' WHERE id = $1`, runID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE id = $1`, childID)
		Expect(err).NotTo(HaveOccurred())
	})

	It("enforces immutable run ownership, policy keys, and complete build labels", func() {
		var templateID, runID, childID, jobID, buildID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'immutable-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'a-user', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) SELECT id, 'immutable-base', '{"run":1}', $1, 1 FROM teams WHERE name = 'template-runs' RETURNING id`, runID).Scan(&childID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		Expect(database.QueryRow(`INSERT INTO jobs(name, pipeline_id, config, run_expected, run_policy_key) VALUES ('job', $1, '{}', true, 'policy') RETURNING id`, childID).Scan(&jobID)).To(Succeed())
		_, err = database.Exec(`UPDATE pipelines SET pipeline_run_id = NULL WHERE id = $1`, childID)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
		_, err = database.Exec(`UPDATE pipelines SET template = false WHERE id = $1`, templateID)
		Expect(err).To(MatchError(ContainSubstring("referenced by pipeline runs")))
		_, err = database.Exec(`UPDATE jobs SET run_policy_key = 'other' WHERE id = $1`, jobID)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
		_, err = database.Exec(`INSERT INTO jobs(name, pipeline_id, config, run_expected) VALUES ('ordinary-job', $1, '{}', true)`, templateID)
		Expect(err).To(MatchError(ContainSubstring("payload pipeline")))
		Expect(database.QueryRow(`INSERT INTO builds(name, status, team_id, pipeline_run_id, run_job_name, run_job_key) SELECT '1', 'pending', id, $1, 'job', 'policy' FROM teams WHERE name = 'template-runs' RETURNING id`, runID).Scan(&buildID)).To(Succeed())
		_, err = database.Exec(`UPDATE builds SET run_job_key = 'changed' WHERE id = $1`, buildID)
		Expect(err).To(MatchError(ContainSubstring("immutable")))
		_, err = database.Exec(`INSERT INTO builds(name, status, team_id, pipeline_run_id) SELECT '2', 'pending', id, $1 FROM teams WHERE name = 'template-runs'`, runID)
		Expect(err).To(MatchError(ContainSubstring("builds_pipeline_run_identity_complete")))
	})

	It("refuses a populated down migration and permits an empty down migration", func() {
		var templateID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'down-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())
		Expect(database.Close()).To(Succeed())
		_, err := postgresRunner.TryOpenDBAtVersion(1773105504)
		Expect(err).To(MatchError(ContainSubstring("cannot roll back pipeline template runs")))

		database = postgresRunner.OpenDBAtVersion(pipelineTemplateRunsVersion)
		_, err = database.Exec(`DELETE FROM pipelines WHERE id = $1`, templateID)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(1773105504)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses a down migration when future template-owned task caches remain", func() {
		var pipelineID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, secondary_ordering) SELECT id, 'future-cache-owner', 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&pipelineID)).To(Succeed())
		_, err := database.Exec(`INSERT INTO task_caches(step_name, path, template_pipeline_id, run_job_name) VALUES ('run-cache', '/cache', $1, 'run-job')`, pipelineID)
		Expect(err).NotTo(HaveOccurred())

		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(1773105504)
		Expect(err).To(MatchError(ContainSubstring("cannot roll back pipeline template runs")))

		database = postgresRunner.OpenDBAtVersion(pipelineTemplateRunsVersion)
	})

	It("refuses a down migration when future run job task caches remain", func() {
		var pipelineID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, secondary_ordering) SELECT id, 'future-run-cache-owner', 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&pipelineID)).To(Succeed())
		_, err := database.Exec(`INSERT INTO task_caches(step_name, path, template_pipeline_id, run_job_name) VALUES ('run-cache', '/cache', $1, 'run-job')`, pipelineID)
		Expect(err).NotTo(HaveOccurred())

		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(1773105504)
		Expect(err).To(MatchError(ContainSubstring("cannot roll back pipeline template runs")))

		database = postgresRunner.OpenDBAtVersion(pipelineTemplateRunsVersion)
	})

	It("persists exactly one ordinary or shared run task cache identity", func() {
		// This fails if task-cache rows can be ambiguous, or a shared run cache
		// cannot survive independently of an ephemeral payload job.
		var templateID, ordinaryPipelineID, ordinaryJobID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, secondary_ordering) SELECT id, 'ordinary-cache', 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&ordinaryPipelineID)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO jobs(name, pipeline_id, config) VALUES ('ordinary', $1, '{}') RETURNING id`, ordinaryPipelineID).Scan(&ordinaryJobID)).To(Succeed())
		_, err := database.Exec(`INSERT INTO task_caches(job_id, step_name, path) VALUES ($1, 'ordinary', '/cache')`, ordinaryJobID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO task_caches(job_id, step_name, path) VALUES ($1, 'ordinary', '/cache')`, ordinaryJobID)
		Expect(err).To(MatchError(ContainSubstring("task_caches_job_id_step_name_path_uniq")))
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'cache-base', true, 1 FROM teams WHERE name = 'template-runs' RETURNING id`).Scan(&templateID)).To(Succeed())
		_, err = database.Exec(`INSERT INTO task_caches(job_id, step_name, path) VALUES (NULL, 'ordinary', '/cache')`)
		Expect(err).To(MatchError(ContainSubstring("task_caches_identity_complete")))
		_, err = database.Exec(`INSERT INTO task_caches(template_pipeline_id, run_job_name, step_name, path) VALUES ($1, 'deploy', 'task', '/cache')`, templateID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO task_caches(template_pipeline_id, run_job_name, step_name, path) VALUES ($1, 'deploy', 'task', '/cache')`, templateID)
		Expect(err).To(MatchError(ContainSubstring("task_caches_run_identity_unique")))
	})
})
