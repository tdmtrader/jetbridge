package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("v3-only workflow liveness migration", func() {
	const constraintName = "agent_workflow_definitions_live_schema_v3_check"

	var (
		database    *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		migrator    migration.Migrator
		legacyV1ID  int
		legacyV2ID  int
		v3ID        int
	)

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		lockFactory = lock.NewLockFactory(lockDB, noop, noop)
		migrator = migration.NewMigrator(database, lockFactory)
		Expect(migrator.Migrate(nil, nil, 1773106122)).To(Succeed())

		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, schema_version, signature_version)
			VALUES
				('v3-only-live-v1', 1, 'v1-live', 'opaque schema 1', true, 1, 0)
			RETURNING id
		`).Scan(&legacyV1ID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, schema_version, signature_version)
			VALUES
				('v3-only-live-v2', 1, 'v2-live', 'opaque schema 2', true, 2, 0)
			RETURNING id
		`).Scan(&legacyV2ID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, schema_version, signature_version)
			VALUES
				('v3-only-live-v3', 1, 'v3-live', 'schema_version: 3', true, 3, 1)
			RETURNING id
		`).Scan(&v3ID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	expectLive := func(id int, expected bool) {
		var live bool
		Expect(database.QueryRow(`
			SELECT live
			FROM agent_workflow_definitions
			WHERE id = $1
		`, id).Scan(&live)).To(Succeed())
		Expect(live).To(Equal(expected), "workflow definition %d", id)
	}

	expectConstraint := func(expected bool) {
		var definition string
		err := database.QueryRow(`
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_definitions'::regclass
			  AND conname = $1
		`, constraintName).Scan(&definition)
		if !expected {
			Expect(err).To(MatchError(sql.ErrNoRows))
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(definition).To(ContainSubstring("schema_version = 3"))
	}

	It("demotes legacy live rows, constrains future liveness, and keeps downgrade data inert", func() {
		Expect(migrator.Migrate(nil, nil, 1773106123)).To(Succeed())

		expectLive(legacyV1ID, false)
		expectLive(legacyV2ID, false)
		expectLive(v3ID, true)
		expectConstraint(true)

		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, schema_version, signature_version)
			VALUES
				('v3-only-inert-history', 1, 'inert-history', 'opaque history', false, 1, 0)
		`)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, schema_version, signature_version)
			VALUES
				('v3-only-rejected-insert', 1, 'rejected-insert', 'opaque history', true, 2, 0)
		`)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE id = $1
		`, legacyV1ID)
		Expect(err).To(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, 1773106122)).To(Succeed())
		expectConstraint(false)
		expectLive(legacyV1ID, false)
		expectLive(legacyV2ID, false)
		expectLive(v3ID, true)

		_, err = database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE id = $1
		`, legacyV1ID)
		Expect(err).NotTo(HaveOccurred())
		expectLive(legacyV1ID, true)
	})

	It("demotes a reactivated legacy row again on same-database re-upgrade", func() {
		Expect(migrator.Migrate(nil, nil, 1773106123)).To(Succeed())
		expectLive(legacyV1ID, false)
		expectLive(legacyV2ID, false)
		expectLive(v3ID, true)
		expectConstraint(true)

		_, firstRejection := database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE id = $1
		`, legacyV1ID)
		Expect(firstRejection).To(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, 1773106122)).To(Succeed())
		expectConstraint(false)
		_, err := database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE id = $1
		`, legacyV1ID)
		Expect(err).NotTo(HaveOccurred())
		expectLive(legacyV1ID, true)
		expectLive(v3ID, true)

		Expect(migrator.Migrate(nil, nil, 1773106123)).To(Succeed())
		expectLive(legacyV1ID, false)
		expectLive(v3ID, true)
		expectConstraint(true)

		_, secondRejection := database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE id = $1
		`, legacyV1ID)
		Expect(secondRejection).To(HaveOccurred())
	})
})
