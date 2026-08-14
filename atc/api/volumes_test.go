package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/testhelpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type destroyResourceCacheAfterTeamVolumeReadRepository struct {
	db.VolumeRepository
	conn          db.DbConn
	resourceCache db.ResourceCache
}

func (repository destroyResourceCacheAfterTeamVolumeReadRepository) GetTeamVolumes(teamID int) ([]db.CreatedVolume, error) {
	volumes, err := repository.VolumeRepository.GetTeamVolumes(teamID)
	if err != nil {
		return nil, err
	}

	tx, err := repository.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deleted, err := repository.resourceCache.Destroy(tx)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, errors.New("resource cache was not deleted")
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return volumes, nil
}

var _ = Describe("Volumes API", func() {
	var (
		realDatabase          *realDB
		team                  db.Team
		worker                db.Worker
		someOtherWorker       db.Worker
		otherTeamVolumeHandle string
		server                *httptest.Server
	)

	BeforeEach(func() {
		realDatabase = useRealDB()

		var err error
		team, err = realDatabase.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
		Expect(err).NotTo(HaveOccurred())

		worker, err = realDatabase.Deps.workerFactory.SaveWorker(atc.Worker{
			Name: "some-worker",
			ResourceTypes: []atc.WorkerResourceType{
				{
					Type:    "some-base-resource-type",
					Version: "some-base-version",
				},
			},
		}, 0)
		Expect(err).NotTo(HaveOccurred())

		someOtherWorker, err = realDatabase.Deps.workerFactory.SaveWorker(atc.Worker{Name: "some-other-worker"}, 0)
		Expect(err).NotTo(HaveOccurred())

		otherTeam, err := realDatabase.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-other-team"})
		Expect(err).NotTo(HaveOccurred())

		otherTeamBuild, err := otherTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		otherTeamContainer, err := someOtherWorker.CreateContainer(
			db.NewBuildStepContainerOwner(otherTeamBuild.ID(), "some-other-plan", otherTeam.ID()),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
		)
		Expect(err).NotTo(HaveOccurred())

		otherTeamVolume, err := realDatabase.Deps.volumeRepository.CreateContainerVolume(
			otherTeam.ID(),
			someOtherWorker.Name(),
			otherTeamContainer,
			"some-other-path",
		)
		Expect(err).NotTo(HaveOccurred())

		createdOtherTeamVolume, err := otherTeamVolume.Created()
		Expect(err).NotTo(HaveOccurred())
		otherTeamVolumeHandle = createdOtherTeamVolume.Handle()
	})

	Describe("GET /api/v1/teams/a-team/volumes", func() {
		var response *http.Response

		JustBeforeEach(func() {
			server = realDatabase.Serve()

			var err error
			response, err = client.Get(server.URL + "/api/v1/teams/a-team/volumes")
			if response != nil {
				DeferCleanup(response.Body.Close)
			}
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				grantProfile(team, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
			})

			Context("when identifying the team succeeds", func() {
				It("does not return volumes owned by another team", func() {
					var volumes []atc.Volume
					Expect(json.NewDecoder(response.Body).Decode(&volumes)).To(Succeed())

					Expect(volumes).To(BeEmpty())
					Expect(volumes).NotTo(ContainElement(HaveField("ID", otherTeamVolumeHandle)))
				})

				Context("when getting all volumes succeeds", func() {
					var expectedVolumes []atc.Volume

					BeforeEach(func() {
						repository := realDatabase.Deps.volumeRepository

						build, err := team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())

						resourceCacheFactory := db.NewResourceCacheFactory(realDatabase.Conn, realDatabase.LockFactory)
						customTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(build.ID()),
							"some-base-resource-type",
							atc.Version{"custom": "version"},
							atc.Source{"custom": "source"},
							nil,
							nil,
						)
						Expect(err).NotTo(HaveOccurred())

						resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(build.ID()),
							"some-custom-resource-type",
							atc.Version{"some": "version"},
							atc.Source{"some": "source"},
							atc.Params{"some": "params"},
							customTypeCache,
						)
						Expect(err).NotTo(HaveOccurred())

						creatingResourceVolume, err := repository.CreateVolumeWithHandle(
							"some-resource-cache-handle",
							team.ID(),
							worker.Name(),
							db.VolumeTypeContainer,
						)
						Expect(err).NotTo(HaveOccurred())

						resourceVolume, err := creatingResourceVolume.Created()
						Expect(err).NotTo(HaveOccurred())
						workerResourceCache, err := resourceVolume.InitializeResourceCache(resourceCache)
						Expect(err).NotTo(HaveOccurred())
						Expect(workerResourceCache).NotTo(BeNil())

						workerBaseResourceType, found, err := db.NewWorkerBaseResourceTypeFactory(realDatabase.Conn).
							Find("some-base-resource-type", worker)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						creatingBaseResourceTypeVolume, err := repository.CreateBaseResourceTypeVolume(workerBaseResourceType)
						Expect(err).NotTo(HaveOccurred())
						baseResourceTypeVolume, err := creatingBaseResourceTypeVolume.Created()
						Expect(err).NotTo(HaveOccurred())

						creatingContainer, err := worker.CreateContainer(
							db.NewFixedHandleContainerOwner("some-container-handle"),
							db.ContainerMetadata{Type: db.ContainerTypeTask},
						)
						Expect(err).NotTo(HaveOccurred())

						creatingParentVolume, err := repository.CreateContainerVolume(
							team.ID(),
							worker.Name(),
							creatingContainer,
							"some-path",
						)
						Expect(err).NotTo(HaveOccurred())
						parentVolume, err := creatingParentVolume.Created()
						Expect(err).NotTo(HaveOccurred())

						creatingChildVolume, err := parentVolume.CreateChildForContainer(creatingContainer, "some-child-path")
						Expect(err).NotTo(HaveOccurred())
						childVolume, err := creatingChildVolume.Created()
						Expect(err).NotTo(HaveOccurred())
						Expect(childVolume.WorkerName()).To(Equal(parentVolume.WorkerName()))

						pipeline, _, err := team.SavePipeline(
							atc.PipelineRef{
								Name:         "some-pipeline",
								InstanceVars: atc.InstanceVars{"branch": "master"},
							},
							atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
							db.ConfigVersion(0),
							false,
						)
						Expect(err).NotTo(HaveOccurred())

						job, found, err := pipeline.Job("some-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						taskCache, err := db.NewTaskCacheFactory(realDatabase.Conn).
							FindOrCreate(job.ID(), "some-task", "some-task-cache-path")
						Expect(err).NotTo(HaveOccurred())
						workerTaskCache, err := db.NewWorkerTaskCacheFactory(realDatabase.Conn).FindOrCreate(db.WorkerTaskCache{
							WorkerName: worker.Name(),
							TaskCache:  taskCache,
						})
						Expect(err).NotTo(HaveOccurred())

						creatingTaskCacheVolume, err := repository.CreateTaskCacheVolume(team.ID(), workerTaskCache)
						Expect(err).NotTo(HaveOccurred())
						taskCacheVolume, err := creatingTaskCacheVolume.Created()
						Expect(err).NotTo(HaveOccurred())

						expectedVolumes = []atc.Volume{
							{
								ID:         resourceVolume.Handle(),
								WorkerName: worker.Name(),
								Type:       string(db.VolumeTypeResource),
								ResourceType: &atc.VolumeResourceType{
									ResourceType: &atc.VolumeResourceType{
										BaseResourceType: &atc.VolumeBaseResourceType{
											Name:    "some-base-resource-type",
											Version: "some-base-version",
										},
										Version: atc.Version{"custom": "version"},
									},
									Version: atc.Version{"some": "version"},
								},
							},
							{
								ID:         baseResourceTypeVolume.Handle(),
								WorkerName: worker.Name(),
								Type:       string(db.VolumeTypeResourceType),
								BaseResourceType: &atc.VolumeBaseResourceType{
									Name:    "some-base-resource-type",
									Version: "some-base-version",
								},
							},
							{
								ID:              parentVolume.Handle(),
								WorkerName:      worker.Name(),
								Type:            string(db.VolumeTypeContainer),
								ContainerHandle: creatingContainer.Handle(),
								Path:            "some-path",
							},
							{
								ID:              childVolume.Handle(),
								WorkerName:      worker.Name(),
								Type:            string(db.VolumeTypeContainer),
								ContainerHandle: creatingContainer.Handle(),
								Path:            "some-child-path",
								ParentHandle:    parentVolume.Handle(),
							},
							{
								ID:                   taskCacheVolume.Handle(),
								WorkerName:           worker.Name(),
								Type:                 string(db.VolumeTypeTaskCache),
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
								JobName:              "some-job",
								StepName:             "some-task",
							},
						}
					})

					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						Expect(response).Should(IncludeHeaderEntries(map[string]string{
							"Content-Type": "application/json",
						}))
					})

					It("returns all real volumes visible to the team", func() {
						var volumes []atc.Volume
						Expect(json.NewDecoder(response.Body).Decode(&volumes)).To(Succeed())

						Expect(volumes).To(ConsistOf(expectedVolumes))
						Expect(volumes).NotTo(ContainElement(HaveField("ID", otherTeamVolumeHandle)))
					})
				})

				Context("when a volume is deleted during the request", func() {
					var expectedBaseResourceTypeVolume atc.Volume

					BeforeEach(func() {
						repository := realDatabase.Deps.volumeRepository

						build, err := team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())

						resourceCacheFactory := db.NewResourceCacheFactory(realDatabase.Conn, realDatabase.LockFactory)
						customTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(build.ID()),
							"some-base-resource-type",
							atc.Version{"custom": "version"},
							atc.Source{"custom": "source"},
							nil,
							nil,
						)
						Expect(err).NotTo(HaveOccurred())

						resourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(build.ID()),
							"some-custom-resource-type",
							atc.Version{"some": "version"},
							atc.Source{"some": "source"},
							atc.Params{"some": "params"},
							customTypeCache,
						)
						Expect(err).NotTo(HaveOccurred())

						creatingResourceVolume, err := repository.CreateVolumeWithHandle(
							"disappearing-resource-cache-volume",
							team.ID(),
							worker.Name(),
							db.VolumeTypeContainer,
						)
						Expect(err).NotTo(HaveOccurred())
						resourceVolume, err := creatingResourceVolume.Created()
						Expect(err).NotTo(HaveOccurred())
						workerResourceCache, err := resourceVolume.InitializeResourceCache(resourceCache)
						Expect(err).NotTo(HaveOccurred())
						Expect(workerResourceCache).NotTo(BeNil())

						workerBaseResourceType, found, err := db.NewWorkerBaseResourceTypeFactory(realDatabase.Conn).
							Find("some-base-resource-type", worker)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						creatingBaseResourceTypeVolume, err := repository.CreateBaseResourceTypeVolume(workerBaseResourceType)
						Expect(err).NotTo(HaveOccurred())
						baseResourceTypeVolume, err := creatingBaseResourceTypeVolume.Created()
						Expect(err).NotTo(HaveOccurred())

						expectedBaseResourceTypeVolume = atc.Volume{
							ID:         baseResourceTypeVolume.Handle(),
							WorkerName: worker.Name(),
							Type:       string(db.VolumeTypeResourceType),
							BaseResourceType: &atc.VolumeBaseResourceType{
								Name:    "some-base-resource-type",
								Version: "some-base-version",
							},
						}

						deleted, err := build.Delete()
						Expect(err).NotTo(HaveOccurred())
						Expect(deleted).To(BeTrue())

						realDatabase.Deps.volumeRepository = destroyResourceCacheAfterTeamVolumeReadRepository{
							VolumeRepository: repository,
							conn:             realDatabase.Conn,
							resourceCache:    resourceCache,
						}
					})

					It("returns a partial list of volumes", func() {
						var volumes []atc.Volume
						Expect(json.NewDecoder(response.Body).Decode(&volumes)).To(Succeed())

						Expect(volumes).To(ConsistOf(expectedBaseResourceTypeVolume))
					})

					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						Expect(response).Should(IncludeHeaderEntries(map[string]string{
							"Content-Type": "application/json",
						}))
					})
				})
			})
		})
	})
})
