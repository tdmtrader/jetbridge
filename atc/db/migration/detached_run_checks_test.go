package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const detachedRunChecksVersion = 1789793144

var _ = Describe("detached Run check retention", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(detachedRunChecksVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	It("refuses rollback with retained checks and permits rollback without them", func() {
		// Isolate the rollback boundary from the earlier admission tables. The
		// DB reclamation suite creates and retains checks through production APIs.
		_, err := database.Exec(`ALTER TABLE builds DISABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO builds(name,status,completed,team_id,pipeline_run_id)
			VALUES ('check','succeeded',true,1,1)`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(detachedRunChecksVersion - 1)
		Expect(err).To(MatchError(ContainSubstring("cannot discard retained detached Run checks")))

		database = postgresRunner.OpenDBAtVersion(detachedRunChecksVersion)
		_, err = database.Exec(`DELETE FROM builds WHERE pipeline_run_id=1`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`ALTER TABLE builds ENABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		database, err = postgresRunner.TryOpenDBAtVersion(detachedRunChecksVersion - 1)
		Expect(err).NotTo(HaveOccurred())
	})
})
