package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Component Notifications", func() {
	// listenFor subscribes to the given channel and returns a function that
	// waits for a notification. The returned function unlistens when done and
	// returns true if a notification arrived within the timeout.
	listenFor := func(channel string) func() bool {
		signal, err := dbConn.Bus().ListenSignal(channel)
		Expect(err).NotTo(HaveOccurred())

		return func() bool {
			defer dbConn.Bus().UnlistenSignal(channel, signal)
			select {
			case <-signal.C():
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
	}

	// createResourceScope creates a resource config scope for a resource,
	// using the same pattern as the existing tests.
	createResourceScope := func(resource db.Resource) db.ResourceConfigScope {
		rc, err := resourceConfigFactory.FindOrCreateResourceConfig(
			resource.Type(),
			resource.Source(),
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		scope, err := rc.FindOrCreateScope(intptr(resource.ID()))
		Expect(err).NotTo(HaveOccurred())
		return scope
	}

	createResourceTypeScope := func(rt db.ResourceType) db.ResourceConfigScope {
		rc, err := resourceConfigFactory.FindOrCreateResourceConfig(
			rt.Type(),
			rt.Source(),
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		scope, err := rc.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		return scope
	}

	Describe("LidarScanner notifications", func() {
		var (
			scenario *dbtest.Scenario
			resource db.Resource
			scope    db.ResourceConfigScope
		)

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "source"},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "some-resource",
									},
								},
							},
						},
					},
				}),
			)

			resource = scenario.Resource("some-resource")
			scope = createResourceScope(resource)
		})

		Describe("Resource.SetResourceConfigScope", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SetResourceConfigScope")
			})
		})

		Describe("Resource.PinVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				received := listenFor(atc.ComponentLidarScanner)

				_, err = resource.PinVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after PinVersion")
			})
		})

		Describe("Resource.UnpinVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				_, err = resource.PinVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.UnpinVersion()
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after UnpinVersion")
			})
		})

		Describe("Resource.DisableVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.DisableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after DisableVersion")
			})
		})

		Describe("Resource.EnableVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				err = resource.DisableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.EnableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after EnableVersion")
			})
		})

		Describe("ResourceConfigScope.SaveVersions", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SaveVersions")
			})
		})

		Describe("ResourceConfigScope.UpdateLastCheckEndTime", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				_, err := scope.UpdateLastCheckEndTime(true)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after UpdateLastCheckEndTime")
			})
		})
	})

	Describe("Build completion notifications", func() {
		var build db.Build

		BeforeEach(func() {
			var err error
			build, err = defaultTeam.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			started, err := build.Start(atc.Plan{})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())
		})

		Describe("build.Finish notifies SyslogDrainer", func() {
			It("notifies the drainer", func() {
				received := listenFor(atc.ComponentSyslogDrainer)

				err := build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected drainer notification after build.Finish")
			})
		})

		Describe("build.Finish notifies BuildReaper", func() {
			It("notifies the reaper", func() {
				received := listenFor(atc.ComponentBuildReaper)

				err := build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected reaper notification after build.Finish")
			})
		})

		Describe("build.Finish notifies CollectorBuilds", func() {
			It("notifies the build collector", func() {
				received := listenFor(atc.ComponentCollectorBuilds)

				err := build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected build collector notification after build.Finish")
			})
		})

		Describe("build.Finish notifies CollectorResourceCacheUses", func() {
			It("notifies the resource cache use collector", func() {
				received := listenFor(atc.ComponentCollectorResourceCacheUses)

				err := build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected resource cache use collector notification after build.Finish")
			})
		})

		Describe("build.Finish notifies CollectorChecks", func() {
			It("notifies the checks collector", func() {
				received := listenFor(atc.ComponentCollectorChecks)

				err := build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected checks collector notification after build.Finish")
			})
		})
	})

	Describe("Pipeline lifecycle notifications", func() {
		var pipeline db.Pipeline

		BeforeEach(func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.TaskStep{
										Name:       "some-task",
										ConfigPath: "some-path",
									},
								},
							},
						},
					},
				}),
			)
			pipeline = scenario.Pipeline
		})

		Describe("pipeline.Archive notifies CollectorPipelines", func() {
			It("notifies the pipeline collector", func() {
				received := listenFor(atc.ComponentCollectorPipelines)

				err := pipeline.Archive()
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected pipeline collector notification after Archive")
			})
		})

		Describe("pipeline.Archive notifies CollectorTaskCaches", func() {
			It("notifies the task cache collector", func() {
				received := listenFor(atc.ComponentCollectorTaskCaches)

				err := pipeline.Archive()
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected task cache collector notification after Archive")
			})
		})

		Describe("pipeline.Destroy notifies CollectorPipelines", func() {
			It("notifies the pipeline collector", func() {
				received := listenFor(atc.ComponentCollectorPipelines)

				err := pipeline.Destroy()
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected pipeline collector notification after Destroy")
			})
		})

		Describe("pipeline.Pause notifies CollectorTaskCaches", func() {
			It("notifies the task cache collector", func() {
				received := listenFor(atc.ComponentCollectorTaskCaches)

				err := pipeline.Pause("test")
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected task cache collector notification after Pause")
			})
		})

		Describe("build.Finish notifies CollectorResourceCaches", func() {
			It("notifies the resource cache collector", func() {
				build, err := defaultTeam.CreateOneOffBuild()
				Expect(err).NotTo(HaveOccurred())

				started, err := build.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())

				received := listenFor(atc.ComponentCollectorResourceCaches)

				err = build.Finish(db.BuildStatusSucceeded)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected resource cache collector notification after build.Finish")
			})
		})
	})

	Describe("ResourceType scanner notifications", func() {
		It("notifies the scanner on SetResourceConfigScope", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					ResourceTypes: atc.ResourceTypes{
						{
							Name:   "some-type",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "type-source"},
						},
					},
				}),
			)

			rt := scenario.ResourceType("some-type")
			scope := createResourceTypeScope(rt)

			received := listenFor(atc.ComponentLidarScanner)

			err := rt.SetResourceConfigScope(scope)
			Expect(err).NotTo(HaveOccurred())

			Expect(received()).To(BeTrue(), "expected scanner notification after ResourceType.SetResourceConfigScope")
		})
	})
})

