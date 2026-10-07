package landing_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/landing"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLanding(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Landing Suite")
}

// The component seam: the engine against real Postgres, the real run
// admission port and real templates. Runs are completed the way the atc/db
// suite completes them, by fixture, and a Run's results are made bindable by
// the rows the output plane would have written. The only stand-in is the
// result reader, which in production transfers through the output plane.
var (
	postgresRunner postgresrunner.Runner
	dbConn         db.DbConn
	lockFactory    lock.LockFactory
	teamFactory    db.TeamFactory
	runFactory     db.PipelineRunFactory
	queues         db.LandingQueueFactory
	admitter       runs.Admitter
	team           db.Team
	results        *verdictResults
	engine         *landing.Engine
	queue          db.LandingQueue
)

const testEpoch int64 = 1

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	previous := atc.PipelineRunActivationEpoch
	atc.PipelineRunActivationEpoch = testEpoch
	DeferCleanup(func() { atc.PipelineRunActivationEpoch = previous })

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(func() { postgresRunner.DropTestDB() })
	dbConn = postgresRunner.OpenConn()
	DeferCleanup(func() { Expect(dbConn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := range lockConns {
		lockConn := postgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		DeferCleanup(func() { Expect(lockConn.Close()).To(Succeed()) })
	}
	logFunc := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(lockConns, logFunc, logFunc)
	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
	runFactory = db.NewPipelineRunFactory(dbConn, lockFactory)
	queues = db.NewLandingQueueFactory(dbConn)
	activateAdmission()

	var err error
	team, err = teamFactory.CreateTeam(atc.Team{Name: "landing-team", Auth: atc.TeamAuth{"member": {"users": []string{"local:member"}}}})
	Expect(err).NotTo(HaveOccurred())

	savePipeline("landing-compose", atc.Config{Template: true,
		Params: []atc.ParamSchema{{Name: "repository", Type: atc.ParamTypeString, Required: true}, {Name: "trunk", Type: atc.ParamTypeString, Required: true}, {Name: "entries", Type: atc.ParamTypeString, Required: true}},
		Jobs: atc.JobConfigs{{Name: "compose", PlanSequence: []atc.Step{
			{Config: &atc.TaskStep{Name: "compose", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "candidate", Output: "candidate"},
				Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "candidate"}, {Name: "manifest"}}}}},
			{Config: &atc.TaskStep{Name: "manifest", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "manifest", Output: "manifest"},
				Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Inputs: []atc.TaskInputConfig{{Name: "manifest"}}, Outputs: []atc.TaskOutputConfig{{Name: "manifest"}}}}},
		}}}})
	savePipeline("landing-land", atc.Config{Template: true,
		Params: []atc.ParamSchema{{Name: "repository", Type: atc.ParamTypeString, Required: true}, {Name: "trunk", Type: atc.ParamTypeString, Required: true}, {Name: "entries", Type: atc.ParamTypeString, Required: true}},
		Jobs: atc.JobConfigs{{Name: "land", PlanSequence: []atc.Step{
			{Config: &atc.TaskStep{Name: "land", TaskID: uuid.NewString(),
				RunInputs: []atc.RunInput{{Name: "manifest", Input: "manifest"}, {Name: "candidate", Input: "candidate"}},
				RunResult: &atc.RunResult{Name: "verdict", Output: "verdict"},
				Config:    &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Inputs: []atc.TaskInputConfig{{Name: "manifest"}, {Name: "candidate"}}, Outputs: []atc.TaskOutputConfig{{Name: "verdict"}}}}},
		}}}})

	displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
	Expect(err).NotTo(HaveOccurred())
	port := runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds, nil)
	port.SetOutputEpoch(testEpoch)
	admitter = port

	Expect(queues.SetQueue(context.Background(), team.ID(), "trunk", atc.LandingQueueConfig{
		Repository: "https://example.test/repo.git", Trunk: "core", Compose: "landing-compose", Land: "landing-land",
	})).To(Succeed())
	var found bool
	queue, found, err = queues.Queue(context.Background(), team.ID(), "trunk")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	results = &verdictResults{verdicts: map[int]landing.Verdict{}}
	engine = newEngine()
})

func newEngine() *landing.Engine {
	return &landing.Engine{
		Logger: lagertest.NewTestLogger("landing"), Conn: dbConn, Queues: queues, Runs: runFactory,
		Results: results, Port: admitter, Epoch: testEpoch,
	}
}

func savePipeline(name string, config atc.Config) db.Pipeline {
	GinkgoHelper()
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())
	return pipeline
}

