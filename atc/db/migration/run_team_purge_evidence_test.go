package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const runTeamPurgeEvidenceVersion = 1789793145

const (
	closedCheckExecution     = "11111111-1111-4111-8111-111111111111"
	openCheckExecution       = "22222222-2222-4222-8222-222222222222"
	closedTaskExecution      = "33333333-3333-4333-8333-333333333333"
	incompleteCheckExecution = "44444444-4444-4444-8444-444444444444"
	closedImageGetExecution  = "55555555-5555-4555-8555-555555555555"
	jobCheckExecution        = "66666666-6666-4666-8666-666666666666"
	jobGetExecution          = "77777777-7777-4777-8777-777777777777"
	outputCheckExecution     = "88888888-8888-4888-8888-888888888888"
)

var _ = Describe("Run evidence deletion allowances", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(runTeamPurgeEvidenceVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		// Isolate the delete guards from the admission tables that normally
		// establish these rows' foreign keys; the DB suite drives the real path.
		for _, table := range []string{"builds", "pipeline_run_executions", "pipeline_run_execution_starts", "pipeline_run_execution_closures", "pipeline_run_output_starts"} {
			_, err := database.Exec(`ALTER TABLE ` + table + ` DISABLE TRIGGER ALL`)
			Expect(err).NotTo(HaveOccurred())
		}
		// 9001 is a completed Run check build (as reclamation leaves it) and
		// 9003 a running one; 9002 is a completed Run job build.
		_, err := database.Exec(`INSERT INTO builds(id,name,status,completed,team_id,pipeline_run_id,run_job_name,run_job_key,resource_id) VALUES
			(9001,'check','succeeded',true,1,1,NULL,NULL,NULL),
			(9002,'task','succeeded',true,1,1,'entry','entry',NULL),
			(9003,'check','started',false,1,1,NULL,NULL,1),
			(9004,'check','succeeded',true,1,1,NULL,NULL,NULL)`)
		Expect(err).NotTo(HaveOccurred())
		for _, execution := range []struct {
			id, kind string
			build    int
			closed   bool
		}{
			{closedCheckExecution, "check", 9001, true},
			{openCheckExecution, "check", 9001, false},
			{closedTaskExecution, "task", 9002, true},
			{incompleteCheckExecution, "check", 9003, true},
			// A check whose image is fetched by a get runs that get inside
			// the check build itself.
			{closedImageGetExecution, "get", 9001, true},
			// A job build's image check and get are job evidence.
			{jobCheckExecution, "check", 9002, true},
			{jobGetExecution, "get", 9002, true},
			// 9004 carries a Run output start, which is never inert.
			{outputCheckExecution, "check", 9004, true},
		} {
			_, err = database.Exec(`INSERT INTO pipeline_run_executions
				(run_id,build_id,plan_id,kind,activation_epoch,node_name,node_uid,execution_id,execution_fence)
				VALUES (1,$1,$2,$3,1,'node','node-uid',$4,1)`, execution.build, execution.id, execution.kind, execution.id)
			Expect(err).NotTo(HaveOccurred())
			_, err = database.Exec(`INSERT INTO pipeline_run_execution_starts(execution_id,execution_fence,witness) VALUES ($1,1,'{}')`, execution.id)
			Expect(err).NotTo(HaveOccurred())
			if execution.closed {
				_, err = database.Exec(`INSERT INTO pipeline_run_execution_closures(execution_id,execution_fence,classification,observation)
					VALUES ($1,1,'authoritative_finish','{}')`, execution.id)
				Expect(err).NotTo(HaveOccurred())
			}
		}
		_, err = database.Exec(`INSERT INTO pipeline_run_output_starts(run_id,build_id,task_id,result_name,task_name,node_name,node_uid,handoff_id)
			VALUES (1,9004,gen_random_uuid(),'result','task','node','node-uid',gen_random_uuid())`)
		Expect(err).NotTo(HaveOccurred())
		for _, table := range []string{"builds", "pipeline_run_executions", "pipeline_run_execution_starts", "pipeline_run_execution_closures", "pipeline_run_output_starts"} {
			_, err := database.Exec(`ALTER TABLE ` + table + ` ENABLE TRIGGER ALL`)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	deleteUnder := func(database *sql.DB, marker, statement string, args ...any) error {
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		if marker != "" {
			_, err = tx.Exec(`SELECT set_config($1, 'on', true)`, marker)
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = tx.Exec(statement, args...)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	witnesses := func(execution string) int {
		var count int
		Expect(database.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_execution_starts WHERE execution_id=$1)
			+ (SELECT count(*) FROM pipeline_run_execution_closures WHERE execution_id=$1)`, execution).Scan(&count)).To(Succeed())
		return count
	}
	const deleteExecution = `DELETE FROM pipeline_run_executions WHERE execution_id=$1`

	It("admits evidence deletion only inside a transaction carrying the purge marker", func() {
		Expect(deleteUnder(database, "", deleteExecution, closedTaskExecution)).To(MatchError(ContainSubstring("deleted only by its team's purge")))
		Expect(deleteUnder(database, "concourse.pipeline_run_team_purge", `DELETE FROM pipeline_run_execution_starts WHERE execution_id=$1`, closedTaskExecution)).To(Succeed())
		Expect(deleteUnder(database, "", `DELETE FROM pipeline_run_execution_closures WHERE execution_id=$1`, closedTaskExecution)).
			To(MatchError(ContainSubstring("deleted only by its team's purge")), "the marker must not leak to the pooled session")
	})

	It("lets check collection delete a closed check or image get execution of a completed Run check build, with its witnesses", func() {
		for _, execution := range []string{closedCheckExecution, closedImageGetExecution} {
			Expect(deleteUnder(database, "", deleteExecution, execution)).
				To(MatchError(ContainSubstring("deleted only by its team's purge")), "outside the marker check evidence is refused")
			Expect(deleteUnder(database, "concourse.pipeline_run_check_gc", deleteExecution, execution)).To(Succeed())
			Expect(witnesses(execution)).To(BeZero())
		}
	})

	It("refuses everything else under the check collection marker", func() {
		for _, refused := range []struct{ why, statement, execution string }{
			{"task evidence stays immutable", deleteExecution, closedTaskExecution},
			{"an open check execution is never collected", deleteExecution, openCheckExecution},
			{"a check of an incomplete build is never collected", deleteExecution, incompleteCheckExecution},
			{"a job build's image check is job evidence", deleteExecution, jobCheckExecution},
			{"a job build's image get is job evidence", deleteExecution, jobGetExecution},
			{"a build with a Run output start is never inert", deleteExecution, outputCheckExecution},
			{"a closure cannot be removed from its execution", `DELETE FROM pipeline_run_execution_closures WHERE execution_id=$1`, closedCheckExecution},
			{"a start cannot be removed from its execution", `DELETE FROM pipeline_run_execution_starts WHERE execution_id=$1`, closedCheckExecution},
		} {
			Expect(deleteUnder(database, "concourse.pipeline_run_check_gc", refused.statement, refused.execution)).
				To(MatchError(ContainSubstring("check collection deletes only")), refused.why)
		}
		Expect(witnesses(closedTaskExecution)).To(Equal(2))
		Expect(witnesses(openCheckExecution)).To(Equal(1))
		Expect(witnesses(jobCheckExecution)).To(Equal(2))
		Expect(witnesses(jobGetExecution)).To(Equal(2))
	})

	It("restores the unconditional guards and non-cascading witnesses on rollback", func() {
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(runTeamPurgeEvidenceVersion - 1)
		for _, marker := range []string{"concourse.pipeline_run_team_purge", "concourse.pipeline_run_check_gc"} {
			Expect(deleteUnder(database, marker, `DELETE FROM pipeline_run_execution_closures WHERE execution_id=$1`, closedCheckExecution)).
				To(MatchError(ContainSubstring("immutable until Run purge is integrated")))
		}
		var helpers int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_proc WHERE proname IN ('run_team_purge_active','run_check_gc_active')`).Scan(&helpers)).To(Succeed())
		Expect(helpers).To(BeZero())
		var cascading int
		Expect(database.QueryRow(`SELECT count(*) FROM pg_constraint WHERE contype='f'
			AND confrelid='pipeline_run_executions'::regclass AND confdeltype='c'`).Scan(&cascading)).To(Succeed())
		Expect(cascading).To(BeZero())
	})
})
