package gc_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

type retentionBuild struct {
	name      string
	status    db.BuildStatus
	completed bool
	drained   bool
	endAgo    time.Duration
	reapAgo   time.Duration
}

type retentionScenario struct {
	jobRetention        atc.JobConfig
	calculator          BuildLogRetentionCalculator
	drainerConfigured   bool
	pausedPipeline      bool
	pausedJob           bool
	builds              []retentionBuild
	expectedDeleted     []string
	expectedFirstLogged string
}

var _ = Describe("BuildLogCollector", func() {
	DescribeTable("persisted PostgreSQL retention", runRetentionScenario,
		Entry("drain filters", retentionScenario{
			jobRetention:      atc.JobConfig{BuildLogsToRetain: 2},
			calculator:        NewBuildLogRetentionCalculator(0, 0, 0, 0),
			drainerConfigured: true,
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b4", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b5", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b6", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b7", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1", "b3"},
			expectedFirstLogged: "b2",
		}),
		Entry("drain disabled", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogsToRetain: 2},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b4", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b5", status: db.BuildStatusFailed, completed: true, drained: false, endAgo: 2 * time.Hour},
				{name: "b6", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1", "b2", "b3", "b4"},
			expectedFirstLogged: "b5",
		}),
		Entry("running rows survive", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogsToRetain: 3},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b4", status: db.BuildStatusStarted, completed: false, drained: true},
				{name: "b5", status: db.BuildStatusStarted, completed: false, drained: true},
				{name: "b6", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1"},
			expectedFirstLogged: "b2",
		}),
		Entry("no eligible reap", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogsToRetain: 2},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusStarted, completed: false, drained: true},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("no builds", retentionScenario{
			jobRetention:        atc.JobConfig{BuildLogsToRetain: 2},
			calculator:          NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds:              []retentionBuild{},
			expectedDeleted:     []string{},
			expectedFirstLogged: "",
		}),
		Entry("count only", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{"b1"},
			expectedFirstLogged: "b2",
		}),
		Entry("days satisfied", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Days: 1}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{"b1"},
			expectedFirstLogged: "b2",
		}),
		Entry("days protect both", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Days: 3}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("combined count protects", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1, Days: 2}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("combined days protect", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1, Days: 2}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 24 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("combined deletes", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1, Days: 2}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{"b1"},
			expectedFirstLogged: "b2",
		}),
		Entry("minimum success", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 3, MinimumSucceededBuilds: 2}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b4", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b5", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1", "b3"},
			expectedFirstLogged: "b2",
		}),
		Entry("already reaped excluded", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour, reapAgo: time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{"b2"},
			expectedFirstLogged: "b3",
		}),
		Entry("all eligible", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Days: 1}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 30 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 30 * time.Hour},
			},
			expectedDeleted:     []string{"b1", "b2"},
			expectedFirstLogged: "b1",
		}),
		Entry("retain zero skips", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 0, Days: 0}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("calculator cap", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogsToRetain: 10},
			calculator:   NewBuildLogRetentionCalculator(3, 3, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b4", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1"},
			expectedFirstLogged: "b2",
		}),
		Entry("paused pipeline", retentionScenario{
			jobRetention:   atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
			calculator:     NewBuildLogRetentionCalculator(0, 0, 0, 0),
			pausedPipeline: true,
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
		Entry("paused job", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
			calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
			pausedJob:    true,
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
	)

	// Each spec below decorates the real, PostgreSQL-backed collaborator so that
	// exactly one call fails. Every other read and write still reaches the
	// database, so what the collector did before and after the fault is visible
	// as real row state.
	Describe("database faults", func() {
		var (
			pipelineFactory   db.PipelineFactory
			pipelineLifecycle db.PipelineLifecycle
			job               db.Job
			faultLogger       *lagertest.TestLogger
			ctx               context.Context
		)

		collectorFor := func(factory db.PipelineFactory, lifecycle db.PipelineLifecycle) GcCollector {
			return NewBuildLogCollector(factory, lifecycle, 5, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)
		}

		firstLoggedBuildID := func() int {
			var id int
			Expect(dbConn.QueryRow("SELECT first_logged_build_id FROM jobs WHERE id = $1", job.ID()).Scan(&id)).To(Succeed())
			return id
		}

		buildEventCount := func() int {
			var count int
			Expect(dbConn.QueryRow("SELECT count(*) FROM build_events").Scan(&count)).To(Succeed())
			return count
		}

		BeforeEach(func() {
			pipelineFactory = db.NewPipelineFactory(dbConn, lockFactory)
			pipelineLifecycle = db.NewPipelineLifecycle(dbConn, lockFactory)

			pipeline, _, err := defaultTeam.SavePipeline(
				atc.PipelineRef{Name: "build-log-fault-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{
					Name:              "some-job",
					BuildLogRetention: &atc.BuildLogRetention{Builds: 1},
				}}},
				db.ConfigVersion(0),
				false,
			)
			Expect(err).NotTo(HaveOccurred())

			var found bool
			job, found, err = pipeline.Job("some-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			for _, name := range []string{"old-build", "new-build"} {
				build, err := job.CreateBuild("collector-fault-test")
				Expect(err).NotTo(HaveOccurred())
				started, err := build.Start(atc.Plan{ID: atc.PlanID(name)})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
			}

			faultLogger = lagertest.NewTestLogger("build-log-collector-fault")
			ctx = lagerctx.NewContext(context.Background(), faultLogger)
		})

		It("reaps the excess build and advances the cursor when nothing fails", func() {
			before := buildEventCount()

			Expect(collectorFor(pipelineFactory, pipelineLifecycle).Run(ctx)).To(Succeed())

			Expect(buildEventCount()).To(BeNumerically("<", before))
			Expect(firstLoggedBuildID()).To(BeNumerically(">", 0))
		})

		It("returns a deleted-pipeline cleanup error", func() {
			err := collectorFor(pipelineFactory, failRemoveBuildEventsForDeletedPipelines{pipelineLifecycle}).Run(ctx)

			Expect(err).To(MatchError(errDisaster))
			Expect(firstLoggedBuildID()).To(BeZero())
		})

		It("returns an all-pipelines lookup error", func() {
			err := collectorFor(failAllPipelines{pipelineFactory}, pipelineLifecycle).Run(ctx)

			Expect(err).To(MatchError(errDisaster))
			Expect(firstLoggedBuildID()).To(BeZero())
		})

		It("logs a pipeline jobs lookup error", func() {
			factory := decoratedPipelines{pipelineFactory, func(p db.Pipeline) db.Pipeline {
				return failPipelineJobs{p}
			}}

			Expect(collectorFor(factory, pipelineLifecycle).Run(ctx)).To(Succeed())

			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			Expect(firstLoggedBuildID()).To(BeZero())
		})

		It("logs an event deletion error and leaves the cursor where it was", func() {
			factory := decoratedPipelines{pipelineFactory, func(p db.Pipeline) db.Pipeline {
				return failDeleteBuildEvents{p}
			}}
			before := buildEventCount()

			Expect(collectorFor(factory, pipelineLifecycle).Run(ctx)).To(Succeed())

			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			Expect(buildEventCount()).To(Equal(before))
			Expect(firstLoggedBuildID()).To(BeZero())
		})

		It("logs a chronological build lookup error", func() {
			factory := decoratedPipelines{pipelineFactory, func(p db.Pipeline) db.Pipeline {
				return decoratedJobs{p, func(j db.Job) db.Job { return failChronoBuilds{j} }}
			}}

			Expect(collectorFor(factory, pipelineLifecycle).Run(ctx)).To(Succeed())

			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			Expect(firstLoggedBuildID()).To(BeZero())
		})

		It("logs a first-logged cursor update error", func() {
			factory := decoratedPipelines{pipelineFactory, func(p db.Pipeline) db.Pipeline {
				return decoratedJobs{p, func(j db.Job) db.Job { return failUpdateFirstLoggedBuildID{j} }}
			}}

			Expect(collectorFor(factory, pipelineLifecycle).Run(ctx)).To(Succeed())

			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			Expect(firstLoggedBuildID()).To(BeZero())
		})
	})
})

