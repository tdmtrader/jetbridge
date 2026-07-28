package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("authoritative validation provenance migration", func() {
	const beforeVersion, targetVersion = 1773106140, 1773106141

	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		migrator = migration.NewMigrator(database, lock.NewLockFactory(lockDB, noop, noop))
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("adds constrained frozen provenance columns and removes them symmetrically", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		for _, column := range []struct{ table, name string }{
			{"agent_workflow_runs", "dev_validation_provenance_hash"},
			{"agent_experiment_variants", "dev_validation_provenance_hash"},
			{"agent_experiments", "evaluator_dev_validation_provenance_hash"},
		} {
			var present int
			Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, column.table, column.name).Scan(&present)).To(Succeed())
			Expect(present).To(Equal(1))
		}
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var present int
		Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'agent_workflow_runs' AND column_name = 'dev_validation_provenance_hash'`).Scan(&present)).To(Succeed())
		Expect(present).To(Equal(0))
	})
})
