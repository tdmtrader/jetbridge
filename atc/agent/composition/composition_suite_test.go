package composition_test

import (
	"database/sql"
	"errors"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestComposition(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Composition Suite")
}

// Only this file names atc/db, and only because someone has to build the
// fixtures: a team, a template, and real builds rows. The production package
// under test imports atc and atc/runs and nothing else in the module, which
// architecture_test.go pins exactly.

var (
	postgresRunner postgresrunner.Runner

	dbConn      db.DbConn
	lockFactory lock.LockFactory
	teamFactory db.TeamFactory
	runFactory  db.PipelineRunFactory
	defaultTeam db.Team

	service *composition.Service

	principal   runs.Principal
	templateRef runs.TemplateRef

	// Real builds rows. composition_calls.build_id is NOT NULL REFERENCES
	// builds(id), and Request.BuildID is a plain int the type system does not
	// constrain -- so a fabricated integer is refused by the foreign key, not
	// by the behaviour under test. A spec that "fails correctly" for the wrong
	// reason is worse than no spec.
	//
	// One build is enough for A1, A2 and A3; A4's first half is two admissions
	// differing only in build_id, so there are two.
	buildID      int
	otherBuildID int
)

var fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(func() {
		postgresRunner.DropTestDB()
	})

	dbConn = postgresRunner.OpenConn()
	// Left at postgresrunner's one-connection default. The consumer holds one
	// transaction across its claim, its admission and its commit, so the whole
	// of Admit has to fit in a single connection -- and that is what this
	// suite is riding on every spec. Raising the limit would hide a port that
	// reached for a second one.
	DeferCleanup(func() {
		Expect(dbConn.Close()).To(Succeed())
	})
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := postgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	lockFactory = lock.NewLockFactory(lockConns, fakeLogFunc, fakeLogFunc)

	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
	runFactory = db.NewPipelineRunFactory(dbConn, lockFactory)

	var err error
	defaultTeam, err = teamFactory.CreateTeam(atc.Team{
		Name: "composition-team",
		Auth: atc.TeamAuth{"member": {"users": []string{"local:composer"}}},
	})
	Expect(err).NotTo(HaveOccurred())

	_, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
		Template: true,
		Resources: atc.ResourceConfigs{{
			Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"},
		}},
		Jobs: atc.JobConfigs{
			{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source", Trigger: true}}}},
		},
	}, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())
	templateRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "template"}}

	principal = runs.Principal{Claims: map[string]any{
		"sub":  "local:composer",
		"name": "composer",
		"federated_claims": map[string]any{
			"connector_id": "local",
			"user_id":      "composer-id",
		},
	}}

	build, err := defaultTeam.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	buildID = build.ID()

	otherBuild, err := defaultTeam.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	otherBuildID = otherBuild.ID()

	service = composition.NewService(newAdmitter(dbConn))
})

func newAdmitter(conn db.DbConn) runs.Admitter {
	GinkgoHelper()
	displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	Expect(err).NotTo(HaveOccurred())

	return runs.NewAdmitter(conn,
		db.NewPipelineRunFactory(conn, lockFactory),
		db.NewTeamFactory(conn, lockFactory),
		displayUserIds, nil)
}