var errDisaster = errors.New("major malfunction")

// decoratedPipelines and decoratedJobs keep the real lookup and wrap only the
// rows it returns, so a fault can be aimed at one method of one collaborator.
type decoratedPipelines struct {
	db.PipelineFactory
	decorate func(db.Pipeline) db.Pipeline
}

func (d decoratedPipelines) AllPipelines() ([]db.Pipeline, error) {
	pipelines, err := d.PipelineFactory.AllPipelines()
	if err != nil {
		return nil, err
	}
	for i, pipeline := range pipelines {
		pipelines[i] = d.decorate(pipeline)
	}
	return pipelines, nil
}

type decoratedJobs struct {
	db.Pipeline
	decorate func(db.Job) db.Job
}

func (d decoratedJobs) Jobs() (db.Jobs, error) {
	jobs, err := d.Pipeline.Jobs()
	if err != nil {
		return nil, err
	}
	for i, job := range jobs {
		jobs[i] = d.decorate(job)
	}
	return jobs, nil
}

type failRemoveBuildEventsForDeletedPipelines struct{ db.PipelineLifecycle }

func (failRemoveBuildEventsForDeletedPipelines) RemoveBuildEventsForDeletedPipelines() error {
	return errDisaster
}

type failAllPipelines struct{ db.PipelineFactory }

