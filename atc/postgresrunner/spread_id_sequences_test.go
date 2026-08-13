package postgresrunner_test

import (
	"database/sql"
	"fmt"

	"github.com/tedsuo/ifrit"

	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("spread id sequences", func() {
	var (
		runner    postgresrunner.Runner
		dbProcess ifrit.Process

		conn *sql.DB
	)

	BeforeEach(func() {
		postgresrunner.InitializeRunnerForGinkgo(&runner, &dbProcess)
		DeferCleanup(func() {
			postgresrunner.FinalizeRunnerForGinkgo(&runner, &dbProcess)
		})

		runner.CreateTestDBFromTemplate()
		DeferCleanup(runner.DropTestDB)

		conn = runner.OpenSingleton()
		DeferCleanup(func() {
			Expect(conn.Close()).To(Succeed())
		})
	})

	It("gives every id sequence a range of its own", func() {
		expectDisjoint(nextIDs(conn))
	})

	It("keeps them apart across a truncate", func() {
		before := nextIDs(conn)

		var id int
		Expect(conn.QueryRow(`INSERT INTO teams (name) VALUES ('some-team') RETURNING id`).Scan(&id)).To(Succeed())

		runner.Truncate()

		Expect(nextIDs(conn)).To(Equal(before))
		expectDisjoint(nextIDs(conn))
	})
})

// nextIDs reads the sequence relations directly. pg_sequences.last_value is
// NULL until a sequence has been read, so it reports nothing after a RESTART.
func nextIDs(conn *sql.DB) map[string]int64 {
	GinkgoHelper()

	rows, err := conn.Query(`
		SELECT sequencename FROM pg_sequences
		WHERE schemaname = 'public' AND sequencename LIKE '%\_id\_seq'`)
	Expect(err).NotTo(HaveOccurred())

	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	Expect(rows.Close()).To(Succeed())

	next := map[string]int64{}
	for _, name := range names {
		var lastValue int64
		var isCalled bool
		err := conn.QueryRow(fmt.Sprintf(`SELECT last_value, is_called FROM %q`, name)).Scan(&lastValue, &isCalled)
		Expect(err).NotTo(HaveOccurred())

		if isCalled {
			lastValue++
		}
		next[name] = lastValue
	}

	return next
}

func expectDisjoint(next map[string]int64) {
	GinkgoHelper()

	// a pattern that matched nothing would leave this empty and every
	// assertion below vacuous, which is the failure this whole mechanism
	// exists to make impossible
	Expect(len(next)).To(BeNumerically(">=", 20))

	sharing := map[int64][]string{}
	for name, value := range next {
		sharing[value] = append(sharing[value], name)
	}

	for value, names := range sharing {
		Expect(names).To(HaveLen(1), fmt.Sprintf("sequences share next value %d: %v", value, names))
	}
}
