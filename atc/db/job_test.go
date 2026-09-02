package db_test

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/tracing"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var _ = Describe("Job", func() {
	var (
		job      db.Job
		pipeline db.Pipeline
		team     db.Team
	)

	BeforeEach(func() {
		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).ToNot(HaveOccurred())

		var created bool
		pipeline, created, err = team.SavePipeline(atc.PipelineRef{Name: "fake-pipeline"}, atc.Config{
			Jobs: atc.JobConfigs{
				{
					Name: "some-job",

					Public: true,

					PlanSequence: []atc.Step{
						{
							Config: &atc.PutStep{
								Name: "some-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
							},
						},
						{
							Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "some-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
								Passed:  []string{"job-1", "job-2"},
								Trigger: true,
							},
						},
						{
							Config: &atc.TaskStep{
								Name:       "some-task",
								Privileged: true,
								ConfigPath: "some/config/path.yml",
								Config: &atc.TaskConfig{
									RootfsURI: "some-image",
								},
							},
						},
					},
				},
				{
					Name: "some-other-job",
				},
				{
					Name:   "some-private-job",
					Public: false,
				},
				{
					Name: "other-serial-group-job",
				},
				{
					Name: "different-serial-group-job",
				},
				{
					Name: "job-1",
				},
				{
					Name: "job-2",
				},
				{
					Name:                 "non-triggerable-job",
					DisableManualTrigger: true,
				},
			},
			Resources: atc.ResourceConfigs{
				{
					Name: "some-resource",
					Type: "some-type",
				},
				{
					Name: "some-other-resource",
					Type: "some-type",
				},
			},
		}, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(created).To(BeTrue())

		var found bool
		job, found, err = pipeline.Job("some-job")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	Describe("Builds", func() {
		var (
			builds       [10]db.Build
			someJob      db.Job
			someOtherJob db.Job
		)
		_ = builds
		_ = someJob
		_ = someOtherJob

		BeforeEach(func() {
			for i := 0; i < 10; i++ {
				var found bool
				var err error
				someJob, found, err = pipeline.Job("some-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				someOtherJob, found, err = pipeline.Job("some-other-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

			}
		})

	})

	Describe("BuildsWithTime", func() {

		var (
			pipeline db.Pipeline
			builds   = make([]db.BuildForAPI, 4)
			job      db.Job
		)

		BeforeEach(func() {
			var (
				err   error
				found bool
			)

			config := atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
					},
					{
						Name: "some-other-job",
					},
				},
			}
			pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, config, db.ConfigVersion(1), false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err = pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			for i := range builds {
				builds[i], err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				buildStart := time.Date(2020, 11, i+1, 0, 0, 0, 0, time.UTC)
				_, err = dbConn.Exec("UPDATE builds SET start_time = to_timestamp($1) WHERE id = $2", buildStart.Unix(), builds[i].ID())
				Expect(err).NotTo(HaveOccurred())

				builds[i], found, err = job.Build(builds[i].Name())
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			}
		})

		Context("when not providing boundaries", func() {
			Context("without a limit specified", func() {
			})

			Context("when a limit specified", func() {
			})
		})

		Context("when providing boundaries", func() {
			Context("only to", func() {
			})

			Context("only from", func() {
			})

			Context("from and to", func() {
			})
		})
	})

	Describe("ScheduleBuild", func() {
		var (
			schedulingBuild            db.Build
			scheduleFound, reloadFound bool
			schedulingErr              error
		)

		saveMaxInFlightPipeline := func() {
			BeforeEach(func() {
				var err error
				pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "fake-pipeline"}, atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",

							Public: true,

							RawMaxInFlight: 2,

							PlanSequence: []atc.Step{
								{
									Config: &atc.PutStep{
										Name: "some-resource",
										Params: atc.Params{
											"some-param": "some-value",
										},
									},
								},
								{
									Config: &atc.GetStep{
										Name:     "some-input",
										Resource: "some-resource",
										Params: atc.Params{
											"some-param": "some-value",
										},
										Passed:  []string{"job-1", "job-2"},
										Trigger: true,
									},
								},
								{
									Config: &atc.TaskStep{
										Name:       "some-task",
										Privileged: true,
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											RootfsURI: "some-image",
										},
									},
								},
							},
						},
						{
							Name: "some-other-job",
						},
						{
							Name:   "some-private-job",
							Public: false,
						},
						{
							Name: "other-serial-group-job",
						},
						{
							Name: "different-serial-group-job",
						},
						{
							Name: "job-1",
						},
						{
							Name: "job-2",
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-type",
						},
						{
							Name: "some-other-resource",
							Type: "some-type",
						},
					},
				}, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())
			})
		}

		saveSerialGroupsPipeline := func() {
			BeforeEach(func() {
				var err error
				pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "fake-pipeline"}, atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",

							Public: true,

							Serial: true,

							SerialGroups: []string{"serial-group"},

							RawMaxInFlight: 2,

							PlanSequence: []atc.Step{
								{
									Config: &atc.PutStep{
										Name: "some-resource",
										Params: atc.Params{
											"some-param": "some-value",
										},
									},
								},
								{
									Config: &atc.GetStep{
										Name:     "some-input",
										Resource: "some-resource",
										Params: atc.Params{
											"some-param": "some-value",
										},
										Passed:  []string{"job-1", "job-2"},
										Trigger: true,
									},
								},
								{
									Config: &atc.TaskStep{
										Name:       "some-task",
										Privileged: true,
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											RootfsURI: "some-image",
										},
									},
								},
							},
						},
						{
							Name: "some-other-job",
						},
						{
							Name:   "some-private-job",
							Public: false,
						},
						{
							Name:         "other-serial-group-job",
							SerialGroups: []string{"serial-group", "really-different-group"},
						},
						{
							Name:         "different-serial-group-job",
							SerialGroups: []string{"different-serial-group"},
						},
						{
							Name: "job-1",
						},
						{
							Name: "job-2",
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-type",
						},
						{
							Name: "some-other-resource",
							Type: "some-type",
						},
					},
				}, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())
			})
		}

		JustBeforeEach(func() {
			var found bool
			var err error
			job, found, err = pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			scheduleFound, schedulingErr = job.ScheduleBuild(schedulingBuild)

			reloadFound, err = schedulingBuild.Reload()
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when the scheduling build is created first", func() {
			BeforeEach(func() {
				var err error
				schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when the job config doesn't specify max in flight", func() {
				BeforeEach(func() {
					var created bool
					var err error
					pipeline, created, err = team.SavePipeline(atc.PipelineRef{Name: "other-pipeline"}, atc.Config{
						Jobs: atc.JobConfigs{
							{
								Name: "some-job",
							},
						},
					}, db.ConfigVersion(0), false)
					Expect(err).ToNot(HaveOccurred())
					Expect(created).To(BeTrue())

					var found bool
					job, found, err = pipeline.Job("some-job")
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
				})

				It("schedules the build", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeTrue())
				})

				Context("when build exists", func() {
					Context("when the pipeline is paused", func() {
						BeforeEach(func() {
							err := pipeline.Pause("")
							Expect(err).ToNot(HaveOccurred())
						})

						It("returns false", func() {
							Expect(schedulingErr).ToNot(HaveOccurred())
							Expect(scheduleFound).To(BeFalse())
							Expect(reloadFound).To(BeTrue())
							Expect(schedulingBuild.IsScheduled()).To(BeFalse())
						})
					})

					Context("when the job is paused", func() {
						BeforeEach(func() {
							err := job.Pause("")
							Expect(err).ToNot(HaveOccurred())
						})

						It("returns false", func() {
							Expect(schedulingErr).ToNot(HaveOccurred())
							Expect(scheduleFound).To(BeFalse())
							Expect(reloadFound).To(BeTrue())
							Expect(schedulingBuild.IsScheduled()).To(BeFalse())
						})
					})

					Context("when the pipeline and job is not paused", func() {
						It("sets the build to scheduled", func() {
							Expect(schedulingErr).ToNot(HaveOccurred())
							Expect(scheduleFound).To(BeTrue())
							Expect(reloadFound).To(BeTrue())
							Expect(schedulingBuild.IsScheduled()).To(BeTrue())
						})
					})
				})

				Context("when the build does not exist", func() {
					var deleteFound bool
					BeforeEach(func() {
						var err error
						deleteFound, err = schedulingBuild.Delete()
						Expect(err).ToNot(HaveOccurred())
					})

					It("returns false", func() {
						Expect(schedulingErr).To(HaveOccurred())
						Expect(scheduleFound).To(BeFalse())
						Expect(reloadFound).To(BeFalse())
						Expect(deleteFound).To(BeTrue())
					})
				})
			})

			Context("when the job config specifies max in flight = 2", func() {
				Context("when there are 2 builds running", func() {
					var startedBuild, scheduledBuild db.Build

					BeforeEach(func() {
						var err error
						startedBuild, err = job.CreateBuild(defaultBuildCreatedBy)
						Expect(err).ToNot(HaveOccurred())
						scheduled, err := job.ScheduleBuild(startedBuild)
						Expect(err).ToNot(HaveOccurred())
						Expect(scheduled).To(BeTrue())
						_, err = startedBuild.Start(atc.Plan{})
						Expect(err).NotTo(HaveOccurred())

						scheduledBuild, err = job.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())
						scheduled, err = job.ScheduleBuild(scheduledBuild)
						Expect(err).ToNot(HaveOccurred())
						Expect(scheduled).To(BeTrue())
						_, err = startedBuild.Start(atc.Plan{})
						Expect(err).NotTo(HaveOccurred())

						for _, s := range []db.BuildStatus{db.BuildStatusSucceeded, db.BuildStatusFailed, db.BuildStatusErrored, db.BuildStatusAborted} {
							finishedBuild, err := job.CreateBuild(defaultBuildCreatedBy)
							Expect(err).NotTo(HaveOccurred())

							scheduled, err = job.ScheduleBuild(finishedBuild)
							Expect(err).NotTo(HaveOccurred())
							Expect(scheduled).To(BeTrue())

							err = finishedBuild.Finish(s)
							Expect(err).NotTo(HaveOccurred())
						}

						otherJob, found, err := pipeline.Job("some-other-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						_, err = otherJob.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())
					})

					saveMaxInFlightPipeline()

					It("returns max in flight reached so it does not schedule", func() {
						Expect(schedulingErr).ToNot(HaveOccurred())
						Expect(scheduleFound).To(BeFalse())
						Expect(reloadFound).To(BeTrue())
					})
				})

				Context("when there is 1 build running", func() {
					BeforeEach(func() {
						startedBuild, err := job.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())
						scheduled, err := job.ScheduleBuild(startedBuild)
						Expect(err).NotTo(HaveOccurred())
						Expect(scheduled).To(BeTrue())
						_, err = startedBuild.Start(atc.Plan{})
						Expect(err).NotTo(HaveOccurred())

						for _, s := range []db.BuildStatus{db.BuildStatusSucceeded, db.BuildStatusFailed, db.BuildStatusErrored, db.BuildStatusAborted} {
							finishedBuild, err := job.CreateBuild(defaultBuildCreatedBy)
							Expect(err).NotTo(HaveOccurred())

							scheduled, err = job.ScheduleBuild(finishedBuild)
							Expect(err).NotTo(HaveOccurred())
							Expect(scheduled).To(BeTrue())

							err = finishedBuild.Finish(s)
							Expect(err).NotTo(HaveOccurred())
						}

						err = job.SaveNextInputMapping(nil, true)
						Expect(err).NotTo(HaveOccurred())
					})

					saveMaxInFlightPipeline()

					It("schedules the build", func() {
						Expect(schedulingErr).ToNot(HaveOccurred())
						Expect(scheduleFound).To(BeTrue())
						Expect(reloadFound).To(BeTrue())
					})
				})
			})

			Context("when the job is in serial groups", func() {
				Context("when multiple jobs in the serial group is running", func() {
					BeforeEach(func() {
						var err error
						_, err = job.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())

						otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						serialGroupBuild, err := otherSerialJob.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())

						scheduled, err := otherSerialJob.ScheduleBuild(serialGroupBuild)
						Expect(err).NotTo(HaveOccurred())
						Expect(scheduled).To(BeTrue())

						differentSerialJob, found, err := pipeline.Job("different-serial-group-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						differentSerialGroupBuild, err := differentSerialJob.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())

						scheduled, err = differentSerialJob.ScheduleBuild(differentSerialGroupBuild)
						Expect(err).NotTo(HaveOccurred())
						Expect(scheduled).To(BeTrue())
					})

					saveSerialGroupsPipeline()

					It("does not schedule the build", func() {
						Expect(schedulingErr).ToNot(HaveOccurred())
						Expect(scheduleFound).To(BeFalse())
					})
				})

				Context("when no jobs in the serial groups are running", func() {
					BeforeEach(func() {
						otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						serialGroupBuild, err := otherSerialJob.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())

						scheduled, err := otherSerialJob.ScheduleBuild(serialGroupBuild)
						Expect(err).NotTo(HaveOccurred())
						Expect(scheduled).To(BeTrue())

						err = serialGroupBuild.Finish(db.BuildStatusSucceeded)
						Expect(err).NotTo(HaveOccurred())

						differentSerialJob, found, err := pipeline.Job("different-serial-group-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						differentSerialGroupBuild, err := differentSerialJob.CreateBuild(defaultBuildCreatedBy)
						Expect(err).NotTo(HaveOccurred())

						scheduled, err = differentSerialJob.ScheduleBuild(differentSerialGroupBuild)
						Expect(err).NotTo(HaveOccurred())
						Expect(scheduled).To(BeTrue())

						err = job.SaveNextInputMapping(nil, true)
						Expect(err).NotTo(HaveOccurred())
					})

					saveSerialGroupsPipeline()

					It("does schedule the build", func() {
						Expect(schedulingErr).ToNot(HaveOccurred())
						Expect(scheduleFound).To(BeTrue())
						Expect(reloadFound).To(BeTrue())
					})
				})
			})
		})

		Context("when the scheduling build is not the first one created (with serial groups)", func() {
			Context("when the scheduling build has inputs determined as false", func() {
				BeforeEach(func() {
					var err error
					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, false)
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does not schedule because the inputs determined is false", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeFalse())
					Expect(reloadFound).To(BeTrue())
				})
			})

			Context("when another build within the serial group is scheduled first", func() {
				BeforeEach(func() {
					otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					_, err = otherSerialJob.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = otherSerialJob.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())

					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does not schedule because the build we are trying to schedule is not the next most pending build in the serial group", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeFalse())
					Expect(reloadFound).To(BeTrue())
				})
			})

			Context("when the scheduling build has it's inputs determined and created earlier", func() {
				BeforeEach(func() {
					var err error
					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					_, err = otherSerialJob.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
					err = otherSerialJob.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does schedule the build", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeTrue())
					Expect(reloadFound).To(BeTrue())
				})
			})

			Context("when the job is paused but has inputs determined", func() {
				BeforeEach(func() {
					var err error
					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					_, err = otherSerialJob.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
					err = otherSerialJob.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())

					err = job.Pause("")
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does not schedule the build", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeFalse())
					Expect(reloadFound).To(BeTrue())
				})
			})

			Context("when there are other succeeded builds within the same serial group", func() {
				BeforeEach(func() {
					otherSerialJob, found, err := pipeline.Job("other-serial-group-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					succeededBuild, err := otherSerialJob.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = succeededBuild.Finish(db.BuildStatusSucceeded)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
					err = otherSerialJob.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())

					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does schedule builds because we only care about running or pending builds", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeTrue())
					Expect(reloadFound).To(BeTrue())
				})
			})

			Context("when the job we are trying to schedule has multiple serial groups", func() {
				BeforeEach(func() {
					otherSerialJob, found, err := pipeline.Job("some-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					_, err = otherSerialJob.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					job, found, err = pipeline.Job("other-serial-group-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					schedulingBuild, err = job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).NotTo(HaveOccurred())

					err = job.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
					err = otherSerialJob.SaveNextInputMapping(nil, true)
					Expect(err).NotTo(HaveOccurred())
				})

				saveSerialGroupsPipeline()

				It("does not schedule a build because the a build within one of the serial groups was created earlier", func() {
					Expect(schedulingErr).ToNot(HaveOccurred())
					Expect(scheduleFound).To(BeFalse())
					Expect(reloadFound).To(BeTrue())
				})
			})
		})
	})

	Describe("RequestSchedule", func() {
		It("sends a NOTIFY on the scheduler channel with the job ID as payload", func(ctx context.Context) {
			pool, err := pgxpool.New(ctx, postgresRunner.DataSourceName())
			Expect(err).ToNot(HaveOccurred())
			defer pool.Close()

			listener := db.NewPgxListener(pool)
			defer listener.Close()

			err = listener.Listen("scheduler")
			Expect(err).ToNot(HaveOccurred())

			err = job.RequestSchedule()
			Expect(err).ToNot(HaveOccurred())

			notifyChan := listener.NotificationChannel()
			var notification *pgconn.Notification
			Eventually(ctx, notifyChan).WithTimeout(2 * time.Second).Should(Receive(&notification))
			Expect(notification.Payload).To(Equal(fmt.Sprintf("%d", job.ID())))
		})
	})

	Describe("GetNextBuildInputs", func() {
		var (
			versions    []atc.ResourceVersion
			spanContext db.SpanContext
			scenario    *dbtest.Scenario
		)

		BeforeEach(func() {
			spanContext = db.SpanContext{"fake": "version"}

			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name:     "some-input",
										Resource: "some-resource",
										Passed:   []string{"job-1", "job-2"},
										Trigger:  true,
									},
								},
								{
									Config: &atc.GetStep{
										Name:     "some-input-2",
										Resource: "some-resource",
										Passed:   []string{"job-1"},
										Trigger:  true,
									},
								},
								{
									Config: &atc.GetStep{
										Name:     "some-input-3",
										Resource: "some-resource",
										Trigger:  true,
									},
								},
							},
						},
						{
							Name: "job-1",
						},
						{
							Name: "job-2",
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-base-resource-type",
						},
					},
				}),
				builder.WithSpanContext(spanContext),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
					atc.Version{"version": "v3"},
				),
			)

			reversions, _, found, err := scenario.Resource("some-resource").Versions(db.Page{Limit: 3}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			versions = []atc.ResourceVersion{reversions[2], reversions[1], reversions[0]}
		})

		Describe("partial next build inputs", func() {
			It("gets partial next build inputs for the given job name", func() {
				inputVersions := db.InputMapping{
					"some-input-2": db.InputResult{
						ResolveError: "disaster",
					},
				}

				err := scenario.Job("some-job").SaveNextInputMapping(inputVersions, false)
				Expect(err).NotTo(HaveOccurred())

				buildInputs := []db.BuildInput{
					{
						Name:         "some-input-2",
						ResolveError: "disaster",
					},
				}

				actualBuildInputs, err := scenario.Job("some-job").GetNextBuildInputs()
				Expect(err).NotTo(HaveOccurred())

				Expect(actualBuildInputs).To(ConsistOf(buildInputs))
			})

			It("gets full next build inputs for the given job name", func() {
				inputVersions := db.InputMapping{
					"some-input-1": db.InputResult{
						Input: &db.AlgorithmInput{
							AlgorithmVersion: db.AlgorithmVersion{
								Version:    db.ResourceVersion(convertToSHA256(versions[0].Version)),
								ResourceID: scenario.Resource("some-resource").ID(),
							},
							FirstOccurrence: false,
						},
						PassedBuildIDs: []int{},
					},
					"some-input-2": db.InputResult{
						Input: &db.AlgorithmInput{
							AlgorithmVersion: db.AlgorithmVersion{
								Version:    db.ResourceVersion(convertToSHA256(versions[1].Version)),
								ResourceID: scenario.Resource("some-resource").ID(),
							},
							FirstOccurrence: false,
						},
						PassedBuildIDs: []int{},
					},
					"some-input-3": db.InputResult{
						Input: &db.AlgorithmInput{
							AlgorithmVersion: db.AlgorithmVersion{
								Version:    db.ResourceVersion(convertToSHA256(versions[2].Version)),
								ResourceID: scenario.Resource("some-resource").ID(),
							},
							FirstOccurrence: false,
						},
						PassedBuildIDs: []int{},
					},
				}

				err := scenario.Job("some-job").SaveNextInputMapping(inputVersions, true)
				Expect(err).NotTo(HaveOccurred())

				buildInputs := []db.BuildInput{
					{
						Name:            "some-input-1",
						ResourceID:      scenario.Resource("some-resource").ID(),
						Version:         atc.Version{"version": "v1"},
						FirstOccurrence: false,
						Context:         spanContext,
					},
					{
						Name:            "some-input-2",
						ResourceID:      scenario.Resource("some-resource").ID(),
						Version:         atc.Version{"version": "v2"},
						FirstOccurrence: false,
						Context:         spanContext,
					},
					{
						Name:            "some-input-3",
						ResourceID:      scenario.Resource("some-resource").ID(),
						Version:         atc.Version{"version": "v3"},
						FirstOccurrence: false,
						Context:         spanContext,
					},
				}

				actualBuildInputs, err := scenario.Job("some-job").GetNextBuildInputs()
				Expect(err).NotTo(HaveOccurred())

				Expect(actualBuildInputs).To(ConsistOf(buildInputs))
			})
		})
	})

	Describe("GetFullNextBuildInputs", func() {
		var (
			versions          []atc.ResourceVersion
			scenarioPipeline1 *dbtest.Scenario
			scenarioPipeline2 *dbtest.Scenario
		)

		BeforeEach(func() {
			scenarioPipeline1 = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name:     "some-input",
										Resource: "some-resource",
									},
								},
							},
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-base-resource-type",
						},
					},
				}),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
					atc.Version{"version": "v3"},
				),
				builder.WithVersionMetadata("some-resource", atc.Version{"version": "v1"}, db.ResourceConfigMetadataFields{
					db.ResourceConfigMetadataField{
						Name:  "name1",
						Value: "value1",
					},
				}),
			)

			reversions, _, found, err := scenarioPipeline1.Resource("some-resource").Versions(db.Page{Limit: 3}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			versions = []atc.ResourceVersion{reversions[2], reversions[1], reversions[0]}

			scenarioPipeline2 = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
						},
						{
							Name: "some-other-job",
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-type",
						},
					},
				}),
			)
		})

		It("gets next build inputs for the given job name", func() {
			inputVersions := db.InputMapping{
				"some-input-1": db.InputResult{
					Input: &db.AlgorithmInput{
						AlgorithmVersion: db.AlgorithmVersion{
							Version:    db.ResourceVersion(convertToSHA256(versions[0].Version)),
							ResourceID: scenarioPipeline1.Resource("some-resource").ID(),
						},
						FirstOccurrence: false,
					},
					PassedBuildIDs: []int{},
				},
				"some-input-2": db.InputResult{
					Input: &db.AlgorithmInput{
						AlgorithmVersion: db.AlgorithmVersion{
							Version:    db.ResourceVersion(convertToSHA256(versions[1].Version)),
							ResourceID: scenarioPipeline1.Resource("some-resource").ID(),
						},
						FirstOccurrence: true,
					},
					PassedBuildIDs: []int{},
				},
			}
			err := scenarioPipeline1.Job("some-job").SaveNextInputMapping(inputVersions, true)
			Expect(err).NotTo(HaveOccurred())

			pipeline2InputVersions := db.InputMapping{
				"some-input-3": db.InputResult{
					Input: &db.AlgorithmInput{
						AlgorithmVersion: db.AlgorithmVersion{
							Version:    db.ResourceVersion(convertToSHA256(versions[2].Version)),
							ResourceID: scenarioPipeline2.Resource("some-resource").ID(),
						},
						FirstOccurrence: false,
					},
					PassedBuildIDs: []int{},
				},
			}
			err = scenarioPipeline2.Job("some-job").SaveNextInputMapping(pipeline2InputVersions, true)
			Expect(err).NotTo(HaveOccurred())

			buildInputs := []db.BuildInput{
				{
					Name:            "some-input-1",
					ResourceID:      scenarioPipeline1.Resource("some-resource").ID(),
					Version:         atc.Version{"version": "v1"},
					FirstOccurrence: false,
				},
				{
					Name:            "some-input-2",
					ResourceID:      scenarioPipeline1.Resource("some-resource").ID(),
					Version:         atc.Version{"version": "v2"},
					FirstOccurrence: true,
				},
			}

			actualBuildInputs, found, err := scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(actualBuildInputs).To(ConsistOf(buildInputs))

			By("updating the set of next build inputs")
			inputVersions2 := db.InputMapping{
				"some-input-2": db.InputResult{
					Input: &db.AlgorithmInput{
						AlgorithmVersion: db.AlgorithmVersion{
							Version:    db.ResourceVersion(convertToSHA256(versions[2].Version)),
							ResourceID: scenarioPipeline1.Resource("some-resource").ID(),
						},
						FirstOccurrence: false,
					},
					PassedBuildIDs: []int{},
				},
				"some-input-3": db.InputResult{
					Input: &db.AlgorithmInput{
						AlgorithmVersion: db.AlgorithmVersion{
							Version:    db.ResourceVersion(convertToSHA256(versions[2].Version)),
							ResourceID: scenarioPipeline1.Resource("some-resource").ID(),
						},
						FirstOccurrence: true,
					},
					PassedBuildIDs: []int{},
				},
			}
			err = scenarioPipeline1.Job("some-job").SaveNextInputMapping(inputVersions2, true)
			Expect(err).NotTo(HaveOccurred())

			buildInputs2 := []db.BuildInput{
				{
					Name:            "some-input-2",
					ResourceID:      scenarioPipeline1.Resource("some-resource").ID(),
					Version:         atc.Version{"version": "v3"},
					FirstOccurrence: false,
				},
				{
					Name:            "some-input-3",
					ResourceID:      scenarioPipeline1.Resource("some-resource").ID(),
					Version:         atc.Version{"version": "v3"},
					FirstOccurrence: true,
				},
			}

			actualBuildInputs2, found, err := scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(actualBuildInputs2).To(ConsistOf(buildInputs2))

			By("updating next build inputs to an empty set when the mapping is nil")
			err = scenarioPipeline1.Job("some-job").SaveNextInputMapping(nil, true)
			Expect(err).NotTo(HaveOccurred())

			actualBuildInputs3, found, err := scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(actualBuildInputs3).To(BeEmpty())
		})

		It("distinguishes between a job with no inputs and a job with missing inputs", func() {
			By("initially returning not found")
			_, found, err := scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			By("returning found when an empty input mapping is saved")
			err = scenarioPipeline1.Job("some-job").SaveNextInputMapping(db.InputMapping{}, true)
			Expect(err).NotTo(HaveOccurred())

			_, found, err = scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("does not grab inputs if inputs were not successfully determined", func() {
			inputVersions := db.InputMapping{
				"some-input-1": db.InputResult{
					ResolveError: "disaster",
				},
			}
			err := scenarioPipeline1.Job("some-job").SaveNextInputMapping(inputVersions, false)
			Expect(err).NotTo(HaveOccurred())

			_, found, err := scenarioPipeline1.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})

	Describe("a build is created for a job", func() {
		var (
			build1DB      db.Build
			otherPipeline db.Pipeline
			otherJob      db.Job
		)

		BeforeEach(func() {
			pipelineConfig := atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
					},
				},
				Resources: atc.ResourceConfigs{
					{
						Name: "some-other-resource",
						Type: "some-type",
					},
				},
			}
			var err error
			otherPipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "some-other-pipeline"}, pipelineConfig, db.ConfigVersion(1), false)
			Expect(err).ToNot(HaveOccurred())

			build1DB, err = job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())

			Expect(build1DB.ID()).NotTo(BeZero())
			Expect(build1DB.JobName()).To(Equal("some-job"))
			Expect(build1DB.Name()).To(Equal("1"))
			Expect(build1DB.Status()).To(Equal(db.BuildStatusPending))
			Expect(build1DB.IsScheduled()).To(BeFalse())

			var found bool
			otherJob, found, err = otherPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("becomes the next pending build for job", func() {
			nextPendings, err := job.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			//time.Sleep(10 * time.Hour)
			Expect(nextPendings).NotTo(BeEmpty())
			Expect(nextPendings[0].ID()).To(Equal(build1DB.ID()))
		})

		Context("and another build for a different pipeline is created with the same job name", func() {
			BeforeEach(func() {
				otherBuild, err := otherJob.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				Expect(otherBuild.ID()).NotTo(BeZero())
				Expect(otherBuild.JobName()).To(Equal("some-job"))
				Expect(otherBuild.Name()).To(Equal("1"))
				Expect(otherBuild.Status()).To(Equal(db.BuildStatusPending))
				Expect(otherBuild.IsScheduled()).To(BeFalse())
			})

			It("does not change the next pending build for job", func() {
				nextPendingBuilds, err := job.GetPendingBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(nextPendingBuilds).To(Equal([]db.Build{build1DB}))
			})
		})

		Context("when scheduled", func() {
			BeforeEach(func() {
				var err error
				var found bool
				found, err = job.ScheduleBuild(build1DB)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
			})

			It("remains the next pending build for job", func() {
				nextPendingBuilds, err := job.GetPendingBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(nextPendingBuilds).NotTo(BeEmpty())
				Expect(nextPendingBuilds[0].ID()).To(Equal(build1DB.ID()))
			})
		})

		Context("when started", func() {
			BeforeEach(func() {
				started, err := build1DB.Start(atc.Plan{ID: "some-id"})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
			})

			It("saves the updated status, and the schema and private plan", func() {
				found, err := build1DB.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(build1DB.Status()).To(Equal(db.BuildStatusStarted))
				Expect(build1DB.Schema()).To(Equal("exec.v2"))
				Expect(build1DB.PrivatePlan()).To(Equal(atc.Plan{ID: "some-id"}))
			})

			It("saves the build's start time", func() {
				found, err := build1DB.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(build1DB.StartTime().Unix()).To(BeNumerically("~", time.Now().Unix(), 3))
			})
		})

		Context("when the build finishes", func() {
			BeforeEach(func() {
				err := build1DB.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets the build's status and end time", func() {
				found, err := build1DB.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(build1DB.Status()).To(Equal(db.BuildStatusSucceeded))
				Expect(build1DB.EndTime().Unix()).To(BeNumerically("~", time.Now().Unix(), 3))
			})
		})

		Context("and another is created for the same job", func() {
			var build2DB db.Build

			BeforeEach(func() {
				var err error
				build2DB, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				Expect(build2DB.ID()).NotTo(BeZero())
				Expect(build2DB.ID()).NotTo(Equal(build1DB.ID()))
				Expect(build2DB.Name()).To(Equal("2"))
				Expect(build2DB.Status()).To(Equal(db.BuildStatusPending))
			})

			Describe("the first build", func() {
				It("remains the next pending build", func() {
					nextPendingBuilds, err := job.GetPendingBuilds()
					Expect(err).NotTo(HaveOccurred())
					Expect(nextPendingBuilds).To(HaveLen(2))
					Expect(nextPendingBuilds[0].ID()).To(Equal(build1DB.ID()))
					Expect(nextPendingBuilds[1].ID()).To(Equal(build2DB.ID()))
				})
			})
		})

		Context("when there is a rerun build created for an old build", func() {
			var rerunBuild db.Build
			var newBuild db.Build
			var newerBuild db.Build

			BeforeEach(func() {
				var err error
				newBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				newerBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				err = newBuild.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				rerunBuild, err = job.RerunBuild(newBuild, defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				Expect(rerunBuild.ID()).NotTo(BeZero())
				Expect(rerunBuild.ID()).NotTo(Equal(newBuild.ID()))
				Expect(rerunBuild.Name()).To(Equal("2.1"))
				Expect(rerunBuild.Status()).To(Equal(db.BuildStatusPending))
			})

			It("orders the builds with regular build first and then rerun of old build", func() {
				nextPendingBuilds, err := job.GetPendingBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(len(nextPendingBuilds)).To(Equal(3))
				Expect(nextPendingBuilds[0].Name()).To(Equal(build1DB.Name()))
				Expect(nextPendingBuilds[1].Name()).To(Equal(rerunBuild.Name()))
				Expect(nextPendingBuilds[2].Name()).To(Equal(newerBuild.Name()))
			})
		})

		Context("when there is a rerun build created for the newest build", func() {
			var rerunBuild db.Build
			var newBuild db.Build
			var newerBuild db.Build

			BeforeEach(func() {
				var err error
				newBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				rerunBuild, err = job.RerunBuild(newBuild, defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				newerBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				Expect(rerunBuild.ID()).NotTo(BeZero())
				Expect(rerunBuild.ID()).NotTo(Equal(newBuild.ID()))
				Expect(rerunBuild.Name()).To(Equal("2.1"))
				Expect(rerunBuild.Status()).To(Equal(db.BuildStatusPending))
			})

			It("orders the builds with rerun of new build", func() {
				nextPendingBuilds, err := job.GetPendingBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(len(nextPendingBuilds)).To(Equal(4))
				Expect(nextPendingBuilds[0].ID()).To(Equal(build1DB.ID()))
				Expect(nextPendingBuilds[1].ID()).To(Equal(newBuild.ID()))
				Expect(nextPendingBuilds[2].ID()).To(Equal(rerunBuild.ID()))
				Expect(nextPendingBuilds[3].ID()).To(Equal(newerBuild.ID()))
			})
		})

		Context("when there are multiple reruns for multiple pending builds", func() {
			var rerunBuild db.Build
			var rerunBuild2 db.Build
			var rerunBuild3 db.Build
			var newBuild db.Build
			var newerBuild db.Build

			BeforeEach(func() {
				var err error
				newBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				newerBuild, err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				rerunBuild3, err = job.RerunBuild(newerBuild, defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				rerunBuild, err = job.RerunBuild(newBuild, defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				rerunBuild2, err = job.RerunBuild(rerunBuild, defaultBuildCreatedBy)
				Expect(err).NotTo(HaveOccurred())

				Expect(rerunBuild.ID()).NotTo(BeZero())
				Expect(rerunBuild.ID()).NotTo(Equal(newBuild.ID()))
				Expect(rerunBuild.Name()).To(Equal("2.1"))
				Expect(rerunBuild.Status()).To(Equal(db.BuildStatusPending))
			})

			It("orders the builds with ascending reruns following original builds", func() {
				nextPendingBuilds, err := job.GetPendingBuilds()
				Expect(err).NotTo(HaveOccurred())
				Expect(len(nextPendingBuilds)).To(Equal(6))
				Expect(nextPendingBuilds[0].Name()).To(Equal(build1DB.Name()))
				Expect(nextPendingBuilds[1].Name()).To(Equal(newBuild.Name()))
				Expect(nextPendingBuilds[2].Name()).To(Equal(rerunBuild.Name()))
				Expect(nextPendingBuilds[3].Name()).To(Equal(rerunBuild2.Name()))
				Expect(nextPendingBuilds[4].Name()).To(Equal(newerBuild.Name()))
				Expect(nextPendingBuilds[5].Name()).To(Equal(rerunBuild3.Name()))
			})
		})
	})

	Describe("EnsurePendingBuildExists", func() {
		Context("when only a started build exists", func() {

			Context("when tracing is configured", func() {
				BeforeEach(func() {
					exporter := tracetest.NewInMemoryExporter()
					tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
					tracing.ConfigureTraceProvider(tp)
				})

				AfterEach(func() {
					tracing.Configured = false
				})

				It("propagates span context", func() {
					ctx, span := tracing.StartSpan(context.Background(), "fake-operation", nil)
					traceID := span.SpanContext().TraceID().String()

					job.EnsurePendingBuildExists(ctx)

					pendingBuilds, _ := job.GetPendingBuilds()
					spanContext := pendingBuilds[0].SpanContext()
					traceParent := spanContext.Get("traceparent")
					Expect(traceParent).To(ContainSubstring(traceID))
				})
			})

		})
	})

	Describe("Clear task cache", func() {
		Context("when task cache exists", func() {
			var (
				someOtherJob db.Job
				rowsDeleted  int64
			)

			BeforeEach(func() {
				var (
					err   error
					found bool
				)

				usedTaskCache, err := taskCacheFactory.FindOrCreate(job.ID(), "some-task", "some-path")
				Expect(err).ToNot(HaveOccurred())

				_, err = workerTaskCacheFactory.FindOrCreate(db.WorkerTaskCache{
					TaskCache:  usedTaskCache,
					WorkerName: defaultWorker.Name(),
				})
				Expect(err).ToNot(HaveOccurred())

				someOtherJob, found, err = pipeline.Job("some-other-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(someOtherJob).ToNot(BeNil())

				otherUsedTaskCache, err := taskCacheFactory.FindOrCreate(someOtherJob.ID(), "some-other-task", "some-other-path")
				Expect(err).ToNot(HaveOccurred())

				_, err = workerTaskCacheFactory.FindOrCreate(db.WorkerTaskCache{
					TaskCache:  otherUsedTaskCache,
					WorkerName: defaultWorker.Name(),
				})
				Expect(err).ToNot(HaveOccurred())

			})

			Context("when a path is provided", func() {
				BeforeEach(func() {
					var err error
					rowsDeleted, err = job.ClearTaskCache("some-task", "some-path")
					Expect(err).NotTo(HaveOccurred())
				})

				It("doesn't remove other jobs caches", func() {
					otherUsedTaskCache, found, err := taskCacheFactory.Find(someOtherJob.ID(), "some-other-task", "some-other-path")
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())

					_, err = workerTaskCacheFactory.FindOrCreate(db.WorkerTaskCache{
						TaskCache:  otherUsedTaskCache,
						WorkerName: defaultWorker.Name(),
					})
					Expect(err).ToNot(HaveOccurred())
				})

				Context("but the cache path doesn't exist", func() {
					BeforeEach(func() {
						var err error
						rowsDeleted, err = job.ClearTaskCache("some-task", "some-nonexistent-path")
						Expect(err).NotTo(HaveOccurred())

					})
					It("deletes 0 rows", func() {
						Expect(rowsDeleted).To(Equal(int64(0)))
					})
				})
			})

			Context("when a path is not provided", func() {
				Context("when a non-existent step-name is provided", func() {
					BeforeEach(func() {
						var err error
						rowsDeleted, err = job.ClearTaskCache("some-nonexistent-task", "")
						Expect(err).NotTo(HaveOccurred())
					})

					It("does not delete any rows from the task_caches table", func() {
						Expect(rowsDeleted).To(BeZero())
					})

					It("should not delete any task steps", func() {
						usedTaskCache, found, err := taskCacheFactory.Find(job.ID(), "some-task", "some-path")
						Expect(err).ToNot(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(err).ToNot(HaveOccurred())

						_, found, err = workerTaskCacheFactory.Find(db.WorkerTaskCache{
							TaskCache:  usedTaskCache,
							WorkerName: defaultWorker.Name(),
						})
						Expect(found).To(BeTrue())
						Expect(err).ToNot(HaveOccurred())
					})

				})

				Context("when an existing step-name is provided", func() {
					BeforeEach(func() {
						var err error
						rowsDeleted, err = job.ClearTaskCache("some-task", "")
						Expect(err).NotTo(HaveOccurred())
					})

					It("doesn't remove other jobs caches", func() {
						_, found, err := taskCacheFactory.Find(someOtherJob.ID(), "some-other-task", "some-other-path")
						Expect(found).To(BeTrue())
						Expect(err).ToNot(HaveOccurred())
					})
				})
			})
		})
	})

	Describe("Database operation spans", func() {
		var spanRecorder *tracetest.SpanRecorder

		BeforeEach(func() {
			spanRecorder = new(tracetest.SpanRecorder)
			tp := trace.NewTracerProvider(
				trace.WithSpanProcessor(spanRecorder),
				trace.WithSyncer(tracetest.NewInMemoryExporter()),
			)
			tracing.ConfigureTraceProvider(tp)
		})

		AfterEach(func() {
			tracing.Configured = false
		})

		It("emits a db.build.create span when creating a build", func() {
			_, err := job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).NotTo(HaveOccurred())

			ended := spanRecorder.Ended()
			var createSpan trace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "db.build.create" {
					createSpan = s
					break
				}
			}
			Expect(createSpan).ToNot(BeNil(), "expected db.build.create span")

			attrMap := make(map[string]string)
			for _, a := range createSpan.Attributes() {
				attrMap[string(a.Key)] = a.Value.AsString()
			}
			Expect(attrMap["db.job_name"]).To(Equal("some-job"))
		})
	})

})
