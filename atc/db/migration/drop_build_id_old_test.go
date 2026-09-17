package migration_test

import (
	"database/sql"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("dropping build_id_old", func() {
	const preMigrationVersion = 1789273599
	const postMigrationVersion = 1789607907

	var db *sql.DB
	var teamID, pipelineID int

	BeforeEach(func() {
		db = postgresRunner.OpenDBAtVersion(preMigrationVersion)

		Expect(db.QueryRow(`
			INSERT INTO teams (name, auth) VALUES ('some-team', '{}') RETURNING id
		`).Scan(&teamID)).To(Succeed())

		Expect(db.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ('some-pipeline', $1, 1) RETURNING id
		`, teamID).Scan(&pipelineID)).To(Succeed())
	})

	AfterEach(func() {
		Expect(db.Close()).To(Succeed())
	})

	// A pre-2020 event carries its build id only in build_id_old. The drop is
	// only safe if that id survives in build_id, so write one of each shape and
	// look for both afterwards.
	It("folds legacy ids into build_id before removing the column", func() {
		for _, table := range []string{
			fmt.Sprintf("pipeline_build_events_%d", pipelineID),
			fmt.Sprintf("team_build_events_%d", teamID),
		} {
			_, err := db.Exec(fmt.Sprintf(
				`INSERT INTO %s (event_id, build_id_old, type, version, payload) VALUES (1, 4242, 'log', '1.0', '{}')`, table))
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Exec(fmt.Sprintf(
				`INSERT INTO %s (event_id, build_id, type, version, payload) VALUES (2, 4243, 'log', '1.0', '{}')`, table))
			Expect(err).ToNot(HaveOccurred())
		}

		postgresRunner.MigrateToVersion(postMigrationVersion)

		for _, table := range []string{
			fmt.Sprintf("pipeline_build_events_%d", pipelineID),
			fmt.Sprintf("team_build_events_%d", teamID),
		} {
			var legacy, current int
			Expect(db.QueryRow(fmt.Sprintf(`SELECT build_id FROM %s WHERE event_id = 1`, table)).Scan(&legacy)).To(Succeed())
			Expect(legacy).To(Equal(4242), "legacy id lost in %s", table)

			Expect(db.QueryRow(fmt.Sprintf(`SELECT build_id FROM %s WHERE event_id = 2`, table)).Scan(&current)).To(Succeed())
			Expect(current).To(Equal(4243))
		}

		var columns int
		Expect(db.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'build_events' AND column_name = 'build_id_old'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(0), "build_id_old still present on the parent")

		var indexes int
		Expect(db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE indexname LIKE '%build_id_old%'`).Scan(&indexes)).To(Succeed())
		Expect(indexes).To(Equal(0), "a legacy index outlived the column")
	})
})
