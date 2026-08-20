package migration_test

import (
	"database/sql"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const pipelineRunEventsVersion = 1773105508

var _ = Describe("run payload event partition", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(pipelineRunEventsVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
		_, err := database.Exec(`INSERT INTO teams(name) VALUES ('run-events')`)
		Expect(err).NotTo(HaveOccurred())
	})

	newPayload := func() (int, int, int) {
		var templateID, runID, payloadID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) SELECT id, 'base', true, 1 FROM teams WHERE name = 'run-events' RETURNING id`).Scan(&templateID)).To(Succeed())
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'a-user', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) SELECT id, 'base', '{"run":1}', $1, 1 FROM teams WHERE name = 'run-events' RETURNING id`, runID).Scan(&payloadID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
		return payloadID, templateID, runID
	}

	It("does not create a pipeline event partition for a payload", func() {
		payloadID, _, _ := newPayload()
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, fmt.Sprintf("pipeline_build_events_%d", payloadID)).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
	})

	It("continues to create a pipeline event partition for an ordinary pipeline", func() {
		var ordinaryID int
		Expect(database.QueryRow(`INSERT INTO pipelines(team_id, name, secondary_ordering) SELECT id, 'ordinary', 1 FROM teams WHERE name = 'run-events' RETURNING id`).Scan(&ordinaryID)).To(Succeed())
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, fmt.Sprintf("pipeline_build_events_%d", ordinaryID)).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeTrue())
	})

	It("refuses a populated down migration and permits an empty one", func() {
		_, _, runID := newPayload()
		Expect(database.Close()).To(Succeed())
		_, err := postgresRunner.TryOpenDBAtVersion(1773105507)
		Expect(err).To(MatchError(ContainSubstring("cannot roll back pipeline template runs")))

		database = postgresRunner.OpenDBAtVersion(pipelineRunEventsVersion)
		_, err = database.Exec(`UPDATE pipeline_runs SET status = 'succeeded' WHERE id = $1`, runID)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE pipeline_run_id IS NOT NULL`)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipeline_runs`)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE template`)
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(1773105507)
		Expect(err).ToNot(HaveOccurred())
	})

	It("restores ordinary pipeline event partitions on an empty down migration", func() {
		Expect(database.Close()).To(Succeed())
		downDatabase, err := postgresRunner.TryOpenDBAtVersion(1773105507)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(downDatabase.Close()).To(Succeed()) })

		var pipelineID int
		Expect(downDatabase.QueryRow(`INSERT INTO pipelines(team_id, name, secondary_ordering) SELECT id, 'ordinary-after-down', 1 FROM teams WHERE name = 'run-events' RETURNING id`).Scan(&pipelineID)).To(Succeed())
		var exists bool
		Expect(downDatabase.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, fmt.Sprintf("pipeline_build_events_%d", pipelineID)).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeTrue())
	})
})
