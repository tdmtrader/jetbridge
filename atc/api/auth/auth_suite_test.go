package auth_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAuth(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Suite")
}

var (
	logger lager.Logger

	postgresRunner postgresrunner.Runner
	dbConn         db.DbConn
	lockConns      [lock.FactoryCount]*sql.DB
	lockFactory    lock.LockFactory
	teamFactory    db.TeamFactory
	buildFactory   db.BuildFactory
	workerFactory  db.WorkerFactory
)

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	logger = lager.NewLogger("test")

	dbConn = nil
	lockConns = [lock.FactoryCount]*sql.DB{}
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	dbConn = postgresRunner.OpenConn()
	conn := dbConn
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})
	db.CleanupBaseResourceTypesCache()

	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
		lockConn := lockConns[i]
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
	buildFactory = db.NewBuildFactory(dbConn, lockFactory, 0, time.Hour)
	workerFactory = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))
})

const (
	accessTokenAudience  = "some-aud"
	accessTokenConnector = "test"
	accessTokenUserID    = "some-user"
	accessTokenSubject   = "some-sub"
	systemClaimValue     = "some-system-sub"
)

// realAccessFactory is the accessor the ATC actually wires up: bearer tokens
// are resolved against the access_tokens table, and the roles a request carries
// are computed from the auth stored on the teams it finds.
func realAccessFactory() accessor.AccessFactory {
	return accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(dbConn), []string{accessTokenAudience}),
		teamFactory,
		"sub",
		[]string{systemClaimValue},
		nil,
	)
}

// validAccessToken stores a token for accessTokenUserID and hands back the
// Authorization header carrying it. Authentication has no other source once the
// accessor is real.
func validAccessToken() string {
	GinkgoHelper()
	return persistAccessToken("valid-token", accessTokenSubject, time.Now().Add(time.Hour))
}

// expiredAccessToken is how a request carries a token and still fails
// verification, which is the only way HasToken and IsAuthenticated disagree.
func expiredAccessToken() string {
	GinkgoHelper()
	return persistAccessToken("expired-token", accessTokenSubject, time.Now().Add(-time.Hour))
}

// systemAccessToken carries the subject the access factory is configured to
// treat as the system user.
func systemAccessToken() string {
	GinkgoHelper()
	return persistAccessToken("system-token", systemClaimValue, time.Now().Add(time.Hour))
}

func persistAccessToken(token, subject string, expiry time.Time) string {
	GinkgoHelper()

	payload, err := json.Marshal(map[string]any{
		"sub": subject,
		"aud": []any{accessTokenAudience},
		"exp": expiry.Unix(),
		"federated_claims": map[string]any{
			"connector_id": accessTokenConnector,
			"user_id":      accessTokenUserID,
		},
	})
	Expect(err).NotTo(HaveOccurred())

	var claims db.Claims
	Expect(json.Unmarshal(payload, &claims)).To(Succeed())
	Expect(db.NewAccessTokenFactory(dbConn).CreateAccessToken(token, claims)).To(Succeed())

	return "bearer " + token
}

// grantRole writes the team auth that makes a real accessor authorized: the
// role is matched against the connector and user id the token claims.
func grantRole(team db.Team, role string) {
	GinkgoHelper()

	Expect(team.UpdateProviderAuth(atc.TeamAuth{
		role: {"users": {accessTokenConnector + ":" + accessTokenUserID}},
	})).To(Succeed())
}

// makeAdmin supplies both halves accessor.IsAdmin insists on: the team is an
// administrator team, and the user owns it.
func makeAdmin(team db.Team) {
	GinkgoHelper()

	grantRole(team, accessor.OwnerRole)

	result, err := dbConn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID())
	Expect(err).NotTo(HaveOccurred())

	rowsAffected, err := result.RowsAffected()
	Expect(err).NotTo(HaveOccurred())
	Expect(rowsAffected).To(Equal(int64(1)))
}

func createTeam(name string) db.Team {
	team, err := teamFactory.CreateTeam(atc.Team{Name: name})
	Expect(err).NotTo(HaveOccurred())
	return team
}

// createPipeline saves a pipeline owned by team. Visibility is deliberately not
// a parameter: it is not a field on atc.Config but a column, flipped by
// Expose()/Hide() afterwards.
func createPipeline(team db.Team, name string) db.Pipeline {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	return pipeline
}

// createJobBuildWithConfig is createJobBuild with control over the job's
// config, so a spec can make the job public -- atc.JobConfig.Public is real and
// lands in jobs.public, unlike pipeline visibility which is Expose()/Hide().
func createJobBuildWithConfig(team db.Team, pipelineName string, job atc.JobConfig) (db.Pipeline, db.Build) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		atc.Config{Jobs: atc.JobConfigs{job}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	dbJob, found, err := pipeline.Job(job.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	build, err := dbJob.CreateBuild("some-user")
	Expect(err).NotTo(HaveOccurred())
	return pipeline, build
}

// createJobBuild gives a build that belongs to a job in a pipeline, which is
// what the build-access handlers scope against.
func createJobBuild(team db.Team, pipelineName, jobName string) db.Build {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		atc.Config{Jobs: atc.JobConfigs{{Name: jobName}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	job, found, err := pipeline.Job(jobName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	build, err := job.CreateBuild("some-user")
	Expect(err).NotTo(HaveOccurred())
	return build
}

// doomedWorkerFactory is the worker-side counterpart of doomedTeamFactory.
func doomedWorkerFactory() db.WorkerFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewWorkerFactory(doomed, db.NewStaticWorkerCache(logger, doomed, 0))
	Expect(doomed.Close()).To(Succeed())
	return factory
}

// doomedBuildFactory is the build-side counterpart of doomedTeamFactory.
func doomedBuildFactory() db.BuildFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewBuildFactory(doomed, lockFactory, 0, time.Hour)
	Expect(doomed.Close()).To(Succeed())
	return factory
}

// doomedTeamFactory returns a factory whose connection is already closed, so
// every lookup through it fails the way a database outage would. It opens its
// own connection: AfterEach asserts the suite's closes cleanly.
func doomedTeamFactory() db.TeamFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewTeamFactory(doomed, lockFactory)
	Expect(doomed.Close()).To(Succeed())
	return factory
}
