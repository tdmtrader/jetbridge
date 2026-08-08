package db_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type inMemoryBuildErrorResource struct {
	db.Resource
	err error
}

func (r inMemoryBuildErrorResource) CreateInMemoryBuild(context.Context, atc.Plan, util.SequenceGenerator) (db.Build, error) {
	return nil, r.err
}

func saveCheckFactoryResource(resourceConfig atc.ResourceConfig, resourceTypeConfigs atc.ResourceTypes) (db.Resource, db.ResourceTypes, db.Pipeline) {
	GinkgoHelper()

	pipeline, created, err := defaultTeam.SavePipeline(
		atc.PipelineRef{
			Name:         "check-factory-pipeline",
			InstanceVars: atc.InstanceVars{"fixture": "try-create-check"},
		},
		atc.Config{
			Resources:     atc.ResourceConfigs{resourceConfig},
			ResourceTypes: resourceTypeConfigs,
		},
		db.ConfigVersion(1),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())

	resource, found, err := pipeline.Resource(resourceConfig.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	found, err = resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	resourceTypes, err := pipeline.ResourceTypes()
	Expect(err).NotTo(HaveOccurred())
	for _, resourceType := range resourceTypes {
		found, err = resourceType.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	}

	return resource, resourceTypes, pipeline
}

func attachCheckScope(resource db.Resource) (db.Resource, db.ResourceConfigScope) {
	GinkgoHelper()

	resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
		resource.Type(),
		resource.Source(),
		nil,
	)
	Expect(err).NotTo(HaveOccurred())

	scope, err := resourceConfig.FindOrCreateScope(intptr(resource.ID()))
	Expect(err).NotTo(HaveOccurred())
	Expect(scope.ID()).NotTo(BeZero())

	Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(resource.ResourceConfigScopeID()).To(Equal(scope.ID()))

	return resource, scope
}

