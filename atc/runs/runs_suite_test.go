package runs_test

import (
	"database/sql"
	"testing"
	"time"

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

	// adminPrincipal is an owner of a team marked admin, which is the one
	// principal accessor.IsAuthorized short-circuits for. It is here because
	// that short-circuit is what makes an unresolvable team reachable after
	// authorization has already passed.
	adminPrincipal runs.Principal

	// buildPrincipal is the port's other form of identity: a build on
	// defaultTeam, acting for itself.
	//
	// There is a real builds row behind it, and there has to be: the port
	// holds every field of a build principal against that row, so a principal
	// invented out of thin air would now be refused rather than admitted, and
	// a suite built on one would be proving the opposite of what it says. It
	// is a real running build of a real job on a real non-template pipeline,
	// created the way the scheduler creates one.
	buildPrincipal runs.Principal

	// callerBuild is the build behind buildPrincipal, kept so that specs can
	// finish it, abort it, or forge a principal that differs from it in one
	// field.
	callerBuild db.Build

	// callerPipeline is the non-template pipeline callerBuild belongs to. It
	// is the target of the direct-recursion specs: a template that is also
	// the caller's own pipeline.
	callerPipeline db.Pipeline

	// templatePipeline is the pipeline behind templateRef, kept so that the
	// recursion specs can create a build of the template itself and so that
	// a payload pipeline's id can be compared with its template's.
	templatePipeline db.Pipeline

	// buildFactory reads a build back by id, which is how the recursion specs
	// reach the entry builds CreateRunInTx writes into a run's payload.
	buildFactory db.BuildFactory

	// otherTeamHandle is the team nobody in this suite is a member of, kept so
	// that a spec can create a real build on it -- the forgery a comparison of
	// the principal's two team names could never catch.
	otherTeamHandle db.Team

	// adminTeamBuild is a running build on the team marked admin. It exists
	// so that "no build is ever an admin" can be asserted against a build
	// that really does belong to that team, rather than against one the port
	// would have refused for not existing.
	adminTeamBuild db.Build

	templateRef  runs.TemplateRef // a valid, runnable template
	paramsRef    runs.TemplateRef // a template declaring one required parameter
	pausedRef    runs.TemplateRef // a template pipeline that is paused
	archivedRef  runs.TemplateRef // a template pipeline that is archived
	instancedRef runs.TemplateRef // a template pipeline carrying instance vars
	ordinaryRef  runs.TemplateRef // a pipeline that is not a template at all
	unknownRef   runs.TemplateRef // no such pipeline, on a team the principal is a member of
	otherTeamRef runs.TemplateRef // a template on a team nobody here is a member of
	missingTeam  runs.TemplateRef // a team that does not exist at all
)

// scratchTable is a table of the consumer's own, so that A10 can demonstrate a
// before-commit callback writing through the callback's Tx without naming a
// core table or a consumer table.
const scratchTable = "a10_consumer_scratch"

// buildCreatedBy is the created_by value buildPrincipal must produce, spelled
// out in full rather than assembled from the same expression the port uses.
// A spec that rebuilt the string from its parts would agree with any format
// the port chose, including one that dropped a separator.
const buildCreatedBy = "build:runs-team/caller/release#1"

var fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	// The operator's hold is off by default, and AdmitVersionedRun answers it before
	// anything else, so every spec in this suite that is about something else
	// needs it open. It is restored rather than left set: it is a
	// process-wide global, and a suite that leaked it would decide the
	// behaviour of whatever ran next in the same binary.
	//
	// creation_gate_test.go turns it back off for the specs that are about
	// the hold itself.
	previousGate := atc.PipelineRunActivationEpoch
	atc.PipelineRunActivationEpoch = 1
	DeferCleanup(func() {
		atc.PipelineRunActivationEpoch = previousGate
	})

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
	activateVersionedAdmission(dbConn)
	runFactory = db.NewPipelineRunFactory(dbConn, lockFactory)
	// The grace periods are the reaper's, and nothing here reaps; five minutes
	// each is the value atccmd passes and is as arbitrary as it is irrelevant.
	buildFactory = db.NewBuildFactory(dbConn, lockFactory, 5*time.Minute, 5*time.Minute)

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

	otherTeamHandle, err = teamFactory.CreateTeam(atc.Team{
		Name: "other-team",
		Auth: atc.TeamAuth{
			"member": {"users": []string{"local:stranger"}},
		},
	})
	Expect(err).NotTo(HaveOccurred())

	// A real admin team, made admin the only way there is: CreateTeam writes
	// the auth, CreateDefaultTeamIfNotExists sets the admin flag on the
	// default-named team. Nothing here is a double either -- the accessor
	// computes isAdmin from these rows exactly as it does in production.
	adminTeam, err := teamFactory.CreateTeam(atc.Team{
		Name: atc.DefaultTeamName,
		Auth: atc.TeamAuth{"owner": {"users": []string{"local:admin-user"}}},
	})
	Expect(err).NotTo(HaveOccurred())

	_, err = teamFactory.CreateDefaultTeamIfNotExists()
	Expect(err).NotTo(HaveOccurred())

	displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	Expect(err).NotTo(HaveOccurred())

	admitter = runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds, nil)

	memberPrincipal = runs.Principal{Claims: claimsFor("member-user", "member-id")}
	viewerPrincipal = runs.Principal{Claims: claimsFor("viewer-user", "viewer-id")}
	adminPrincipal = runs.Principal{Claims: claimsFor("admin-user", "admin-id")}

	// The caller: an ordinary pipeline with one job, and one build of it left
	// running. Created through the factories rather than by INSERT, so that
	// what the port reads back is what the scheduler would really have
	// written -- in particular the job row the build's name is resolved
	// through.
	callerPipeline = savePipeline(defaultTeam, "caller", callerConfig())

	callerJob, found, err := callerPipeline.Job("release")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	callerBuild, err = callerJob.CreateBuild("someone")
	Expect(err).NotTo(HaveOccurred())

	// buildCreatedBy is spelled out as a constant, so the name the scheduler
	// chose has to be the one it was spelled with.
	Expect(callerBuild.Name()).To(Equal("1"))

	buildPrincipal = runs.Principal{Build: buildPrincipalFor(callerBuild)}

	adminTeamBuild = runningBuildOn(adminTeam, "admin-caller")

	templatePipeline = savePipeline(defaultTeam, "runnable", templateConfig(nil))
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

	// A pipeline carrying instance vars. It cannot also be a template: the
	// schema forbids the combination outright (the check constraint
	// pipelines_templates_are_not_instances), and a fixture that tried was
	// refused by it. That is precisely why "instanced" is a refusal of its own
	// rather than a shade of "not a template" -- admission answers it first,
	// and a caller pointed at an instance is told what it actually is.
	instancedConfig := templateConfig(nil)
	instancedConfig.Template = false
	instanced := atc.PipelineRef{Name: "instanced", InstanceVars: atc.InstanceVars{"branch": "main"}}
	_, _, err = defaultTeam.SavePipeline(instanced, instancedConfig, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())
	instancedRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: instanced}

	ordinary := templateConfig(nil)
	ordinary.Template = false
	savePipeline(defaultTeam, "ordinary", ordinary)
	ordinaryRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "ordinary"}}

	unknownRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "no-such-pipeline"}}

	missingTeam = runs.TemplateRef{Team: "no-such-team", Pipeline: atc.PipelineRef{Name: "whatever"}}

	savePipeline(otherTeamHandle, "secret", templateConfig(nil))
	otherTeamRef = runs.TemplateRef{Team: otherTeamHandle.Name(), Pipeline: atc.PipelineRef{Name: "secret"}}

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

// callerConfig is the pipeline the calling build belongs to: an ordinary
// pipeline, not a template, with the one job the build principal names.
func callerConfig() atc.Config {
	return atc.Config{
		Jobs: atc.JobConfigs{
			{Name: "release", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "noop"}}}},
		},
	}
}

// runningBuildOn creates an ordinary pipeline on the team and leaves one build
// of its single job running, which is the shape every build principal in this
// suite is assembled from.
func runningBuildOn(team db.Team, pipelineName string) db.Build {
	GinkgoHelper()

	pipeline := savePipeline(team, pipelineName, callerConfig())

	job, found, err := pipeline.Job("release")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	build, err := job.CreateBuild("someone")
	Expect(err).NotTo(HaveOccurred())

	return build
}

// buildPrincipalFor assembles a principal out of a real build exactly as
// exec.RunPipelineStep does out of its StepMetadata -- the same five fields,
// from the same accessors the engine fills that metadata from. A spec that
// composed one by hand would be free to compose one the port could never see
// in production.
func buildPrincipalFor(build db.Build) *runs.BuildPrincipal {
	return &runs.BuildPrincipal{
		TeamName:     build.TeamName(),
		PipelineName: build.PipelineName(),
		JobName:      build.JobName(),
		BuildName:    build.Name(),
		BuildID:      build.ID(),
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
