package runs_test

import (
	"database/sql"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRuns(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Runs Suite")
}

// The suite file may name atc/db types freely: it is the composition root for
// these specs, and the reach guard counts production edges only
// (architecture_test.go). db_free_consumer_test.go -- A10's test consumer --
// may not, and that is the whole point of this package.

var (
	postgresRunner postgresrunner.Runner

	dbConn      db.DbConn
	lockFactory lock.LockFactory
	teamFactory db.TeamFactory
	runFactory  db.PipelineRunFactory
	defaultTeam db.Team

	// admitter is the already-constructed port. A10's consumer uses this
	// package-level value rather than calling runs.NewAdmitter, because the
	// constructor names atc/db types by necessity (D-P3a): it is called from
	// the composition root and from suite files, never from a consumer.
	admitter runs.Admitter

	// Fixtures A10's consumer needs, all in port or atc types, so that its own
	// file names no atc/db type.
	memberPrincipal runs.Principal
	viewerPrincipal runs.Principal

	templateRef  runs.TemplateRef // a valid, runnable template
	paramsRef    runs.TemplateRef // a template declaring one required parameter
	pausedRef    runs.TemplateRef // a template pipeline that is paused
	archivedRef  runs.TemplateRef // a template pipeline that is archived
	ordinaryRef  runs.TemplateRef // a pipeline that is not a template at all
	unknownRef   runs.TemplateRef // no such pipeline, on a team the principal is a member of
	otherTeamRef runs.TemplateRef // a template on a team nobody here is a member of
)

// scratchTable is a table of the consumer's own, so that A10 can demonstrate a
// before-commit callback writing through the callback's Tx without naming a
// core table or a consumer table.
const scratchTable = "a10_consumer_scratch"

var fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(func() {
		postgresRunner.DropTestDB()
	})

	dbConn = postgresRunner.OpenConn()
	// Left at postgresrunner's one-connection default, deliberately. That
	// default exists to catch a code path that needs two connections at once,
	// and admission is exactly such a path if any of its reads goes to the
	// pool while the caller holds the transaction Begin handed out. Raising
	// the limit here would silence the whole suite's evidence for the port's
	// connection budget; connection_budget_test.go asserts it head-on.
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

	// A real team with a real atc.TeamAuth. The roles are what A11 part 1
	// exercises and what A10's admissions ride on; nothing here is a double.
	var err error
	defaultTeam, err = teamFactory.CreateTeam(atc.Team{
		Name: "runs-team",
		Auth: atc.TeamAuth{
			"member": {"users": []string{"local:member-user"}},
			"viewer": {"users": []string{"local:viewer-user"}},
		},
	})
	Expect(err).NotTo(HaveOccurred())

	otherTeam, err := teamFactory.CreateTeam(atc.Team{
		Name: "other-team",
		Auth: atc.TeamAuth{
			"member": {"users": []string{"local:stranger"}},
		},
	})
	Expect(err).NotTo(HaveOccurred())

	displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	Expect(err).NotTo(HaveOccurred())

	admitter = runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds, nil)

	memberPrincipal = runs.Principal{Claims: claimsFor("member-user", "member-id")}
	viewerPrincipal = runs.Principal{Claims: claimsFor("viewer-user", "viewer-id")}

	savePipeline(defaultTeam, "runnable", templateConfig(nil))
	templateRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "runnable"}}

	savePipeline(defaultTeam, "parameterized", templateConfig([]atc.ParamSchema{
		{Name: "target", Type: atc.ParamTypeString, Required: true},
	}))
	paramsRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "parameterized"}}

	paused := savePipeline(defaultTeam, "paused-template", templateConfig(nil))
	Expect(paused.Pause("someone")).To(Succeed())
	pausedRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "paused-template"}}

	archived := savePipeline(defaultTeam, "archived-template", templateConfig(nil))
	Expect(archived.Archive()).To(Succeed())
	archivedRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "archived-template"}}

	ordinary := templateConfig(nil)
	ordinary.Template = false
	savePipeline(defaultTeam, "ordinary", ordinary)
	ordinaryRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "ordinary"}}

	unknownRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "no-such-pipeline"}}

	savePipeline(otherTeam, "secret", templateConfig(nil))
	otherTeamRef = runs.TemplateRef{Team: otherTeam.Name(), Pipeline: atc.PipelineRef{Name: "secret"}}

	_, err = dbConn.Exec("CREATE TABLE " + scratchTable + " (note text NOT NULL)")
	Expect(err).NotTo(HaveOccurred())
})

// templateConfig is the shape the branch's own admission specs use
// (atc/db/pipeline_run_admission_test.go): a resource and a triggering get, so
// the materialized payload validates and produces one entry build.
func templateConfig(params []atc.ParamSchema) atc.Config {
	return atc.Config{
		Template: true,
		Params:   params,
		Resources: atc.ResourceConfigs{{
			Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"},
		}},
		Jobs: atc.JobConfigs{
			{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source", Trigger: true}}}},
		},
	}
}

func savePipeline(team db.Team, name string, config atc.Config) db.Pipeline {
	GinkgoHelper()
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())

	return pipeline
}

// claimsFor builds the verified-claims map the API's token verification
// produces, in the shape accessor reads it: the connector and user id live
// under federated_claims, and the display name under name.
func claimsFor(userName, userID string) map[string]any {
	return map[string]any{
		"sub":  "local:" + userName,
		"name": userName,
		"federated_claims": map[string]any{
			"connector_id": "local",
			"user_id":      userID,
		},
	}
}
