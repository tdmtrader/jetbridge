package gc_test

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

	It("reaps the excess build and advances the persisted cursor", func() {
		pipeline, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "build-log-cleanup-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{
				Name:              "some-job",
				BuildLogRetention: &atc.BuildLogRetention{Builds: 1},
			}}},
			db.ConfigVersion(0),
			false,
		)
		Expect(err).NotTo(HaveOccurred())

		job, found, err := pipeline.Job("some-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		builds := make([]db.Build, 0, 2)
		for _, name := range []string{"old-build", "new-build"} {
			build, err := job.CreateBuild("collector-state-test")
			Expect(err).NotTo(HaveOccurred())
			started, err := build.Start(atc.Plan{ID: atc.PlanID(name)})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())
			Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
			builds = append(builds, build)
		}

		Expect(buildEventCount(pipeline.ID(), builds[0].ID())).To(BeNumerically(">", 0))
		Expect(buildEventCount(pipeline.ID(), builds[1].ID())).To(BeNumerically(">", 0))

		collector := NewBuildLogCollector(
			db.NewPipelineFactory(dbConn, lockFactory),
			db.NewPipelineLifecycle(dbConn, lockFactory),
			5,
			NewBuildLogRetentionCalculator(0, 0, 0, 0),
			false,
		)
		ctx := lagerctx.NewContext(context.Background(), logger)
		Expect(collector.Run(ctx)).To(Succeed())

		Expect(buildEventCount(pipeline.ID(), builds[0].ID())).To(BeZero())
		Expect(buildEventCount(pipeline.ID(), builds[1].ID())).To(BeNumerically(">", 0))
		job, found, err = pipeline.Job("some-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(job.FirstLoggedBuildID()).To(Equal(builds[1].ID()))
	})
})

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
