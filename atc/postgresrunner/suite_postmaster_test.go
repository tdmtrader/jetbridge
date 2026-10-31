package postgresrunner

import (
	"database/sql"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// suiteRunner is this suite's own GinkgoRunner: one postmaster for every
// process `ginkgo -p` runs the suite in. SuiteRunner (export_test.go) hands it
// to the external test package.
var suiteRunner Runner

var _ = GinkgoRunner(&suiteRunner)

var _ = Describe("one postmaster per suite", func() {
	// another stands in for a second Ginkgo process: it joins the same
	// postmaster from the same handoff that SynchronizedBeforeSuite gave this
	// one, under a process number no real process of this run has.
	another := func() Runner {
		return suitePostmaster{Port: suiteRunner.Port, FileCopy: suiteRunner.fileCopy}.
			runner(GinkgoParallelProcess() + 1000)
	}

	It("names each process's database after the process", func() {
		Expect(suiteRunner.DBName()).To(Equal(fmt.Sprintf("testdb_%d", GinkgoParallelProcess())))
	})

	It("gives two processes distinct databases on the one postmaster", func() {
		other := another()
		Expect(other.Port).To(Equal(suiteRunner.Port))
		Expect(other.DBName()).NotTo(Equal(suiteRunner.DBName()))

		suiteRunner.CreateTestDBFromTemplate()
		DeferCleanup(suiteRunner.DropTestDB)
		other.CreateTestDBFromTemplate()
		DeferCleanup(other.DropTestDB)

		mine := suiteRunner.OpenSingleton()
		DeferCleanup(mine.Close)
		theirs := other.OpenSingleton()
		DeferCleanup(theirs.Close)

		Expect(currentDatabase(mine)).To(Equal(suiteRunner.DBName()))
		Expect(currentDatabase(theirs)).To(Equal(other.DBName()))

		_, err := mine.Exec("INSERT INTO teams (name) VALUES ('only-mine')")
		Expect(err).NotTo(HaveOccurred())

		var count int
		Expect(theirs.QueryRow("SELECT count(*) FROM teams WHERE name = 'only-mine'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero(), "a row one process writes must not reach another's database")
	})

	It("gives every process the connection budget a postmaster of its own had", func() {
		suiteRunner.CreateTestDBFromTemplate()
		DeferCleanup(suiteRunner.DropTestDB)
		conn := suiteRunner.OpenSingleton()
		DeferCleanup(conn.Close)

		var maxConnections int
		Expect(conn.QueryRow("SELECT setting::int FROM pg_settings WHERE name = 'max_connections'").Scan(&maxConnections)).To(Succeed())
		suiteConfig, _ := GinkgoConfiguration()
		Expect(maxConnections).To(Equal(100 * suiteConfig.ParallelTotal))
	})

	It("lets one process drop its database while another holds its own open", func() {
		other := another()
		other.CreateTestDBFromTemplate()
		DeferCleanup(other.DropTestDB)
		theirs := other.OpenSingleton()
		DeferCleanup(theirs.Close)
		Expect(theirs.Ping()).To(Succeed())

		suiteRunner.CreateTestDBFromTemplate()
		suiteRunner.DropTestDB()

		Expect(theirs.Ping()).To(Succeed())
	})
})

func currentDatabase(conn *sql.DB) string {
	GinkgoHelper()
	var name string
	Expect(conn.QueryRow("SELECT current_database()").Scan(&name)).To(Succeed())
	return name
}
