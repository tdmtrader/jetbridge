package db_test

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckFactory", func() {
	var (
		err     error
		created bool
		build   db.Build
	)

	BeforeEach(func() {
		atc.DefaultCheckInterval = defaultCheckInterval
		atc.DefaultWebhookInterval = defaultWebhookCheckInterval
		atc.DefaultResourceTypeInterval = defaultResourceTypeInterval
	})

	AfterEach(func() {
		atc.DefaultCheckInterval = 0
		atc.DefaultWebhookInterval = 0
		atc.DefaultResourceTypeInterval = 0
	})

	Describe("TryCreateCheck", func() {
		var (
			checkResource      db.Resource
			checkResourceType  db.ResourceType
			checkResourceTypes db.ResourceTypes

			fromVersion       atc.Version
			manuallyTriggered bool
			toDb              bool

			pipelineNum int

			savePipeline  func(atc.ResourceConfig, atc.ResourceType)
			scopeResource func(db.Resource) db.ResourceConfigScope
			backdateCheck func(db.ResourceConfigScope, time.Duration)
			persistedPlan func(db.Build) atc.Plan
			buildCount    func(string, int) int
		)

		BeforeEach(func() {
			fromVersion = atc.Version{"from": "version"}
			manuallyTriggered = false
			toDb = true
			pipelineNum = 0

			savePipeline = func(resourceConfig atc.ResourceConfig, resourceTypeConfig atc.ResourceType) {
				pipelineNum++

				pipeline, _, err := defaultTeam.SavePipeline(
					atc.PipelineRef{Name: fmt.Sprintf("check-pipeline-%d", pipelineNum)},
					atc.Config{
						Resources:     atc.ResourceConfigs{resourceConfig},
						ResourceTypes: atc.ResourceTypes{resourceTypeConfig},
					},
					db.ConfigVersion(0),
					false,
				)
				Expect(err).NotTo(HaveOccurred())

				checkResourceTypes, err = pipeline.ResourceTypes()
				Expect(err).NotTo(HaveOccurred())
				Expect(checkResourceTypes).To(HaveLen(1))
				checkResourceType = checkResourceTypes[0]

				var found bool
				checkResource, found, err = pipeline.Resource(resourceConfig.Name)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
			}

			scopeResource = func(resource db.Resource) db.ResourceConfigScope {
				resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
				Expect(err).NotTo(HaveOccurred())

				scope, err := resourceConfig.FindOrCreateScope(intptr(resource.ID()))
				Expect(err).NotTo(HaveOccurred())

				err = resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				_, err = resource.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(resource.ResourceConfigScopeID()).NotTo(BeZero())

				return scope
			}

			backdateCheck = func(scope db.ResourceConfigScope, ago time.Duration) {
				found, err := scope.UpdateLastCheckStartTime(99, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				found, err = scope.UpdateLastCheckEndTime(true)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				_, err = dbConn.Exec(`
					UPDATE resource_config_scopes
					SET last_check_start_time = last_check_start_time - $1::interval,
					    last_check_end_time = last_check_end_time - $1::interval
					WHERE id = $2
				`, fmt.Sprintf("%d milliseconds", ago.Milliseconds()), scope.ID())
				Expect(err).NotTo(HaveOccurred())
			}

			persistedPlan = func(b db.Build) atc.Plan {
				reloaded, found, err := buildFactory.Build(b.ID())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				return reloaded.PrivatePlan()
			}

			buildCount = func(column string, id int) int {
				var count int
				err := dbConn.QueryRow(
					fmt.Sprintf(`SELECT COUNT(1) FROM builds WHERE %s = $1`, column), id,
				).Scan(&count)
				Expect(err).NotTo(HaveOccurred())
				return count
			}

			savePipeline(
				atc.ResourceConfig{
					Name:   "some-name",
					Type:   "some-base-resource-type",
					Source: atc.Source{"some": "source"},
					Tags:   atc.Tags{"tag-a", "tag-b"},
				},
				atc.ResourceType{
					Name:     "some-type",
					Type:     "some-base-type",
					Source:   atc.Source{"some": "type-source"},
					Tags:     atc.Tags{"some-tag"},
					Defaults: atc.Source{"some-default": "some-default-value"},
				},
			)
		})

		Context("when it is run on a resource", func() {
			JustBeforeEach(func() {
				build, created, err = checkFactory.TryCreateCheck(context.TODO(), checkResource, checkResourceTypes, fromVersion, manuallyTriggered, false, toDb)
			})

			Context("when the resource parent type is not a custom type", func() {
				Context("when build is created in db", func() {
					It("returns the build", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						Expect(build.ID()).NotTo(BeZero())
						Expect(build.Name()).To(Equal(db.CheckBuildName))
						Expect(build.ResourceID()).To(Equal(checkResource.ID()))
					})

					It("starts the build with the check plan", func() {
						Expect(buildCount("resource_id", checkResource.ID())).To(Equal(1))
						Expect(build.IsManuallyTriggered()).To(BeFalse())

						plan := persistedPlan(build)
						Expect(plan.ID).NotTo(BeEmpty())
						Expect(plan.Check.Name).To(Equal("some-name"))
						Expect(plan.Check.Resource).To(Equal("some-name"))
						Expect(plan.Check.Type).To(Equal("some-base-resource-type"))
						Expect(plan.Check.Tags).To(Equal(atc.Tags{"tag-a", "tag-b"}))
						Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
						Expect(plan.Check.Interval.Interval).To(Equal(defaultCheckInterval))
						Expect(plan.Check.SkipInterval).To(BeFalse())
						Expect(plan.Check.Source).To(Equal(atc.Source{"some": "source"}))
						Expect(plan.Check.TypeImage).To(Equal(atc.TypeImage{BaseType: "some-base-resource-type"}))
					})
				})

				Context("when build is created in memory", func() {
					BeforeEach(func() {
						toDb = false
					})

					It("returns the build", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						Expect(build.ID()).To(BeZero())
						Expect(build.Name()).To(Equal(db.CheckBuildName))
						Expect(build.ResourceID()).To(Equal(checkResource.ID()))
					})

					It("starts the build with the check plan", func() {
						Expect(buildCount("resource_id", checkResource.ID())).To(BeZero())

						plan := build.PrivatePlan()
						Expect(plan.Check.Name).To(Equal("some-name"))
						Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
						Expect(plan.Check.Interval.Interval).To(Equal(defaultCheckInterval))
						Expect(plan.Check.Source).To(Equal(atc.Source{"some": "source"}))
					})

					It("numbers the build from the factory's sequence generator", func() {
						Expect(build.RunStateID()).To(Equal("in-memory-check-build:1"))

						next, _, err := checkFactory.TryCreateCheck(context.TODO(), checkResource, checkResourceTypes, fromVersion, true, false, false)
						Expect(err).NotTo(HaveOccurred())
						Expect(next.RunStateID()).To(Equal("in-memory-check-build:2"))
					})

					It("send the build to tracker", func() {
						var sent db.Build
						select {
						case sent = <-checkBuildChan:
						default:
						}
						Expect(sent).To(Equal(build))
					})

					Context("when an in-memory check is already in-flight for the same scope", func() {
						BeforeEach(func() {
							scopeResource(checkResource)

							firstBuild, firstCreated, firstErr := checkFactory.TryCreateCheck(
								context.TODO(), checkResource, checkResourceTypes, fromVersion, false, false, false)
							Expect(firstErr).NotTo(HaveOccurred())
							Expect(firstCreated).To(BeTrue())
							Expect(firstBuild).NotTo(BeNil())
						})

						It("skips creation for the second call", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeFalse())
							Expect(build).To(BeNil())
						})

						It("does not create a second in-memory build", func() {
							Expect(checkBuildChan).To(HaveLen(1))
						})

						Context("but the check is manually triggered", func() {
							BeforeEach(func() {
								manuallyTriggered = true
							})

							It("creates the build anyway", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
								Expect(checkBuildChan).To(HaveLen(2))
							})
						})

						Context("when the in-flight build finishes successfully", func() {
							BeforeEach(func() {
								select {
								case b := <-checkBuildChan:
									Expect(b.Finish(db.BuildStatusSucceeded)).To(Succeed())
								default:
									Fail("expected a build on the channel")
								}
							})

							It("allows a new check to be created", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
							})
						})

						Context("when the in-flight build errors", func() {
							BeforeEach(func() {
								select {
								case b := <-checkBuildChan:
									Expect(b.Finish(db.BuildStatusErrored)).To(Succeed())
								default:
									Fail("expected a build on the channel")
								}
							})

							It("allows a new check to be created", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
							})
						})
					})
				})

				Context("when the interval has not elapsed", func() {
					BeforeEach(func() {
						scope := scopeResource(checkResource)
						backdateCheck(scope, defaultCheckInterval/2)

						_, err := checkResource.Reload()
						Expect(err).NotTo(HaveOccurred())
					})

					It("does not create a build for the resource", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						Expect(buildCount("resource_id", checkResource.ID())).To(BeZero())
					})

					Context("but the check is manually triggered", func() {
						BeforeEach(func() {
							manuallyTriggered = true
						})

						It("creates the build anyway", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeTrue())
							Expect(build.IsManuallyTriggered()).To(BeTrue())
							Expect(persistedPlan(build).Check.SkipInterval).To(BeTrue())
						})
					})
				})

				Context("when a check build is already running for the resource", func() {
					BeforeEach(func() {
						_, alreadyCreated, err := checkResource.CreateBuild(context.TODO(), false, atc.Plan{ID: "some-plan"})
						Expect(err).NotTo(HaveOccurred())
						Expect(alreadyCreated).To(BeTrue())
					})

					It("returns false", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						Expect(buildCount("resource_id", checkResource.ID())).To(Equal(1))
					})
				})
			})

			Context("when the resource has a webhook configured", func() {
				BeforeEach(func() {
					savePipeline(
						atc.ResourceConfig{
							Name:         "some-name",
							Type:         "some-base-resource-type",
							Source:       atc.Source{"some": "source"},
							WebhookToken: "some-token",
						},
						atc.ResourceType{
							Name:   "some-type",
							Type:   "some-base-type",
							Source: atc.Source{"some": "type-source"},
						},
					)
				})

				It("creates a check plan with the default webhook interval", func() {
					plan := persistedPlan(build)
					Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
					Expect(plan.Check.Interval.Interval).To(Equal(defaultWebhookCheckInterval))
					Expect(plan.Check.Source).To(Equal(atc.Source{"some": "source"}))
					Expect(plan.Check.TypeImage).To(Equal(atc.TypeImage{BaseType: "some-base-resource-type"}))
				})

				Context("when the default webhook interval has not elapsed", func() {
					BeforeEach(func() {
						scope := scopeResource(checkResource)
						backdateCheck(scope, defaultWebhookCheckInterval/2)

						_, err := checkResource.Reload()
						Expect(err).NotTo(HaveOccurred())
					})

					It("does not create a build for the resource", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						Expect(buildCount("resource_id", checkResource.ID())).To(BeZero())
					})
				})
			})

			Context("when an interval is specified", func() {
				BeforeEach(func() {
					savePipeline(
						atc.ResourceConfig{
							Name:       "some-name",
							Type:       "some-base-resource-type",
							Source:     atc.Source{"some": "source"},
							CheckEvery: &atc.CheckEvery{Interval: 42 * time.Second},
						},
						atc.ResourceType{
							Name:   "some-type",
							Type:   "some-base-type",
							Source: atc.Source{"some": "type-source"},
						},
					)
				})

				It("sets it in the check plan", func() {
					plan := persistedPlan(build)
					Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
					Expect(plan.Check.Interval.Interval).To(Equal(42 * time.Second))
					Expect(plan.Check.Source).To(Equal(atc.Source{"some": "source"}))
					Expect(plan.Check.TypeImage).To(Equal(atc.TypeImage{BaseType: "some-base-resource-type"}))
				})
			})

			Context("when CheckEvery is never", func() {
				BeforeEach(func() {
					savePipeline(
						atc.ResourceConfig{
							Name:       "some-name",
							Type:       "some-base-resource-type",
							Source:     atc.Source{"some": "source"},
							CheckEvery: &atc.CheckEvery{Never: true},
						},
						atc.ResourceType{
							Name:   "some-type",
							Type:   "some-base-type",
							Source: atc.Source{"some": "type-source"},
						},
					)
				})

				It("sets it in the check plan", func() {
					plan := persistedPlan(build)
					Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
					Expect(plan.Check.Interval.Never).To(BeTrue())
					Expect(plan.Check.Source).To(Equal(atc.Source{"some": "source"}))
					Expect(plan.Check.TypeImage).To(Equal(atc.TypeImage{BaseType: "some-base-resource-type"}))
				})
			})

			Context("when the resource has a parent type", func() {
				BeforeEach(func() {
					savePipeline(
						atc.ResourceConfig{
							Name:   "some-name",
							Type:   "custom-type",
							Source: atc.Source{"some": "source"},
							Tags:   atc.Tags{"tag-a", "tag-b"},
						},
						atc.ResourceType{
							Name:     "custom-type",
							Type:     "some-base-type",
							Source:   atc.Source{"some": "type-source"},
							Tags:     atc.Tags{"some-tag"},
							Defaults: atc.Source{"sdk": "sdk"},
						},
					)
				})

				It("creates a check plan with the parent type's defaults and image", func() {
					plan := persistedPlan(build)
					Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
					Expect(plan.Check.Interval.Interval).To(Equal(defaultCheckInterval))
					Expect(plan.Check.Source).To(Equal(atc.Source{"sdk": "sdk", "some": "source"}))

					typeImage := plan.Check.TypeImage
					Expect(typeImage.BaseType).To(Equal("some-base-type"))
					Expect(typeImage.GetPlan).NotTo(BeNil())
					Expect(typeImage.GetPlan.Get.Name).To(Equal("custom-type"))
					Expect(typeImage.GetPlan.Get.Type).To(Equal("some-base-type"))
					Expect(typeImage.GetPlan.Get.Source).To(Equal(atc.Source{"some": "type-source"}))
					Expect(typeImage.GetPlan.Get.Tags).To(Equal(atc.Tags{"some-tag"}))
					Expect(typeImage.CheckPlan).NotTo(BeNil())
					Expect(typeImage.CheckPlan.Check.ResourceType).To(Equal("custom-type"))
				})

				It("returns the build", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(build.ID()).NotTo(BeZero())
					Expect(build.ResourceID()).To(Equal(checkResource.ID()))
				})

				It("starts the build with the check plan", func() {
					Expect(build.IsManuallyTriggered()).To(BeFalse())

					plan := persistedPlan(build)
					Expect(plan.ID).NotTo(BeEmpty())
					Expect(plan.Check.Resource).To(Equal("some-name"))
				})
			})
		})

		Context("when it is run for a resource type", func() {
			JustBeforeEach(func() {
				build, created, err = checkFactory.TryCreateCheck(context.TODO(), checkResourceType, checkResourceTypes, fromVersion, manuallyTriggered, false, toDb)
			})

			Context("when build is created in db", func() {
				It("creates a check plan", func() {
					plan := persistedPlan(build)
					Expect(plan.Check.Name).To(Equal("some-type"))
					Expect(plan.Check.ResourceType).To(Equal("some-type"))
					Expect(plan.Check.FromVersion).To(Equal(atc.Version{"from": "version"}))
					Expect(plan.Check.Interval.Interval).To(Equal(defaultResourceTypeInterval))
					Expect(plan.Check.Source).To(Equal(atc.Source{"some": "type-source"}))
					Expect(plan.Check.TypeImage).To(Equal(atc.TypeImage{BaseType: "some-base-type"}))
				})

				It("returns the build", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(build.ID()).NotTo(BeZero())
					Expect(build.Name()).To(Equal(db.CheckBuildName))
				})

				It("starts the build with the check plan", func() {
					Expect(buildCount("resource_type_id", checkResourceType.ID())).To(Equal(1))
					Expect(build.IsManuallyTriggered()).To(BeFalse())
					Expect(persistedPlan(build).ID).NotTo(BeEmpty())
				})
			})

			Context("when the build is asked for in memory", func() {
				BeforeEach(func() {
					toDb = false
				})

				It("returns false, as resource types have no in-memory check build", func() {
					Expect(err).To(MatchError(ContainSubstring("resource type not supporting in-memory check build")))
					Expect(created).To(BeFalse())
					Expect(build).To(BeNil())
					Expect(checkBuildChan).To(BeEmpty())
				})

				Context("when the resource type has a config scope", func() {
					BeforeEach(func() {
						resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(checkResourceType.Type(), checkResourceType.Source(), nil)
						Expect(err).NotTo(HaveOccurred())

						scope, err := resourceConfig.FindOrCreateScope(nil)
						Expect(err).NotTo(HaveOccurred())

						err = checkResourceType.SetResourceConfigScope(scope)
						Expect(err).NotTo(HaveOccurred())

						_, err = checkResourceType.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(checkResourceType.ResourceConfigScopeID()).NotTo(BeZero())
					})

					It("stops tracking the scope as in-flight, so the next check is attempted", func() {
						Expect(err).To(HaveOccurred())

						_, retryCreated, retryErr := checkFactory.TryCreateCheck(
							context.TODO(), checkResourceType, checkResourceTypes, fromVersion, false, false, false)
						Expect(retryErr).To(MatchError(ContainSubstring("resource type not supporting in-memory check build")))
						Expect(retryCreated).To(BeFalse())
					})
				})
			})
		})
	})

	Describe("Resources", func() {
		var (
			resources                  []db.Resource
			putOnlyResource            db.Resource
			putOnlyResourceConfigScope db.ResourceConfigScope
		)

		BeforeEach(func() {
			defaultPipelineConfig = atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-put-only-resource",
								},
							},
						},
					},
				},
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-base-resource-type",
						Source: atc.Source{
							"some": "source",
						},
					},
					{
						Name: "some-put-only-resource",
						Type: "some-base-resource-type",
						Source: atc.Source{
							"some": "source",
						},
					},
				},
				ResourceTypes: atc.ResourceTypes{
					{
						Name: "some-type",
						Type: "some-base-resource-type",
						Source: atc.Source{
							"some-type": "source",
						},
					},
				},
			}

			defaultPipelineRef = atc.PipelineRef{Name: "default-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
			defaultPipeline, _, err = defaultTeam.SavePipeline(defaultPipelineRef, defaultPipelineConfig, db.ConfigVersion(1), false)
			Expect(err).NotTo(HaveOccurred())

			var found bool
			putOnlyResource, found, err = defaultPipeline.Resource("some-put-only-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-base-resource-type",
				atc.Source{
					"some": "source",
				},
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

			putOnlyResourceConfigScope, err = resourceConfig.FindOrCreateScope(intptr(putOnlyResource.ID()))
			Expect(err).NotTo(HaveOccurred())

			err = putOnlyResource.SetResourceConfigScope(putOnlyResourceConfigScope)
			Expect(err).NotTo(HaveOccurred())

			found, err = putOnlyResourceConfigScope.UpdateLastCheckStartTime(99, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			found, err = putOnlyResourceConfigScope.UpdateLastCheckEndTime(true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		JustBeforeEach(func() {
			resources, err = checkFactory.Resources()
			Expect(err).NotTo(HaveOccurred())
		})

		It("include only resources-in-use in return", func() {
			Expect(resources).To(HaveLen(1))
			Expect(resources[0].Name()).To(Equal("some-resource"))
		})

		Context("when the resource is not active", func() {
			BeforeEach(func() {
				_, err = dbConn.Exec(`UPDATE resources SET active = false`)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not return the resource", func() {
				Expect(resources).To(HaveLen(0))
			})
		})

		Context("when the resource pipeline is paused", func() {
			BeforeEach(func() {
				_, err = dbConn.Exec(`UPDATE pipelines SET paused = true`)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not return the resource", func() {
				Expect(resources).To(HaveLen(0))
			})
		})

		Context("when a put-only resource", func() {
			Context("has failed to check last time", func() {
				BeforeEach(func() {
					found, err := putOnlyResourceConfigScope.UpdateLastCheckStartTime(99, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					found, err = putOnlyResourceConfigScope.UpdateLastCheckEndTime(false)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
				})
				It("returns the resource", func() {
					Expect(resources).To(HaveLen(2))
				})
			})
			Context("has NOT errored", func() {
				BeforeEach(func() {
					By("creating a successful build for the put-only resource")
					found, err := putOnlyResourceConfigScope.UpdateLastCheckStartTime(99, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					found, err = putOnlyResourceConfigScope.UpdateLastCheckEndTime(true)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
				})
				It("returns does not return the resource", func() {
					Expect(resources).To(HaveLen(1))
				})
			})
		})
	})

	Describe("ResourceTypes", func() {
		var (
			resourceTypes    map[int]db.ResourceTypes
			somePipeline     db.Pipeline
			atcResourceTypes atc.ResourceTypes
		)

		JustBeforeEach(func() {
			resourceTypes, err = checkFactory.ResourceTypesByPipeline()
			Expect(err).NotTo(HaveOccurred())
		})

		BeforeEach(func() {
			atcResourceTypes = atc.ResourceTypes{
				{
					Name: "some-type",
					Type: "some-base-resource-type",
					Source: atc.Source{
						"some-type": "source",
					},
				},
				{
					Name: "some-other-type",
					Type: "some-base-resource-type",
					Source: atc.Source{
						"some-other-type": "source",
					},
				},
			}

			somePipelineConfig := atc.Config{
				ResourceTypes: atcResourceTypes,
			}

			somePipelineRef := atc.PipelineRef{Name: "some-pipeline"}
			somePipeline, _, err = defaultTeam.SavePipeline(somePipelineRef, somePipelineConfig, db.ConfigVersion(1), false)
			Expect(err).NotTo(HaveOccurred())
		})

		It("include resource types in return", func() {
			Expect(resourceTypes).To(HaveLen(2))

			Expect(resourceTypes[defaultPipeline.ID()]).To(HaveLen(1))
			Expect(resourceTypes[defaultPipeline.ID()][0].Name()).To(Equal("some-type"))

			Expect(resourceTypes[somePipeline.ID()]).To(HaveLen(2))
			Expect(resourceTypes[somePipeline.ID()].Deserialize()).To(ConsistOf(atcResourceTypes))
		})

		Context("when the resource type is not active", func() {
			BeforeEach(func() {
				_, err = dbConn.Exec(`UPDATE resource_types SET active = false`)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not return the resource type", func() {
				Expect(resourceTypes).To(HaveLen(0))
			})
		})

		Context("when the pipeline is paused", func() {
			BeforeEach(func() {
				_, err = dbConn.Exec(`UPDATE pipelines SET paused = true`)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does not return resource types from paused pipelines", func() {
				Expect(resourceTypes).To(HaveLen(0))
			})
		})
	})
})
