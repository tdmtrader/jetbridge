package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume", func() {
	var defaultCreatingContainer db.CreatingContainer

	BeforeEach(func() {
		expiries := db.ContainerOwnerExpiries{
			Min: 5 * time.Minute,
			Max: 1 * time.Hour,
		}

		resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig("some-base-resource-type", atc.Source{}, nil)
		Expect(err).ToNot(HaveOccurred())

		defaultCreatingContainer, err = defaultWorker.CreateContainer(
			db.NewResourceConfigCheckSessionContainerOwner(
				resourceConfig.ID(),
				resourceConfig.OriginBaseResourceType().ID,
				expiries,
			),
			db.ContainerMetadata{Type: "check"},
		)
		Expect(err).ToNot(HaveOccurred())

		_, err = defaultCreatingContainer.Created()
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("creatingVolume.Created", func() {
		var (
			creatingVolume db.CreatingVolume
			createdVolume  db.CreatedVolume
			createErr      error
		)

		BeforeEach(func() {
			var err error
			creatingVolume, err = volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), defaultCreatingContainer, "/path/to/volume")
			Expect(err).ToNot(HaveOccurred())
		})

		JustBeforeEach(func() {
			createdVolume, createErr = creatingVolume.Created()
		})

		Describe("the database query succeeds", func() {
			It("returns a createdVolume and no error", func() {
				Expect(createdVolume).ToNot(BeNil())
				Expect(createErr).ToNot(HaveOccurred())
			})
		})
	})

	Describe("createdVolume.InitializeResourceCache", func() {
		var createdVolume db.CreatedVolume
		var resourceCache db.ResourceCache
		var workerResourceCache *db.UsedWorkerResourceCache
		var build db.Build
		var scenario *dbtest.Scenario
		var buildStartTime time.Time

		volumeOnWorker := func(worker db.Worker) db.CreatedVolume {
			creatingContainer, err := worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", scenario.Team.ID()), db.ContainerMetadata{
				Type:     "get",
				StepName: "some-resource",
			})
			Expect(err).ToNot(HaveOccurred())

			creatingVolume, err := volumeRepository.CreateContainerVolume(scenario.Team.ID(), worker.Name(), creatingContainer, "some-path")
			Expect(err).ToNot(HaveOccurred())

			createdVolume, err := creatingVolume.Created()
			Expect(err).ToNot(HaveOccurred())

			return createdVolume
		}

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithTeam("some-team"),
				builder.WithBaseWorker(),
			)

			var err error
			build, err = scenario.Team.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				dbtest.BaseResourceType,
				atc.Version{"some-type": "version"},
				atc.Source{
					"some-type": "source",
				},
				nil,
				nil,
			)

			resourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-type",
				atc.Version{"some": "version"},
				atc.Source{
					"some": "source",
				},
				atc.Params{"some": "params"},
				resourceTypeCache,
			)
			Expect(err).ToNot(HaveOccurred())

			createdVolume = volumeOnWorker(scenario.Workers[0])
			workerResourceCache, err = createdVolume.InitializeResourceCache(resourceCache)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdVolume.Type()).To(Equal(db.VolumeTypeResource))

			buildStartTime = time.Now().Add(-100 * time.Second)
		})

		Context("when initialize created resource cache", func() {
			It("should find the worker resource cache", func() {
				uwrc, found, err := db.WorkerResourceCache{
					WorkerName:    scenario.Workers[0].Name(),
					ResourceCache: resourceCache,
				}.Find(dbConn, time.Now())
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(uwrc).ToNot(BeNil())
				Expect(uwrc.WorkerBaseResourceTypeID).ToNot(BeZero())
				Expect(uwrc.WorkerBaseResourceTypeID).To(Equal(workerResourceCache.WorkerBaseResourceTypeID))
				Expect(uwrc.ID).To(Equal(workerResourceCache.ID))
			})

			It("associates the volume to the resource cache", func() {
				foundVolume, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[0].Name(), resourceCache, buildStartTime)
				Expect(err).ToNot(HaveOccurred())
				Expect(foundVolume.Handle()).To(Equal(createdVolume.Handle()))
				Expect(found).To(BeTrue())
			})

			Context("when there's already an initialized resource cache on the same worker", func() {
				It("leaves the volume owned by the container", func() {
					createdVolume2 := volumeOnWorker(scenario.Workers[0])
					_, err := createdVolume2.InitializeResourceCache(resourceCache)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdVolume2.Type()).To(Equal(db.VolumeTypeContainer))
				})
			})
		})

		Context("when the same resource cache is initialized from another source worker", func() {
			It("leaves the volume owned by the container", func() {
				scenario.Run(builder.WithBaseWorker())
				worker2CacheVolume := volumeOnWorker(scenario.Workers[1])
				uwrc, err := worker2CacheVolume.InitializeResourceCache(resourceCache)
				Expect(err).ToNot(HaveOccurred())

				worker1Volume := volumeOnWorker(scenario.Workers[0])
				_, err = worker1Volume.InitializeStreamedResourceCache(resourceCache, uwrc.ID)
				Expect(err).ToNot(HaveOccurred())

				Expect(worker1Volume.Type()).To(Equal(db.VolumeTypeContainer))
			})
		})

		Context("when initialize streamed resource cache", func() {
			var streamedVolume1 db.CreatedVolume
			var workerResourceCache1 *db.UsedWorkerResourceCache

			BeforeEach(func() {
				scenario.Run(builder.WithBaseWorker())

				streamedVolume1 = volumeOnWorker(scenario.Workers[1])
				var err error
				workerResourceCache1, err = streamedVolume1.InitializeStreamedResourceCache(resourceCache, workerResourceCache.ID)
				Expect(err).ToNot(HaveOccurred())
			})

			It("associates the volume to the resource cache", func() {
				foundVolume, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[1].Name(), resourceCache, buildStartTime)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(foundVolume.Handle()).To(Equal(streamedVolume1.Handle()))
			})

			Context("when a streamed resource cache is streamed to another worker", func() {
				var streamedVolume2 db.CreatedVolume
				BeforeEach(func() {
					scenario.Run(builder.WithBaseWorker())

					streamedVolume2 = volumeOnWorker(scenario.Workers[2])
					var err error
					workerResourceCache1, err = streamedVolume2.InitializeStreamedResourceCache(resourceCache, workerResourceCache1.ID)
					Expect(err).ToNot(HaveOccurred())
				})

				It("associates the volume to the resource cache", func() {
					foundVolume, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[2].Name(), resourceCache, buildStartTime)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(foundVolume.Handle()).To(Equal(streamedVolume2.Handle()))
				})

				Context("when the original base resource type is invalidated", func() {
					BeforeEach(func() {
						scenario.Run(
							builder.WithWorker(atc.Worker{
								Name:          scenario.Workers[0].Name(),
								ResourceTypes: []atc.WorkerResourceType{
									// empty => invalidate the existing worker_base_resource_type
								},
							}),
						)
					})

					Context("when build started before original invalidated", func() {
						BeforeEach(func() {
							buildStartTime = time.Now().Add(-100 * time.Second)
						})

						It("should be still usable when the original base resource type is invalidated", func() {

							_, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[0].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeTrue())

							_, found, err = volumeRepository.FindResourceCacheVolume(scenario.Workers[1].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeTrue())

							_, found, err = volumeRepository.FindResourceCacheVolume(scenario.Workers[2].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeTrue())
						})
					})

					Context("when build started after original invalidated", func() {
						BeforeEach(func() {
							buildStartTime = time.Now().Add(100 * time.Second)
						})

						It("should not be usable when the original base resource type is invalidated", func() {
							_, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[0].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeFalse())

							_, found, err = volumeRepository.FindResourceCacheVolume(scenario.Workers[1].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeFalse())

							_, found, err = volumeRepository.FindResourceCacheVolume(scenario.Workers[2].Name(), resourceCache, buildStartTime)
							Expect(err).ToNot(HaveOccurred())
							Expect(found).To(BeFalse())
						})
					})
				})
			})
		})

		Context("when streaming a volume cache that has been invalidated on the source worker", func() {
			var streamedVolume db.CreatedVolume

			BeforeEach(func() {
				scenario.Run(
					builder.WithWorker(atc.Worker{
						Name:          scenario.Workers[0].Name(),
						ResourceTypes: []atc.WorkerResourceType{
							// empty => invalidate the existing worker_resource_cache from worker0
						},
					}),
				)

				scenario.Run(
					builder.WithBaseWorker(), // workers[1]
				)

				streamedVolume = volumeOnWorker(scenario.Workers[1])
			})

			It("InitializeStreamedResourceCache should not fail", func() {
				_, err := streamedVolume.InitializeStreamedResourceCache(resourceCache, workerResourceCache.ID)
				Expect(err).ToNot(HaveOccurred())

				// leaves the volume owned by the container
				Expect(streamedVolume.Type()).To(Equal(db.VolumeTypeContainer))
			})

			Context("when build started before cache is invalidated", func() {
				BeforeEach(func() {
					buildStartTime = time.Now().Add(-100 * time.Second)
				})

				It("should still found the resource cache because source is invalidated", func() {
					workers, err := defaultTeam.FindWorkersForResourceCache(resourceCache.ID(), buildStartTime)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(workers)).To(Equal(1))
					Expect(workers[0].Name()).To(Equal(scenario.Workers[0].Name()))
				})
			})

			Context("when build started after cache is invalidated", func() {
				BeforeEach(func() {
					buildStartTime = time.Now().Add(100 * time.Second)
				})

				It("should still found the resource cache because source is invalidated", func() {
					workers, err := defaultTeam.FindWorkersForResourceCache(resourceCache.ID(), buildStartTime)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(workers)).To(Equal(0))
				})
			})
		})

		Context("when a streamed resource cache is invalidated", func() {
			var streamedVolumeOnWorker1 db.CreatedVolume
			var workerResourceCacheOnWorker1 *db.UsedWorkerResourceCache
			var newVolume db.CreatedVolume
			var newWorkerResourceCache *db.UsedWorkerResourceCache
			var initErr error

			BeforeEach(func() {
				scenario.Run(
					builder.WithBaseWorker(), // workers[1]
				)

				streamedVolumeOnWorker1 = volumeOnWorker(scenario.Workers[1])
				var err error
				workerResourceCacheOnWorker1, err = streamedVolumeOnWorker1.InitializeStreamedResourceCache(resourceCache, workerResourceCache.ID)
				Expect(err).ToNot(HaveOccurred())
				Expect(streamedVolumeOnWorker1.Type()).To(Equal(db.VolumeTypeResource))
				Expect(workerResourceCacheOnWorker1).NotTo(BeNil())

				workers, err := defaultTeam.FindWorkersForResourceCache(resourceCache.ID(), buildStartTime)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(workers)).To(Equal(2))

				// Delete worker0, so that streamed worker resource caches should be invalidated.
				err = scenario.Workers[0].Delete()
				Expect(err).ToNot(HaveOccurred())

				// After the old cached is invalidated, init a new cache.
				newVolume = volumeOnWorker(scenario.Workers[1])
				newWorkerResourceCache, initErr = newVolume.InitializeResourceCache(resourceCache)
			})

			It("initializing resource cache on the new volume should succeed", func() {
				Expect(initErr).ToNot(HaveOccurred())
				Expect(newWorkerResourceCache).NotTo(BeNil())
				Expect(newWorkerResourceCache.WorkerBaseResourceTypeID).ToNot(BeZero())
				Expect(newWorkerResourceCache.ID).NotTo(Equal(workerResourceCacheOnWorker1))
			})

			It("new volume owned by the resource", func() {
				Expect(newVolume.Type()).To(Equal(db.VolumeTypeResource))
			})

			Context("when build started before the cache is invalidated", func() {
				BeforeEach(func() {
					buildStartTime = time.Now().Add(-100 * time.Second)
				})

				It("resource cache volume should be found on both worker1", func() {
					foundVolume, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[1].Name(), resourceCache, buildStartTime)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(foundVolume.Handle()).To(Equal(newVolume.Handle()))
				})

				It("there is an invalid worker resource cache on worker1", func() {
					invalidUwrc, found, err := db.WorkerResourceCache{}.FindByID(dbConn, workerResourceCacheOnWorker1.ID)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(invalidUwrc).ToNot(BeNil())
					Expect(invalidUwrc.WorkerBaseResourceTypeID).To(BeZero())
				})
			})

			// In this test, no matter build started before or after the original cache is invalidated,
			// they should behave the same because new cache has been initialized on worker-1.
			Context("when build started after the cache is invalidated", func() {
				BeforeEach(func() {
					buildStartTime = time.Now().Add(100 * time.Second)
				})

				It("resource cache volume should be found on worker1", func() {
					foundVolume, found, err := volumeRepository.FindResourceCacheVolume(scenario.Workers[1].Name(), resourceCache, buildStartTime)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(foundVolume.Handle()).To(Equal(newVolume.Handle()))
				})

				It("there is an invalid worker resource cache on worker1", func() {
					invalidUwrc, found, err := db.WorkerResourceCache{}.FindByID(dbConn, workerResourceCacheOnWorker1.ID)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(invalidUwrc).ToNot(BeNil())
					Expect(invalidUwrc.WorkerBaseResourceTypeID).To(BeZero())
				})
			})
		})
	})

	Describe("Resource cache volumes", func() {
		It("returns volume type, resource type, resource version", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					ResourceTypes: atc.ResourceTypes{
						{
							Name: "some-type",
							Type: "some-base-resource-type",
							Source: atc.Source{
								"some-type": "source",
							},
						},
					},
				}),
				builder.WithResourceTypeVersions(
					"some-type",
					atc.Version{"some": "version"},
					atc.Version{"some-custom-type": "version"},
				),
			)

			build, err := scenario.Team.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-base-resource-type",
				atc.Version{"some-custom-type": "version"},
				atc.Source{"some-type": "((source-param))"},
				nil,
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-type",
				atc.Version{"some": "version"},
				atc.Source{"some": "source"},
				atc.Params{"some": "params"},
				resourceTypeCache,
			)
			Expect(err).ToNot(HaveOccurred())

			creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
				Type:     "get",
				StepName: "some-resource",
			})
			Expect(err).ToNot(HaveOccurred())

			creatingVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path")
			Expect(err).ToNot(HaveOccurred())

			createdVolume, err := creatingVolume.Created()
			Expect(err).ToNot(HaveOccurred())

			Expect(createdVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeContainer)))

			_, err = createdVolume.InitializeResourceCache(resourceCache)
			Expect(err).ToNot(HaveOccurred())

			Expect(createdVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeResource)))

			volumeResourceType, err := createdVolume.ResourceType()
			Expect(err).ToNot(HaveOccurred())
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Name).To(Equal("some-base-resource-type"))
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Version).To(Equal("some-brt-version"))
			Expect(volumeResourceType.ResourceType.Version).To(Equal(atc.Version{"some-custom-type": "version"}))
			Expect(volumeResourceType.Version).To(Equal(atc.Version{"some": "version"}))

			createdVolume, found, err := volumeRepository.FindResourceCacheVolume(defaultWorker.Name(), resourceCache, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(createdVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeResource)))
			volumeResourceType, err = createdVolume.ResourceType()
			Expect(err).ToNot(HaveOccurred())
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Name).To(Equal("some-base-resource-type"))
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Version).To(Equal("some-brt-version"))
			Expect(volumeResourceType.ResourceType.Version).To(Equal(atc.Version{"some-custom-type": "version"}))
			Expect(volumeResourceType.Version).To(Equal(atc.Version{"some": "version"}))
		})

		It("returns volume type from streamed source volume", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					ResourceTypes: atc.ResourceTypes{
						{
							Name: "some-type",
							Type: "some-base-resource-type",
							Source: atc.Source{
								"some-type": "source",
							},
						},
					},
				}),
				builder.WithResourceTypeVersions(
					"some-type",
					atc.Version{"some": "version"},
					atc.Version{"some-custom-type": "version"},
				),
				builder.WithWorker(atc.Worker{
					Name:     "weird-worker",
					Platform: "weird",
				}),
			)

			sourceWorker := scenario.Workers[0]
			destinationWorker := scenario.Workers[1]

			build, err := scenario.Team.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			resourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				dbtest.BaseResourceType,
				atc.Version{"some-custom-type": "version"},
				atc.Source{"some-type": "((source-param))"},
				nil,
				nil,
			)

			resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				db.ForBuild(build.ID()),
				"some-type",
				atc.Version{"some": "version"},
				atc.Source{"some": "source"},
				atc.Params{"some": "params"},
				resourceTypeCache,
			)
			Expect(err).ToNot(HaveOccurred())

			creatingSourceContainer, err := sourceWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
				Type:     "get",
				StepName: "some-resource",
			})
			Expect(err).ToNot(HaveOccurred())

			creatingSourceVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), sourceWorker.Name(), creatingSourceContainer, "some-path")
			Expect(err).ToNot(HaveOccurred())

			sourceVolume, err := creatingSourceVolume.Created()
			Expect(err).ToNot(HaveOccurred())

			Expect(sourceVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeContainer)))

			var workerResourceCache *db.UsedWorkerResourceCache
			workerResourceCache, err = sourceVolume.InitializeResourceCache(resourceCache)
			Expect(err).ToNot(HaveOccurred())

			Expect(sourceVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeResource)))

			creatingDestinationContainer, err := destinationWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
				Type:     "get",
				StepName: "some-resource",
			})
			Expect(err).ToNot(HaveOccurred())

			creatingDestinationVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), destinationWorker.Name(), creatingDestinationContainer, "some-path")
			Expect(err).ToNot(HaveOccurred())

			destinationVolume, err := creatingDestinationVolume.Created()
			Expect(err).ToNot(HaveOccurred())

			Expect(destinationVolume.Type()).To(Equal(db.VolumeType(db.VolumeTypeContainer)))

			_, err = destinationVolume.InitializeStreamedResourceCache(resourceCache, workerResourceCache.ID)
			Expect(err).ToNot(HaveOccurred())

			volumeResourceType, err := destinationVolume.ResourceType()
			Expect(err).ToNot(HaveOccurred())
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Name).To(Equal(dbtest.BaseResourceType))
			Expect(volumeResourceType.ResourceType.WorkerBaseResourceType.Version).To(Equal(dbtest.BaseResourceTypeVersion))
			Expect(volumeResourceType.ResourceType.Version).To(Equal(atc.Version{"some-custom-type": "version"}))
			Expect(volumeResourceType.Version).To(Equal(atc.Version{"some": "version"}))
		})
	})

	Context("when worker is no longer in database", func() {
		BeforeEach(func() {
			var err error
			_, err = volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), defaultCreatingContainer, "/path/to/volume")
			Expect(err).ToNot(HaveOccurred())
		})

		It("the container goes away from the db", func() {
			err := defaultWorker.Delete()
			Expect(err).ToNot(HaveOccurred())

			creatingVolume, createdVolume, err := volumeRepository.FindContainerVolume(defaultTeam.ID(), defaultWorker.Name(), defaultCreatingContainer, "/path/to/volume")
			Expect(err).ToNot(HaveOccurred())
			Expect(creatingVolume).To(BeNil())
			Expect(createdVolume).To(BeNil())
		})
	})
})