func (failAllPipelines) AllPipelines() ([]db.Pipeline, error) { return nil, errDisaster }

type failPipelineJobs struct{ db.Pipeline }

func (failPipelineJobs) Jobs() (db.Jobs, error) { return nil, errDisaster }

type failDeleteBuildEvents struct{ db.Pipeline }

func (failDeleteBuildEvents) DeleteBuildEventsByBuildIDs([]int) error { return errDisaster }

type failChronoBuilds struct{ db.Job }

func (failChronoBuilds) ChronoBuilds(db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	return nil, db.Pagination{}, errDisaster
}

type failUpdateFirstLoggedBuildID struct{ db.Job }

func (failUpdateFirstLoggedBuildID) UpdateFirstLoggedBuildID(int) error { return errDisaster }

func runRetentionScenario(scenario retentionScenario) {
	fixtureTime := time.Now()

	team, err := teamFactory.CreateTeam(atc.Team{Name: "build-log-retention-team"})
	Expect(err).NotTo(HaveOccurred())

	jobConfig := scenario.jobRetention
	jobConfig.Name = "some-job"
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "build-log-retention-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{jobConfig}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	job, found, err := pipeline.Job(jobConfig.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	buildsByName := make(map[string]db.Build, len(scenario.builds))
	for _, buildSpec := range scenario.builds {
		build, err := job.CreateBuild("collector-test")
		Expect(err).NotTo(HaveOccurred())

		started, err := build.Start(atc.Plan{ID: atc.PlanID("log-" + buildSpec.name)})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())

		if buildSpec.completed {
			Expect(build.Finish(buildSpec.status)).To(Succeed())
		}
		Expect(build.SetDrained(buildSpec.drained)).To(Succeed())

		if buildSpec.endAgo > 0 {
			_, err = dbConn.Exec(
				"UPDATE builds SET end_time = $1 WHERE id = $2",
				fixtureTime.Add(-buildSpec.endAgo),
				build.ID(),
			)
			Expect(err).NotTo(HaveOccurred())
		}
		if buildSpec.reapAgo > 0 {
			_, err = dbConn.Exec(
				"UPDATE builds SET reap_time = $1 WHERE id = $2",
				fixtureTime.Add(-buildSpec.reapAgo),
				build.ID(),
			)
			Expect(err).NotTo(HaveOccurred())
		}

		buildsByName[buildSpec.name] = build
	}

	if len(scenario.builds) > 0 {
		oldestBuild := buildsByName[scenario.builds[0].name]
		_, err = dbConn.Exec(
			"UPDATE jobs SET first_logged_build_id = $1 WHERE id = $2",
			oldestBuild.ID(),
			job.ID(),
		)
		Expect(err).NotTo(HaveOccurred())
	}

	job, found, err = pipeline.Job(jobConfig.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	if scenario.pausedPipeline {
		Expect(pipeline.Pause("collector-test")).To(Succeed())
		found, err = pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(pipeline.Paused()).To(BeTrue())
	}
	if scenario.pausedJob {
		Expect(job.Pause("collector-test")).To(Succeed())
		job, found, err = pipeline.Job(jobConfig.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(job.Paused()).To(BeTrue())
	}

	for _, buildSpec := range scenario.builds {
		Expect(buildEventCount(pipeline.ID(), buildsByName[buildSpec.name].ID())).To(
			BeNumerically(">", 0),
			"expected %s to have a seeded build event",
			buildSpec.name,
		)
	}

	ctx := lagerctx.NewContext(context.Background(), logger)
	collector := NewBuildLogCollector(
		db.NewPipelineFactory(dbConn, lockFactory),
		db.NewPipelineLifecycle(dbConn, lockFactory),
		5,
		scenario.calculator,
		scenario.drainerConfigured,
	)
	Expect(collector.Run(ctx)).To(Succeed())

	deletedNames := []string{}
	for _, buildSpec := range scenario.builds {
		if buildEventCount(pipeline.ID(), buildsByName[buildSpec.name].ID()) == 0 {
			deletedNames = append(deletedNames, buildSpec.name)
		}
	}
	Expect(deletedNames).To(ConsistOf(scenario.expectedDeleted))

	job, found, err = pipeline.Job(jobConfig.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	if scenario.expectedFirstLogged == "" {
		Expect(job.FirstLoggedBuildID()).To(BeZero())
	} else {
		Expect(job.FirstLoggedBuildID()).To(Equal(buildsByName[scenario.expectedFirstLogged].ID()))
	}
}

func buildEventCount(pipelineID int, buildID int) int {
	var count int
	err := dbConn.QueryRow(
		fmt.Sprintf("SELECT count(*) FROM pipeline_build_events_%d WHERE build_id = $1", pipelineID),
		buildID,
	).Scan(&count)
	Expect(err).NotTo(HaveOccurred())
	return count
}