func recentlyCompletedCheck(resource db.Resource, scope db.ResourceConfigScope, age time.Duration) db.Resource {
	GinkgoHelper()

	seedPlan := atc.NewPlanFactory(0).NewPlan(atc.CheckPlan{
		Name:     resource.Name(),
		Type:     resource.Type(),
		Source:   resource.Source(),
		Resource: resource.Name(),
	})
	seedBuild, created, err := resource.CreateBuild(context.TODO(), false, seedPlan)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	Expect(seedBuild).NotTo(BeNil())

	updated, err := scope.UpdateLastCheckStartTime(seedBuild.ID(), seedBuild.PublicPlan())
	Expect(err).NotTo(HaveOccurred())
	Expect(updated).To(BeTrue())
	Expect(seedBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
	updated, err = scope.UpdateLastCheckEndTime(true)
	Expect(err).NotTo(HaveOccurred())
	Expect(updated).To(BeTrue())

	// Keep the completed-check lifecycle ordering intact while making its age
	// deterministic: scanned resources only expose an end time after the start.
	var backdatedEndTime time.Time
	err = dbConn.QueryRow(`
		UPDATE resource_config_scopes
		SET last_check_start_time = clock_timestamp() - (($1::double precision + 1) * interval '1 second'),
			last_check_end_time = clock_timestamp() - ($1::double precision * interval '1 second')
		WHERE id = $2
		RETURNING last_check_end_time
	`, age.Seconds(), scope.ID()).Scan(&backdatedEndTime)
	Expect(err).NotTo(HaveOccurred())

	found, err := resource.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(resource.LastCheckStartTime()).To(BeTemporally("<", backdatedEndTime))
	Expect(resource.LastCheckEndTime()).To(Equal(backdatedEndTime))

	return resource
}

func expectUnfinishedResourceChecks(resource db.Resource, expected int) {
	GinkgoHelper()

	var unfinishedBuilds int
	err := dbConn.QueryRow(
		`SELECT count(*) FROM builds WHERE resource_id = $1 AND completed = false`,
		resource.ID(),
	).Scan(&unfinishedBuilds)
	Expect(err).NotTo(HaveOccurred())
	Expect(unfinishedBuilds).To(Equal(expected))
}

func expectStartedResourceCheck(build db.Build, resource db.Resource, pipeline db.Pipeline, manuallyTriggered bool) {
	GinkgoHelper()

	Expect(build).NotTo(BeNil())
	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(build.ResourceID()).To(Equal(resource.ID()))
	Expect(build.ResourceName()).To(Equal(resource.Name()))
	Expect(build.ResourceTypeID()).To(BeZero())
	Expect(build.PipelineID()).To(Equal(pipeline.ID()))
	Expect(build.PipelineName()).To(Equal(pipeline.Name()))
	Expect(build.PipelineInstanceVars()).To(Equal(pipeline.InstanceVars()))
	Expect(build.TeamID()).To(Equal(defaultTeam.ID()))
	Expect(build.TeamName()).To(Equal(defaultTeam.Name()))
	Expect(build.Name()).To(Equal(db.CheckBuildName))
	Expect(build.RunStateID()).To(Equal(fmt.Sprintf("build:%d", build.ID())))
	Expect(build.Status()).To(Equal(db.BuildStatusStarted))
	Expect(build.IsRunning()).To(BeTrue())
	Expect(build.IsCompleted()).To(BeFalse())
	Expect(build.IsManuallyTriggered()).To(Equal(manuallyTriggered))
	Expect(build.HasPlan()).To(BeTrue())
	Expect(build.PublicPlan()).NotTo(BeNil())
	privatePlan := build.PrivatePlan()
	Expect(privatePlan.ID).NotTo(BeEmpty())
	Expect(build.PublicPlan()).To(Equal(privatePlan.Public()))
	expectUnfinishedResourceChecks(resource, 1)
}

func expectBaseResourceCheckPlan(build db.Build, fromVersion atc.Version, interval atc.CheckEvery, skipInterval bool) {
	GinkgoHelper()

	plan := build.PrivatePlan()
	Expect(plan.ID).NotTo(BeEmpty())
	Expect(plan).To(Equal(atc.Plan{
		ID: plan.ID,
		Check: &atc.CheckPlan{
			Name:         "some-name",
			Type:         "base-type",
			Source:       atc.Source{"some": "source"},
			TypeImage:    atc.TypeImage{BaseType: "base-type"},
			FromVersion:  fromVersion,
			Resource:     "some-name",
			Interval:     interval,
			SkipInterval: skipInterval,
			Tags:         atc.Tags{"tag-a", "tag-b"},
		},
	}))
}

func expectCustomResourceCheckPlan(build db.Build, fromVersion atc.Version) {
	GinkgoHelper()

	plan := build.PrivatePlan()
	Expect(plan.ID).NotTo(BeEmpty())
	imageCheckPlanID := plan.ID + "/image-check"
	imageGetPlanID := plan.ID + "/image-get"
	Expect(plan).To(Equal(atc.Plan{
		ID: plan.ID,
		Check: &atc.CheckPlan{
			Name:   "some-name",
			Type:   "custom-type",
			Source: atc.Source{"sdk": "sdk", "some": "source"},
			TypeImage: atc.TypeImage{
				BaseType: "some-base-type",
				CheckPlan: &atc.Plan{
					ID: imageCheckPlanID,
					Check: &atc.CheckPlan{
						Name:         "custom-type",
						Type:         "some-base-type",
						Source:       atc.Source{"some": "type-source"},
						TypeImage:    atc.TypeImage{BaseType: "some-base-type"},
						ResourceType: "custom-type",
						Interval:     atc.CheckEvery{Interval: defaultCheckInterval},
						Tags:         atc.Tags{"some-tag"},
					},
				},
				GetPlan: &atc.Plan{
					ID: imageGetPlanID,
					Get: &atc.GetPlan{
						Name:        "custom-type",
						Type:        "some-base-type",
						Source:      atc.Source{"some": "type-source"},
						TypeImage:   atc.TypeImage{BaseType: "some-base-type"},
						VersionFrom: &imageCheckPlanID,
						Tags:        atc.Tags{"some-tag"},
					},
				},
			},
			FromVersion: fromVersion,
			Resource:    "some-name",
			Interval:    atc.CheckEvery{Interval: defaultCheckInterval},
			Tags:        atc.Tags{"tag-a", "tag-b"},
		},
	}))
}

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
			resourceConfig      atc.ResourceConfig
			resourceTypeConfigs atc.ResourceTypes
			resource            db.Resource
			resourceTypes       db.ResourceTypes
			pipeline            db.Pipeline
			checkable           db.Checkable
			fromVersion         atc.Version
			manuallyTriggered   bool
			toDb                bool

			setupResource func()
		)

		BeforeEach(func() {
			fromVersion = atc.Version{"from": "version"}
			resourceConfig = atc.ResourceConfig{
				Name:   "some-name",
				Type:   "base-type",
				Source: atc.Source{"some": "source"},
				Tags:   atc.Tags{"tag-a", "tag-b"},
			}
			resourceTypeConfigs = nil
			resource = nil
			resourceTypes = nil
			pipeline = nil
			checkable = nil
			manuallyTriggered = false
			toDb = true

			setupResource = func() {
				GinkgoHelper()
				if resource != nil {
					return
				}

				resource, resourceTypes, pipeline = saveCheckFactoryResource(resourceConfig, resourceTypeConfigs)
				checkable = resource
			}
		})

		Context("when it is run on a resource", func() {
			JustBeforeEach(func() {
				setupResource()
				build, created, err = checkFactory.TryCreateCheck(context.TODO(), checkable, resourceTypes, fromVersion, manuallyTriggered, false, toDb)
			})

			Context("when the resource parent type is not a custom type", func() {
				Context("when build is created in db", func() {
					It("returns the build", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						expectStartedResourceCheck(build, resource, pipeline, false)
						expectBaseResourceCheckPlan(
							build,
							fromVersion,
							atc.CheckEvery{Interval: defaultCheckInterval},
							false,
						)
					})

					It("starts the build with the check plan", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						expectStartedResourceCheck(build, resource, pipeline, false)
						expectBaseResourceCheckPlan(
							build,
							fromVersion,
							atc.CheckEvery{Interval: defaultCheckInterval},
							false,
						)
					})

					Context("when a build is not created", func() {
						var existingBuild db.Build

						BeforeEach(func() {
							setupResource()
							var existingCreated bool
							existingBuild, existingCreated, err = checkFactory.TryCreateCheck(
								context.TODO(), resource, resourceTypes, fromVersion, false, false, true,
							)
							Expect(err).NotTo(HaveOccurred())
							Expect(existingCreated).To(BeTrue())
							Expect(existingBuild).NotTo(BeNil())
						})

						It("returns false", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeFalse())
							Expect(build).To(BeNil())
							expectUnfinishedResourceChecks(resource, 1)
							found, reloadErr := existingBuild.Reload()
							Expect(reloadErr).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							Expect(existingBuild.IsCompleted()).To(BeFalse())
						})
					})
				})

				Context("when build is created in memory", func() {
					BeforeEach(func() {
						toDb = false
					})

					It("returns the build", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						Expect(build).NotTo(BeNil())
						Expect(build.ID()).To(BeZero())
						Expect(build.RunStateID()).To(Equal("in-memory-check-build:1"))
						Expect(build.ResourceID()).To(Equal(resource.ID()))
						Expect(build.ResourceName()).To(Equal(resource.Name()))
						Expect(build.PipelineID()).To(Equal(pipeline.ID()))
						Expect(build.TeamID()).To(Equal(defaultTeam.ID()))
						Expect(build.Status()).To(Equal(db.BuildStatusPending))
						Expect(build.IsRunning()).To(BeTrue())
						Expect(build.IsCompleted()).To(BeFalse())
						expectBaseResourceCheckPlan(
							build,
							fromVersion,
							atc.CheckEvery{Interval: defaultCheckInterval},
							false,
						)
					})

					It("starts the build with the check plan", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						Expect(build.RunStateID()).To(Equal("in-memory-check-build:1"))
						expectBaseResourceCheckPlan(
							build,
							fromVersion,
							atc.CheckEvery{Interval: defaultCheckInterval},
							false,
						)
					})

					It("send the build to tracker", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						var queuedBuild db.Build
						Expect(checkBuildChan).To(Receive(&queuedBuild))
						Expect(queuedBuild).To(BeIdenticalTo(build))
						Expect(queuedBuild.RunStateID()).To(Equal("in-memory-check-build:1"))
						Expect(checkBuildChan).NotTo(Receive())
					})

					Context("when a build is not created", func() {
						var sentinel error

						BeforeEach(func() {
							setupResource()
							sentinel = errors.New("some-error")
							checkable = inMemoryBuildErrorResource{
								Resource: resource,
								err:      sentinel,
							}
						})

						It("returns false", func() {
							Expect(err).To(BeIdenticalTo(sentinel))
							Expect(created).To(BeFalse())
							Expect(build).To(BeNil())
							Expect(checkBuildChan).NotTo(Receive())
							expectUnfinishedResourceChecks(resource, 0)
						})
					})

					Context("when an in-memory check is already in-flight for the same scope", func() {
						var firstBuild db.Build

						BeforeEach(func() {
							setupResource()
							resource, _ = attachCheckScope(resource)
							checkable = resource
							Expect(resource.ResourceConfigScopeID()).NotTo(BeZero())

							var firstCreated bool
							var firstErr error
							firstBuild, firstCreated, firstErr = checkFactory.TryCreateCheck(
								context.TODO(), resource, resourceTypes, fromVersion, false, false, false,
							)
							Expect(firstErr).NotTo(HaveOccurred())
							Expect(firstCreated).To(BeTrue())
							Expect(firstBuild).NotTo(BeNil())
							Expect(firstBuild.RunStateID()).To(Equal("in-memory-check-build:1"))
						})

						It("skips creation for the second call", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeFalse())
							Expect(build).To(BeNil())
							var queuedBuild db.Build
							Expect(checkBuildChan).To(Receive(&queuedBuild))
							Expect(queuedBuild).To(BeIdenticalTo(firstBuild))
							Expect(checkBuildChan).NotTo(Receive())
						})

						It("does not create a second in-memory build", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeFalse())
							Expect(build).To(BeNil())
							Expect(firstBuild.RunStateID()).To(Equal("in-memory-check-build:1"))
							var queuedBuild db.Build
							Expect(checkBuildChan).To(Receive(&queuedBuild))
							Expect(queuedBuild).To(BeIdenticalTo(firstBuild))
							Expect(checkBuildChan).NotTo(Receive())
						})

						Context("but the check is manually triggered", func() {
							BeforeEach(func() {
								manuallyTriggered = true
							})

							It("creates the build anyway", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
								Expect(firstBuild.RunStateID()).To(Equal("in-memory-check-build:1"))
								Expect(build.RunStateID()).To(Equal("in-memory-check-build:2"))
								Expect(build.RunStateID()).NotTo(Equal(firstBuild.RunStateID()))
								Expect(build.IsManuallyTriggered()).To(BeFalse())
								var queuedFirstBuild, queuedSecondBuild db.Build
								Expect(checkBuildChan).To(Receive(&queuedFirstBuild))
								Expect(queuedFirstBuild).To(BeIdenticalTo(firstBuild))
								Expect(checkBuildChan).To(Receive(&queuedSecondBuild))
								Expect(queuedSecondBuild).To(BeIdenticalTo(build))
								Expect(checkBuildChan).NotTo(Receive())
							})
						})

						Context("when the in-flight build finishes successfully", func() {
							BeforeEach(func() {
								var queuedBuild db.Build
								Expect(checkBuildChan).To(Receive(&queuedBuild))
								Expect(queuedBuild).To(BeIdenticalTo(firstBuild))
								Expect(firstBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
							})

							It("allows a new check to be created", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
								Expect(build.RunStateID()).To(Equal("in-memory-check-build:2"))
								var queuedBuild db.Build
								Expect(checkBuildChan).To(Receive(&queuedBuild))
								Expect(queuedBuild).To(BeIdenticalTo(build))
								Expect(checkBuildChan).NotTo(Receive())
							})
						})

						Context("when the in-flight build errors", func() {
							BeforeEach(func() {
								var queuedBuild db.Build
								Expect(checkBuildChan).To(Receive(&queuedBuild))
								Expect(queuedBuild).To(BeIdenticalTo(firstBuild))
								Expect(firstBuild.Finish(db.BuildStatusErrored)).To(Succeed())
							})

							It("allows a new check to be created", func() {
								Expect(err).NotTo(HaveOccurred())
								Expect(created).To(BeTrue())
								Expect(build).NotTo(BeNil())
								Expect(build.RunStateID()).To(Equal("in-memory-check-build:2"))
								var queuedBuild db.Build
								Expect(checkBuildChan).To(Receive(&queuedBuild))
								Expect(queuedBuild).To(BeIdenticalTo(build))
								Expect(checkBuildChan).NotTo(Receive())
							})
						})
					})
				})

				Context("when the interval has not elapsed", func() {
					BeforeEach(func() {
						setupResource()
						var scope db.ResourceConfigScope
						resource, scope = attachCheckScope(resource)
						resource = recentlyCompletedCheck(resource, scope, defaultCheckInterval/2)
						checkable = resource
					})

					It("does not create a build for the resource", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						expectUnfinishedResourceChecks(resource, 0)
						Expect(checkBuildChan).NotTo(Receive())
					})

					Context("but the check is manually triggered", func() {
						BeforeEach(func() {
							manuallyTriggered = true
						})

						It("creates the build anyway", func() {
							Expect(err).NotTo(HaveOccurred())
							Expect(created).To(BeTrue())
							expectStartedResourceCheck(build, resource, pipeline, true)
							expectBaseResourceCheckPlan(
								build,
								fromVersion,
								atc.CheckEvery{Interval: defaultCheckInterval},
								true,
							)
						})
					})
				})

				Context("when a build is not created", func() {
					var existingBuild db.Build

					BeforeEach(func() {
						setupResource()
						var existingCreated bool
						existingBuild, existingCreated, err = checkFactory.TryCreateCheck(
							context.TODO(), resource, resourceTypes, fromVersion, false, false, true,
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(existingCreated).To(BeTrue())
						Expect(existingBuild).NotTo(BeNil())
					})

					It("returns false", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						expectUnfinishedResourceChecks(resource, 1)
						found, reloadErr := existingBuild.Reload()
						Expect(reloadErr).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(existingBuild.IsCompleted()).To(BeFalse())
					})
				})
			})

			Context("when the resource has a webhook configured", func() {
				BeforeEach(func() {
					resourceConfig.WebhookToken = "some-webhook-token"
				})

				It("creates a check plan with the default webhook interval", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(resource.WebhookToken()).To(Equal("some-webhook-token"))
					Expect(resource.HasWebhook()).To(BeTrue())
					expectStartedResourceCheck(build, resource, pipeline, false)
					expectBaseResourceCheckPlan(
						build,
						fromVersion,
						atc.CheckEvery{Interval: defaultWebhookCheckInterval},
						false,
					)
				})

				Context("when the default webhook interval has not elapsed", func() {
					BeforeEach(func() {
						setupResource()
						var scope db.ResourceConfigScope
						resource, scope = attachCheckScope(resource)
						resource = recentlyCompletedCheck(resource, scope, defaultWebhookCheckInterval/2)
						checkable = resource
					})

					It("does not create a build for the resource", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeFalse())
						Expect(build).To(BeNil())
						expectUnfinishedResourceChecks(resource, 0)
						Expect(checkBuildChan).NotTo(Receive())
					})
				})
			})

			Context("when an interval is specified", func() {
				BeforeEach(func() {
					resourceConfig.CheckEvery = &atc.CheckEvery{Interval: 42 * time.Second}
				})

				It("sets it in the check plan", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(resource.CheckEvery()).To(Equal(&atc.CheckEvery{Interval: 42 * time.Second}))
					expectStartedResourceCheck(build, resource, pipeline, false)
					expectBaseResourceCheckPlan(
						build,
						fromVersion,
						atc.CheckEvery{Interval: 42 * time.Second},
						false,
					)
				})
			})

			Context("when CheckEvery is never", func() {
				BeforeEach(func() {
					resourceConfig.CheckEvery = &atc.CheckEvery{Never: true}
				})

				It("sets it in the check plan", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(resource.CheckEvery()).To(Equal(&atc.CheckEvery{Never: true}))
					expectStartedResourceCheck(build, resource, pipeline, false)
					expectBaseResourceCheckPlan(
						build,
						fromVersion,
						atc.CheckEvery{Never: true},
						false,
					)
				})
			})

			Context("when the resource has a parent type", func() {
				BeforeEach(func() {
					resourceConfig.Type = "custom-type"
					resourceTypeConfigs = atc.ResourceTypes{
						{
							Name:     "custom-type",
							Type:     "some-base-type",
							Source:   atc.Source{"some": "type-source"},
							Defaults: atc.Source{"sdk": "sdk"},
							Tags:     atc.Tags{"some-tag"},
						},
					}
				})

				Context("when the checkable's interval has elapsed", func() {
					BeforeEach(func() {
						setupResource()
						var scope db.ResourceConfigScope
						resource, scope = attachCheckScope(resource)
						resource = recentlyCompletedCheck(resource, scope, defaultCheckInterval+time.Second)
						checkable = resource
					})

					It("creates a check plan", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						expectStartedResourceCheck(build, resource, pipeline, false)
						Expect(resourceTypes.Deserialize()).To(Equal(atc.ResourceTypes{
							{
								Name:     "custom-type",
								Type:     "some-base-type",
								Source:   atc.Source{"some": "type-source"},
								Defaults: atc.Source{"sdk": "sdk"},
								Tags:     atc.Tags{"some-tag"},
							},
						}))
						expectCustomResourceCheckPlan(build, fromVersion)
					})

					It("returns the build", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						expectStartedResourceCheck(build, resource, pipeline, false)
						expectCustomResourceCheckPlan(build, fromVersion)
					})

					It("starts the build with the check plan", func() {
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(BeTrue())
						expectStartedResourceCheck(build, resource, pipeline, false)
						expectCustomResourceCheckPlan(build, fromVersion)
					})
				})
			})
		})

		Context("when it is run for a resource type", func() {
			var resourceTypes db.ResourceTypes

			BeforeEach(func() {
				resourceTypes, err = defaultPipeline.ResourceTypes()
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				build, created, err = checkFactory.TryCreateCheck(context.TODO(), defaultResourceType, resourceTypes, fromVersion, manuallyTriggered, false, toDb)
			})

			Context("when build is created in db", func() {
				It("persists one started check with the resource type plan", func() {
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					Expect(build).NotTo(BeNil())

					found, err := build.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(build.ResourceTypeID()).To(Equal(defaultResourceType.ID()))
					Expect(build.Status()).To(Equal(db.BuildStatusStarted))
					Expect(build.IsCompleted()).To(BeFalse())
					Expect(build.IsManuallyTriggered()).To(BeFalse())
					Expect(build.PrivatePlan().ID).NotTo(BeEmpty())
					Expect(build.PrivatePlan().Check).To(Equal(&atc.CheckPlan{
						Name:         "some-type",
						Type:         "some-base-resource-type",
						Source:       atc.Source{"some-type": "source"},
						TypeImage:    atc.TypeImage{BaseType: "some-base-resource-type"},
						FromVersion:  atc.Version{"from": "version"},
						ResourceType: "some-type",
						Interval:     atc.CheckEvery{Interval: defaultResourceTypeInterval},
					}))

					var unfinishedBuilds int
					err = dbConn.QueryRow(
						`SELECT count(*) FROM builds WHERE resource_type_id = $1 AND completed = false`,
						defaultResourceType.ID(),
					).Scan(&unfinishedBuilds)
					Expect(err).NotTo(HaveOccurred())
					Expect(unfinishedBuilds).To(Equal(1))

					duplicateBuild, duplicateCreated, err := checkFactory.TryCreateCheck(
						context.TODO(), defaultResourceType, resourceTypes, fromVersion, manuallyTriggered, false, true,
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(duplicateCreated).To(BeFalse())
					Expect(duplicateBuild).To(BeNil())

					err = dbConn.QueryRow(
						`SELECT count(*) FROM builds WHERE resource_type_id = $1 AND completed = false`,
						defaultResourceType.ID(),
					).Scan(&unfinishedBuilds)
					Expect(err).NotTo(HaveOccurred())
					Expect(unfinishedBuilds).To(Equal(1))
				})
			})

			Context("when build is created in memory", func() {
				BeforeEach(func() {
					toDb = false
				})

				It("rejects the impossible in-memory resource type check without enqueueing", func() {
					Expect(err).To(MatchError("resource type not supporting in-memory check build as lidar no longer checking resource types"))
					Expect(created).To(BeFalse())
					Expect(build).To(BeNil())
					Expect(checkBuildChan).NotTo(Receive())
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

		Context("with template pipelines", func() {
			It("excludes resources of base templates and run instances, keeps ordinary instanced pipelines", func() {
				resourceJob := func(template bool) atc.Config {
					return atc.Config{
						Template: template,
						Resources: atc.ResourceConfigs{
							{Name: "check-res", Type: "some-base-resource-type", Source: atc.Source{"a": "b"}},
						},
						Jobs: atc.JobConfigs{
							{Name: "j", PlanSequence: []atc.Step{
								{Config: &atc.GetStep{Name: "check-res"}},
							}},
						},
					}
				}

				template, _, err := defaultTeam.SavePipeline(
					atc.PipelineRef{Name: "check-template"}, resourceJob(true), db.ConfigVersion(0), false)
				Expect(err).ToNot(HaveOccurred())

				ordinary, _, err := defaultTeam.SavePipeline(
					atc.PipelineRef{Name: "check-ordinary", InstanceVars: atc.InstanceVars{"branch": "main"}},
					resourceJob(false), db.ConfigVersion(0), false)
				Expect(err).ToNot(HaveOccurred())
				Expect(ordinary.Reload()).To(BeTrue())

				resources, err := checkFactory.Resources()
				Expect(err).ToNot(HaveOccurred())

				var pipelineIDs []int
				for _, r := range resources {
					pipelineIDs = append(pipelineIDs, r.PipelineID())
				}
				Expect(pipelineIDs).To(ContainElement(ordinary.ID()))
				Expect(pipelineIDs).ToNot(ContainElement(template.ID()))
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
