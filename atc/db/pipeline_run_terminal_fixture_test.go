package db_test

import (
	sq "github.com/Masterminds/squirrel"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// forgeTerminalRun writes a terminal header with an empty result manifest for
// a spec about what a settled Run refuses, where the spec needs the Run
// settled while one of its builds is still pending -- a state terminal
// publication itself never produces. It suspends only the deferred check that
// publication and build work commit together, for this one statement.
func forgeTerminalRun(runID int, status string) {
	GinkgoHelper()
	_, err := dbConn.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER run_terminal_result_match`)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		_, err := dbConn.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER run_terminal_result_match`)
		Expect(err).NotTo(HaveOccurred())
	}()
	_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = $2, completed_at = now(), result_manifest = '{}' WHERE id = $1`, runID, status)
	Expect(err).NotTo(HaveOccurred())
}

// settleRunBuilds finishes every build of a Run, so a spec can publish its
// terminal header the way the finalizer would find it: no work outstanding.
func settleRunBuilds(runID int) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1 AND NOT completed`, runID)
	Expect(err).NotTo(HaveOccurred())
}

// forgeRun writes a template pipeline named name, a running Run of it, and
// that Run's payload instance {"run":1}, for a spec about how pipeline-scoped
// objects hydrate run identity. The Run and its payload commit together, as
// admission writes them. A non-empty templateCacheScope opts the template into
// a task cache scope.
func forgeRun(name string, templateCacheScope string) (templateID, runID, payloadID int) {
	GinkgoHelper()
	templateID = insertRunTemplate(name, templateCacheScope)

	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	runID = insertPipelineRun(tx, templateID, "running")
	payloadID = insertRunPayload(tx, name, runID)
	Expect(tx.Commit()).To(Succeed())
	return templateID, runID, payloadID
}

// insertRunTemplate is the one place these specs write a template pipeline
// row by hand.
func insertRunTemplate(name string, cacheScope string) int {
	GinkgoHelper()
	var id int
	if cacheScope == "" {
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, $2, true, 1) RETURNING id`, defaultTeam.ID(), name).Scan(&id)).To(Succeed())
	} else {
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, cache_scope, secondary_ordering) VALUES ($1, $2, true, $3, 1) RETURNING id`, defaultTeam.ID(), name, cacheScope).Scan(&id)).To(Succeed())
	}
	return id
}

// insertPipelineRun is the one place these specs write a pipeline_runs row by
// hand: run number 1 of templateID. A "running" Run has no completion; any
// other status is written terminal with an empty result manifest.
func insertPipelineRun(q sq.QueryRower, templateID int, status string) int {
	GinkgoHelper()
	var id int
	if status == "running" {
		Expect(q.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id`, templateID).Scan(&id)).To(Succeed())
	} else {
		Expect(q.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash, completed_at, result_manifest) VALUES ($1, 1, '{}', $2, 'creator', 'hash', now(), '{}') RETURNING id`, templateID, status).Scan(&id)).To(Succeed())
	}
	return id
}

// insertRunPayload is the one place these specs write a Run's payload
// pipeline row by hand: instance {"run":1} of name, owned by runID.
func insertRunPayload(q sq.QueryRower, name string, runID int) int {
	GinkgoHelper()
	var id int
	Expect(q.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, $2, '{"run":1}', $3, 1) RETURNING id`, defaultTeam.ID(), name, runID).Scan(&id)).To(Succeed())
	return id
}
