package db_test

import (
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VolumeRepository", func() {
	var (
		team2             db.Team
		resourceTypeCache db.ResourceCache
		usedResourceCache db.ResourceCache
		build             db.Build
	)

	BeforeEach(func() {
		var err error
		build, err = defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())

		resourceTypeCache, err = resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(build.ID()),
			"some-base-resource-type",
			atc.Version{"some-type": "version"},
			atc.Source{
				"some-type": "source",
			},
			nil,
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		usedResourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(build.ID()),
			"some-type",
			atc.Version{"some": "version"},
			atc.Source{
				"some": "source",
			},
			atc.Params{"some": "params"},
			resourceTypeCache,
		)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("GetTeamVolumes", func() {
		var (
			team1handles []string
			team2handles []string
		)

		Context("with container volumes", func() {
			JustBeforeEach(func() {
				creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
					Type:     "task",
					StepName: "some-task",
				})
				Expect(err).ToNot(HaveOccurred())

				team1handles = []string{}
				team2handles = []string{}

				team2, err = teamFactory.CreateTeam(atc.Team{Name: "some-other-defaultTeam"})
				Expect(err).ToNot(HaveOccurred())

				creatingVolume1, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-1")
				Expect(err).NotTo(HaveOccurred())
				createdVolume1, err := creatingVolume1.Created()
				Expect(err).NotTo(HaveOccurred())
				team1handles = append(team1handles, createdVolume1.Handle())

				creatingVolume2, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-2")
				Expect(err).NotTo(HaveOccurred())
				createdVolume2, err := creatingVolume2.Created()
				Expect(err).NotTo(HaveOccurred())
				team1handles = append(team1handles, createdVolume2.Handle())

				creatingVolume3, err := volumeRepository.CreateContainerVolume(team2.ID(), defaultWorker.Name(), creatingContainer, "some-path-3")
				Expect(err).NotTo(HaveOccurred())
				createdVolume3, err := creatingVolume3.Created()
				Expect(err).NotTo(HaveOccurred())
				team2handles = append(team2handles, createdVolume3.Handle())
			})

			Context("when worker has expired", func() {
				BeforeEach(func() {
					var err error
					defaultWorker, err = workerFactory.SaveWorker(defaultWorkerPayload, -10*time.Minute)
					Expect(err).NotTo(HaveOccurred())
				})

			})
		})
	})

	Describe("GetOrphanedVolumes", func() {
		var (
			expectedCreatedHandles      []string
			expectedDestroyingHandles   []string
			childVolume, createdVolume2 db.CreatedVolume
		)

		BeforeEach(func() {
			creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
				Type:     "task",
				StepName: "some-task",
			})
			Expect(err).ToNot(HaveOccurred())
			expectedCreatedHandles = []string{}
			expectedDestroyingHandles = []string{}

			creatingVolume1, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-1")
			Expect(err).NotTo(HaveOccurred())
			createdVolume1, err := creatingVolume1.Created()
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, createdVolume1.Handle())

			creatingVolume2, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-2")
			Expect(err).NotTo(HaveOccurred())
			createdVolume2, err = creatingVolume2.Created()
			Expect(err).NotTo(HaveOccurred())

			// createdVolume2 is not expected to be returned as it has a child
			creatingChildVolume, err := createdVolume2.CreateChildForContainer(creatingContainer, "some-chile-path-1")
			Expect(err).NotTo(HaveOccurred())
			childVolume, err = creatingChildVolume.Created()
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, childVolume.Handle())

			creatingVolume3, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-3")
			Expect(err).NotTo(HaveOccurred())
			createdVolume3, err := creatingVolume3.Created()
			Expect(err).NotTo(HaveOccurred())
			destroyingVolume3, err := createdVolume3.Destroying()
			Expect(err).NotTo(HaveOccurred())
			expectedDestroyingHandles = append(expectedDestroyingHandles, destroyingVolume3.Handle())

			creatingVolumeOtherWorker, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), otherWorker.Name(), creatingContainer, "some-path-other-1")
			Expect(err).NotTo(HaveOccurred())
			createdVolumeOtherWorker, err := creatingVolumeOtherWorker.Created()
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, createdVolumeOtherWorker.Handle())

			resourceCacheVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-4")
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, resourceCacheVolume.Handle())

			resourceCacheVolumeCreated, err := resourceCacheVolume.Created()
			Expect(err).NotTo(HaveOccurred())

			_, err = resourceCacheVolumeCreated.InitializeResourceCache(usedResourceCache)
			Expect(err).NotTo(HaveOccurred())

			artifactVolume, err := volumeRepository.CreateVolume(defaultTeam.ID(), defaultWorker.Name(), db.VolumeTypeArtifact)
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, artifactVolume.Handle())

			_, err = artifactVolume.Created()
			Expect(err).NotTo(HaveOccurred())

			usedWorkerBaseResourceType, found, err := workerBaseResourceTypeFactory.Find(defaultWorkerResourceType.Type, defaultWorker)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			baseResourceTypeVolume, err := volumeRepository.CreateBaseResourceTypeVolume(usedWorkerBaseResourceType)
			Expect(err).NotTo(HaveOccurred())

			oldResourceTypeVolume, err := baseResourceTypeVolume.Created()
			Expect(err).NotTo(HaveOccurred())
			expectedCreatedHandles = append(expectedCreatedHandles, oldResourceTypeVolume.Handle())

			newVersion := defaultWorkerResourceType
			newVersion.Version = "some-new-brt-version"

			newWorker := defaultWorkerPayload
			newWorker.ResourceTypes = []atc.WorkerResourceType{newVersion}

			defaultWorker, err = workerFactory.SaveWorker(newWorker, 0)
			Expect(err).ToNot(HaveOccurred())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			workerResourceCerts, err := db.WorkerResourceCerts{
				WorkerName: defaultWorker.Name(),
				CertsPath:  "/etc/blah/blah/certs",
			}.FindOrCreate(tx)
			Expect(err).NotTo(HaveOccurred())
			err = tx.Commit()
			Expect(err).NotTo(HaveOccurred())

			certsVolume, err := volumeRepository.CreateResourceCertsVolume(defaultWorker.Name(), workerResourceCerts)
			Expect(err).NotTo(HaveOccurred())

			_ = certsVolume.Handle()

			deleted, err := build.Delete()
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).To(BeTrue())

			deleteTx, err := dbConn.Begin()
			Expect(err).ToNot(HaveOccurred())
			deleted, err = usedResourceCache.Destroy(deleteTx)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).To(BeTrue())
			Expect(deleteTx.Commit()).To(Succeed())

			createdContainer, err := creatingContainer.Created()
			Expect(err).NotTo(HaveOccurred())
			destroyingContainer, err := createdContainer.Destroying()
			Expect(err).NotTo(HaveOccurred())
			destroyed, err := destroyingContainer.Destroy()
			Expect(err).NotTo(HaveOccurred())
			Expect(destroyed).To(BeTrue())
		})

	})

	Describe("DestroyFailedVolumes", func() {
		BeforeEach(func() {
			creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
				Type:     "task",
				StepName: "some-task",
			})
			Expect(err).ToNot(HaveOccurred())

			creatingVolume1, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-1")
			Expect(err).NotTo(HaveOccurred())
			_, err = creatingVolume1.Failed()
			Expect(err).NotTo(HaveOccurred())
		})

	})

	Describe("GetDestroyingVolumes", func() {
		var expectedDestroyingHandles []string
		var destroyingVol db.DestroyingVolume

		Context("when worker has detroying volumes", func() {
			BeforeEach(func() {
				creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
					Type:     "task",
					StepName: "some-task",
				})
				Expect(err).ToNot(HaveOccurred())

				expectedDestroyingHandles = []string{}

				creatingVol, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-1")
				Expect(err).NotTo(HaveOccurred())

				createdVol, err := creatingVol.Created()
				Expect(err).NotTo(HaveOccurred())

				destroyingVol, err = createdVol.Destroying()
				Expect(err).NotTo(HaveOccurred())

				expectedDestroyingHandles = append(expectedDestroyingHandles, destroyingVol.Handle())
			})

			Context("when worker doesn't have detroying volume", func() {
				BeforeEach(func() {
					deleted, err := destroyingVol.Destroy()
					Expect(err).NotTo(HaveOccurred())
					Expect(deleted).To(BeTrue())
				})

				It("returns empty volumes", func() {
					destroyingVolumes, err := volumeRepository.GetDestroyingVolumes(defaultWorker.Name())
					Expect(err).NotTo(HaveOccurred())
					Expect(destroyingVolumes).To(BeEmpty())
				})
			})
		})
	})

	Describe("CreateBaseResourceTypeVolume", func() {
		var usedWorkerBaseResourceType *db.UsedWorkerBaseResourceType
		BeforeEach(func() {
			workerBaseResourceTypeFactory := db.NewWorkerBaseResourceTypeFactory(dbConn)
			var err error
			var found bool
			usedWorkerBaseResourceType, found, err = workerBaseResourceTypeFactory.Find("some-base-resource-type", defaultWorker)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("creates a CreatingVolume with no team ID set", func() {
			volume, err := volumeRepository.CreateBaseResourceTypeVolume(usedWorkerBaseResourceType)
			Expect(err).NotTo(HaveOccurred())
			var teamID int
			err = psql.Select("team_id").From("volumes").
				Where(sq.Eq{"handle": volume.Handle()}).RunWith(dbConn).QueryRow().Scan(&teamID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Scan error"))
		})
	})

	Describe("CreateVolume", func() {
	})

	Describe("CreateVolumeWithHandle", func() {
	})

	Describe("FindBaseResourceTypeVolume", func() {
		var usedWorkerBaseResourceType *db.UsedWorkerBaseResourceType
		BeforeEach(func() {
			workerBaseResourceTypeFactory := db.NewWorkerBaseResourceTypeFactory(dbConn)
			var err error
			var found bool
			usedWorkerBaseResourceType, found, err = workerBaseResourceTypeFactory.Find("some-base-resource-type", defaultWorker)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		Context("when there is a created volume for base resource type", func() {
			BeforeEach(func() {
				var err error
				volume, err := volumeRepository.CreateBaseResourceTypeVolume(usedWorkerBaseResourceType)
				Expect(err).NotTo(HaveOccurred())
				_, err = volume.Created()
				Expect(err).NotTo(HaveOccurred())
			})

		})

		Context("when there is a creating volume for base resource type", func() {
			BeforeEach(func() {
				var err error
				_, err = volumeRepository.CreateBaseResourceTypeVolume(usedWorkerBaseResourceType)
				Expect(err).NotTo(HaveOccurred())
			})

		})
	})

	Describe("FindResourceCacheVolume", func() {
		var usedResourceCache db.ResourceCache

		BeforeEach(func() {
			build, err := defaultPipeline.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			usedResourceCache, err = resourceCacheFactory.FindOrCreateResourceCache(
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
		})

		Context("when there is a created volume for resource cache", func() {
			var existingVolume db.CreatedVolume

			BeforeEach(func() {
				var err error
				creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", defaultTeam.ID()), db.ContainerMetadata{
					Type:     "get",
					StepName: "some-resource",
				})
				Expect(err).ToNot(HaveOccurred())

				resourceCacheVolume, err := volumeRepository.CreateContainerVolume(defaultTeam.ID(), defaultWorker.Name(), creatingContainer, "some-path-4")
				Expect(err).NotTo(HaveOccurred())

				existingVolume, err = resourceCacheVolume.Created()
				Expect(err).NotTo(HaveOccurred())

				_, err = existingVolume.InitializeResourceCache(usedResourceCache)
				Expect(err).NotTo(HaveOccurred())
			})

		})
	})

	Describe("RemoveDestroyingVolumes", func() {
		var failedErr error
		var handles []string

		JustBeforeEach(func() {
			_, failedErr = volumeRepository.RemoveDestroyingVolumes(defaultWorker.Name(), handles)
		})

		Context("when there are volumes to destroy", func() {

			Context("when volume is in destroying state", func() {
				BeforeEach(func() {
					handles = []string{"some-handle1", "some-handle2"}
					result, err := psql.Insert("volumes").SetMap(map[string]any{
						"state":       "destroying",
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})
				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})

			Context("when handles are empty list", func() {
				BeforeEach(func() {
					handles = []string{}
					result, err := psql.Insert("volumes").SetMap(map[string]any{
						"state":       "destroying",
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})
				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})

			Context("when volume is in create/creating state", func() {
				BeforeEach(func() {
					handles = []string{"some-handle1", "some-handle2"}
					result, err := psql.Insert("volumes").SetMap(map[string]any{
						"state":       "creating",
						"handle":      "123-456-abc-def",
						"worker_name": defaultWorker.Name(),
					}).RunWith(dbConn).Exec()

					Expect(err).ToNot(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})
				It("does not return an error", func() {
					Expect(failedErr).ToNot(HaveOccurred())
				})
			})
		})

		Context("when there are no volumes to destroy", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2"}

				result, err := psql.Insert("volumes").SetMap(
					map[string]any{
						"state":       "destroying",
						"handle":      "some-handle1",
						"worker_name": defaultWorker.Name(),
					},
				).RunWith(dbConn).Exec()
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))

				result, err = psql.Insert("volumes").SetMap(
					map[string]any{
						"state":       "destroying",
						"handle":      "some-handle2",
						"worker_name": defaultWorker.Name(),
					},
				).RunWith(dbConn).Exec()
				Expect(err).ToNot(HaveOccurred())
				Expect(result.RowsAffected()).To(Equal(int64(1)))
			})

			It("does not return an error", func() {
				Expect(failedErr).ToNot(HaveOccurred())
			})
		})
	})

	Describe("RemoveMissingVolumes", func() {
		var (
			today       time.Time
			gracePeriod time.Duration
			err         error
		)

		JustBeforeEach(func() {
			_, err = volumeRepository.RemoveMissingVolumes(gracePeriod)
		})

		Context("when there are multiple volumes with varying missing since times", func() {
			BeforeEach(func() {
				today = time.Now()

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":      "some-handle-1",
					"state":       db.VolumeStateCreated,
					"worker_name": defaultWorker.Name(),
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":        "some-handle-2",
					"state":         db.VolumeStateCreated,
					"worker_name":   otherWorker.Name(),
					"missing_since": today,
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":        "some-handle-3",
					"state":         db.VolumeStateFailed,
					"worker_name":   otherWorker.Name(),
					"missing_since": today.Add(-5 * time.Minute),
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":        "some-handle-4",
					"state":         db.VolumeStateDestroying,
					"worker_name":   defaultWorker.Name(),
					"missing_since": today.Add(-10 * time.Minute),
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when no created/failed volumes have expired", func() {
				BeforeEach(func() {
					gracePeriod = 7 * time.Minute
				})

			})

			Context("when some created/failed volumes have expired", func() {
				BeforeEach(func() {
					gracePeriod = 3 * time.Minute
				})

			})
		})

		Context("when there is a missing parent volume", func() {
			BeforeEach(func() {
				today = time.Now()

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":      "alive-handle",
					"state":       db.VolumeStateCreated,
					"worker_name": defaultWorker.Name(),
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				var parentID int
				err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":        "parent-handle",
					"state":         db.VolumeStateCreated,
					"worker_name":   defaultWorker.Name(),
					"missing_since": today.Add(-10 * time.Minute),
				}).Suffix("RETURNING id").RunWith(dbConn).QueryRow().Scan(&parentID)
				Expect(err).NotTo(HaveOccurred())

				_, err = psql.Insert("volumes").SetMap(map[string]any{
					"handle":      "child-handle",
					"state":       db.VolumeStateCreated,
					"worker_name": defaultWorker.Name(),
					"parent_id":   parentID,
				}).RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				gracePeriod = 3 * time.Minute
			})

		})
	})

	Describe("UpdateVolumesMissingSince", func() {
		var (
			today   time.Time
			err     error
			handles []string
		)

		BeforeEach(func() {
			result, err := psql.Insert("volumes").SetMap(map[string]any{
				"state":       db.VolumeStateDestroying,
				"handle":      "some-handle1",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			result, err = psql.Insert("volumes").SetMap(map[string]any{
				"state":       db.VolumeStateDestroying,
				"handle":      "some-handle2",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			today = time.Date(2018, 9, 24, 0, 0, 0, 0, time.UTC)

			result, err = psql.Insert("volumes").SetMap(map[string]any{
				"state":         db.VolumeStateCreated,
				"handle":        "some-handle3",
				"worker_name":   defaultWorker.Name(),
				"missing_since": today,
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))
		})

		JustBeforeEach(func() {
			err = volumeRepository.UpdateVolumesMissingSince(defaultWorker.Name(), handles)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when the reported handles is a subset", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1"}
			})

			Context("having the volumes in the creating state in the db", func() {
				BeforeEach(func() {
					result, err := psql.Update("volumes").
						Where(sq.Eq{"handle": "some-handle3"}).
						SetMap(map[string]any{
							"state":         db.VolumeStateCreating,
							"missing_since": nil,
						}).RunWith(dbConn).Exec()
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RowsAffected()).To(Equal(int64(1)))
				})

			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when the reported handles is the full set", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2"}
			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when the reported handles includes a volume marked as missing", func() {
			BeforeEach(func() {
				handles = []string{"some-handle1", "some-handle2", "some-handle3"}
			})

			It("does not return an error", func() {
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	Describe("DestroyUnknownVolumes", func() {
		var (
			err                   error
			workerReportedHandles []string
		)

		BeforeEach(func() {
			result, err := psql.Insert("volumes").SetMap(map[string]any{
				"state":       db.VolumeStateDestroying,
				"handle":      "some-handle1",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))

			result, err = psql.Insert("volumes").SetMap(map[string]any{
				"state":       db.VolumeStateCreated,
				"handle":      "some-handle2",
				"worker_name": defaultWorker.Name(),
			}).RunWith(dbConn).Exec()

			Expect(err).ToNot(HaveOccurred())
			Expect(result.RowsAffected()).To(Equal(int64(1)))
		})

		JustBeforeEach(func() {
			_, err = volumeRepository.DestroyUnknownVolumes(defaultWorker.Name(), workerReportedHandles)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when there are volumes on the worker that are not in the db", func() {
			BeforeEach(func() {
				workerReportedHandles = []string{"some-handle3", "some-handle4"}
			})

		})

		Context("when there are no unknown volumes on the worker", func() {
			BeforeEach(func() {
				workerReportedHandles = []string{"some-handle1", "some-handle2"}
			})

		})
	})
})
