package db_test

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc/util"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var _ = Describe("Resource", func() {
	var pipeline db.Pipeline

	BeforeEach(func() {
		var (
			created bool
			err     error
		)

		pipeline, created, err = defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "pipeline-with-resources"},
			atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name:         "some-resource",
						Type:         "registry-image",
						WebhookToken: "some-token",
						Source:       atc.Source{"some": "repository"},
						Version:      atc.Version{"ref": "abcdef"},
						CheckTimeout: "999m",
					},
					{
						Name:   "some-other-resource",
						Public: true,
						Type:   "git",
						Source: atc.Source{"some": "other-repository"},
					},
					{
						Name:   "some-secret-resource",
						Public: false,
						Type:   "git",
						Source: atc.Source{"some": "((secret-repository))"},
					},
					{
						Name:         "some-resource-custom-check",
						Type:         "git",
						Source:       atc.Source{"some": "some-repository"},
						CheckEvery:   &atc.CheckEvery{Interval: 10 * time.Millisecond},
						CheckTimeout: "1m",
					},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "job-using-resource",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-other-resource",
								},
							},
						},
					},
					{
						Name: "not-using-resource",
					},
				},
			},
			0,
			false,
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(created).To(BeTrue())
	})

	Describe("(Pipeline).Resources", func() {
		var resources []db.Resource

		JustBeforeEach(func() {
			var err error
			resources, err = pipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns the resources", func() {
			Expect(resources).To(HaveLen(4))

			ids := map[int]struct{}{}

			for _, r := range resources {
				ids[r.ID()] = struct{}{}

				switch r.Name() {
				case "some-resource":
					Expect(r.Type()).To(Equal("registry-image"))
					Expect(r.Source()).To(Equal(atc.Source{"some": "repository"}))
					Expect(r.ConfigPinnedVersion()).To(Equal(atc.Version{"ref": "abcdef"}))
					Expect(r.CurrentPinnedVersion()).To(Equal(r.ConfigPinnedVersion()))
					Expect(r.HasWebhook()).To(BeTrue())
				case "some-other-resource":
					Expect(r.Type()).To(Equal("git"))
					Expect(r.Source()).To(Equal(atc.Source{"some": "other-repository"}))
					Expect(r.HasWebhook()).To(BeFalse())
				case "some-secret-resource":
					Expect(r.Type()).To(Equal("git"))
					Expect(r.Source()).To(Equal(atc.Source{"some": "((secret-repository))"}))
					Expect(r.HasWebhook()).To(BeFalse())
				case "some-resource-custom-check":
					Expect(r.Type()).To(Equal("git"))
					Expect(r.Source()).To(Equal(atc.Source{"some": "some-repository"}))
					Expect(r.CheckEvery().Interval.String()).To(Equal("10ms"))
					Expect(r.CheckTimeout()).To(Equal("1m"))
					Expect(r.HasWebhook()).To(BeFalse())
				}
			}
		})
	})

	Describe("(Pipeline).Resource", func() {
		var (
			scenario *dbtest.Scenario
		)

		Context("when the resource exists", func() {
			BeforeEach(func() {
				scenario = dbtest.Setup(
					builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name:   "some-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
					}),
				)
			})

			It("returns the resource", func() {
				Expect(scenario.Resource("some-resource").Name()).To(Equal("some-resource"))
				Expect(scenario.Resource("some-resource").Type()).To(Equal("some-base-resource-type"))
				Expect(scenario.Resource("some-resource").Source()).To(Equal(atc.Source{"some": "repository"}))
			})

			Context("when the resource has check build", func() {
				var publicPlan atc.Plan
				BeforeEach(func() {
					resource := scenario.Resource("some-resource")
					resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
						"some-base-resource-type",
						atc.Source{"some": "repository"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())

					resourceConfigScope, err := resourceConfig.FindOrCreateScope(intptr(resource.ID()))
					Expect(err).NotTo(HaveOccurred())

					err = resource.SetResourceConfigScope(resourceConfigScope)
					Expect(err).NotTo(HaveOccurred())

					publicPlan = atc.Plan{
						ID: atc.PlanID("1234"),
						Check: &atc.CheckPlan{
							Name: "some-resource",
							Type: "some-resource-type",
						},
					}
					bytes, err := json.Marshal(publicPlan)
					jr := json.RawMessage(bytes)
					resourceConfigScope.UpdateLastCheckStartTime(99, &jr)
					resourceConfigScope.UpdateLastCheckEndTime(false)
				})

				It("return check build info", func() {
					Expect(scenario.Resource("some-resource").LastCheckStartTime()).Should(BeTemporally("~", time.Now(), time.Second))
					Expect(scenario.Resource("some-resource").LastCheckEndTime()).Should(BeTemporally("~", time.Now(), time.Second))

				})

				It("return build summary", func() {
					buildSummary := scenario.Resource("some-resource").BuildSummary()
					Expect(buildSummary).NotTo(BeNil())
					Expect(buildSummary.ID).To(Equal(99))
					Expect(buildSummary.Name).To(Equal(db.CheckBuildName))
					Expect(buildSummary.TeamName).To(Equal(scenario.Team.Name()))
					Expect(buildSummary.PipelineName).To(Equal(scenario.Pipeline.Name()))
					Expect(buildSummary.JobName).To(BeEmpty())
					Expect(time.Unix(buildSummary.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
					Expect(time.Unix(buildSummary.EndTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
					Expect(buildSummary.PublicPlan).ToNot(BeNil())

					var plan atc.Plan
					err := json.Unmarshal(*buildSummary.PublicPlan, &plan)
					Expect(err).ToNot(HaveOccurred())
					Expect(plan).To(Equal(publicPlan))
				})
			})
		})

	})

	Describe("Enable/Disable Version", func() {
		var scenario *dbtest.Scenario

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "job-using-resource",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "some-other-resource",
									},
								},
							},
						},
						{
							Name: "not-using-resource",
						},
					},
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-other-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "other-repository"},
						},
					},
				}),
				builder.WithResourceVersions("some-other-resource", atc.Version{"disabled": "version"}),
			)
		})

		Context("when disabling a version that exists", func() {
			var disableErr error
			var requestedSchedule1 time.Time
			var requestedSchedule2 time.Time

			BeforeEach(func() {
				requestedSchedule1 = scenario.Job("job-using-resource").ScheduleRequestedTime()
				requestedSchedule2 = scenario.Job("not-using-resource").ScheduleRequestedTime()

				scenario.Run(builder.WithDisabledVersion("some-other-resource", atc.Version{"disabled": "version"}))
			})

			It("successfully disables the version", func() {
				Expect(disableErr).ToNot(HaveOccurred())

				versions, _, found, err := scenario.Resource("some-other-resource").Versions(db.Page{Limit: 3}, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(versions).To(HaveLen(1))
				Expect(versions[0].Version).To(Equal(atc.Version{"disabled": "version"}))
				Expect(versions[0].Enabled).To(BeFalse())
			})

			It("requests schedule on the jobs using that resource", func() {
				found, err := scenario.Job("job-using-resource").Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(scenario.Job("job-using-resource").ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule1))
			})

			It("does not request schedule on jobs that do not use the resource", func() {
				found, err := scenario.Job("not-using-resource").Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(scenario.Job("not-using-resource").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule2))
			})

			Context("when enabling that version", func() {
				BeforeEach(func() {
					scenario.Run(builder.WithEnabledVersion("some-other-resource", atc.Version{"disabled": "version"}))
				})

				It("successfully enables the version", func() {
					versions, _, found, err := scenario.Resource("some-other-resource").Versions(db.Page{Limit: 3}, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(versions).To(HaveLen(1))
					Expect(versions[0].Version).To(Equal(atc.Version{"disabled": "version"}))
					Expect(versions[0].Enabled).To(BeTrue())
				})

				It("request schedule on the jobs using that resource", func() {
					found, err := scenario.Job("job-using-resource").Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					Expect(scenario.Job("job-using-resource").ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule1))
				})

				It("does not request schedule on jobs that do not use the resource", func() {
					found, err := scenario.Job("not-using-resource").Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					Expect(scenario.Job("not-using-resource").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule2))
				})
			})
		})

		Context("when disabling version that does not exist", func() {
			var disableErr error
			BeforeEach(func() {
				resource, found, err := scenario.Pipeline.Resource("some-other-resource")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				disableErr = resource.DisableVersion(123456)
			})

			It("returns an error", func() {
				Expect(disableErr).To(HaveOccurred())
			})
		})

		Context("when enabling a version that is already enabled", func() {
			var enableError error
			BeforeEach(func() {
				resource, found, err := scenario.Pipeline.Resource("some-other-resource")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				enableError = resource.EnableVersion(scenario.ResourceVersion("some-other-resource", atc.Version{"disabled": "version"}).ID())
			})

			It("returns a non one row affected error", func() {
				Expect(enableError).To(Equal(db.NonOneRowAffectedError{0}))
			})
		})
	})

	Describe("SetResourceConfigScope", func() {
		var pipeline db.Pipeline
		var resource db.Resource
		var scope db.ResourceConfigScope

		BeforeEach(func() {
			config := atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name:   "some-resource",
						Type:   defaultWorkerResourceType.Type,
						Source: atc.Source{"some": "repository"},
					},
					{
						Name:   "some-other-resource",
						Type:   defaultWorkerResourceType.Type,
						Source: atc.Source{"some": "other-repository"},
					},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "using-resource",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "not-using-resource",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-other-resource",
								},
							},
						},
					},
				},
			}

			var err error

			var created bool
			pipeline, created, err = defaultTeam.SavePipeline(
				atc.PipelineRef{Name: "some-pipeline-with-two-jobs"},
				config,
				0,
				false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			var found bool
			resource, found, err = pipeline.Resource("some-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
			Expect(err).ToNot(HaveOccurred())

			scope, err = resourceConfig.FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())
		})

		It("associates the resource to the config and scope", func() {
			Expect(resource.ResourceConfigID()).To(BeZero())
			Expect(resource.ResourceConfigScopeID()).To(BeZero())

			Expect(resource.SetResourceConfigScope(scope)).To(Succeed())

			_, err := resource.Reload()
			Expect(err).ToNot(HaveOccurred())

			Expect(resource.ResourceConfigID()).To(Equal(scope.ResourceConfig().ID()))
			Expect(resource.ResourceConfigScopeID()).To(Equal(scope.ID()))
		})

	})

	Describe("CreateBuild", func() {
		var ctx context.Context
		var manuallyTriggered bool
		var plan atc.Plan
		var build db.Build
		var created bool

		BeforeEach(func() {
			ctx = context.TODO()
			manuallyTriggered = false
			plan = atc.Plan{
				ID: "some-plan",
				Check: &atc.CheckPlan{
					Name: "wreck",
				},
			}
		})

		JustBeforeEach(func() {
			var err error
			build, created, err = defaultResource.CreateBuild(ctx, manuallyTriggered, plan)
			Expect(err).ToNot(HaveOccurred())
		})


		Context("when tracing is configured", func() {
			var span trace.Span

			BeforeEach(func() {
				exporter := tracetest.NewInMemoryExporter()
				tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
				tracing.ConfigureTraceProvider(tp)

				ctx, span = tracing.StartSpan(context.Background(), "fake-operation", nil)
			})

			AfterEach(func() {
				tracing.Configured = false
			})

			It("propagates span context", func() {
				traceID := span.SpanContext().TraceID().String()
				buildContext := build.SpanContext()
				traceParent := buildContext.Get("traceparent")
				Expect(traceParent).To(ContainSubstring(traceID))
			})
		})

		Context("when another running build already exists", func() {
			var prevBuild db.Build

			BeforeEach(func() {
				var err error
				var prevCreated bool
				By("creating a completed build")
				prevBuild, prevCreated, err = defaultResource.CreateBuild(ctx, false, plan)
				Expect(err).ToNot(HaveOccurred())
				Expect(prevCreated).To(BeTrue())
				err = prevBuild.Finish(db.BuildStatusSucceeded)
				Expect(err).ToNot(HaveOccurred())

				By("creating a running build")
				prevBuild, prevCreated, err = defaultResource.CreateBuild(ctx, false, plan)
				Expect(err).ToNot(HaveOccurred())
				Expect(prevCreated).To(BeTrue())
			})

			Context("when manually triggered", func() {
				BeforeEach(func() {
					manuallyTriggered = true
				})

			})

			Context("when the previous build is finished", func() {
				BeforeEach(func() {
					Expect(prevBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
				})

				It("creates the build", func() {
					Expect(created).To(BeTrue())
					Expect(build.ResourceID()).To(Equal(defaultResource.ID()))
				})
			})
		})
	})

	Describe("CreateInMemoryBuild", func() {
		var ctx context.Context
		var plan atc.Plan
		var build db.Build

		BeforeEach(func() {
			ctx = context.TODO()
			plan = atc.Plan{
				ID: "some-plan",
				Check: &atc.CheckPlan{
					Name: "wreck",
				},
			}
		})

		JustBeforeEach(func() {
			var err error
			build, err = defaultResource.CreateInMemoryBuild(ctx, plan, util.NewSequenceGenerator(1))
			Expect(err).ToNot(HaveOccurred())
		})

		It("creates a started build for a resource", func() {
			Expect(build).ToNot(BeNil())
			Expect(build.ID()).To(Equal(0))
			Expect(build.Name()).To(Equal(db.CheckBuildName))
			Expect(build.ResourceID()).To(Equal(defaultResource.ID()))
			Expect(build.PipelineID()).To(Equal(defaultResource.PipelineID()))
			Expect(build.TeamID()).To(Equal(defaultResource.TeamID()))
			Expect(build.IsManuallyTriggered()).To(BeFalse())
			Expect(build.PrivatePlan()).To(Equal(plan))
		})

		It("should not log to the check_build_events partition", func() {
			err := build.SaveEvent(event.Log{Payload: "log"})
			Expect(err).ToNot(HaveOccurred())
			Expect(numBuildEventsForCheck(build)).To(Equal(0))
		})

		Context("when tracing is configured", func() {
			var span trace.Span

			BeforeEach(func() {
				exporter := tracetest.NewInMemoryExporter()
				tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
				tracing.ConfigureTraceProvider(tp)

				ctx, span = tracing.StartSpan(context.Background(), "fake-operation", nil)
			})

			AfterEach(func() {
				tracing.Configured = false
			})

			It("propagates span context", func() {
				traceID := span.SpanContext().TraceID().String()
				buildContext := build.SpanContext()
				traceParent := buildContext.Get("traceparent")
				Expect(traceParent).To(ContainSubstring(traceID))
			})
		})
	})


	Context("Versions", func() {
		var (
			scenario *dbtest.Scenario
		)

		Context("when resource has versions created in order of check order", func() {
			var resourceVersions []atc.ResourceVersion

			BeforeEach(func() {
				scenario = dbtest.Setup(
					builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name:   "some-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
					}),
					builder.WithResourceVersions(
						"some-resource",
						atc.Version{"ref": "v0"},
						atc.Version{"ref": "v1"},
						atc.Version{"ref": "v2"},
						atc.Version{"ref": "v3"},
						atc.Version{"ref": "v4"},
						atc.Version{"ref": "v5"},
						atc.Version{"ref": "v6"},
						atc.Version{"ref": "v7"},
						atc.Version{"ref": "v8"},
						atc.Version{"ref": "v9"},
					),
				)

				resourceVersions = make([]atc.ResourceVersion, 0)

				for i := 0; i < 10; i++ {
					rcv := scenario.ResourceVersion("some-resource", atc.Version{"ref": "v" + strconv.Itoa(i)})

					var metadata atc.Metadata

					for _, v := range rcv.Metadata() {
						metadata = append(metadata, atc.MetadataField(v))
					}

					resourceVersion := atc.ResourceVersion{
						ID:       rcv.ID(),
						Version:  atc.Version(rcv.Version()),
						Metadata: metadata,
						Enabled:  true,
					}

					resourceVersions = append(resourceVersions, resourceVersion)
				}
			})

			Context("with a from that places it at the beginning of the most recent versions", func() {
				It("returns the versions, with no previous page", func() {
					historyPage, pagination, found, err := scenario.Resource("some-resource").Versions(db.Page{From: db.NewIntPtr(resourceVersions[8].ID), Limit: 2}, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(len(historyPage)).To(Equal(2))
					Expect(historyPage[0].Version).To(Equal(resourceVersions[9].Version))
					Expect(historyPage[1].Version).To(Equal(resourceVersions[8].Version))
					Expect(pagination.Newer).To(BeNil())
					Expect(pagination.Older).To(Equal(&db.Page{To: db.NewIntPtr(resourceVersions[7].ID), Limit: 2}))
				})
			})

		})

	})

	Describe("PinVersion/UnpinVersion", func() {
		var (
			scenario *dbtest.Scenario
		)

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "repository"},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "job-using-resource",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "some-resource",
									},
								},
							},
						},
						{
							Name: "not-using-resource",
						},
					},
				}),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
					atc.Version{"version": "v3"},
				),
			)
		})

		Context("when we use an invalid version id (does not exist)", func() {
			var (
				pinnedVersion atc.Version
			)

			BeforeEach(func() {
				found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v1"}).ID())
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())

				Expect(scenario.Resource("some-resource").CurrentPinnedVersion()).To(Equal(scenario.Resource("some-resource").APIPinnedVersion()))
				pinnedVersion = scenario.Resource("some-resource").APIPinnedVersion()
			})

			It("returns not found and does not update anything", func() {
				found, err := scenario.Resource("some-resource").PinVersion(-1)
				Expect(found).To(BeFalse())
				Expect(err).To(HaveOccurred())

				Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(pinnedVersion))
			})
		})

		Context("when requesting schedule for version pinning", func() {
			It("requests schedule on all jobs using the resource", func() {
				requestedSchedule := scenario.Job("job-using-resource").ScheduleRequestedTime()

				found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v1"}).ID())
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())

				Expect(scenario.Job("job-using-resource").ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule))
			})

			It("does not request schedule on jobs that do not use the resource", func() {
				requestedSchedule := scenario.Job("not-using-resource").ScheduleRequestedTime()

				found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v1"}).ID())
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())

				Expect(scenario.Job("not-using-resource").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule))
			})
		})

		Context("when we pin a resource to a version", func() {
			BeforeEach(func() {
				found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v1"}).ID())
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when the resource is not pinned", func() {
				It("sets the api pinned version", func() {
					Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))
					Expect(scenario.Resource("some-resource").CurrentPinnedVersion()).To(Equal(scenario.Resource("some-resource").APIPinnedVersion()))
				})
			})

			Context("when the resource is pinned by another version already", func() {
				BeforeEach(func() {
					found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v3"}).ID())
					Expect(found).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())
				})

				It("switch the pin to given version", func() {
					Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(atc.Version{"version": "v3"}))
					Expect(scenario.Resource("some-resource").CurrentPinnedVersion()).To(Equal(scenario.Resource("some-resource").APIPinnedVersion()))
				})
			})

			Context("when we set the pin comment on a resource", func() {
				BeforeEach(func() {
					err := scenario.Resource("some-resource").SetPinComment("foo")
					Expect(err).ToNot(HaveOccurred())
				})

				It("should set the pin comment", func() {
					Expect(scenario.Resource("some-resource").PinComment()).To(Equal("foo"))
				})
			})

			Context("when requesting schedule for version unpinning", func() {
				It("requests schedule on all jobs using the resource", func() {
					requestedSchedule := scenario.Job("job-using-resource").ScheduleRequestedTime()

					err := scenario.Resource("some-resource").UnpinVersion()
					Expect(err).ToNot(HaveOccurred())

					Expect(scenario.Job("job-using-resource").ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule))
				})

				It("does not request schedule on jobs that do not use the resource", func() {
					requestedSchedule := scenario.Job("not-using-resource").ScheduleRequestedTime()

					err := scenario.Resource("some-resource").UnpinVersion()
					Expect(err).ToNot(HaveOccurred())

					Expect(scenario.Job("not-using-resource").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule))
				})
			})

			Context("when we unpin a resource to a version", func() {
				BeforeEach(func() {
					err := scenario.Resource("some-resource").UnpinVersion()
					Expect(err).ToNot(HaveOccurred())
				})

				It("sets the api pinned version to nil", func() {
					Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(BeNil())
					Expect(scenario.Resource("some-resource").CurrentPinnedVersion()).To(BeNil())
				})

				It("unsets the pin comment", func() {
					Expect(scenario.Resource("some-resource").PinComment()).To(BeEmpty())
				})
			})
		})

		Context("when we pin a resource that is already pinned to a version (through the config)", func() {
			BeforeEach(func() {
				scenario.Run(
					builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name:    "some-resource",
								Type:    "some-base-resource-type",
								Source:  atc.Source{"some": "repository"},
								Version: atc.Version{"pinned": "version"},
							},
						},
					}),
					builder.WithResourceVersions(
						"some-resource",
						atc.Version{"version": "v1"},
						atc.Version{"version": "v2"},
						atc.Version{"version": "v3"},
					))
			})

			It("should fail to update the pinned version", func() {
				found, err := scenario.Resource("some-resource").PinVersion(scenario.ResourceVersion("some-resource", atc.Version{"version": "v1"}).ID())
				Expect(found).To(BeFalse())
				Expect(err).To(Equal(db.ErrPinnedThroughConfig))
			})
		})
	})


	Describe("Clear resource cache", func() {
		Context("when resource cache exists", func() {
			var (
				scenario                *dbtest.Scenario
				firstUsedResourceCache  db.ResourceCache
				secondUsedResourceCache db.ResourceCache
			)

			initializeResourceCacheVolume := func(build db.Build, workerName string, resourceCache db.ResourceCache) {
				creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
					Type:     "get",
					StepName: "some-resource",
				})
				Expect(err).ToNot(HaveOccurred())

				resourceCacheVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), workerName, creatingContainer, "some-path")
				Expect(err).ToNot(HaveOccurred())

				createdVolume, err := resourceCacheVolume.Created()
				Expect(err).ToNot(HaveOccurred())

				_, err = createdVolume.InitializeResourceCache(resourceCache)
				Expect(err).ToNot(HaveOccurred())
			}

			hasResourceCacheVolume := func(workerName string, usedResourceCache db.ResourceCache) bool {
				_, found, err := volumeRepository.FindResourceCacheVolume(workerName, usedResourceCache, time.Now())
				Expect(err).ToNot(HaveOccurred())
				return found
			}

			BeforeEach(func() {
				scenario = dbtest.Setup(
					builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name: "some-resource",
								Type: dbtest.BaseResourceType,
							},
							{
								Name: "some-other-resource",
								Type: dbtest.BaseResourceType,
							},
						},
					}),
					builder.WithResourceVersions(
						"some-resource",
						atc.Version{"version": "v1"},
						atc.Version{"version": "v2"},
						atc.Version{"version": "v3"},
					),
					builder.WithBaseWorker(),
					builder.WithBaseWorker(),
				)

				build, err := defaultTeam.CreateOneOffBuild()
				Expect(err).ToNot(HaveOccurred())

				firstUsedResourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					dbtest.BaseResourceType,
					atc.Version{"some": "version"},
					scenario.Resource("some-resource").Source(),
					atc.Params{"some": "params"},
					nil,
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(firstUsedResourceCache.ID()).ToNot(BeZero())

				secondUsedResourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					dbtest.BaseResourceType,
					atc.Version{"some": "other-version"},
					scenario.Resource("some-resource").Source(),
					atc.Params{"some": "params"},
					nil,
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(secondUsedResourceCache.ID()).ToNot(BeZero())

				initializeResourceCacheVolume(build, scenario.Workers[0].Name(), firstUsedResourceCache)
				initializeResourceCacheVolume(build, scenario.Workers[0].Name(), secondUsedResourceCache)
				initializeResourceCacheVolume(build, scenario.Workers[1].Name(), firstUsedResourceCache)
			})

			Context("when a version is not provided", func() {

				It("Invalidated all resource-cache volumes associated to a resource", func() {
					resource := scenario.Resource("some-resource")

					rowsDeleted, err := resource.ClearResourceCache(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(rowsDeleted).To(Equal(int64(3)))

					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), firstUsedResourceCache)).To(BeFalse())
					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), secondUsedResourceCache)).To(BeFalse())
					Expect(hasResourceCacheVolume(scenario.Workers[1].Name(), firstUsedResourceCache)).To(BeFalse())
				})

				It("Should not invalidate any resource-cache volume for other resources", func() {
					resource := scenario.Resource("some-other-resource")

					rowsDeleted, err := resource.ClearResourceCache(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(rowsDeleted).To(Equal(int64(0)))

					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), firstUsedResourceCache)).To(BeTrue())
					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), secondUsedResourceCache)).To(BeTrue())
					Expect(hasResourceCacheVolume(scenario.Workers[1].Name(), firstUsedResourceCache)).To(BeTrue())
				})
			})

			Context("when a version is provided", func() {

				It("Invalidated all resource-cache volumes associated to a resource for that version", func() {
					resource := scenario.Resource("some-resource")
					rowsDeleted, err := resource.ClearResourceCache(atc.Version{"some": "version"})
					Expect(err).ToNot(HaveOccurred())
					Expect(rowsDeleted).To(Equal(int64(2)))

					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), firstUsedResourceCache)).To(BeFalse())
					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), secondUsedResourceCache)).To(BeTrue())
					Expect(hasResourceCacheVolume(scenario.Workers[1].Name(), firstUsedResourceCache)).To(BeFalse())
				})

				It("Should not invalidate any resource-cache volume for other resources", func() {
					resource := scenario.Resource("some-other-resource")

					rowsDeleted, err := resource.ClearResourceCache(atc.Version{"some": "some-version"})
					Expect(err).ToNot(HaveOccurred())
					Expect(rowsDeleted).To(Equal(int64(0)))

					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), firstUsedResourceCache)).To(BeTrue())
					Expect(hasResourceCacheVolume(scenario.Workers[0].Name(), secondUsedResourceCache)).To(BeTrue())
					Expect(hasResourceCacheVolume(scenario.Workers[1].Name(), firstUsedResourceCache)).To(BeTrue())
				})
			})
		})
	})

	// This test focus on build id, status, start/end time. Other build data,
	// like team/pipeline name, has been covered by other tests.
	Describe("BuildSummary", func() {
		var (
			scenario            *dbtest.Scenario
			resource            db.Resource
			resourceConfigScope db.ResourceConfigScope
		)

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "repository"},
						},
					},
				}),
			)

			resource = scenario.Resource("some-resource")

			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "repository"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

			resourceConfigScope, err = resourceConfig.FindOrCreateScope(intptr(resource.ID()))
			Expect(err).NotTo(HaveOccurred())

			err = resource.SetResourceConfigScope(resourceConfigScope)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when there is no check build", func() {
		})

		Context("when first check build starts, not set a scope yet", func() {
			var build db.Build
			var publicPlan atc.Plan

			BeforeEach(func() {
				publicPlan = atc.Plan{
					ID: atc.PlanID("1234"),
					Check: &atc.CheckPlan{
						Name: "some-resource",
						Type: "some-resource-type",
					},
				}

				var err error
				build, err = resource.CreateInMemoryBuild(context.Background(), publicPlan, seqGenerator)
				Expect(err).ToNot(HaveOccurred())

				err = build.OnCheckBuildStart()
				Expect(err).ToNot(HaveOccurred())
			})

			JustBeforeEach(func() {
				found, err := resource.Reload()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			})

			It("has build summary", func() {
				bs := resource.BuildSummary()
				Expect(bs.ID).ToNot(BeZero())
				Expect(bs.ID).To(Equal(build.ID()))
				Expect(bs.Status).To(Equal(atc.StatusStarted))
				Expect(time.Unix(bs.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
				Expect(bs.EndTime).To(BeZero())
				Expect(bs.PublicPlan).To(Equal(build.PublicPlan()))
			})

			Context("when a scope is set", func() {
				BeforeEach(func() {
					err := resource.SetResourceConfigScope(resourceConfigScope)
					Expect(err).NotTo(HaveOccurred())

					resourceConfigScope.UpdateLastCheckStartTime(build.ID(), build.PublicPlan())
					resourceConfigScope.UpdateLastCheckEndTime(false)
				})

				It("has build summary", func() {
					bs := resource.BuildSummary()
					Expect(bs.ID).ToNot(BeZero())
					Expect(bs.ID).To(Equal(build.ID()))
					Expect(bs.Status).To(Equal(atc.StatusFailed))
					Expect(time.Unix(bs.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
					Expect(time.Unix(bs.EndTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
					Expect(bs.PublicPlan).To(Equal(build.PublicPlan()))
				})

				Context("when other resource ran a check build for the scope", func() {
					BeforeEach(func() {
						resourceConfigScope.UpdateLastCheckStartTime(999999, build.PublicPlan())
						resourceConfigScope.UpdateLastCheckEndTime(true)
					})

					It("has build summary", func() {
						bs := resource.BuildSummary()
						Expect(bs.ID).To(Equal(999999))
						Expect(bs.Status).To(Equal(atc.StatusSucceeded))
						Expect(time.Unix(bs.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
						Expect(time.Unix(bs.EndTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
						Expect(bs.PublicPlan).To(Equal(build.PublicPlan()))
					})

					Context("when the resource starts a new build", func() {
						var build2 db.Build
						BeforeEach(func() {
							var err error
							build2, err = resource.CreateInMemoryBuild(context.Background(), publicPlan, seqGenerator)
							Expect(err).ToNot(HaveOccurred())

							err = build2.OnCheckBuildStart()
							Expect(err).ToNot(HaveOccurred())
						})

						It("has build summary", func() {
							bs := resource.BuildSummary()
							Expect(bs.ID).ToNot(BeZero())
							Expect(bs.ID).To(Equal(build2.ID()))
							Expect(bs.Status).To(Equal(atc.StatusStarted))
							Expect(time.Unix(bs.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
							Expect(bs.EndTime).To(BeZero())
							Expect(bs.PublicPlan).To(Equal(build.PublicPlan()))
						})
					})
				})
			})
		})
	})

	Describe("SharedResourcesAndTypes", func() {
		var (
			scenario, scenario2 *dbtest.Scenario
			resource            db.Resource
			sharedErr           error
			shared              atc.ResourcesAndTypes
		)

		BeforeEach(func() {
			atc.EnableGlobalResources = true

			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "repository"},
						},
					},
				}),
			)

			resource = scenario.Resource("some-resource")
		})

		JustBeforeEach(func() {
			reloaded, err := resource.Reload()
			Expect(err).ToNot(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			shared, sharedErr = resource.SharedResourcesAndTypes()
		})

		Context("when the resource has no scope", func() {
			It("returns resource has no scope error", func() {
				Expect(sharedErr).To(HaveOccurred())
				Expect(sharedErr).To(Equal(db.ResourceHasNoScopeErr{"some-resource"}))
				Expect(shared).To(Equal(atc.ResourcesAndTypes{}))
			})
		})

		Context("when the resource has a scope", func() {
			BeforeEach(func() {
				scenario.Run(builder.WithResourceVersions(
					"some-resource",
					atc.Version{"ref": "v0"},
					atc.Version{"ref": "v1"},
					atc.Version{"ref": "v2"},
				))
			})

			Context("when the resource is sharing resources/resource types", func() {
				BeforeEach(func() {
					scenario2 = dbtest.Setup(builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name:   "some-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
							{
								Name:   "other-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
						ResourceTypes: atc.ResourceTypes{
							{
								Name:   "some-resource-type",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
					}),
						builder.WithResourceVersions("some-resource", atc.Version{"ref": "v0"}),
						builder.WithResourceVersions("other-resource", atc.Version{"ref": "v0"}),
						builder.WithResourceTypeVersions("some-resource-type", atc.Version{"ref": "v0"}),
					)
				})

				It("fetches the shared resources and types", func() {
					Expect(sharedErr).ToNot(HaveOccurred())
					Expect(shared.Resources).To(ConsistOf(atc.ResourceIdentifiers{
						{
							Name:         "some-resource",
							PipelineName: scenario.Pipeline.Name(),
							TeamName:     scenario.Team.Name(),
						},
						{
							Name:         "some-resource",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
						{
							Name:         "other-resource",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
					}))
					Expect(shared.ResourceTypes).To(Equal(atc.ResourceIdentifiers{
						{
							Name:         "some-resource-type",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
					}))
				})
			})

			Context("when the resource has not sharing the scope", func() {
				It("returns the resource itself", func() {
					Expect(sharedErr).ToNot(HaveOccurred())
					Expect(shared).To(Equal(atc.ResourcesAndTypes{
						Resources: atc.ResourceIdentifiers{
							{
								Name:         "some-resource",
								PipelineName: scenario.Pipeline.Name(),
								TeamName:     scenario.Team.Name(),
							},
						},
					}))
				})
			})
		})
	})

	Describe("SharedResourcesAndTypes", func() {
		var (
			scenario, scenario2 *dbtest.Scenario
			resourceType        db.ResourceType
			sharedErr           error
			shared              atc.ResourcesAndTypes
		)

		BeforeEach(func() {
			atc.EnableGlobalResources = true

			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					ResourceTypes: atc.ResourceTypes{
						{
							Name:   "some-resource-type",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "repository"},
						},
					},
				}),
			)

			resourceType = scenario.ResourceType("some-resource-type")
		})

		JustBeforeEach(func() {
			reloaded, err := resourceType.Reload()
			Expect(err).ToNot(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			shared, sharedErr = resourceType.SharedResourcesAndTypes()
		})

		Context("when the resource type has no scope", func() {
			It("returns resource type has no scope error", func() {
				Expect(sharedErr).To(HaveOccurred())
				Expect(sharedErr).To(Equal(db.ResourceHasNoScopeErr{"some-resource-type"}))
				Expect(shared).To(Equal(atc.ResourcesAndTypes{}))
			})
		})

		Context("when the resource type has a scope", func() {
			BeforeEach(func() {
				scenario.Run(builder.WithResourceTypeVersions(
					"some-resource-type",
					atc.Version{"ref": "v0"},
					atc.Version{"ref": "v1"},
					atc.Version{"ref": "v2"},
				))
			})

			Context("when the resource type is sharing resources/resource types", func() {
				BeforeEach(func() {
					scenario2 = dbtest.Setup(builder.WithPipeline(atc.Config{
						Resources: atc.ResourceConfigs{
							{
								Name:   "some-resource",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
						ResourceTypes: atc.ResourceTypes{
							{
								Name:   "some-resource-type",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
							{
								Name:   "other-resource-type",
								Type:   "some-base-resource-type",
								Source: atc.Source{"some": "repository"},
							},
						},
					}),
						builder.WithResourceVersions("some-resource", atc.Version{"ref": "v0"}),
						builder.WithResourceTypeVersions("other-resource-type", atc.Version{"ref": "v0"}),
						builder.WithResourceTypeVersions("some-resource-type", atc.Version{"ref": "v0"}),
					)
				})

				It("fetches the shared resources and types", func() {
					Expect(sharedErr).ToNot(HaveOccurred())
					Expect(shared.Resources).To(Equal(atc.ResourceIdentifiers{
						{
							Name:         "some-resource",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
					}))
					Expect(shared.ResourceTypes).To(ConsistOf(atc.ResourceIdentifiers{
						{
							Name:         "some-resource-type",
							PipelineName: scenario.Pipeline.Name(),
							TeamName:     scenario.Team.Name(),
						},
						{
							Name:         "some-resource-type",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
						{
							Name:         "other-resource-type",
							PipelineName: scenario2.Pipeline.Name(),
							TeamName:     scenario2.Team.Name(),
						},
					}))
				})
			})

			Context("when the resource type has not sharing the scope", func() {
				It("returns the resource type itself", func() {
					Expect(sharedErr).ToNot(HaveOccurred())
					Expect(shared).To(Equal(atc.ResourcesAndTypes{
						ResourceTypes: atc.ResourceIdentifiers{
							{
								Name:         "some-resource-type",
								PipelineName: scenario.Pipeline.Name(),
								TeamName:     scenario.Team.Name(),
							},
						},
					}))
				})
			})
		})
	})
})
