package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const simplifyRunContractVersion = 1789793159

var _ = Describe("simplify Run contract upgrade", func() {
	var database *sql.DB
	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	// Isolate migration of old headers from the now-removed admission contract.
	seed := func(terminal, invocation bool) {
		GinkgoHelper()
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = tx.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch,caused_by_run,correlation)
   VALUES (7,1,1,'{}','hash','running','owner','v2',1,6,'historical-intent')`)
		Expect(err).NotTo(HaveOccurred())
		if terminal {
			_, err = tx.Exec(`UPDATE pipeline_runs SET status='succeeded',completed_at=now(),result_manifest='{}',terminal_observation_version='historical-digest' WHERE id=7`)
			Expect(err).NotTo(HaveOccurred())
		}
		if invocation {
			_, err = tx.Exec(`INSERT INTO pipeline_run_invocations VALUES (7,1,1,repeat('a',64),repeat('b',64),'retained document',repeat('c',64),repeat('d',64))`)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tx.Commit()).To(Succeed())
	}

	DescribeTable("refuses retained invocation evidence before changing the schema", func(terminal bool) {
		seed(terminal, true)
		Expect(database.Close()).To(Succeed())
		_, err := postgresRunner.TryOpenDBAtVersion(simplifyRunContractVersion)
		Expect(err).To(MatchError(ContainSubstring("1789793159_simplify_run_contract requires empty pipeline_run_invocations")))
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion - 1)
		var document string
		Expect(database.QueryRow(`SELECT caller_document FROM pipeline_run_invocations WHERE run_id=7`).Scan(&document)).To(Succeed())
		Expect(document).To(Equal("retained document"))
		var epoch int
		Expect(database.QueryRow(`SELECT activation_epoch FROM pipeline_runs WHERE id=7`).Scan(&epoch)).To(Succeed())
		Expect(epoch).To(Equal(1))
		// The documented operator action deliberately relinquishes replay evidence.
		_, err = database.Exec(`TRUNCATE pipeline_run_invocations`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion)
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE id=7`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	}, Entry("running Run", false), Entry("terminal Run with no payload", true))

	It("preserves terminal shape, immutability and exact-claim checks after dropping populated birth fields", func() {
		seed(true, false)
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion)
		var columns int
		Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='pipeline_runs'
   AND column_name IN ('activation_epoch','run_contract_version','caused_by_run','correlation','terminal_observation_version')`).Scan(&columns)).To(Succeed())
		Expect(columns).To(BeZero())
		for _, query := range []string{
			`UPDATE pipeline_runs SET status='running' WHERE id=7`,
			`UPDATE pipeline_runs SET completed_at=completed_at+interval '1 second' WHERE id=7`,
			`UPDATE pipeline_runs SET result_manifest='{"unbacked":{}}' WHERE id=7`,
		} {
			_, err := database.Exec(query)
			Expect(err).To(MatchError(ContainSubstring("Run terminal publication is immutable")))
		}
		// A new terminal row still cannot invent a result without an exact candidate.
		_, err := database.Exec(`INSERT INTO teams(id,name,auth) VALUES(1,'migration-team','{}');
   INSERT INTO pipelines(id,name,team_id,secondary_ordering,template) VALUES(1,'template',1,1,true)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,completed_at,result_manifest)
   VALUES(8,1,2,'{}','hash','succeeded','owner',now(),'{"unbacked":{}}')`)
		Expect(err).To(MatchError(ContainSubstring("Run manifest requires its exact retained candidates")))
		_, err = database.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by)
   VALUES(8,1,2,'{}','hash','succeeded','owner')`)
		Expect(err).To(MatchError(ContainSubstring("pipeline_run_result_shape")))
	})

	It("restores the previous shapes on an empty rollback and can upgrade again", func() {
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion)
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion - 1)
		var epoch int
		Expect(database.QueryRow(`SELECT epoch FROM pipeline_run_activation`).Scan(&epoch)).To(Succeed())
		Expect(epoch).To(BeZero())
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(simplifyRunContractVersion)
	})
})
