package db_test

import (
	"context"
	"fmt"

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

			})
		})

		Context("when it is run for a resource type", func() {
			JustBeforeEach(func() {
				build, created, err = checkFactory.TryCreateCheck(context.TODO(), checkResourceType, checkResourceTypes, fromVersion, manuallyTriggered, false, toDb)
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

})
