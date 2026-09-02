package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Team", func() {
	var (
		team      db.Team
		otherTeam db.Team
	)

	expectConfigsEqual := func(config, expectedConfig atc.Config) {
		ExpectWithOffset(1, config.Groups).To(ConsistOf(expectedConfig.Groups))
		ExpectWithOffset(1, config.Resources).To(ConsistOf(expectedConfig.Resources))
		ExpectWithOffset(1, config.ResourceTypes).To(ConsistOf(expectedConfig.ResourceTypes))
		ExpectWithOffset(1, config.Jobs).To(ConsistOf(expectedConfig.Jobs))
	}

	BeforeEach(func() {
		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).ToNot(HaveOccurred())
		otherTeam, err = teamFactory.CreateTeam(atc.Team{Name: "some-other-team"})
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("Workers", func() {
		BeforeEach(func() {
			postgresRunner.Truncate()
		})

		Context("when there are no workers", func() {
			It("returns an error", func() {
				workers, err := workerFactory.Workers()
				Expect(err).ToNot(HaveOccurred())
				Expect(workers).To(BeEmpty())
			})
		})
	})

	Describe("FindContainersByMetadata", func() {
		var sampleMetadata []db.ContainerMetadata
		var metaContainers map[db.ContainerMetadata][]db.Container

		BeforeEach(func() {
			baseMetadata := fullMetadata

			diffType := fullMetadata
			diffType.Type = db.ContainerTypeCheck

			diffStepName := fullMetadata
			diffStepName.StepName = fullMetadata.StepName + "-other"

			diffAttempt := fullMetadata
			diffAttempt.Attempt = fullMetadata.Attempt + ",2"

			diffPipelineID := fullMetadata
			diffPipelineID.PipelineID = fullMetadata.PipelineID + 1

			diffJobID := fullMetadata
			diffJobID.JobID = fullMetadata.JobID + 1

			diffBuildID := fullMetadata
			diffBuildID.BuildID = fullMetadata.BuildID + 1

			diffWorkingDirectory := fullMetadata
			diffWorkingDirectory.WorkingDirectory = fullMetadata.WorkingDirectory + "/other"

			diffUser := fullMetadata
			diffUser.User = fullMetadata.User + "-other"

			sampleMetadata = []db.ContainerMetadata{
				baseMetadata,
				diffType,
				diffStepName,
				diffAttempt,
				diffPipelineID,
				diffJobID,
				diffBuildID,
				diffWorkingDirectory,
				diffUser,
			}

			job, found, err := defaultPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			build, err := job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())
			Expect(build.CreatedBy()).ToNot(BeNil())
			Expect(*build.CreatedBy()).To(Equal(defaultBuildCreatedBy))

			metaContainers = make(map[db.ContainerMetadata][]db.Container)
			for _, meta := range sampleMetadata {
				firstContainerCreating, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("some-job"), defaultTeam.ID()), meta)
				Expect(err).ToNot(HaveOccurred())

				metaContainers[meta] = append(metaContainers[meta], firstContainerCreating)

				secondContainerCreating, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("some-job"), defaultTeam.ID()), meta)
				Expect(err).ToNot(HaveOccurred())

				secondContainerCreated, err := secondContainerCreating.Created()
				Expect(err).ToNot(HaveOccurred())

				metaContainers[meta] = append(metaContainers[meta], secondContainerCreated)

				thirdContainerCreating, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("some-job"), defaultTeam.ID()), meta)
				Expect(err).ToNot(HaveOccurred())

				thirdContainerCreated, err := thirdContainerCreating.Created()
				Expect(err).ToNot(HaveOccurred())

				// third container is not appended; we don't want Destroying containers
				thirdContainerDestroying, err := thirdContainerCreated.Destroying()
				Expect(err).ToNot(HaveOccurred())

				metaContainers[meta] = append(metaContainers[meta], thirdContainerDestroying)
			}
		})

	})

	Describe("Containers", func() {
		var (
			resourceContainer      db.CreatingContainer
			resourceTypeContainer  db.CreatingContainer
			prototypeContainer     db.CreatingContainer
			firstContainerCreating db.CreatingContainer
			scenario               *dbtest.Scenario
		)

		resourceConfigCheckContainer := func(worker db.Worker, resourceConfigID int) db.CreatingContainer {
			rc, found, err := resourceConfigFactory.FindResourceConfigByID(resourceConfigID)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			c, err := worker.CreateContainer(
				db.NewResourceConfigCheckSessionContainerOwner(
					rc.ID(),
					rc.OriginBaseResourceType().ID,
					db.ContainerOwnerExpiries{
						Min: 5 * time.Minute,
						Max: 1 * time.Hour,
					},
				),
				db.ContainerMetadata{},
			)
			Expect(err).ToNot(HaveOccurred())

			return c
		}

		Context("when there are task and check containers", func() {
			BeforeEach(func() {
				scenario = dbtest.Setup(
					builder.WithPipeline(atc.Config{
						Jobs: atc.JobConfigs{
							{
								Name: "some-job",
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
						Prototypes: atc.Prototypes{
							{
								Name: "some-prototype",
								Type: "some-base-resource-type",
								Source: atc.Source{
									"some-prototype": "source",
								},
							},
						},
					}),
					builder.WithWorker(atc.Worker{
						ResourceTypes: []atc.WorkerResourceType{defaultWorkerResourceType},
						Name:          "some-default-worker",
					}),
					builder.WithResourceVersions("some-resource"),
					builder.WithResourceTypeVersions("some-type"),
					builder.WithPrototypeVersions("some-prototype"),
				)

				build, err := scenario.Job("some-job").CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				firstContainerCreating, err = scenario.Workers[0].CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("some-job"), scenario.Team.ID()), db.ContainerMetadata{Type: "task", StepName: "some-task"})
				Expect(err).ToNot(HaveOccurred())

				resourceContainer = resourceConfigCheckContainer(scenario.Workers[0], scenario.Resource("some-resource").ResourceConfigID())
				resourceTypeContainer = resourceConfigCheckContainer(scenario.Workers[0], scenario.ResourceType("some-type").ResourceConfigID())
				prototypeContainer = resourceConfigCheckContainer(scenario.Workers[0], scenario.Prototype("some-prototype").ResourceConfigID())
			})

			It("finds all the containers", func() {
				containers, err := scenario.Team.Containers()
				Expect(err).ToNot(HaveOccurred())

				Expect(containers).To(ConsistOf(firstContainerCreating, resourceContainer, resourceTypeContainer, prototypeContainer))
			})

			It("does not find containers for other teams", func() {
				containers, err := otherTeam.Containers()
				Expect(err).ToNot(HaveOccurred())
				Expect(containers).To(BeEmpty())
			})
		})

		Context("when there is a check container on a team worker", func() {
			var resourceContainer db.Container

			BeforeEach(func() {
				scenario = dbtest.Setup(
					builder.WithTeam("some-test-team"),
					builder.WithPipeline(atc.Config{
						Jobs: atc.JobConfigs{
							{
								Name: "some-job",
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
					}),
					builder.WithWorker(atc.Worker{
						Name:          "default-team-worker",
						Team:          "some-test-team",
						ResourceTypes: []atc.WorkerResourceType{defaultWorkerResourceType},
					}),
					builder.WithResourceVersions("some-resource"),
				)

				expiries := db.ContainerOwnerExpiries{
					Min: 5 * time.Minute,
					Max: 1 * time.Hour,
				}

				rc, found, err := resourceConfigFactory.FindResourceConfigByID(scenario.Resource("some-resource").ResourceConfigID())
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				resourceContainer, err = scenario.Workers[0].CreateContainer(
					db.NewResourceConfigCheckSessionContainerOwner(
						rc.ID(),
						rc.OriginBaseResourceType().ID,
						expiries,
					),
					db.ContainerMetadata{
						Type: "check",
					},
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("finds the container", func() {
				containers, err := scenario.Team.Containers()
				Expect(err).ToNot(HaveOccurred())

				Expect(containers).To(HaveLen(1))
				Expect(containers).To(ConsistOf(resourceContainer))
			})

			Context("when there is another check container with the same resource config on a different team worker", func() {
				var (
					resource2Container db.Container
					otherScenario      *dbtest.Scenario
				)

				BeforeEach(func() {
					otherScenario = dbtest.Setup(
						builder.WithTeam("other-team"),
						builder.WithPipeline(atc.Config{
							Jobs: atc.JobConfigs{
								{
									Name: "some-job",
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
						}),
						builder.WithWorker(atc.Worker{
							Name:          "other-team-worker",
							Team:          "other-team",
							ResourceTypes: []atc.WorkerResourceType{defaultWorkerResourceType},
						}),
						builder.WithResourceVersions("some-resource"),
					)

					resource2Container = resourceConfigCheckContainer(otherScenario.Workers[0], otherScenario.Resource("some-resource").ResourceConfigID())
				})

				It("returns the container only from the team", func() {
					containers, err := otherScenario.Team.Containers()
					Expect(err).ToNot(HaveOccurred())

					Expect(containers).To(HaveLen(1))
					Expect(containers).To(ConsistOf(resource2Container))
				})
			})

			Context("when there is a check container with the same resource config on a global worker", func() {
				var (
					globalResourceContainer db.Container
				)

				BeforeEach(func() {
					globalResourceContainer = resourceConfigCheckContainer(scenario.Workers[0], scenario.Resource("some-resource").ResourceConfigID())
				})

				It("returns the container only from the team worker and global worker", func() {
					containers, err := scenario.Team.Containers()
					Expect(err).ToNot(HaveOccurred())

					Expect(containers).To(HaveLen(2))
					Expect(containers).To(ConsistOf(resourceContainer, globalResourceContainer))
				})
			})
		})
	})

	Describe("FindContainerByHandle", func() {
		var createdContainer db.CreatedContainer

		BeforeEach(func() {
			job, found, err := defaultPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			build, err := job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())

			creatingContainer, err := defaultWorker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("some-job"), defaultTeam.ID()), db.ContainerMetadata{Type: "task", StepName: "some-task"})
			Expect(err).ToNot(HaveOccurred())

			createdContainer, err = creatingContainer.Created()
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when worker is no longer in database", func() {
			BeforeEach(func() {
				err := defaultWorker.Delete()
				Expect(err).ToNot(HaveOccurred())
			})

			It("the container goes away from the db", func() {
				_, found, err := defaultTeam.FindContainerByHandle(createdContainer.Handle())
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

	})

	Describe("FindVolumeForWorkerArtifact", func() {

		Context("when the artifact doesn't exist", func() {
			It("returns not found", func() {
				_, found, err := defaultTeam.FindVolumeForWorkerArtifact(12)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the artifact exists", func() {
			BeforeEach(func() {
				_, err := dbConn.Exec("INSERT INTO worker_artifacts (id, name) VALUES ($1, '')", 18)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when the associated volume doesn't exist", func() {
				It("returns not found", func() {
					_, found, err := defaultTeam.FindVolumeForWorkerArtifact(18)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeFalse())
				})
			})

		})
	})

	Describe("FindWorkerForContainer", func() {
		Context("when there is no container", func() {
			It("returns nil", func() {
				worker, found, err := defaultTeam.FindWorkerForContainer("bogus-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(worker).To(BeNil())
			})
		})
	})

	Describe("PrivateAndPublicBuilds", func() {
		Context("when there are no builds", func() {
			It("returns an empty list of builds", func() {
				builds, pagination, err := team.PrivateAndPublicBuilds(db.Page{Limit: 2})
				Expect(err).ToNot(HaveOccurred())

				Expect(pagination.Older).To(BeNil())
				Expect(pagination.Newer).To(BeNil())
				Expect(builds).To(BeEmpty())
			})
		})

		Context("when there are builds", func() {
			var allBuilds [5]db.Build
			var pipeline db.Pipeline
			var pipelineBuilds [2]db.Build

			BeforeEach(func() {
				for i := 0; i < 3; i++ {
					build, err := team.CreateOneOffBuild()
					Expect(err).ToNot(HaveOccurred())
					allBuilds[i] = build
				}

				config := atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
						},
					},
				}
				var err error
				pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, config, db.ConfigVersion(1), false)
				Expect(err).ToNot(HaveOccurred())

				job, found, err := pipeline.Job("some-job")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				for i := 3; i < 5; i++ {
					build, err := job.CreateBuild(defaultBuildCreatedBy)
					Expect(err).ToNot(HaveOccurred())
					allBuilds[i] = build
					pipelineBuilds[i-3] = build
				}
			})

			Context("when there are builds that belong to different teams", func() {
				var teamABuilds [3]db.Build
				var teamBBuilds [3]db.Build

				var caseInsensitiveTeamA db.Team
				var caseInsensitiveTeamB db.Team

				BeforeEach(func() {
					_, err := teamFactory.CreateTeam(atc.Team{Name: "team-a"})
					Expect(err).ToNot(HaveOccurred())

					_, err = teamFactory.CreateTeam(atc.Team{Name: "team-b"})
					Expect(err).ToNot(HaveOccurred())

					var found bool
					caseInsensitiveTeamA, found, err = teamFactory.FindTeam("team-A")
					Expect(found).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())

					caseInsensitiveTeamB, found, err = teamFactory.FindTeam("team-B")
					Expect(found).To(BeTrue())
					Expect(err).ToNot(HaveOccurred())

					for i := 0; i < 3; i++ {
						teamABuilds[i], err = caseInsensitiveTeamA.CreateOneOffBuild()
						Expect(err).ToNot(HaveOccurred())

						teamBBuilds[i], err = caseInsensitiveTeamB.CreateOneOffBuild()
						Expect(err).ToNot(HaveOccurred())
					}
				})

				Context("when other team builds are private", func() {
				})

			})
		})
	})

	Describe("BuildsWithTime", func() {
		var (
			pipeline db.Pipeline
			builds   = make([]db.Build, 4)
		)

		BeforeEach(func() {
			var (
				err   error
				found bool
			)

			config := atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
					},
					{
						Name: "some-other-job",
					},
				},
			}
			pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, config, db.ConfigVersion(1), false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			for i := range builds {
				builds[i], err = job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				buildStart := time.Date(2020, 11, i+1, 0, 0, 0, 0, time.UTC)
				_, err = dbConn.Exec("UPDATE builds SET start_time = to_timestamp($1) WHERE id = $2", buildStart.Unix(), builds[i].ID())
				Expect(err).NotTo(HaveOccurred())

				builds[i], found, err = job.Build(builds[i].Name())
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			}
		})

		Context("When not providing boundaries", func() {
			Context("without a limit specified", func() {
			})
		})
	})

	Describe("Builds", func() {
		var (
			expectedBuilds                              []db.Build
			pipeline                                    db.Pipeline
			oneOffBuild, build, secondBuild, thirdBuild db.Build
		)

		BeforeEach(func() {
			var err error

			oneOffBuild, err = team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			expectedBuilds = append(expectedBuilds, oneOffBuild)

			config := atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
					},
					{
						Name: "some-other-job",
					},
				},
			}
			pipeline, _, err = team.SavePipeline(atc.PipelineRef{Name: "some-pipeline"}, config, db.ConfigVersion(1), false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			build, err = job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())
			expectedBuilds = append(expectedBuilds, build)

			secondBuild, err = job.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())
			expectedBuilds = append(expectedBuilds, secondBuild)

			someOtherJob, found, err := pipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			thirdBuild, err = someOtherJob.CreateBuild(defaultBuildCreatedBy)
			Expect(err).ToNot(HaveOccurred())
			expectedBuilds = append(expectedBuilds, thirdBuild)
		})

		Context("when limiting the range of build ids", func() {
			Context("specifying from greater than the biggest ID in the database", func() {
			})

			Context("specifying invalid boundaries", func() {
			})
		})

		Context("when there are builds that belong to different teams", func() {
			var teamABuilds [3]db.Build
			var teamBBuilds [3]db.Build

			var caseInsensitiveTeamA db.Team
			var caseInsensitiveTeamB db.Team

			BeforeEach(func() {
				_, err := teamFactory.CreateTeam(atc.Team{Name: "team-a"})
				Expect(err).ToNot(HaveOccurred())

				_, err = teamFactory.CreateTeam(atc.Team{Name: "team-b"})
				Expect(err).ToNot(HaveOccurred())

				var found bool
				caseInsensitiveTeamA, found, err = teamFactory.FindTeam("team-A")
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())

				caseInsensitiveTeamB, found, err = teamFactory.FindTeam("team-B")
				Expect(found).To(BeTrue())
				Expect(err).ToNot(HaveOccurred())

				for i := 0; i < 3; i++ {
					teamABuilds[i], err = caseInsensitiveTeamA.CreateOneOffBuild()
					Expect(err).ToNot(HaveOccurred())

					teamBBuilds[i], err = caseInsensitiveTeamB.CreateOneOffBuild()
					Expect(err).ToNot(HaveOccurred())
				}
			})

		})
	})

	Describe("SavePipeline", func() {
		type SerialGroup struct {
			JobID int
			Name  string
		}

		var (
			config       atc.Config
			otherConfig  atc.Config
			pipelineRef  atc.PipelineRef
			pipelineName string
		)

		BeforeEach(func() {
			config = atc.Config{
				Groups: atc.GroupConfigs{
					{
						Name:      "some-group",
						Jobs:      []string{"job-1", "job-2"},
						Resources: []string{"resource-1", "resource-2"},
					},
				},

				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
						Icon: "some-icon",
					},
				},

				ResourceTypes: atc.ResourceTypes{
					{
						Name: "some-resource-type",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				Prototypes: atc.Prototypes{
					{
						Name: "some-prototype",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				Jobs: atc.JobConfigs{
					{
						Name: "some-job",

						Public: true,

						Serial:       true,
						SerialGroups: []string{"serial-group-1", "serial-group-2"},

						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name:     "some-input",
									Resource: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
									Passed:  []string{"job-1", "job-2"},
									Trigger: true,
								},
							},
							{
								Config: &atc.TaskStep{
									Name:       "some-task",
									Privileged: true,
									ConfigPath: "some/config/path.yml",
									Config: &atc.TaskConfig{
										RootfsURI: "some-image",
									},
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
								},
							},
						},
					},
					{
						Name: "job-1",
					},
					{
						Name: "job-2",
					},
				},
			}

			otherConfig = atc.Config{
				Groups: atc.GroupConfigs{
					{
						Name:      "some-group",
						Jobs:      []string{"some-other-job", "job-1", "job-2"},
						Resources: []string{"resource-1", "resource-2"},
					},
				},

				Resources: atc.ResourceConfigs{
					{
						Name: "some-other-resource",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				Jobs: atc.JobConfigs{
					{
						Name: "some-other-job",
					},
				},
			}

			pipelineName = "some-pipeline"
			pipelineRef = atc.PipelineRef{Name: pipelineName}
		})

		It("caches the team id", func() {
			_, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			pipeline, found, err := team.Pipeline(pipelineRef)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(pipeline.TeamID()).To(Equal(team.ID()))
		})

		It("is not archived by default", func() {
			_, _, err := team.SavePipeline(pipelineRef, config, 0, true)
			Expect(err).ToNot(HaveOccurred())

			pipeline, found, err := team.Pipeline(pipelineRef)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(pipeline.Archived()).To(BeFalse())
		})

		It("requests schedule on the pipeline", func() {
			requestedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			otherPipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "other-pipeline"}, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			requestedJob, found, err := requestedPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			otherJob, found, err := otherPipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			requestedSchedule1 := requestedJob.ScheduleRequestedTime()
			requestedSchedule2 := otherJob.ScheduleRequestedTime()

			config.Resources[0].Source = atc.Source{
				"source-other-config": "some-other-value",
			}

			_, _, err = team.SavePipeline(pipelineRef, config, requestedPipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			found, err = requestedJob.Reload()
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			found, err = otherJob.Reload()
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(requestedJob.ScheduleRequestedTime()).Should(BeTemporally(">", requestedSchedule1))
			Expect(otherJob.ScheduleRequestedTime()).Should(BeTemporally("==", requestedSchedule2))
		})

		It("creates all of the resources from the pipeline in the database", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			resource, found, err := savedPipeline.Resource("some-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.Type()).To(Equal("some-type"))
			Expect(resource.Source()).To(Equal(atc.Source{
				"source-config": "some-value",
			}))
		})

		It("updates resource config source and resets config_id and config_scope_id", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithBaseResourceType(dbConn, "some-type"),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
				),
			)
			Expect(scenario.Resource("some-resource").Source()).To(Equal(atc.Source{
				"source-config": "some-value",
			}))
			Expect(scenario.Resource("some-resource").ResourceConfigID()).ToNot(BeZero())
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).ToNot(BeZero())

			config.Resources[0].Source = atc.Source{"new-source-config": "some-other-value"}
			scenario.Run(
				builder.WithPipeline(config),
			)

			Expect(scenario.Resource("some-resource").Source()).To(Equal(atc.Source{
				"new-source-config": "some-other-value",
			}))
			Expect(scenario.Resource("some-resource").ResourceConfigID()).To(BeZero())
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(BeZero())
		})

		It("updates resource config type and resets config_id and config_scope_id", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithBaseResourceType(dbConn, "some-type"),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
				),
			)
			Expect(scenario.Resource("some-resource").Type()).To(Equal("some-type"))
			Expect(scenario.Resource("some-resource").ResourceConfigID()).ToNot(BeZero())
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).ToNot(BeZero())

			config.Resources[0].Type = "some-other-type"
			scenario.Run(
				builder.WithPipeline(config),
			)

			Expect(scenario.Resource("some-resource").Type()).To(Equal("some-other-type"))
			Expect(scenario.Resource("some-resource").ResourceConfigID()).To(BeZero())
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(BeZero())
		})

		It("updates other resource fields and does not reset config_id and config_scope_id", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithBaseResourceType(dbConn, "some-type"),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
				),
			)
			Expect(scenario.Resource("some-resource").Icon()).To(Equal("some-icon"))
			configID, configScopeID := scenario.Resource("some-resource").ResourceConfigID(), scenario.Resource("some-resource").ResourceConfigScopeID()
			Expect(configID).ToNot(BeZero())
			Expect(configScopeID).ToNot(BeZero())

			config.Resources[0].Icon = "some-other-icon"
			scenario.Run(
				builder.WithPipeline(config),
			)

			Expect(scenario.Resource("some-resource").Icon()).To(Equal("some-other-icon"))
			Expect(scenario.Resource("some-resource").ResourceConfigID()).To(Equal(configID))
			Expect(scenario.Resource("some-resource").ResourceConfigScopeID()).To(Equal(configScopeID))
		})

		It("clears out api pinned version when resaving a pinned version on the pipeline config", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithBaseResourceType(dbConn, "some-type"),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
				),
				builder.WithPinnedVersion("some-resource", atc.Version{"version": "v1"}),
			)

			Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))

			config.Resources[0].Version = atc.Version{
				"version": "v2",
			}

			scenario.Run(
				builder.WithPipeline(config),
			)

			Expect(scenario.Resource("some-resource").ConfigPinnedVersion()).To(Equal(atc.Version{"version": "v2"}))
			Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(BeNil())
		})

		It("clears out config pinned version when it is removed", func() {
			config.Resources[0].Version = atc.Version{
				"version": "v1",
			}

			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			resource, found, err := pipeline.Resource("some-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.ConfigPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))
			Expect(resource.APIPinnedVersion()).To(BeNil())

			config.Resources[0].Version = nil

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			resource, found, err = savedPipeline.Resource("some-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.ConfigPinnedVersion()).To(BeNil())
			Expect(resource.APIPinnedVersion()).To(BeNil())
		})

		It("does not clear the api pinned version when resaving pipeline config", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(config),
				builder.WithBaseResourceType(dbConn, "some-type"),
				builder.WithResourceVersions(
					"some-resource",
					atc.Version{"version": "v1"},
					atc.Version{"version": "v2"},
				),
				builder.WithPinnedVersion("some-resource", atc.Version{"version": "v1"}),
			)

			Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))

			scenario.Run(
				builder.WithPipeline(config),
			)

			Expect(scenario.Resource("some-resource").APIPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))
		})

		It("marks resource as inactive if it is no longer in config", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.Resources = []atc.ResourceConfig{}
			config.Jobs = atc.JobConfigs{
				{
					Name: "some-job",

					Public: true,

					Serial:       true,
					SerialGroups: []string{"serial-group-1", "serial-group-2"},

					PlanSequence: []atc.Step{
						{
							Config: &atc.TaskStep{
								Name:       "some-task",
								Privileged: true,
								ConfigPath: "some/config/path.yml",
								Config: &atc.TaskConfig{
									RootfsURI: "some-image",
								},
							},
						},
						{
							Config: &atc.PutStep{
								Name: "some-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
							},
						},
					},
				},
				{
					Name: "job-1",
				},
				{
					Name: "job-2",
				},
			}
			config.Resources = atc.ResourceConfigs{
				{
					Name: "some-resource",
					Type: "some-type",
				},
			}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err := savedPipeline.Resource("some-other-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("creates all of the resource types from the pipeline in the database", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			resourceType, found, err := savedPipeline.ResourceType("some-resource-type")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resourceType.Type()).To(Equal("some-type"))
			Expect(resourceType.Source()).To(Equal(atc.Source{
				"source-config": "some-value",
			}))
		})

		It("updates resource type config from the pipeline in the database", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.ResourceTypes[0].Source = atc.Source{
				"source-other-config": "some-other-value",
			}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			resourceType, found, err := savedPipeline.ResourceType("some-resource-type")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resourceType.Type()).To(Equal("some-type"))
			Expect(resourceType.Source()).To(Equal(atc.Source{
				"source-other-config": "some-other-value",
			}))
		})

		It("marks resource type as inactive if it is no longer in config", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.ResourceTypes = []atc.ResourceType{}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err := savedPipeline.ResourceType("some-resource-type")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("creates all of the prototypes from the pipeline in the database", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			prototype, found, err := savedPipeline.Prototype("some-prototype")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(prototype.Type()).To(Equal("some-type"))
			Expect(prototype.Source()).To(Equal(atc.Source{
				"source-config": "some-value",
			}))
		})

		It("updates prototype config from the pipeline in the database", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.Prototypes[0].Source = atc.Source{
				"source-other-config": "some-other-value",
			}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			prototype, found, err := savedPipeline.Prototype("some-prototype")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(prototype.Type()).To(Equal("some-type"))
			Expect(prototype.Source()).To(Equal(atc.Source{
				"source-other-config": "some-other-value",
			}))
		})

		It("marks prototype as inactive if it is no longer in config", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.Prototypes = atc.Prototypes{}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err := savedPipeline.Prototype("some-resource-type")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("creates all of the jobs from the pipeline in the database", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := savedPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.Config()).To(Equal(config.Jobs[0]))
		})

		It("updates job config", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.Jobs[0].Public = false

			_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.Public()).To(BeFalse())
		})

		It("marks job inactive when it is no longer in pipeline", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			config.Jobs = []atc.JobConfig{}

			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err := savedPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		Context("get steps have passed constraints with glob patterns", func() {
			var (
				newConfig atc.Config
			)

			BeforeEach(func() {
				newConfig = atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name: "some-resource",
							Type: "some-type",
							Source: atc.Source{
								"source-config": "some-value",
							},
							Icon: "some-icon",
						},
					},

					ResourceTypes: atc.ResourceTypes{
						{
							Name: "some-resource-type",
							Type: "some-type",
							Source: atc.Source{
								"source-config": "some-value",
							},
						},
					},

					Jobs: atc.JobConfigs{
						{
							Name: "final-tasking",

							Public: true,

							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name:     "some-input",
										Resource: "some-resource",
										Params: atc.Params{
											"some-param": "some-value",
										},
										Passed:  []string{"job-*"},
										Trigger: true,
									},
								},
							},
						},
						{
							Name: "job-1",
						},
						{
							Name: "job-2",
						},
					},
				}
			})

			It("resolves appropriately", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, newConfig, 0, false)
				Expect(err).ToNot(HaveOccurred())

				job, found, err := pipeline.Job("final-tasking")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				jobConfig, err := job.Config()
				getStep := jobConfig.PlanSequence[0].Config.(*atc.GetStep)
				Expect(getStep.Passed).To(ConsistOf("job-*"))
			})
		})

		Context("update job names but keeps history", func() {
			BeforeEach(func() {
				newJobConfig := atc.JobConfig{
					Name: "new-job",

					Public: true,

					Serial:       true,
					SerialGroups: []string{"serial-group-1", "serial-group-2"},

					PlanSequence: []atc.Step{
						{
							Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "some-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
								Passed:  []string{"job-1", "job-2"},
								Trigger: true,
							},
						},
						{
							Config: &atc.TaskStep{
								Name:       "some-task",
								ConfigPath: "some/config/path.yml",
							},
						},
						{
							Config: &atc.PutStep{
								Name: "some-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
							},
						},
						{
							Config: &atc.DoStep{
								Steps: []atc.Step{
									{
										Config: &atc.TaskStep{
											Name:       "some-nested-task",
											ConfigPath: "some/config/path.yml",
										},
									},
								},
							},
						},
					},
				}

				config.Jobs = append(config.Jobs, newJobConfig)
			})

			It("should handle when there are multiple name changes", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				job, _, _ := pipeline.Job("some-job")
				otherJob, _, _ := pipeline.Job("new-job")

				config.Jobs[0].Name = "new-job"
				config.Jobs[0].OldName = "some-job"

				config.Jobs[3].Name = "new-other-job"
				config.Jobs[3].OldName = "new-job"

				updatedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())

				updatedJob, _, _ := updatedPipeline.Job("new-job")
				Expect(updatedJob.ID()).To(Equal(job.ID()))

				otherUpdatedJob, _, _ := updatedPipeline.Job("new-other-job")
				Expect(otherUpdatedJob.ID()).To(Equal(otherJob.ID()))
			})

			It("should handle when old job has the same name as new job", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				job, _, _ := pipeline.Job("some-job")

				config.Jobs[0].Name = "some-job"
				config.Jobs[0].OldName = "some-job"

				updatedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())

				updatedJob, _, _ := updatedPipeline.Job("some-job")
				Expect(updatedJob.ID()).To(Equal(job.ID()))
			})

			It("should return an error when there is a swap with job name", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				config.Jobs[0].Name = "new-job"
				config.Jobs[0].OldName = "some-job"

				config.Jobs[1].Name = "some-job"
				config.Jobs[1].OldName = "new-job"

				_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).To(HaveOccurred())
			})

			Context("when new job name is in database but is inactive", func() {
				It("should successfully update job name", func() {
					pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
					Expect(err).ToNot(HaveOccurred())

					config.Jobs = config.Jobs[:len(config.Jobs)-1]

					_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
					Expect(err).ToNot(HaveOccurred())

					config.Jobs[0].Name = "new-job"
					config.Jobs[0].OldName = "some-job"

					_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion()+1, false)
					Expect(err).ToNot(HaveOccurred())
				})
			})
		})

		Context("update resource names but keeps data", func() {

			BeforeEach(func() {

				config.Resources = append(config.Resources, atc.ResourceConfig{
					Name: "new-resource",
					Type: "some-type",
					Source: atc.Source{
						"source-config": "some-value",
					},
				})
			})

			It("should successfully update resource name", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				resource, _, _ := pipeline.Resource("some-resource")

				config.Resources[0].Name = "renamed-resource"
				config.Resources[0].OldName = "some-resource"

				config.Jobs[0].PlanSequence = []atc.Step{
					{
						Config: &atc.GetStep{
							Name:     "some-input",
							Resource: "renamed-resource",
							Params: atc.Params{
								"some-param": "some-value",
							},
							Passed:  []string{"job-1", "job-2"},
							Trigger: true,
						},
					},
				}

				updatedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())

				updatedResource, _, _ := updatedPipeline.Resource("renamed-resource")
				Expect(updatedResource.ID()).To(Equal(resource.ID()))
			})

			It("should handle when there are multiple name changes", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				resource, _, _ := pipeline.Resource("some-resource")
				otherResource, _, _ := pipeline.Resource("new-resource")

				config.Resources[0].Name = "new-resource"
				config.Resources[0].OldName = "some-resource"

				config.Resources[1].Name = "new-other-resource"
				config.Resources[1].OldName = "new-resource"

				config.Jobs[0].PlanSequence = []atc.Step{
					{
						Config: &atc.GetStep{
							Name:     "some-input",
							Resource: "new-resource",
							Params: atc.Params{
								"some-param": "some-value",
							},
							Passed:  []string{"job-1", "job-2"},
							Trigger: true,
						},
					},
				}

				updatedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())

				updatedResource, _, _ := updatedPipeline.Resource("new-resource")
				Expect(updatedResource.ID()).To(Equal(resource.ID()))

				otherUpdatedResource, _, _ := updatedPipeline.Resource("new-other-resource")
				Expect(otherUpdatedResource.ID()).To(Equal(otherResource.ID()))
			})

			It("should handle when old resource has the same name as new resource", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				resource, _, _ := pipeline.Resource("some-resource")

				config.Resources[0].Name = "some-resource"
				config.Resources[0].OldName = "some-resource"

				updatedPipeline, _, err := team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).ToNot(HaveOccurred())

				updatedResource, _, _ := updatedPipeline.Resource("some-resource")
				Expect(updatedResource.ID()).To(Equal(resource.ID()))
			})

			It("should return an error when there is a swap with resource name", func() {
				pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
				Expect(err).ToNot(HaveOccurred())

				config.Resources[0].Name = "new-resource"
				config.Resources[0].OldName = "some-resource"

				config.Resources[1].Name = "some-resource"
				config.Resources[1].OldName = "new-resource"

				_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
				Expect(err).To(HaveOccurred())
			})

			Context("when resource is renamed but has a disabled version", func() {
				var scenario *dbtest.Scenario
				var resource db.Resource

				BeforeEach(func() {
					scenario = dbtest.Setup(
						builder.WithPipeline(config),
						builder.WithBaseResourceType(dbConn, "some-type"),
						builder.WithDisabledVersion("some-resource", atc.Version{"disabled": "version"}),
					)

					versions, _, found, err := scenario.Resource("some-resource").Versions(db.Page{Limit: 3}, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(versions).To(HaveLen(1))
					Expect(versions[0].Version).To(Equal(atc.Version{"disabled": "version"}))
					Expect(versions[0].Enabled).To(BeFalse())

					resource = scenario.Resource("some-resource")
				})

				It("the disabled version should still be in the resource's version history", func() {
					config.Resources[0].Name = "disabled-resource"
					config.Resources[0].OldName = "some-resource"
					config.Jobs[0].PlanSequence = []atc.Step{
						{
							Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "disabled-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
								Passed:  []string{"job-1", "job-2"},
								Trigger: true,
							},
						},
					}

					scenario.Run(
						builder.WithPipeline(config),
						// Imitate a check run that found no new versions
						builder.WithResourceVersions("disabled-resource"),
					)

					updatedResource := scenario.Resource("disabled-resource")
					Expect(updatedResource.ID()).To(Equal(resource.ID()))

					versions, _, found, err := updatedResource.Versions(db.Page{Limit: 3}, nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(versions).To(HaveLen(1))
					Expect(versions[0].Version).To(Equal(atc.Version{"disabled": "version"}))
					Expect(versions[0].Enabled).To(BeFalse())
				})
			})

			Context("when new resource exists but the version is pinned", func() {
				var scenario *dbtest.Scenario
				var resource db.Resource

				BeforeEach(func() {
					scenario = dbtest.Setup(
						builder.WithPipeline(config),
						builder.WithBaseResourceType(dbConn, "some-type"),
						builder.WithResourceVersions(
							"some-resource",
							atc.Version{"version": "v1"},
							atc.Version{"version": "v2"},
							atc.Version{"version": "v3"},
						),
						builder.WithPinnedVersion("some-resource", atc.Version{"version": "v1"}),
					)

					resource = scenario.Resource("some-resource")
				})

				It("should not change the pinned version", func() {
					config.Resources[0].Name = "pinned-resource"
					config.Resources[0].OldName = "some-resource"
					config.Jobs[0].PlanSequence = []atc.Step{
						{
							Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "pinned-resource",
								Params: atc.Params{
									"some-param": "some-value",
								},
								Passed:  []string{"job-1", "job-2"},
								Trigger: true,
							},
						},
					}

					scenario.Run(
						builder.WithPipeline(config),
					)

					updatedResource := scenario.Resource("pinned-resource")
					Expect(updatedResource.ID()).To(Equal(resource.ID()))
					Expect(updatedResource.APIPinnedVersion()).To(Equal(atc.Version{"version": "v1"}))
				})
			})
		})

		It("removes task caches for jobs that are no longer in pipeline", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			config.Jobs = []atc.JobConfig{}

			_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("removes task caches for tasks that are no longer exist", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			config.Jobs = []atc.JobConfig{
				{
					Name: "some-job",
					PlanSequence: []atc.Step{
						{
							Config: &atc.TaskStep{
								Name:       "some-other-task",
								ConfigPath: "some/config/path.yml",
							},
						},
					},
				},
			}

			_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-nested-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("should not remove task caches in other pipeline", func() {
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			otherPipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "other-pipeline"}, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			otherJob, found, err := otherPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			_, err = taskCacheFactory.FindOrCreate(otherJob.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(otherJob.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			config.Jobs = []atc.JobConfig{
				{
					Name: "some-job",
					PlanSequence: []atc.Step{
						{
							Config: &atc.TaskStep{
								Name:       "some-other-task",
								ConfigPath: "some/config/path.yml",
							},
						},
					},
				},
			}

			_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			_, found, err = taskCacheFactory.Find(job.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())

			_, found, err = taskCacheFactory.Find(otherJob.ID(), "some-task", "some-path")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("creates all of the serial groups from the jobs in the database", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			serialGroups := []SerialGroup{}
			rows, err := dbConn.Query("SELECT job_id, serial_group FROM jobs_serial_groups")
			Expect(err).ToNot(HaveOccurred())

			for rows.Next() {
				var serialGroup SerialGroup
				err = rows.Scan(&serialGroup.JobID, &serialGroup.Name)
				Expect(err).ToNot(HaveOccurred())
				serialGroups = append(serialGroups, serialGroup)
			}

			job, found, err := savedPipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(serialGroups).To(ConsistOf([]SerialGroup{
				{
					JobID: job.ID(),
					Name:  "serial-group-1",
				},
				{
					JobID: job.ID(),
					Name:  "serial-group-2",
				},
			}))
		})

		It("saves tags in the jobs table", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := savedPipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(job.Tags()).To(Equal([]string{"some-group"}))
		})

		It("saves tags in the jobs table based on globs", func() {
			otherConfig.Groups[0].Jobs = []string{"*-other-job"}
			savedPipeline, _, err := team.SavePipeline(pipelineRef, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := savedPipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(job.Tags()).To(Equal([]string{"some-group"}))
		})

		It("updates tags in the jobs table", func() {
			savedPipeline, _, err := team.SavePipeline(pipelineRef, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err := savedPipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(job.Tags()).To(Equal([]string{"some-group"}))

			otherConfig.Groups = atc.GroupConfigs{
				{
					Name: "some-other-group",
					Jobs: []string{"job-1", "job-2", "some-other-job"},
				},
				{
					Name: "some-another-group",
					Jobs: []string{"*-other-job"},
				},
			}

			savedPipeline, _, err = team.SavePipeline(pipelineRef, otherConfig, savedPipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			job, found, err = savedPipeline.Job("some-other-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(job.Tags()).To(ConsistOf([]string{"some-another-group", "some-other-group"}))
		})

		It("deletes old job pipes and inserts new ones", func() {
			config = atc.Config{
				Groups: atc.GroupConfigs{
					{
						Name:      "some-group",
						Jobs:      []string{"job-1", "job-2"},
						Resources: []string{"resource-1", "resource-2"},
					},
				},

				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				ResourceTypes: atc.ResourceTypes{
					{
						Name: "some-resource-type",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				Jobs: atc.JobConfigs{
					{
						Name: "job-1",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "job-2",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "some-job",

						Public: true,

						Serial:       true,
						SerialGroups: []string{"serial-group-1", "serial-group-2"},

						PlanSequence: []atc.Step{
							{
								Config: &atc.DoStep{
									Steps: []atc.Step{
										{
											Config: &atc.GetStep{
												Name:     "other-input",
												Resource: "some-resource",
											},
										},
									},
								},
							},
							{
								Config: &atc.GetStep{
									Name:     "some-input",
									Resource: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
									Passed:  []string{"job-1", "job-2"},
									Trigger: true,
								},
							},
							{
								Config: &atc.TaskStep{
									Name:       "some-task",
									Privileged: true,
									ConfigPath: "some/config/path.yml",
									Config: &atc.TaskConfig{
										RootfsURI: "some-image",
									},
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
								},
							},
						},
					},
				},
			}

			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, true)
			Expect(err).ToNot(HaveOccurred())

			rows, err := psql.Select("name", "job_id", "resource_id", "passed_job_id").
				From("job_inputs").
				Where(sq.Expr(`job_id in (
					SELECT j.id
					FROM jobs j
					WHERE j.pipeline_id = $1
				)`, pipeline.ID())).
				RunWith(dbConn).
				Query()
			Expect(err).ToNot(HaveOccurred())

			type jobPipe struct {
				name        string
				jobID       int
				resourceID  int
				passedJobID int
			}

			var jobPipes []jobPipe
			for rows.Next() {
				var jp jobPipe
				var passedJob sql.NullInt64
				err = rows.Scan(&jp.name, &jp.jobID, &jp.resourceID, &passedJob)
				Expect(err).ToNot(HaveOccurred())

				if passedJob.Valid {
					jp.passedJobID = int(passedJob.Int64)
				}

				jobPipes = append(jobPipes, jp)
			}

			job1, found, err := pipeline.Job("job-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			job2, found, err := pipeline.Job("job-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			someJob, found, err := pipeline.Job("some-job")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			someResource, found, err := pipeline.Resource("some-resource")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(jobPipes).To(ConsistOf(
				jobPipe{
					name:       "some-resource",
					jobID:      job1.ID(),
					resourceID: someResource.ID(),
				},
				jobPipe{
					name:       "some-resource",
					jobID:      job2.ID(),
					resourceID: someResource.ID(),
				},
				jobPipe{
					name:       "other-input",
					jobID:      someJob.ID(),
					resourceID: someResource.ID(),
				},
				jobPipe{
					name:        "some-input",
					jobID:       someJob.ID(),
					resourceID:  someResource.ID(),
					passedJobID: job1.ID(),
				},
				jobPipe{
					name:        "some-input",
					jobID:       someJob.ID(),
					resourceID:  someResource.ID(),
					passedJobID: job2.ID(),
				},
			))

			config = atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				ResourceTypes: atc.ResourceTypes{
					{
						Name: "some-resource-type",
						Type: "some-type",
						Source: atc.Source{
							"source-config": "some-value",
						},
					},
				},

				Jobs: atc.JobConfigs{
					{
						Name: "job-2",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "some-job",

						Public: true,

						Serial:       true,
						SerialGroups: []string{"serial-group-1", "serial-group-2"},

						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name:     "some-input",
									Resource: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
									Passed:  []string{"job-2"},
									Trigger: true,
								},
							},
							{
								Config: &atc.TaskStep{
									Name:       "some-task",
									Privileged: true,
									ConfigPath: "some/config/path.yml",
									Config: &atc.TaskConfig{
										RootfsURI: "some-image",
									},
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
									Params: atc.Params{
										"some-param": "some-value",
									},
								},
							},
						},
					},
				},
			}

			_, _, err = team.SavePipeline(pipelineRef, config, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			rows, err = psql.Select("name", "job_id", "resource_id", "passed_job_id").
				From("job_inputs").
				Where(sq.Expr(`job_id in (
					SELECT j.id
					FROM jobs j
					WHERE j.pipeline_id = $1
				)`, pipeline.ID())).
				RunWith(dbConn).
				Query()
			Expect(err).ToNot(HaveOccurred())

			var newJobPipes []jobPipe
			for rows.Next() {
				var jp jobPipe
				var passedJob sql.NullInt64
				err = rows.Scan(&jp.name, &jp.jobID, &jp.resourceID, &passedJob)
				Expect(err).ToNot(HaveOccurred())

				if passedJob.Valid {
					jp.passedJobID = int(passedJob.Int64)
				}

				newJobPipes = append(newJobPipes, jp)
			}

			Expect(newJobPipes).To(ConsistOf(
				jobPipe{
					name:       "some-resource",
					jobID:      job2.ID(),
					resourceID: someResource.ID(),
				},
				jobPipe{
					name:        "some-input",
					jobID:       someJob.ID(),
					resourceID:  someResource.ID(),
					passedJobID: job2.ID(),
				},
			))
		})

		Context("updating an existing pipeline", func() {
			It("resets to unarchived", func() {
				team.SavePipeline(pipelineRef, config, 0, false)
				pipeline, _, _ := team.Pipeline(pipelineRef)
				pipeline.Archive()

				team.SavePipeline(pipelineRef, config, db.ConfigVersion(0), true)
				pipeline.Reload()
				Expect(pipeline.Archived()).To(BeFalse(), "the pipeline remained archived")
			})
		})

		It("can lookup a pipeline by name", func() {
			otherPipelineFilter := atc.PipelineRef{Name: "an-other-pipeline-name"}

			_, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = team.SavePipeline(otherPipelineFilter, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			pipeline, found, err := team.Pipeline(pipelineRef)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(pipeline.Name()).To(Equal(pipelineName))
			Expect(pipeline.ID()).ToNot(Equal(0))
			resourceTypes, err := pipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			resources, err := pipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			jobs, err := pipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			jobConfigs, err := jobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        pipeline.Groups(),
				Resources:     resources.Configs(),
				ResourceTypes: resourceTypes.Configs(),
				Jobs:          jobConfigs,
			}, config)

			otherPipeline, found, err := team.Pipeline(otherPipelineFilter)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(otherPipeline.Name()).To(Equal(otherPipelineFilter.Name))
			Expect(otherPipeline.ID()).ToNot(Equal(0))
			otherResourceTypes, err := otherPipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			otherResources, err := otherPipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			otherJobs, err := otherPipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			otherJobConfigs, err := otherJobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        otherPipeline.Groups(),
				Resources:     otherResources.Configs(),
				ResourceTypes: otherResourceTypes.Configs(),
				Jobs:          otherJobConfigs,
			}, otherConfig)

		})

		It("can manage multiple pipeline configurations", func() {
			otherPipelineFilter := atc.PipelineRef{Name: "an-other-pipeline-name"}

			By("being able to save the config")
			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			otherPipeline, _, err := team.SavePipeline(otherPipelineFilter, otherConfig, 0, false)
			Expect(err).ToNot(HaveOccurred())

			By("returning the saved config to later gets")
			resourceTypes, err := pipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			resources, err := pipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			jobs, err := pipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			jobConfigs, err := jobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        pipeline.Groups(),
				Resources:     resources.Configs(),
				ResourceTypes: resourceTypes.Configs(),
				Jobs:          jobConfigs,
			}, config)

			otherResourceTypes, err := otherPipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			otherResources, err := otherPipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			otherJobs, err := otherPipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			otherJobConfigs, err := otherJobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        otherPipeline.Groups(),
				Resources:     otherResources.Configs(),
				ResourceTypes: otherResourceTypes.Configs(),
				Jobs:          otherJobConfigs,
			}, otherConfig)

			By("returning the saved groups")
			returnedGroups := pipeline.Groups()
			Expect(returnedGroups).To(Equal(config.Groups))

			otherReturnedGroups := otherPipeline.Groups()
			Expect(otherReturnedGroups).To(Equal(otherConfig.Groups))

			updatedConfig := config

			updatedConfig.Groups = append(config.Groups, atc.GroupConfig{
				Name: "new-group",
				Jobs: []string{"new-job-1", "new-job-2"},
			})

			updatedConfig.Resources = append(config.Resources, atc.ResourceConfig{
				Name: "new-resource",
				Type: "new-type",
				Source: atc.Source{
					"new-source-config": "new-value",
				},
			})

			updatedConfig.Jobs = append(config.Jobs, atc.JobConfig{
				Name: "new-job",
				PlanSequence: []atc.Step{
					{
						Config: &atc.GetStep{
							Name:     "new-input",
							Resource: "new-resource",
							Params: atc.Params{
								"new-param": "new-value",
							},
						},
					},
					{
						Config: &atc.TaskStep{
							Name:       "some-task",
							ConfigPath: "new/config/path.yml",
						},
					},
				},
			})

			By("not allowing non-sequential updates")
			_, _, err = team.SavePipeline(pipelineRef, updatedConfig, pipeline.ConfigVersion()-1, false)
			Expect(err).To(Equal(db.ErrConfigComparisonFailed))

			_, _, err = team.SavePipeline(pipelineRef, updatedConfig, pipeline.ConfigVersion()+10, false)
			Expect(err).To(Equal(db.ErrConfigComparisonFailed))

			_, _, err = team.SavePipeline(otherPipelineFilter, updatedConfig, otherPipeline.ConfigVersion()-1, false)
			Expect(err).To(Equal(db.ErrConfigComparisonFailed))

			_, _, err = team.SavePipeline(otherPipelineFilter, updatedConfig, otherPipeline.ConfigVersion()+10, false)
			Expect(err).To(Equal(db.ErrConfigComparisonFailed))

			By("being able to update the config with a valid con")
			pipeline, _, err = team.SavePipeline(pipelineRef, updatedConfig, pipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())
			otherPipeline, _, err = team.SavePipeline(otherPipelineFilter, updatedConfig, otherPipeline.ConfigVersion(), false)
			Expect(err).ToNot(HaveOccurred())

			By("returning the updated config")
			resourceTypes, err = pipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			resources, err = pipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			jobs, err = pipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			jobConfigs, err = jobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        pipeline.Groups(),
				Resources:     resources.Configs(),
				ResourceTypes: resourceTypes.Configs(),
				Jobs:          jobConfigs,
			}, updatedConfig)

			otherResourceTypes, err = otherPipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			otherResources, err = otherPipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			otherJobs, err = otherPipeline.Jobs()
			Expect(err).ToNot(HaveOccurred())
			otherJobConfigs, err = otherJobs.Configs()
			Expect(err).ToNot(HaveOccurred())
			expectConfigsEqual(atc.Config{
				Groups:        otherPipeline.Groups(),
				Resources:     otherResources.Configs(),
				ResourceTypes: otherResourceTypes.Configs(),
				Jobs:          otherJobConfigs,
			}, updatedConfig)

			By("returning the saved groups")
			returnedGroups = pipeline.Groups()
			Expect(returnedGroups).To(Equal(updatedConfig.Groups))

			otherReturnedGroups = otherPipeline.Groups()
			Expect(otherReturnedGroups).To(Equal(updatedConfig.Groups))
		})

		It("should return sorted resources and resource_types", func() {
			config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{
				Name: "new-resource-type",
				Type: "new-type",
				Source: atc.Source{
					"new-source-config": "new-value",
				},
			})

			config.Resources = append(config.Resources, atc.ResourceConfig{
				Name: "new-resource",
				Type: "new-type",
				Source: atc.Source{
					"new-source-config": "new-value",
				},
			})

			pipeline, _, err := team.SavePipeline(pipelineRef, config, 0, false)
			Expect(err).ToNot(HaveOccurred())

			resourceTypes, err := pipeline.ResourceTypes()
			Expect(err).ToNot(HaveOccurred())
			rtConfigs := resourceTypes.Configs()
			Expect(rtConfigs[0].Name).To(Equal(config.ResourceTypes[1].Name)) // "new-resource-type"
			Expect(rtConfigs[1].Name).To(Equal(config.ResourceTypes[0].Name)) // "some-resource-type"

			resources, err := pipeline.Resources()
			Expect(err).ToNot(HaveOccurred())
			rConfigs := resources.Configs()
			Expect(rConfigs[0].Name).To(Equal(config.Resources[1].Name)) // "new-resource"
			Expect(rConfigs[1].Name).To(Equal(config.Resources[0].Name)) // "some-resource"
		})

		It("does not deadlock when concurrently setting pipelines and running checks/gets", func() {
			// enable concurrent use of database. this is set to 1 by default to
			// ensure methods don't require more than one in a single connection,
			// which can cause deadlocking as the pool is limited.
			dbConn.SetMaxOpenConns(4)

			config := atc.Config{
				ResourceTypes: atc.ResourceTypes{
					{
						Name:   "some-resource-type",
						Type:   dbtest.BaseResourceType,
						Source: atc.Source{"foo": "bar"},
					},
				},
				Resources: atc.ResourceConfigs{
					{
						Name:   "some-resource",
						Type:   "some-resource-type",
						Source: atc.Source{"foo": "baz"},
					},
				},
			}
			scenario := dbtest.Setup(
				builder.WithBaseWorker(),
				builder.WithPipeline(config),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			errChan := make(chan error, 1)

			var wg sync.WaitGroup

			loopUntilTimeoutOrPanic := func(name string, run func(i int)) func() {
				wg.Add(1)
				return func() {
					defer wg.Done()
					defer func() {
						if err := recover(); err != nil {
							errChan <- fmt.Errorf("%s error: %v", name, err)
						}
					}()

					i := 0
					for {
						select {
						case <-ctx.Done():
							return
						default:
							run(i)
							i++
						}
					}
				}
			}

			pipeline := scenario.Pipeline
			go loopUntilTimeoutOrPanic("set pipeline", func(_ int) {
				var err error
				pipeline, _, err = scenario.Team.SavePipeline(
					atc.PipelineRef{Name: pipeline.Name()},
					config,
					pipeline.ConfigVersion(),
					false,
				)
				if err != nil {
					panic(err)
				}
			})()

			resource := scenario.Resource("some-resource")
			go loopUntilTimeoutOrPanic("check resource", func(i int) {
				scenario.Run(
					builder.WithResourceVersions(resource.Name(), atc.Version{"v": strconv.Itoa(i / 10)}),
				)
			})()

			build, err := defaultJob.CreateBuild("some-user")
			Expect(err).ToNot(HaveOccurred())

			go loopUntilTimeoutOrPanic("check resource type", func(i int) {
				scenario.Run(
					builder.WithResourceTypeVersions("some-resource-type", atc.Version{"foo": strconv.Itoa(i / 10)}),
				)
			})()

			go loopUntilTimeoutOrPanic("get resource", func(i int) {
				customResourceTypeCache, err := resourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					dbtest.BaseResourceType,
					atc.Version{"foo": strconv.Itoa(i / 10)},
					atc.Source{"foo": "bar"},
					atc.Params{},
					nil,
				)
				if err != nil {
					panic(err)
				}

				_, err = resourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					resource.Type(),
					atc.Version{"v": strconv.Itoa(i / 10)},
					resource.Source(),
					atc.Params{},
					customResourceTypeCache,
				)
				if err != nil {
					panic(err)
				}
			})()

			go func() {
				wg.Wait()
				close(errChan)
			}()

			Expect(<-errChan).ToNot(HaveOccurred())
		})
	})

	Describe("FindCheckContainers", func() {
		var (
			logger lager.Logger
		)

		BeforeEach(func() {
			logger = lagertest.NewTestLogger("db-test")
		})

		Context("when pipeline exists", func() {
			Context("when resource exists", func() {
				Context("when check container does not exist", func() {
					It("returns empty list", func() {
						containers, checkContainersExpiresAt, err := defaultTeam.FindCheckContainers(logger, defaultPipelineRef, "some-resource")
						Expect(err).ToNot(HaveOccurred())
						Expect(containers).To(BeEmpty())
						Expect(checkContainersExpiresAt).To(BeEmpty())
					})
				})
			})

			Context("when resource does not exist", func() {
				It("returns empty list", func() {
					containers, checkContainersExpiresAt, err := defaultTeam.FindCheckContainers(logger, defaultPipelineRef, "non-existent-resource")
					Expect(err).ToNot(HaveOccurred())
					Expect(containers).To(BeEmpty())
					Expect(checkContainersExpiresAt).To(BeEmpty())
				})
			})
		})

		Context("when pipeline does not exist", func() {
			It("returns empty list", func() {
				containers, checkContainersExpiresAt, err := defaultTeam.FindCheckContainers(logger, atc.PipelineRef{Name: "non-existent-pipeline"}, "some-resource")
				Expect(err).ToNot(HaveOccurred())
				Expect(containers).To(BeEmpty())
				Expect(checkContainersExpiresAt).To(BeEmpty())
			})
		})
	})

})
