package db_test

import (
	"os"
	"strings"
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
			It("notifies the scanner when scope changes", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SetResourceConfigScope")
			})

			It("does not notify the scanner when scope is unchanged", func() {
				// First call — sets the scope.
				err := resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				// Second call — same scope, should not notify.
				received := listenFor(atc.ComponentLidarScanner)

				err = resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeFalse(), "did not expect scanner notification when scope is unchanged")
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
			It("notifies the scanner when new versions are saved", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SaveVersions with new version")
			})

			It("does not notify the scanner when re-saving existing versions", func() {
				// First save — inserts the version.
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				// Second save — same version already exists.
				received := listenFor(atc.ComponentLidarScanner)

				err = scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeFalse(), "did not expect scanner notification when re-saving existing version")
			})
		})

		Describe("ResourceConfigScope.UpdateLastCheckEndTime", func() {
			It("does not notify the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				_, err := scope.UpdateLastCheckEndTime(true)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeFalse(), "did not expect scanner notification after UpdateLastCheckEndTime")
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
		var (
			rt    db.ResourceType
			scope db.ResourceConfigScope
		)

		BeforeEach(func() {
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

			rt = scenario.ResourceType("some-type")
			scope = createResourceTypeScope(rt)
		})

		It("notifies the scanner when scope changes", func() {
			received := listenFor(atc.ComponentLidarScanner)

			err := rt.SetResourceConfigScope(scope)
			Expect(err).NotTo(HaveOccurred())

			Expect(received()).To(BeTrue(), "expected scanner notification after ResourceType.SetResourceConfigScope")
		})

		It("does not notify the scanner when scope is unchanged", func() {
			// First call — sets the scope.
			err := rt.SetResourceConfigScope(scope)
			Expect(err).NotTo(HaveOccurred())

			// Second call — same scope, should not notify.
			received := listenFor(atc.ComponentLidarScanner)

			err = rt.SetResourceConfigScope(scope)
			Expect(err).NotTo(HaveOccurred())

			Expect(received()).To(BeFalse(), "did not expect scanner notification when ResourceType scope is unchanged")
		})
	})
})

var _ = Describe("Pipeline run completion notifications", func() {
	// A run reaching a terminal status is announced on its own channel so a
	// walker can be event-driven. Poll stays the source of truth -- the bus
	// coalesces and silently drops -- but a completion that notifies nothing
	// forces every consumer to wait out a full sweep interval.
	listenForCompletion := func() func() bool {
		signal, err := dbConn.Bus().ListenSignal(atc.PipelineRunCompletedChannel)
		Expect(err).NotTo(HaveOccurred())

		return func() bool {
			defer dbConn.Bus().UnlistenSignal(atc.PipelineRunCompletedChannel, signal)
			select {
			case <-signal.C():
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
	}

	It("notifies when a finished build terminalises the run", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		consumeObservedSchedule(entry)

		received := listenForCompletion()

		Expect(pendingRunBuild(entry).Finish(db.BuildStatusSucceeded)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusSucceeded))

		Expect(received()).To(BeTrue(), "expected a run completion notification once the run reached a terminal status")
	})

	It("notifies when consuming a schedule request settles the last outstanding debt", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		Expect(pendingRunBuild(entry).Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning), "schedule debt still blocks completion")

		received := listenForCompletion()

		consumeObservedSchedule(entry)
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))

		Expect(received()).To(BeTrue(), "the scheduler's own completion attempt must announce the run too")
	})

	It("announces every completion site through the one notification helper", func() {
		// A new completion site that forgets to announce costs a walker a full
		// poll interval and fails no test, so the count is guarded here rather
		// than left to whoever adds the fifth site.
		for _, name := range []string{"build.go", "job.go", "pipeline.go"} {
			source, err := os.ReadFile(name)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Count(string(source), "announceRunCompletion(")).To(
				Equal(strings.Count(string(source), "attemptRunCompletion(")),
				name+" must announce every run completion it can cause",
			)
		}

		lifecycle, err := os.ReadFile("pipeline_run_lifecycle.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(lifecycle)).To(ContainSubstring("atc.PipelineRunCompletedChannel"), "the helper must announce the dedicated completion channel")
		Expect(string(lifecycle)).To(ContainSubstring("atc.ComponentReclaimerPipelineRuns"), "the helper must keep waking the reclaimer")
	})

	It("does not notify while the run is still running", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry", "other"))
		entry := fixture.jobs["entry"]
		consumeObservedSchedule(entry)
		consumeObservedSchedule(fixture.jobs["other"])

		received := listenForCompletion()

		Expect(pendingRunBuild(entry).Finish(db.BuildStatusSucceeded)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		Expect(received()).To(BeFalse(), "a run with work still outstanding must not announce completion")
	})
})
