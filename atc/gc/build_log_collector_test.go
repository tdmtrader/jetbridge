package gc_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
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
		}), // A job may declare min_success_builds without declaring builds:
		// configvalidate's min_success_builds > builds check is gated on
		// builds > 0, so this config is accepted by the config API and by the
		// set_pipeline step, and the count then comes from
		// --default-build-logs-to-retain. Unbounded, the min-succeeded arm retains
		// b3 and b2 WITHOUT appending either to the keep list, so the trailing
		// over-retention correction computes delta=1 against an empty list and
		// evaluates candidateBuildIDsToKeep[-1].
		Entry("min-succeeded above the default budget does not run off the keep list", retentionScenario{
			jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{MinimumSucceededBuilds: 2}},
			calculator:   NewBuildLogRetentionCalculator(1, 0, 0, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b2", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
				{name: "b3", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
			},
			expectedDeleted:     []string{"b1", "b2"},
			expectedFirstLogged: "b3",
		}),
		// An absurd --default-days-to-retain-build-logs truncates from uint64 to
		// int as -1, and AddDate(0, 0, -1) is in the past for every finished
		// build, so the days arm deletes the job's whole log history with no
		// panic and no error. Bounded, the same flag retains everything.
		Entry("an absurd days default retains rather than wiping the history", retentionScenario{
			jobRetention: atc.JobConfig{},
			calculator:   NewBuildLogRetentionCalculator(0, 0, math.MaxUint64, 0),
			builds: []retentionBuild{
				{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 49 * time.Hour},
				{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 23 * time.Hour},
			},
			expectedDeleted:     []string{},
			expectedFirstLogged: "b1",
		}),
	)

	Describe("numbered runs", func() {
		DescribeTable("uses the ordinary retention decisions across runs", runNumberedRetentionScenario,
			Entry("days", retentionScenario{
				jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Days: 1}},
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
			Entry("running rows", retentionScenario{
				jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 3}},
				calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
				builds: []retentionBuild{
					{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
					{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
					{name: "b3", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
					{name: "b4", status: db.BuildStatusStarted, drained: true},
					{name: "b5", status: db.BuildStatusStarted, drained: true},
					{name: "b6", status: db.BuildStatusSucceeded, completed: true, drained: true, endAgo: 2 * time.Hour},
				},
				expectedDeleted:     []string{"b1"},
				expectedFirstLogged: "b2",
			}),
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
			Entry("paused template", retentionScenario{
				jobRetention:   atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
				calculator:     NewBuildLogRetentionCalculator(0, 0, 0, 0),
				pausedPipeline: true,
				builds: []retentionBuild{
					{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
					{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				},
				expectedFirstLogged: "b1",
			}),
			Entry("paused base job", retentionScenario{
				jobRetention: atc.JobConfig{BuildLogRetention: &atc.BuildLogRetention{Builds: 1}},
				calculator:   NewBuildLogRetentionCalculator(0, 0, 0, 0),
				pausedJob:    true,
				builds: []retentionBuild{
					{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
					{name: "b2", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour},
				},
				expectedFirstLogged: "b1",
			}),
		)

		It("applies one global budget after every page across dynamic live and reclaimed runs", func() {
			ttl := 1
			template := saveRunLogTemplate("global-budget", atc.JobConfig{
				Name: "deploy-((environment))", BuildLogRetention: &atc.BuildLogRetention{Builds: 3},
			}, &atc.RunRetentionConfig{TTLDays: &ttl})
			builds := make([]db.Build, 8)
			var oldestRun db.PipelineRun
			for i := range builds {
				run, build := createNumberedLogBuild(template, fmt.Sprintf("environment-%d", i), retentionBuild{
					name: fmt.Sprintf("b%d", i+1), status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 48 * time.Hour,
				})
				if i == 0 {
					oldestRun = run
				}
				builds[i] = build
			}
			_, err := dbConn.Exec("UPDATE pipeline_runs SET status = 'failed', completed_at = now() - interval '2 days' WHERE id = $1", oldestRun.ID())
			Expect(err).NotTo(HaveOccurred())
			destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(oldestRun.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(destroyed).To(BeTrue())

			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)

			for i, build := range builds {
				if i < 5 {
					Expect(numberedBuildEventCount(build.ID())).To(BeZero(), "old build %d must be reaped by the one global budget", i+1)
				} else {
					Expect(numberedBuildEventCount(build.ID())).To(Equal(1), "new build %d must be globally retained", i+1)
				}
			}
			job := reloadRunLogJob(template, "deploy-((environment))")
			Expect(job.FirstLoggedBuildID()).To(Equal(builds[5].ID()))
		})

		It("uses current tightening, cannot restore after loosening, and leaves absent keys untouched until they reappear", func() {
			template := saveRunLogTemplate("current-policy", atc.JobConfig{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 4}}, nil)
			builds := make([]db.Build, 6)
			for i := range builds {
				_, builds[i] = createNumberedLogBuild(template, "", retentionBuild{name: fmt.Sprintf("b%d", i+1), status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour})
			}

			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)
			expectRunEventPresence(builds, []bool{false, false, true, true, true, true})

			var err error
			template, _, err = defaultTeam.SavePipeline(template.PipelineRef(), atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "other", BuildLogRetention: &atc.BuildLogRetention{Builds: 1}}}}, template.ConfigVersion(), false)
			Expect(err).NotTo(HaveOccurred())
			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)
			expectRunEventPresence(builds, []bool{false, false, true, true, true, true})

			template, _, err = defaultTeam.SavePipeline(template.PipelineRef(), atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 2}}}}, template.ConfigVersion(), false)
			Expect(err).NotTo(HaveOccurred())
			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)
			expectRunEventPresence(builds, []bool{false, false, false, false, true, true})

			template, _, err = defaultTeam.SavePipeline(template.PipelineRef(), atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 5}}}}, template.ConfigVersion(), false)
			Expect(err).NotTo(HaveOccurred())
			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)
			expectRunEventPresence(builds, []bool{false, false, false, false, true, true})
		})

		It("revisits an undrained build through the base cursor after newer logs were reaped", func() {
			template := saveRunLogTemplate("late-drain", atc.JobConfig{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 1}}, nil)
			builds := make([]db.Build, 4)
			for i := range builds {
				drained := i != 0
				_, builds[i] = createNumberedLogBuild(template, "", retentionBuild{name: fmt.Sprintf("b%d", i+1), status: db.BuildStatusFailed, completed: true, drained: drained, endAgo: 2 * time.Hour})
			}

			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), true)
			expectRunEventPresence(builds, []bool{true, false, false, true})
			Expect(reloadRunLogJob(template, "source").FirstLoggedBuildID()).To(Equal(builds[0].ID()))

			Expect(builds[0].SetDrained(true)).To(Succeed())
			runBuildLogCollector(2, NewBuildLogRetentionCalculator(0, 0, 0, 0), true)
			expectRunEventPresence(builds, []bool{false, false, false, true})
			Expect(reloadRunLogJob(template, "source").FirstLoggedBuildID()).To(Equal(builds[3].ID()))
		})

		It("leaves team events and the base cursor untouched when numbered deletion fails", func() {
			template := saveRunLogTemplate("delete-fault", atc.JobConfig{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 1}}, nil)
			builds := make([]db.Build, 2)
			for i := range builds {
				_, builds[i] = createNumberedLogBuild(template, "", retentionBuild{name: fmt.Sprintf("b%d", i+1), status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour})
			}
			faultLogger := lagertest.NewTestLogger("numbered-delete-fault")
			factory := decoratedPipelines{db.NewPipelineFactory(dbConn, lockFactory), func(p db.Pipeline) db.Pipeline {
				if p.Template() {
					return failDeleteRunBuildEvents{p}
				}
				return p
			}}
			collector := NewBuildLogCollector(factory, db.NewPipelineLifecycle(dbConn, lockFactory), 2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)

			Expect(collector.Run(lagerctx.NewContext(context.Background(), faultLogger))).To(Succeed())
			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			expectRunEventPresence(builds, []bool{true, true})
			Expect(reloadRunLogJob(template, "source").FirstLoggedBuildID()).To(BeZero())
		})

		It("leaves the base cursor untouched when numbered history lookup fails", func() {
			template := saveRunLogTemplate("query-fault", atc.JobConfig{Name: "source", BuildLogRetention: &atc.BuildLogRetention{Builds: 1}}, nil)
			_, build := createNumberedLogBuild(template, "", retentionBuild{name: "b1", status: db.BuildStatusFailed, completed: true, drained: true, endAgo: 2 * time.Hour})
			faultLogger := lagertest.NewTestLogger("numbered-query-fault")
			factory := decoratedPipelines{db.NewPipelineFactory(dbConn, lockFactory), func(p db.Pipeline) db.Pipeline {
				if p.Template() {
					return failChronoRunBuilds{p}
				}
				return p
			}}
			collector := NewBuildLogCollector(factory, db.NewPipelineLifecycle(dbConn, lockFactory), 2, NewBuildLogRetentionCalculator(0, 0, 0, 0), false)

			Expect(collector.Run(lagerctx.NewContext(context.Background(), faultLogger))).To(Succeed())
			Eventually(faultLogger.Buffer()).Should(gbytes.Say(errDisaster.Error()))
			Expect(numberedBuildEventCount(build.ID())).To(Equal(1))
			Expect(reloadRunLogJob(template, "source").FirstLoggedBuildID()).To(BeZero())
		})
	})

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

