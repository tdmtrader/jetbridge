package db_test

import (
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ContainerOwner", func() {
	Describe("AgentAttemptContainerOwner", func() {
		It("preserves attempt one while isolating later deterministic attempts", func() {
			buildID := 42
			planID := atc.PlanID("agent-review")
			teamID := 7

			legacyColumns, _, err := db.NewBuildStepContainerOwner(buildID, planID, teamID).Find(nil)
			Expect(err).NotTo(HaveOccurred())

			firstColumns, _, err := db.NewAgentAttemptContainerOwner(buildID, planID, teamID, 1).Find(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(firstColumns).To(Equal(legacyColumns))
			Expect(firstColumns).NotTo(HaveKey("handle"))

			secondColumns, _, err := db.NewAgentAttemptContainerOwner(buildID, planID, teamID, 2).Find(nil)
			Expect(err).NotTo(HaveOccurred())
			secondAgainColumns, _, err := db.NewAgentAttemptContainerOwner(buildID, planID, teamID, 2).Find(nil)
			Expect(err).NotTo(HaveOccurred())
			thirdColumns, _, err := db.NewAgentAttemptContainerOwner(buildID, planID, teamID, 3).Find(nil)
			Expect(err).NotTo(HaveOccurred())

			Expect(secondColumns).To(Equal(secondAgainColumns))
			Expect(secondColumns).NotTo(Equal(firstColumns))
			Expect(secondColumns).NotTo(Equal(thirdColumns))
			Expect(secondColumns["handle"]).To(HavePrefix("agent-attempt-"))
			Expect(secondColumns["build_id"]).To(Equal(buildID))
			Expect(secondColumns["plan_id"]).To(Equal(planID))
			Expect(secondColumns["team_id"]).To(Equal(teamID))
		})

		It("isolates attempts for lookup while retaining normal build garbage collection", func() {
			build, err := defaultJob.CreateBuild(defaultBuildCreatedBy)
			Expect(err).NotTo(HaveOccurred())

			planID := atc.PlanID("agent-review")
			firstOwner := db.NewAgentAttemptContainerOwner(build.ID(), planID, defaultTeam.ID(), 1)
			secondOwner := db.NewAgentAttemptContainerOwner(build.ID(), planID, defaultTeam.ID(), 2)

			second, err := defaultWorker.CreateContainer(secondOwner, db.ContainerMetadata{Type: db.ContainerTypeAgent})
			Expect(err).NotTo(HaveOccurred())
			first, err := defaultWorker.CreateContainer(firstOwner, db.ContainerMetadata{Type: db.ContainerTypeAgent})
			Expect(err).NotTo(HaveOccurred())
			Expect(first.ID()).NotTo(Equal(second.ID()))

			foundFirst, _, err := defaultWorker.FindContainer(firstOwner)
			Expect(err).NotTo(HaveOccurred())
			Expect(foundFirst.ID()).To(Equal(first.ID()))
			foundSecond, _, err := defaultWorker.FindContainer(secondOwner)
			Expect(err).NotTo(HaveOccurred())
			Expect(foundSecond.ID()).To(Equal(second.ID()))

			Expect(build.SetInterceptible(false)).To(Succeed())
			creating, created, destroying, err := containerRepository.FindOrphanedContainers()
			Expect(err).NotTo(HaveOccurred())
			Expect(creating).To(HaveLen(2))
			Expect([]string{creating[0].Handle(), creating[1].Handle()}).To(ConsistOf(first.Handle(), second.Handle()))
			Expect(created).To(BeEmpty())
			Expect(destroying).To(BeEmpty())
		})
	})

	Describe("ResourceConfigCheckSessionContainerOwner", func() {
		var (
			worker db.Worker

			owner         db.ContainerOwner
			ownerExpiries db.ContainerOwnerExpiries
			found         bool

			resourceConfig db.ResourceConfig
		)

		ownerExpiries = db.ContainerOwnerExpiries{
			Min: 5 * time.Minute,
			Max: 5 * time.Minute,
		}

		BeforeEach(func() {
			workerPayload := atc.Worker{
				ResourceTypes: []atc.WorkerResourceType{defaultWorkerResourceType},
				Name:          "resource-config-check-session-worker",
			}

			var err error
			worker, err = workerFactory.SaveWorker(workerPayload, 0)
			Expect(err).NotTo(HaveOccurred())

			resourceConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
				defaultWorkerResourceType.Type,
				atc.Source{
					"some-type": "source",
				},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		JustBeforeEach(func() {
			owner = db.NewResourceConfigCheckSessionContainerOwner(
				resourceConfig.ID(),
				resourceConfig.OriginBaseResourceType().ID,
				ownerExpiries,
			)
		})

		Describe("Find/Create", func() {
			var foundColumns sq.Eq

			JustBeforeEach(func() {
				var err error
				foundColumns, found, err = owner.Find(dbConn)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("when a resource config exists", func() {
				var createdColumns map[string]any

				BeforeEach(func() {
					existingOwner := db.NewResourceConfigCheckSessionContainerOwner(
						resourceConfig.ID(),
						resourceConfig.OriginBaseResourceType().ID,
						ownerExpiries,
					)

					tx, err := dbConn.Begin()
					Expect(err).ToNot(HaveOccurred())

					createdColumns, err = existingOwner.Create(tx, worker.Name())
					Expect(err).ToNot(HaveOccurred())
					Expect(createdColumns).ToNot(BeEmpty())

					Expect(tx.Commit()).To(Succeed())
				})

				It("finds the resource config check session", func() {
					Expect(foundColumns).To(HaveLen(1))
					Expect(foundColumns["resource_config_check_session_id"]).To(ConsistOf(createdColumns["resource_config_check_session_id"]))
					Expect(found).To(BeTrue())
				})
			})

			Context("when there are multiple resource config check sessions", func() {
				var createdColumns, createdColumns2 map[string]any

				BeforeEach(func() {
					existingOwner := db.NewResourceConfigCheckSessionContainerOwner(
						resourceConfig.ID(),
						resourceConfig.OriginBaseResourceType().ID,
						ownerExpiries,
					)

					tx, err := dbConn.Begin()
					Expect(err).ToNot(HaveOccurred())

					createdColumns, err = existingOwner.Create(tx, worker.Name())
					Expect(err).ToNot(HaveOccurred())
					Expect(createdColumns).ToNot(BeEmpty())

					createdColumns2, err = existingOwner.Create(tx, defaultWorker.Name())
					Expect(err).ToNot(HaveOccurred())
					Expect(createdColumns).ToNot(BeEmpty())

					Expect(tx.Commit()).To(Succeed())
				})

				It("finds both resource config check sessions", func() {
					Expect(foundColumns).To(HaveLen(1))
					Expect(foundColumns["resource_config_check_session_id"]).To(ConsistOf(createdColumns["resource_config_check_session_id"], createdColumns2["resource_config_check_session_id"]))
					Expect(found).To(BeTrue())
				})
			})

			Context("when a resource config check session doesn't exist", func() {
				It("doesn't find a resource config check session", func() {
					Expect(found).To(BeFalse())
				})
			})
		})
	})
})