// A11 part 3, the consumer invariant.
//
// The spec says "after the whole suite". This suite creates and drops a
// database per spec, so a true AfterSuite would look at an empty database and
// pass vacuously. Per-spec is strictly more often than once, so the obligation
// is met and then some.
//
// The Ginkgo ordering this relies on, stated because a later reader can
// silently break it: the drop is registered with DeferCleanup inside
// BeforeEach, and DeferCleanup callbacks run *after* a spec's AfterEach nodes.
// So this query sees the populated database and the drop follows it. Move the
// drop into an AfterEach of its own and this check runs against an empty
// database instead -- a vacuous green. If the drop moves, this moves with it.
var _ = AfterEach(func() {
	if dbConn == nil {
		return
	}

	var runsAdmitted int
	Expect(dbConn.QueryRow(`
		SELECT count(*) FROM pipeline_runs r
		WHERE EXISTS (SELECT 1 FROM composition_iterations i WHERE i.run_id = r.id)
	`).Scan(&runsAdmitted)).To(Succeed())

	// Every run reachable from an iteration row has exactly one iteration
	// naming it, and exactly one call owning that iteration.
	var offenders int
	Expect(dbConn.QueryRow(`
		SELECT count(*) FROM (
			SELECT i.run_id
			FROM composition_iterations i
			JOIN composition_calls c ON c.id = i.call_id
			GROUP BY i.run_id
			HAVING count(*) <> 1 OR count(DISTINCT c.id) <> 1
		) AS bad
	`).Scan(&offenders)).To(Succeed())
	Expect(offenders).To(Equal(0),
		"a run admitted through the consumer is named by more than one iteration row")

	// And no iteration names a run that is not there. The foreign key makes
	// this a schema property; asserting it is how we notice if the key is ever
	// dropped.
	var dangling int
	Expect(dbConn.QueryRow(`
		SELECT count(*) FROM composition_iterations i
		WHERE NOT EXISTS (SELECT 1 FROM pipeline_runs r WHERE r.id = i.run_id)
	`).Scan(&dangling)).To(Succeed())
	Expect(dangling).To(Equal(0))

	// Every run that exists at all was admitted through the consumer: this
	// suite has no other admitter. Runs from the port's own suite live in a
	// different database and are excluded by construction, as the criterion
	// says.
	var totalRuns int
	Expect(dbConn.QueryRow("SELECT count(*) FROM pipeline_runs").Scan(&totalRuns)).To(Succeed())
	Expect(runsAdmitted).To(Equal(totalRuns),
		"a run exists that no composition_iterations row accounts for")

	if runsAdmitted > 0 {
		AddReportEntry(admittedRunsEntry, runsAdmitted, ReportEntryVisibilityNever)
	}
})

// admittedRunsEntry is how the check above proves it is not vacuous.
//
// Every assertion in it is satisfied by an empty database, and an empty
// database is exactly what it would see if the per-spec drop were ever
// reordered ahead of it. So record whether it ever looked at a populated one,
// and fail the suite if it never did.
//
// A package-level bool cannot carry that record. Under `ginkgo -p` -- which is
// how the unit tier runs -- the ReportAfterSuite below runs in one process
// while the specs ran in others, so a bool set by the AfterEach is false
// wherever the report is assembled. That is a parallel-only red on a suite
// whose every spec passed, and it is exactly what the first full `make
// test-unit` on this branch hit. A report entry is the Ginkgo-native carrier
// instead: it travels with the spec report from whichever process ran the
// spec, and Ginkgo aggregates those into the Report the node below receives.
// Visibility Never keeps it out of the console; it is still in the report.
const admittedRunsEntry = "composition: the consumer invariant saw admitted runs"

// ReportAfterSuite rather than AfterSuite: postgresrunner.GinkgoRunner already
// defines the suite's one permitted AfterSuite, and reports may be many.
var _ = ReportAfterSuite("the consumer invariant looked at something", func(report Report) {
	var specsThatAdmitted int
	for _, spec := range report.SpecReports {
		for _, entry := range spec.ReportEntries {
			if entry.Name == admittedRunsEntry {
				specsThatAdmitted++
			}
		}
	}

	Expect(specsThatAdmitted).To(BeNumerically(">", 0),
		"the consumer invariant never saw a single admitted run, so every run of it "+
			"passed over an empty database. Either no spec admits any more, or the "+
			"per-spec database drop was reordered ahead of the AfterEach that checks it.")
})

// Helpers the specs read the database through. They use dbConn because a spec
// is allowed to; the package under test is not, and never does.

func countOf(query string, args ...any) int {
	GinkgoHelper()
	var count int
	Expect(dbConn.QueryRow(query, args...).Scan(&count)).To(Succeed())

	return count
}

// iterationRunID reads the admitted run from the iteration row for the call's
// first ordinal. Every criterion that reads an admitted run id reads it here;
// composition_calls has no run id column to read.
func iterationRunID(buildID int, planID string) int {
	GinkgoHelper()
	var runID int
	Expect(dbConn.QueryRow(`
		SELECT i.run_id FROM composition_iterations i
		JOIN composition_calls c ON c.id = i.call_id
		WHERE c.build_id = $1 AND c.plan_id = $2 AND i.ordinal = 1
	`, buildID, planID).Scan(&runID)).To(Succeed())

	return runID
}

func callDigest(buildID int, planID string) string {
	GinkgoHelper()
	var digest string
	Expect(dbConn.QueryRow(
		"SELECT input_digest FROM composition_calls WHERE build_id = $1 AND plan_id = $2",
		buildID, planID).Scan(&digest)).To(Succeed())

	return digest
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
