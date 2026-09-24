package db_test

import (
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
	_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = $2, completed_at = now(), result_manifest = '{}', terminal_observation_version = 'fixture' WHERE id = $1`, runID, status)
	Expect(err).NotTo(HaveOccurred())
}

// settleRunBuilds finishes every build of a Run, so a spec can publish its
// terminal header the way the finalizer would find it: no work outstanding.
func settleRunBuilds(runID int) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1 AND NOT completed`, runID)
	Expect(err).NotTo(HaveOccurred())
}
