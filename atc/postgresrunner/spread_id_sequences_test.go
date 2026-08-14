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

		for name := range before {
			var id int64
			Expect(conn.QueryRow(`SELECT nextval($1::regclass)`, name).Scan(&id)).To(Succeed())
		}

		runner.Truncate()

		Expect(nextIDs(conn)).To(Equal(before))
		expectDisjoint(nextIDs(conn))
	})

	It("removes spec-created state without disturbing the migrated template", func() {
		sequenceNames := persistentSequenceNames(conn)
		Expect(len(sequenceNames)).To(BeNumerically(">", 20))
		for _, name := range sequenceNames {
			Expect(name).To(Or(
				HaveSuffix("_id_seq"),
				Equal("one_off_name"),
				Equal("config_version_seq"),
			), "Truncate must explicitly reset every persistent sequence")
		}

		var migrationCount int
		Expect(conn.QueryRow(`SELECT count(*) FROM migrations_history`).Scan(&migrationCount)).To(Succeed())
		Expect(migrationCount).To(BeNumerically(">", 0))

		_, err := conn.Exec(`INSERT INTO teams (name) VALUES ('truncate-me')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Exec(`CREATE TABLE pipeline_build_events_123 (id integer)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Exec(`INSERT INTO pipeline_build_events_123 VALUES (1)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Exec(`CREATE TABLE team_build_events_456 (id integer)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Exec(`CREATE SEQUENCE build_event_id_seq_123`)
		Expect(err).NotTo(HaveOccurred())

		for _, sequence := range []string{"one_off_name", "config_version_seq"} {
			var value int64
			Expect(conn.QueryRow(fmt.Sprintf(`SELECT nextval('%s')`, sequence)).Scan(&value)).To(Succeed())
			Expect(conn.QueryRow(fmt.Sprintf(`SELECT nextval('%s')`, sequence)).Scan(&value)).To(Succeed())
			Expect(value).To(BeNumerically(">", 1))
		}

		runner.Truncate()

		var migrationCountAfter int
		Expect(conn.QueryRow(`SELECT count(*) FROM migrations_history`).Scan(&migrationCountAfter)).To(Succeed())
		Expect(migrationCountAfter).To(Equal(migrationCount))
		Expect(nonEmptyPublicTables(conn)).To(BeEmpty())

		for _, relation := range []string{
			"pipeline_build_events_123",
			"team_build_events_456",
			"build_event_id_seq_123",
		} {
			var persisted sql.NullString
			Expect(conn.QueryRow(`SELECT to_regclass($1)`, "public."+relation).Scan(&persisted)).To(Succeed())
			Expect(persisted.Valid).To(BeFalse(), relation)
		}

		for _, sequence := range []string{"one_off_name", "config_version_seq"} {
			var value int64
			Expect(conn.QueryRow(fmt.Sprintf(`SELECT nextval('%s')`, sequence)).Scan(&value)).To(Succeed())
			Expect(value).To(Equal(int64(1)), sequence)
		}
	})
})

func persistentSequenceNames(conn *sql.DB) []string {
	GinkgoHelper()

	rows, err := conn.Query(`
		SELECT sequencename
		FROM pg_sequences
		WHERE schemaname = 'public'
		AND sequencename !~ '^build_event_id_seq_[0-9]+$'
		ORDER BY sequencename`)
	Expect(err).NotTo(HaveOccurred())

	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	Expect(rows.Close()).To(Succeed())
	return names
}

func nonEmptyPublicTables(conn *sql.DB) []string {
	GinkgoHelper()

	rows, err := conn.Query(`
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename != 'migrations_history'
		ORDER BY tablename`)
	Expect(err).NotTo(HaveOccurred())

	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	Expect(rows.Close()).To(Succeed())
	Expect(len(names)).To(BeNumerically(">", 30))

	var nonEmpty []string
	for _, name := range names {
		var found bool
		Expect(conn.QueryRow(fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %q LIMIT 1)`, name)).Scan(&found)).To(Succeed())
		if found {
			nonEmpty = append(nonEmpty, name)
		}
	}

	return nonEmpty
}

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
