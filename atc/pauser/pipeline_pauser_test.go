package pauser_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/pauser"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

func TestPauser(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pauser Suite")
}

var _ = Describe("PipelinePauser", func() {
	var (
		pauseComp component.Runnable
		dbConn    db.DbConn
		team      db.Team
		dbPauser  db.PipelinePauser
	)

	BeforeEach(func() {
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(postgresRunner.DropTestDB)

		dbConn = postgresRunner.OpenConn()
		conn := dbConn
		DeferCleanup(func() {
			Expect(conn.Close()).To(Succeed())
		})

		db.CleanupBaseResourceTypesCache()

		var lockConns [lock.FactoryCount]*sql.DB
		for i := 0; i < lock.FactoryCount; i++ {
			lockConns[i] = postgresRunner.OpenSingleton()
			lockConn := lockConns[i]
			DeferCleanup(func() {
				Expect(lockConn.Close()).To(Succeed())
			})
		}
		ignore := func(lager.Logger, lock.LockID) {}
		lockFactory := lock.NewLockFactory(lockConns, ignore, ignore)

		var err error
		team, err = db.NewTeamFactory(dbConn, lockFactory).CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).NotTo(HaveOccurred())

		dbPauser = db.NewPipelinePauser(dbConn, lockFactory)
	})

	// idlePipeline is an unpaused pipeline that was last set daysAgo days ago
	// and whose only job last completed a build then too, which is what the
	// pauser's query looks at.
	idlePipeline := func(name string, daysAgo int) db.Pipeline {
		GinkgoHelper()

		pipeline, _, err := team.SavePipeline(
			atc.PipelineRef{Name: name},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			db.ConfigVersion(0),
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(pipeline.Paused()).To(BeFalse(), "pipeline should start unpaused")

		_, err = dbConn.Exec(`UPDATE pipelines SET last_updated = NOW() - $1 * INTERVAL '1 DAY' WHERE id = $2`, daysAgo, pipeline.ID())
		Expect(err).NotTo(HaveOccurred())

		job, found, err := pipeline.Job("some-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		build, err := job.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

		_, err = dbConn.Exec(`UPDATE builds SET end_time = NOW() - $1 * INTERVAL '1 DAY' WHERE id = $2`, daysAgo, build.ID())
		Expect(err).NotTo(HaveOccurred())

		return pipeline
	}

	paused := func(pipeline db.Pipeline) bool {
		GinkgoHelper()

		_, err := pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		return pipeline.Paused()
	}

	Describe("Run", func() {
		It("pauses the pipelines that have been idle longer than the configured days", func() {
			stale := idlePipeline("stale-pipeline", 15)
			recent := idlePipeline("recent-pipeline", 5)

			pauseComp = pauser.NewPipelinePauser(dbPauser, 10)
			Expect(pauseComp.Run(context.TODO())).To(Succeed())

			Expect(paused(stale)).To(BeTrue(), "idle for 15 days, should be paused")
			Expect(stale.PausedBy()).To(Equal("automatic-pipeline-pauser"))
			Expect(paused(recent)).To(BeFalse(), "idle for 5 days, should not be paused")
		})

		It("it short circuts if days is zero", func() {
			// Zero days would otherwise match every pipeline set before today,
			// so the short circuit is the only thing standing between this
			// setting and a paused installation.
			pipelines := []db.Pipeline{
				idlePipeline("idle-pipeline", 1),
				idlePipeline("ancient-pipeline", 400),
			}

			pauseComp = pauser.NewPipelinePauser(dbPauser, 0)
			Expect(pauseComp.Run(context.TODO())).To(Succeed())

			for _, pipeline := range pipelines {
				Expect(paused(pipeline)).To(BeFalse(), fmt.Sprintf("%s should not have been paused", pipeline.Name()))
			}
		})
	})
})