// activateAdmission enables the Run contract and one Hangar output epoch,
// which admission of a template that declares results requires.
func activateAdmission() {
	GinkgoHelper()
	err := postgresrunner.ExecAsActivationRole(dbConn, `
		INSERT INTO hangar_output_activation_epochs
			(epoch_id, base_state, output_state, base_attestation, output_attestation,
			 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
			 materialization_key_id, bucket_fingerprint, derived_namespace)
		VALUES ($1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
			now() - interval '1 day', now() + interval '30 days',
			'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`, testEpoch)
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, testEpoch)
	Expect(err).NotTo(HaveOccurred())
}

// finishRun settles a Run the way the finalizer would find it, then writes
// its terminal header by fixture. For a succeeded Run each named result gets
// the lifecycle and claim rows the output plane would have registered, so a
// later admission can bind it.
func finishRun(runID int, status atc.RunStatus, resultNames ...string) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE builds SET status = $2, completed = true, end_time = now() WHERE pipeline_run_id = $1 AND NOT completed`, runID, string(status))
	Expect(err).NotTo(HaveOccurred())
	manifest := map[string]atc.RunResultBinding{}
	for _, name := range resultNames {
		digest := fmt.Sprintf("sha256:%064x", runID*7919+len(name))
		var lifecycleID int64
		Expect(dbConn.QueryRow(`INSERT INTO hangar_exact_lifecycles (scope, digest, generation, metageneration, activation_epoch, marker_version, origin, state)
			VALUES ('landing-team', $1, 1, 1, $2, 'hangar-output-v1', 'registered', 'registered') RETURNING id`, digest, testEpoch).Scan(&lifecycleID)).To(Succeed())
		claimID := uuid.NewString()
		_, err := dbConn.Exec(`INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id) VALUES ($1, $2, $3, $4)`, claimID, lifecycleID, testEpoch, fmt.Sprintf("run:%d/%s", runID, name))
		Expect(err).NotTo(HaveOccurred())
		ref, err := hangar.NewTreeRef("landing-team", hangar.Digest(digest), 1)
		Expect(err).NotTo(HaveOccurred())
		manifest[name] = atc.RunResultBinding{Ref: ref, ClaimID: output.ClaimID(claimID)}
	}
	body, err := json.Marshal(manifest)
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER run_terminal_result_match`)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		_, err := dbConn.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER run_terminal_result_match`)
		Expect(err).NotTo(HaveOccurred())
	}()
	_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = $2, completed_at = now(), result_manifest = $3, terminal_observation_version = 'fixture' WHERE id = $1`, runID, string(status), body)
	Expect(err).NotTo(HaveOccurred())
}

// verdictResults stands in for the output plane's result reader: it hands
// the engine a directory holding the verdict.json a land Run published.
type verdictResults struct {
	verdicts map[int]landing.Verdict
	reads    int
}

func (r *verdictResults) Read(_ context.Context, runID int, name string) (*hangar.CapturedTree, error) {
	r.reads++
	verdict, ok := r.verdicts[runID]
	if !ok || name != "verdict" {
		return nil, fmt.Errorf("no %s result for Run %d", name, runID)
	}
	dir := GinkgoT().TempDir()
	body, err := json.Marshal(verdict)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict.json"), body, 0o644); err != nil {
		return nil, err
	}
	return &hangar.CapturedTree{Root: dir}, nil
}

type entryView struct {
	State      atc.LandingEntryState
	Reason     string
	ComposeRun int
	LandRun    int
}

func entry(id string) entryView {
	GinkgoHelper()
	status, err := queues.Status(context.Background(), queue)
	Expect(err).NotTo(HaveOccurred())
	for _, e := range status.Entries {
		if e.ID == id {
			return entryView{State: e.State, Reason: e.SettleReason, ComposeRun: e.ComposeRun, LandRun: e.LandRun}
		}
	}
	Fail("no entry " + id)
	return entryView{}
}

func runCount(createdBy string) int {
	GinkgoHelper()
	var n int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE created_by = $1`, createdBy).Scan(&n)).To(Succeed())
	return n
}

func runIDByNumber(templateName string, number int) int {
	GinkgoHelper()
	var id int
	Expect(dbConn.QueryRow(`SELECT r.id FROM pipeline_runs r JOIN pipelines p ON p.id = r.template_pipeline_id WHERE p.name = $1 AND r.number = $2`, templateName, number).Scan(&id)).To(Succeed())
	return id
}