type failDeleteRunBuildEvents struct{ db.Pipeline }

func (failDeleteRunBuildEvents) DeleteRunBuildEventsByBuildIDs([]int) error { return errDisaster }

type failChronoRunBuilds struct{ db.Pipeline }

func (failChronoRunBuilds) ChronoRunBuilds(string, db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	return nil, db.Pagination{}, errDisaster
}

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

func runNumberedRetentionScenario(scenario retentionScenario) {
	jobConfig := scenario.jobRetention
	jobConfig.Name = "some-job"
	template := saveRunLogTemplate("numbered-retention", jobConfig, nil)
	buildsByName := make(map[string]db.Build, len(scenario.builds))
	for _, buildSpec := range scenario.builds {
		_, build := createNumberedLogBuild(template, buildSpec.name, buildSpec)
		buildsByName[buildSpec.name] = build
	}
	if len(scenario.builds) > 0 {
		_, err := dbConn.Exec("UPDATE jobs SET first_logged_build_id = $1 WHERE pipeline_id = $2 AND name = $3", buildsByName[scenario.builds[0].name].ID(), template.ID(), jobConfig.Name)
		Expect(err).NotTo(HaveOccurred())
	}

	job := reloadRunLogJob(template, jobConfig.Name)
	if scenario.pausedPipeline {
		Expect(template.Pause("collector-test")).To(Succeed())
	}
	if scenario.pausedJob {
		Expect(job.Pause("collector-test")).To(Succeed())
	}

	runBuildLogCollector(2, scenario.calculator, scenario.drainerConfigured)
	deleted := []string{}
	for _, buildSpec := range scenario.builds {
		if numberedBuildEventCount(buildsByName[buildSpec.name].ID()) == 0 {
			deleted = append(deleted, buildSpec.name)
		}
	}
	Expect(deleted).To(ConsistOf(scenario.expectedDeleted))

	job = reloadRunLogJob(template, jobConfig.Name)
	if scenario.expectedFirstLogged == "" {
		Expect(job.FirstLoggedBuildID()).To(BeZero())
	} else {
		Expect(job.FirstLoggedBuildID()).To(Equal(buildsByName[scenario.expectedFirstLogged].ID()))
	}
}

