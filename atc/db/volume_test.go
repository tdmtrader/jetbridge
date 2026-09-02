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
			})
		})

	})
})
