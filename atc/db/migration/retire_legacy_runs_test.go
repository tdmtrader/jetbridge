package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const retireLegacyRunsVersion = 1789793147

var _ = Describe("Retiring legacy_v1 Runs", func() {
	var database *sql.DB

	seeded := []string{"teams", "pipelines", "pipeline_runs", "pipeline_run_definitions", "builds"}

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(retireLegacyRunsVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		// Isolate the seed from admission: the DB suite drives the real path.
		// The purge itself runs with every guard armed.
		for _, table := range seeded {
			_, err := database.Exec(`ALTER TABLE ` + table + ` DISABLE TRIGGER ALL`)
			Expect(err).NotTo(HaveOccurred())
		}
		for _, statement := range []string{
			`INSERT INTO teams(id, name) VALUES (1, 'main')`,
			`INSERT INTO pipelines(id, team_id, name, secondary_ordering, template) VALUES (1, 1, 'template', 1, true)`,
			// Run 10 is legacy_v1, Run 20 is v2; each has a payload pipeline,
			// a retained definition and a build with an event.
			`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch)
				VALUES (10,1,1,'{}','h','succeeded','owner','legacy_v1',NULL),
				       (20,1,2,'{}','h','running','owner','v2',1)`,
			`INSERT INTO pipelines(id, team_id, name, secondary_ordering, pipeline_run_id, instance_vars) VALUES (11, 1, 'payload', 1, 10, '{"run":1}'), (21, 1, 'payload', 2, 20, '{"run":2}')`,
			`INSERT INTO pipeline_run_definitions(run_id, template_digest, config) VALUES
				(10, repeat('a', 64), '{}'), (20, repeat('b', 64), '{}')`,
			`INSERT INTO builds(id, name, status, completed, team_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key)
				VALUES (100, '1', 'succeeded', true, 1, 11, 10, 'entry', 'entry'),
				       (200, '1', 'started', false, 1, 21, 20, 'entry', 'entry')`,
			`INSERT INTO build_events(build_id, type, payload, event_id, version) VALUES
				(100, 'log', '{}', 0, '1.0'), (200, 'log', '{}', 0, '1.0')`,
		} {
			_, err := database.Exec(statement)
			Expect(err).NotTo(HaveOccurred(), statement)
		}
		for _, table := range seeded {
			_, err := database.Exec(`ALTER TABLE ` + table + ` ENABLE TRIGGER ALL`)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(retireLegacyRunsVersion)
	})

	count := func(query string, args ...any) int {
		GinkgoHelper()
		var n int
		Expect(database.QueryRow(query, args...).Scan(&n)).To(Succeed())
		return n
	}

	It("drops each legacy_v1 Run with its payload, definition, builds and events, and keeps v2 Runs whole", func() {
		Expect(count(`SELECT count(*) FROM pipeline_runs WHERE id=10`)).To(BeZero())
		Expect(count(`SELECT count(*) FROM pipelines WHERE pipeline_run_id=10 OR id=11`)).To(BeZero())
		Expect(count(`SELECT count(*) FROM pipeline_run_definitions WHERE run_id=10`)).To(BeZero())
		Expect(count(`SELECT count(*) FROM builds WHERE id=100`)).To(BeZero())
		Expect(count(`SELECT count(*) FROM build_events WHERE build_id=100`)).To(BeZero())

		Expect(count(`SELECT count(*) FROM pipeline_runs WHERE id=20`)).To(Equal(1))
		Expect(count(`SELECT count(*) FROM pipelines WHERE id=21`)).To(Equal(1))
		Expect(count(`SELECT count(*) FROM pipeline_run_definitions WHERE run_id=20`)).To(Equal(1))
		Expect(count(`SELECT count(*) FROM builds WHERE id=200`)).To(Equal(1))
		Expect(count(`SELECT count(*) FROM build_events WHERE build_id=200`)).To(Equal(1))
		Expect(count(`SELECT count(*) FROM pipelines WHERE id=1`)).To(Equal(1), "the template survives")
	})

	It("admits only the v2 class afterwards, with no default to fall back on", func() {
		_, err := database.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			_, err := database.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER ALL`)
			Expect(err).NotTo(HaveOccurred())
		}()
		_, err = database.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,run_contract_version,activation_epoch)
			VALUES (30,1,3,'{}','h','running','owner','legacy_v1',NULL)`)
		Expect(err).To(MatchError(ContainSubstring("pipeline_run_birth_contract")))
		_, err = database.Exec(`INSERT INTO pipeline_runs(id,template_pipeline_id,number,params,config_hash,status,created_by,activation_epoch)
			VALUES (30,1,3,'{}','h','running','owner',1)`)
		Expect(err).To(MatchError(ContainSubstring("run_contract_version")), "a Run must name its class")
	})

	It("rolls back the schema only: the retired Runs are not restored", func() {
		Expect(database.Close()).To(Succeed())
		var err error
		database, err = postgresRunner.TryOpenDBAtVersion(retireLegacyRunsVersion - 1)
		Expect(err).NotTo(HaveOccurred())

		Expect(count(`SELECT count(*) FROM pipeline_runs WHERE id=10`)).To(BeZero())
		Expect(count(`SELECT count(*) FROM pipeline_runs WHERE id=20`)).To(Equal(1))
		var fallback sql.NullString
		Expect(database.QueryRow(`SELECT column_default FROM information_schema.columns
			WHERE table_name='pipeline_runs' AND column_name='run_contract_version'`).Scan(&fallback)).To(Succeed())
		Expect(fallback.String).To(ContainSubstring("legacy_v1"), "the older binary's default is back")
	})
})