func saveRunLogTemplate(name string, job atc.JobConfig, retention *atc.RunRetentionConfig) db.Pipeline {
	GinkgoHelper()
	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{
		Template:     true,
		Params:       []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString}},
		RunRetention: retention,
		Jobs:         atc.JobConfigs{job},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	return template
}

func createNumberedLogBuild(template db.Pipeline, environment string, spec retentionBuild) (db.PipelineRun, db.Build) {
	GinkgoHelper()
	creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{
		Vars: atc.RunParams{"environment": environment},
	}, "creator")
	Expect(err).NotTo(HaveOccurred())
	Expect(creation.EntryBuilds).To(HaveLen(1))
	build := creation.EntryBuilds[0]
	Expect(build.SaveEvent(event.Log{Payload: spec.name})).To(Succeed())

	var endTime any
	if spec.endAgo > 0 {
		endTime = time.Now().Add(-spec.endAgo)
	}
	_, err = dbConn.Exec(`
		UPDATE builds
		SET status = $2, completed = $3, drained = $4, end_time = $5
		WHERE id = $1
	`, build.ID(), spec.status, spec.completed, spec.drained, endTime)
	Expect(err).NotTo(HaveOccurred())
	if spec.reapAgo > 0 {
		_, err = dbConn.Exec("UPDATE builds SET reap_time = $2 WHERE id = $1", build.ID(), time.Now().Add(-spec.reapAgo))
		Expect(err).NotTo(HaveOccurred())
	}
	return creation.Run, build
}

func runBuildLogCollector(batchSize int, calculator BuildLogRetentionCalculator, drainerConfigured bool) {
	GinkgoHelper()
	collector := NewBuildLogCollector(
		db.NewPipelineFactory(dbConn, lockFactory),
		db.NewPipelineLifecycle(dbConn, lockFactory),
		batchSize,
		calculator,
		drainerConfigured,
	)
	Expect(collector.Run(lagerctx.NewContext(context.Background(), logger))).To(Succeed())
}

func reloadRunLogJob(template db.Pipeline, name string) db.Job {
	GinkgoHelper()
	job, found, err := template.Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return job
}

func numberedBuildEventCount(buildID int) int {
	GinkgoHelper()
	var count int
	Expect(dbConn.QueryRow(fmt.Sprintf("SELECT count(*) FROM team_build_events_%d WHERE build_id = $1", defaultTeam.ID()), buildID).Scan(&count)).To(Succeed())
	return count
}

func expectRunEventPresence(builds []db.Build, expected []bool) {
	GinkgoHelper()
	Expect(builds).To(HaveLen(len(expected)))
	for i, build := range builds {
		Expect(numberedBuildEventCount(build.ID()) > 0).To(Equal(expected[i]), "build %d event presence", i+1)
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
