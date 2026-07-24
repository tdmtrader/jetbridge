package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"
	"github.com/lib/pq"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agentic authority orphan migration", func() {
	const beforeVersion, targetVersion = 1773106121, 1773106122

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

	It("removes retired question grants and only the blank-hash historical sentinel", func() {
		const keptName = "authority-migration-normal"
		_, err := database.Exec(`
			INSERT INTO agent_principals
				(name, token_prefix, token_hash, scopes)
			VALUES
				($1, 'cap1.normal', 'normal-token-hash',
				 ARRAY['reviews:write', 'questions:answer']),
				('legacy-publish', 'cap1.operator', 'operator-token-hash',
				 ARRAY['costs:write'])
		`, keptName)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var scopes []string
		Expect(database.QueryRow(`
			SELECT scopes FROM agent_principals WHERE name = $1
		`, keptName).Scan(pq.Array(&scopes))).To(Succeed())
		Expect(scopes).To(Equal([]string{"reviews:write"}))

		var sentinelCount int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_principals
			WHERE name = 'legacy-publish' AND token_hash = ''
		`).Scan(&sentinelCount)).To(Succeed())
		Expect(sentinelCount).To(Equal(0))

		var operatorPrincipalCount int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_principals
			WHERE name = 'legacy-publish' AND token_hash = 'operator-token-hash'
			  AND scopes = ARRAY['costs:write']
		`).Scan(&operatorPrincipalCount)).To(Succeed())
		Expect(operatorPrincipalCount).To(Equal(1),
			"an operator-created principal sharing the historical name must survive")
	})
})
