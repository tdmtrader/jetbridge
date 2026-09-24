package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const runCausationVersion = 1789793146

var _ = Describe("Run causation and correlation retention", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(runCausationVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	It("refuses rollback while any Run retains causation or correlation", func() {
		// Isolate the rollback boundary from admission: the DB suite creates
		// these Runs through the production factory.
		_, err := database.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch,correlation)
			VALUES (7,1,1,'{}','h','running','owner','v2',1,'batch-7')`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(runCausationVersion - 1)
		Expect(err).To(MatchError(ContainSubstring("cannot discard retained Run causation or correlation")))

		database = postgresRunner.OpenDBAtVersion(runCausationVersion)
		_, err = database.Exec(`DELETE FROM pipeline_runs WHERE id=7`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		database, err = postgresRunner.TryOpenDBAtVersion(runCausationVersion - 1)
		Expect(err).NotTo(HaveOccurred())
		var columns int
		Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns
			WHERE table_name='pipeline_runs' AND column_name IN ('caused_by_run','correlation')`).Scan(&columns)).To(Succeed())
		Expect(columns).To(BeZero())
	})

	It("refuses a cause that is not strictly earlier and a correlation outside the alphabet", func() {
		_, err := database.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		for _, insert := range []string{
			`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch,caused_by_run) VALUES (8,1,1,'{}','h','running','o','v2',1,8)`,
			`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch,caused_by_run) VALUES (8,1,1,'{}','h','running','o','v2',1,9)`,
			`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch,correlation) VALUES (8,1,1,'{}','h','running','o','v2',1,'not/allowed')`,
			`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,correlation) VALUES (8,1,1,'{}','h','running','o','legacy')`,
		} {
			_, err := database.Exec(insert)
			Expect(err).To(MatchError(ContainSubstring("violates check constraint")), insert)
		}
		_, err = database.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
	})
})
