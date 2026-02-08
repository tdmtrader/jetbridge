package db_test

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worker", func() {
	var (
		atcWorker atc.Worker
		worker    Worker
	)

	BeforeEach(func() {
		atcWorker = atc.Worker{
			Ephemeral:        true,
			ActiveContainers: 140,
			ResourceTypes: []atc.WorkerResourceType{
				{
					Type:    "some-resource-type",
					Image:   "some-image",
					Version: "some-version",
				},
				{
					Type:    "other-resource-type",
					Image:   "other-image",
					Version: "other-version",
				},
			},
			Platform:  "some-platform",
			Tags:      atc.Tags{"some", "tags"},
			Name:      "some-name",
			StartTime: 55912945,
		}
	})

	Describe("Delete", func() {
		BeforeEach(func() {
			var err error
			worker, err = workerFactory.SaveWorker(atcWorker, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())
		})

		It("deletes the record for the worker", func() {
			err := worker.Delete()
			Expect(err).NotTo(HaveOccurred())

			_, found, err := workerFactory.GetWorker(atcWorker.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})

	Describe("FindContainer/CreateContainer", func() {
		var (
			containerMetadata ContainerMetadata
			containerOwner    ContainerOwner

			foundCreatingContainer CreatingContainer
			foundCreatedContainer  CreatedContainer
			worker                 Worker
		)

		expiries := ContainerOwnerExpiries{
			Min: 5 * time.Minute,
			Max: 1 * time.Hour,
		}

		BeforeEach(func() {
			containerMetadata = ContainerMetadata{
				Type: "check",
			}

			var err error
			worker, err = workerFactory.SaveWorker(atcWorker, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			atcWorker2 := atcWorker
			atcWorker2.Name = "some-name2"
			otherWorker, err = workerFactory.SaveWorker(atcWorker2, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-resource-type",
				atc.Source{"some": "source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			containerOwner = NewResourceConfigCheckSessionContainerOwner(
				resourceConfig.ID(),
				resourceConfig.OriginBaseResourceType().ID,
				expiries,
			)
		})

		JustBeforeEach(func() {
			var err error
			foundCreatingContainer, foundCreatedContainer, err = worker.FindContainer(containerOwner)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when there is a creating container", func() {
			var creatingContainer CreatingContainer

			BeforeEach(func() {
				var err error
				creatingContainer, err = worker.CreateContainer(containerOwner, containerMetadata)
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns it", func() {
				Expect(foundCreatedContainer).To(BeNil())
				Expect(foundCreatingContainer).ToNot(BeNil())
			})

			Context("when finding on another worker", func() {
				BeforeEach(func() {
					worker = otherWorker
				})

				It("does not find it", func() {
					Expect(foundCreatingContainer).To(BeNil())
					Expect(foundCreatedContainer).To(BeNil())
				})
			})

			Context("when there is a created container", func() {
				BeforeEach(func() {
					_, err := creatingContainer.Created()
					Expect(err).ToNot(HaveOccurred())
				})

				It("returns it", func() {
					Expect(foundCreatedContainer).ToNot(BeNil())
					Expect(foundCreatingContainer).To(BeNil())
				})

				Context("when finding on another worker", func() {
					BeforeEach(func() {
						worker = otherWorker
					})

					It("does not find it", func() {
						Expect(foundCreatingContainer).To(BeNil())
						Expect(foundCreatedContainer).To(BeNil())
					})
				})
			})

			Context("when the creating container is failed and gced", func() {
				BeforeEach(func() {
					var err error
					_, err = creatingContainer.Failed()
					Expect(err).ToNot(HaveOccurred())

					containerRepository := NewContainerRepository(dbConn)
					containersDestroyed, err := containerRepository.DestroyFailedContainers()
					Expect(containersDestroyed).To(Equal(1))
					Expect(err).ToNot(HaveOccurred())

					var checkSessions int
					err = dbConn.QueryRow("SELECT COUNT(*) FROM resource_config_check_sessions").Scan(&checkSessions)
					Expect(err).ToNot(HaveOccurred())
					Expect(checkSessions).To(Equal(1))
				})

				Context("and we create a new container", func() {
					BeforeEach(func() {
						_, err := worker.CreateContainer(containerOwner, containerMetadata)
						Expect(err).ToNot(HaveOccurred())
					})

					It("does not duplicate the resource config check session", func() {
						var checkSessions int
						err := dbConn.QueryRow("SELECT COUNT(*) FROM resource_config_check_sessions").Scan(&checkSessions)
						Expect(err).ToNot(HaveOccurred())
						Expect(checkSessions).To(Equal(1))
					})
				})
			})
		})

		Context("when there is no container", func() {
			It("returns nil", func() {
				Expect(foundCreatedContainer).To(BeNil())
				Expect(foundCreatingContainer).To(BeNil())
			})
		})

		Context("when the container has a meta type", func() {
			var container CreatingContainer

			Context("when the meta type is check", func() {
				BeforeEach(func() {
					containerMetadata = ContainerMetadata{
						Type: "check",
					}

					var err error
					container, err = worker.CreateContainer(containerOwner, containerMetadata)
					Expect(err).ToNot(HaveOccurred())
				})

				It("returns a container with empty team id", func() {
					var teamID sql.NullString

					err := dbConn.QueryRow(fmt.Sprintf("SELECT team_id FROM containers WHERE id='%d'", container.ID())).Scan(&teamID)
					Expect(err).ToNot(HaveOccurred())
					Expect(teamID.Valid).To(BeFalse())
				})
			})

			Context("when the meta type is not check", func() {
				BeforeEach(func() {
					containerMetadata = ContainerMetadata{
						Type: "get",
					}

					oneOffBuild, err := defaultTeam.CreateOneOffBuild()
					Expect(err).ToNot(HaveOccurred())

					container, err = worker.CreateContainer(NewBuildStepContainerOwner(oneOffBuild.ID(), atc.PlanID("1"), 1), containerMetadata)
					Expect(err).ToNot(HaveOccurred())
				})

				It("returns a container with a team id", func() {
					var teamID sql.NullString

					err := dbConn.QueryRow(fmt.Sprintf("SELECT team_id FROM containers WHERE id='%d'", container.ID())).Scan(&teamID)
					Expect(err).ToNot(HaveOccurred())
					Expect(teamID.Valid).To(BeTrue())
				})
			})
		})
	})

	Describe("Find/CreateContainer, fixed handle", func() {
		It("uses the specified handle for the container", func() {
			owner := NewFixedHandleContainerOwner("my-handle")
			creating, err := defaultWorker.CreateContainer(owner, ContainerMetadata{})
			Expect(err).ToNot(HaveOccurred())
			Expect(creating.Handle()).To(Equal("my-handle"))

			foundCreating, _, err := defaultWorker.FindContainer(owner)
			Expect(err).ToNot(HaveOccurred())
			Expect(foundCreating).To(Equal(creating))

			created, err := creating.Created()
			Expect(err).ToNot(HaveOccurred())

			_, foundCreated, err := defaultWorker.FindContainer(owner)
			Expect(err).ToNot(HaveOccurred())
			Expect(foundCreated).To(Equal(created))
		})
	})

})
