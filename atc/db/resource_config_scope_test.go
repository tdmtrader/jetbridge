package db_test

import (
	"encoding/json"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resource Config Scope", func() {
	var scenario *dbtest.Scenario
	var resourceScope db.ResourceConfigScope

	BeforeEach(func() {
		scenario = dbtest.Setup(
			builder.WithPipeline(atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-base-resource-type",
						Source: atc.Source{
							"some": "source",
						},
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
					{
						Name: "downstream-job",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name:   "some-resource",
									Passed: []string{"some-job"},
								},
							},
						},
					},
					{
						Name: "some-other-job",
					},
				},
			}),
			builder.WithResourceVersions("some-resource"),
		)

		rc, found, err := resourceConfigFactory.FindResourceConfigByID(scenario.Resource("some-resource").ResourceConfigID())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		resourceScope, err = rc.FindOrCreateScope(intptr(scenario.Resource("some-resource").ID()))
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("SaveVersions", func() {
		var (
			originalVersionSlice []atc.Version
		)

		BeforeEach(func() {
			originalVersionSlice = []atc.Version{
				{"ref": "v1"},
				{"ref": "v3"},
			}
		})

		// XXX: Can make test more resilient if there is a method that gives all versions by descending check order
		It("ensures versioned resources have the correct check_order", func() {
			err := resourceScope.SaveVersions(nil, originalVersionSlice)
			Expect(err).ToNot(HaveOccurred())

			latestVR, found, err := resourceScope.LatestVersion()
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(latestVR.Version()).To(Equal(db.Version{"ref": "v3"}))
			Expect(latestVR.CheckOrder()).To(Equal(2))

			pretendCheckResults := []atc.Version{
				{"ref": "v2"},
				{"ref": "v3"},
			}

			err = resourceScope.SaveVersions(nil, pretendCheckResults)
			Expect(err).ToNot(HaveOccurred())

			latestVR, found, err = resourceScope.LatestVersion()
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(latestVR.Version()).To(Equal(db.Version{"ref": "v3"}))
			Expect(latestVR.CheckOrder()).To(Equal(4))
		})

		Context("when the versions already exists", func() {
			var newVersionSlice []atc.Version

			BeforeEach(func() {
				newVersionSlice = []atc.Version{
					{"ref": "v1"},
					{"ref": "v3"},
				}

				err := resourceScope.SaveVersions(nil, originalVersionSlice)
				Expect(err).ToNot(HaveOccurred())

				latestVR, found, err := resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(latestVR.Version()).To(Equal(db.Version{"ref": "v3"}))
				Expect(latestVR.CheckOrder()).To(Equal(2))
			})

			It("does not change the check order", func() {
				err := resourceScope.SaveVersions(nil, newVersionSlice)
				Expect(err).ToNot(HaveOccurred())

				latestVR, found, err := resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(latestVR.Version()).To(Equal(db.Version{"ref": "v3"}))
				Expect(latestVR.CheckOrder()).To(Equal(2))
			})

			Context("when a new version is added", func() {
				It("requests schedule on the jobs that use the resource", func() {
					err := resourceScope.SaveVersions(nil, originalVersionSlice)
					Expect(err).ToNot(HaveOccurred())

					requestedSchedule := scenario.Job("some-job").ScheduleRequestedTime()

					newVersions := []atc.Version{
						{"ref": "v0"},
						{"ref": "v3"},
					}
					err = resourceScope.SaveVersions(nil, newVersions)
					Expect(err).ToNot(HaveOccurred())

					Expect(scenario.Job("some-job").ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule))
				})

				It("does not request schedule on the jobs that use the resource but through passed constraints", func() {
					err := resourceScope.SaveVersions(nil, originalVersionSlice)
					Expect(err).ToNot(HaveOccurred())

					requestedSchedule := scenario.Job("downstream-job").ScheduleRequestedTime()

					newVersions := []atc.Version{
						{"ref": "v0"},
						{"ref": "v3"},
					}
					err = resourceScope.SaveVersions(nil, newVersions)
					Expect(err).ToNot(HaveOccurred())

					Expect(scenario.Job("downstream-job").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule))
				})

				It("does not request schedule on the jobs that do not use the resource", func() {
					err := resourceScope.SaveVersions(nil, originalVersionSlice)
					Expect(err).ToNot(HaveOccurred())

					requestedSchedule := scenario.Job("some-other-job").ScheduleRequestedTime()

					newVersions := []atc.Version{
						{"ref": "v0"},
						{"ref": "v3"},
					}
					err = resourceScope.SaveVersions(nil, newVersions)
					Expect(err).ToNot(HaveOccurred())

					Expect(scenario.Job("some-other-job").ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule))
				})
			})
		})
		Context("when a version is empty", func() {
			It("returns an error", func() {
				emptyVersions := []atc.Version{
					{},
				}

				err := resourceScope.SaveVersions(nil, emptyVersions)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("resource output version is empty. Version must contain at least one key-value pair"))
			})
		})
	})

	Describe("LatestVersion", func() {
		Context("when the resource config exists", func() {
			var latestCV db.ResourceConfigVersion

			BeforeEach(func() {
				originalVersionSlice := []atc.Version{
					{"ref": "v1"},
					{"ref": "v3"},
				}

				err := resourceScope.SaveVersions(nil, originalVersionSlice)
				Expect(err).ToNot(HaveOccurred())

				var found bool
				latestCV, found, err = resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			})

			It("gets latest version of resource", func() {
				Expect(latestCV.Version()).To(Equal(db.Version{"ref": "v3"}))
				Expect(latestCV.CheckOrder()).To(Equal(2))
			})

			It("disabled versions do not affect fetching the latest version", func() {
				err := resourceScope.SaveVersions(nil, []atc.Version{{"version": "1"}})
				Expect(err).ToNot(HaveOccurred())

				savedRCV, found, err := resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(savedRCV.Version()).To(Equal(db.Version{"version": "1"}))

				scenario.Run(builder.WithDisabledVersion("some-resource", atc.Version(savedRCV.Version())))

				latestVR, found, err := resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(latestVR.Version()).To(Equal(db.Version{"version": "1"}))

				scenario.Run(builder.WithEnabledVersion("some-resource", atc.Version(savedRCV.Version())))

				latestVR, found, err = resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(latestVR.Version()).To(Equal(db.Version{"version": "1"}))
			})

			It("saving versioned resources updates the latest versioned resource", func() {
				err := resourceScope.SaveVersions(nil, []atc.Version{{"ref": "4"}, {"ref": "5"}})
				Expect(err).ToNot(HaveOccurred())

				savedVR, found, err := resourceScope.LatestVersion()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(savedVR.Version()).To(Equal(db.Version{"ref": "5"}))
			})
		})
	})

	Describe("FindVersion", func() {
		BeforeEach(func() {
			originalVersionSlice := []atc.Version{
				{"ref": "v1"},
				{"ref": "v3"},
			}

			err := resourceScope.SaveVersions(nil, originalVersionSlice)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when the version exists", func() {
			var latestCV db.ResourceConfigVersion

			BeforeEach(func() {
				var err error
				var found bool
				latestCV, found, err = resourceScope.FindVersion(atc.Version{"ref": "v1"})
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			})

			It("gets the version of resource", func() {
				Expect(latestCV.Version()).To(Equal(db.Version{"ref": "v1"}))
				Expect(latestCV.CheckOrder()).To(Equal(1))
			})
		})

		Context("when the version does not exist", func() {
			var found bool
			var latestCV db.ResourceConfigVersion

			BeforeEach(func() {
				var err error
				latestCV, found, err = resourceScope.FindVersion(atc.Version{"ref": "v2"})
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not get the version of resource", func() {
				Expect(found).To(BeFalse())
				Expect(latestCV).To(BeNil())
			})
		})
	})

	Describe("UpdateLastCheckStartTime", func() {
		BeforeEach(func() {
			err := scenario.Resource("some-resource").SetResourceConfigScope(resourceScope)
			Expect(err).ToNot(HaveOccurred())
		})
		It("updates last check start time", func() {
			lastTime := scenario.Resource("some-resource").LastCheckEndTime()
			publicPlan := atc.Plan{
				ID: atc.PlanID("1234"),
				Check: &atc.CheckPlan{
					Name: "some-resource",
					Type: "some-resource-type",
				},
			}
			bytes, err := json.Marshal(publicPlan)
			jr := json.RawMessage(bytes)
			updated, err := resourceScope.UpdateLastCheckStartTime(99, &jr)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())

			Expect(scenario.Resource("some-resource").LastCheckStartTime()).To(BeTemporally(">", lastTime))

			buildSummary := scenario.Resource("some-resource").BuildSummary()
			Expect(buildSummary.ID).To(Equal(99))
			Expect(time.Unix(buildSummary.StartTime, 0)).Should(BeTemporally("~", time.Now(), time.Second))
			Expect(buildSummary.PublicPlan).ToNot(BeNil())
			var plan atc.Plan
			err = json.Unmarshal(*buildSummary.PublicPlan, &plan)
			Expect(err).ToNot(HaveOccurred())
			Expect(plan).To(Equal(publicPlan))
		})
	})

	Describe("UpdateLastCheckEndTime", func() {
		BeforeEach(func() {
			err := scenario.Resource("some-resource").SetResourceConfigScope(resourceScope)
			Expect(err).ToNot(HaveOccurred())
			_, err = resourceScope.UpdateLastCheckStartTime(99, nil)
			Expect(err).ToNot(HaveOccurred())
		})
		It("updates last check end time", func() {
			lastTime := scenario.Resource("some-resource").LastCheckEndTime()
			updated, err := resourceScope.UpdateLastCheckEndTime(true)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(scenario.Resource("some-resource").LastCheckEndTime()).To(BeTemporally(">", lastTime))
		})
	})

	Describe("Scope Deprecation on Config Change", func() {
		It("soft-deletes old scope when FindOrCreateScope is called with a different config", func() {
			resource := scenario.Resource("some-resource")
			oldScopeID := resource.ResourceConfigScopeID()
			Expect(oldScopeID).ToNot(BeZero())

			// Save versions to the old scope
			err := resourceScope.SaveVersions(db.SpanContext{}, []atc.Version{
				{"ref": "v1"},
				{"ref": "v2"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Create a new resource config with different source (simulating a source change)
			newRC, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "different-source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			// Call FindOrCreateScope with the new config — this should deprecate the old scope
			_, err = newRC.FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())

			// Verify old scope still exists (soft-deleted, not hard-deleted)
			var deprecatedAt *time.Time
			var deprecatedFromResourceID *int
			err = dbConn.QueryRow(
				`SELECT deprecated_at, deprecated_from_resource_id FROM resource_config_scopes WHERE id = $1`,
				oldScopeID,
			).Scan(&deprecatedAt, &deprecatedFromResourceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecatedAt).ToNot(BeNil())
			Expect(*deprecatedFromResourceID).To(Equal(resource.ID()))

			// Verify versions still exist on the deprecated scope
			var versionCount int
			err = dbConn.QueryRow(
				`SELECT COUNT(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`,
				oldScopeID,
			).Scan(&versionCount)
			Expect(err).ToNot(HaveOccurred())
			Expect(versionCount).To(Equal(2))
		})
	})

	Describe("DeprecatedScopes", func() {
		It("returns deprecated scopes for a resource after config change", func() {
			resource := scenario.Resource("some-resource")

			// Trigger a config change to deprecate the current scope
			newRC, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{"some": "changed-source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			_, err = newRC.FindOrCreateScope(intptr(resource.ID()))
			Expect(err).ToNot(HaveOccurred())

			// Reload resource to get fresh state
			reloaded, err := resource.Reload()
			Expect(err).ToNot(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			// Query deprecated scopes
			deprecated, err := resource.DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecated).To(HaveLen(1))
			Expect(deprecated[0].DeprecatedAt).ToNot(BeZero())
		})

		It("returns empty slice when no deprecated scopes exist", func() {
			resource := scenario.Resource("some-resource")

			deprecated, err := resource.DeprecatedScopes()
			Expect(err).ToNot(HaveOccurred())
			Expect(deprecated).To(BeEmpty())
		})
	})

	Describe("CopyVersionsFrom", func() {
		It("copies versions from a source scope to the target scope", func() {
			// Save versions to the current scope
			err := resourceScope.SaveVersions(db.SpanContext{}, []atc.Version{
				{"ref": "v1"},
				{"ref": "v2"},
				{"ref": "v3"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Create a second resource with a different config to get a new scope
			scenario2 := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name: "target-resource",
							Type: "some-base-resource-type",
							Source: atc.Source{
								"some": "other-source",
							},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "target-resource",
									},
								},
							},
						},
					},
				}),
				builder.WithResourceVersions("target-resource"),
			)

			targetResource := scenario2.Resource("target-resource")
			rc2, found, err := resourceConfigFactory.FindResourceConfigByID(targetResource.ResourceConfigID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			targetScope, err := rc2.FindOrCreateScope(intptr(targetResource.ID()))
			Expect(err).ToNot(HaveOccurred())

			// Copy versions from source to target
			copied, err := targetScope.CopyVersionsFrom(resourceScope.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(copied).To(Equal(3))

			// Verify versions exist in target scope (3 copied + versions from WithResourceVersions)
			var count int
			err = dbConn.QueryRow(
				`SELECT COUNT(*) FROM resource_config_versions WHERE resource_config_scope_id = $1`,
				targetScope.ID(),
			).Scan(&count)
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(BeNumerically(">=", 3))
		})

		It("skips duplicates with ON CONFLICT DO NOTHING", func() {
			// Save versions to source scope
			err := resourceScope.SaveVersions(db.SpanContext{}, []atc.Version{
				{"ref": "v1"},
				{"ref": "v2"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Create target scope with one overlapping version
			scenario2 := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name: "target-resource",
							Type: "some-base-resource-type",
							Source: atc.Source{
								"some": "other-source-2",
							},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "target-resource",
									},
								},
							},
						},
					},
				}),
				builder.WithResourceVersions("target-resource"),
			)

			targetResource := scenario2.Resource("target-resource")
			rc2, found, err := resourceConfigFactory.FindResourceConfigByID(targetResource.ResourceConfigID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			targetScope, err := rc2.FindOrCreateScope(intptr(targetResource.ID()))
			Expect(err).ToNot(HaveOccurred())

			// Pre-populate target with v1
			err = targetScope.SaveVersions(db.SpanContext{}, []atc.Version{
				{"ref": "v1"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Copy — should only add v2 (v1 already exists)
			copied, err := targetScope.CopyVersionsFrom(resourceScope.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(copied).To(Equal(1))
		})

		It("returns 0 when all source versions already exist in target", func() {
			// Save versions to the source scope
			err := resourceScope.SaveVersions(db.SpanContext{}, []atc.Version{
				{"ref": "v1"},
				{"ref": "v2"},
			})
			Expect(err).ToNot(HaveOccurred())

			// Copy from source to itself — all versions already exist
			copied, err := resourceScope.CopyVersionsFrom(resourceScope.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(copied).To(Equal(0))
		})
	})

	Describe("AcquireResourceCheckingLock", func() {
		Context("when there has been a check recently", func() {
			var lock lock.Lock
			var err error

			BeforeEach(func() {
				var err error
				var acquired bool
				lock, acquired, err = resourceScope.AcquireResourceCheckingLock(logger)
				Expect(err).ToNot(HaveOccurred())
				Expect(acquired).To(BeTrue())
			})

			AfterEach(func() {
				_ = lock.Release()
			})

			It("does not get the lock", func() {
				_, acquired, err := resourceScope.AcquireResourceCheckingLock(logger)
				Expect(err).ToNot(HaveOccurred())
				Expect(acquired).To(BeFalse())
			})

			Context("and the lock gets released", func() {
				BeforeEach(func() {
					err = lock.Release()
					Expect(err).ToNot(HaveOccurred())
				})

				It("gets the lock", func() {
					lock, acquired, err := resourceScope.AcquireResourceCheckingLock(logger)
					Expect(err).ToNot(HaveOccurred())
					Expect(acquired).To(BeTrue())

					err = lock.Release()
					Expect(err).ToNot(HaveOccurred())
				})
			})
		})

		Context("when there has not been a check recently", func() {
			It("gets and keeps the lock and stops others from periodically getting it", func() {
				lock, acquired, err := resourceScope.AcquireResourceCheckingLock(logger)
				Expect(err).ToNot(HaveOccurred())
				Expect(acquired).To(BeTrue())

				Consistently(func() bool {
					_, acquired, err = resourceScope.AcquireResourceCheckingLock(logger)
					Expect(err).ToNot(HaveOccurred())

					return acquired
				}, 1500*time.Millisecond, 100*time.Millisecond).Should(BeFalse())

				err = lock.Release()
				Expect(err).ToNot(HaveOccurred())

				time.Sleep(time.Second)

				lock, acquired, err = resourceScope.AcquireResourceCheckingLock(logger)
				Expect(err).ToNot(HaveOccurred())
				Expect(acquired).To(BeTrue())

				err = lock.Release()
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})
