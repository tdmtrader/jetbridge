package gc_test

import (
	"context"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VolumeCollector", func() {
	var (
		volumeCollector          GcCollector
		missingVolumeGracePeriod time.Duration

		volumeRepository   db.VolumeRepository
		workerFactory      db.WorkerFactory
		creatingContainer1 db.CreatingContainer
		creatingContainer2 db.CreatingContainer
		team               db.Team
		worker             db.Worker
		build              db.Build
	)

	BeforeEach(func() {
		postgresRunner.Truncate()

		volumeRepository = db.NewVolumeRepository(dbConn)
		workerFactory = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))

		missingVolumeGracePeriod = 1 * time.Minute

		volumeCollector = gc.NewVolumeCollector(
			volumeRepository,
			missingVolumeGracePeriod,
		)
	})

	Describe("Run", func() {
		BeforeEach(func() {
			var err error
			team, err = teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).ToNot(HaveOccurred())

			build, err = team.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			worker, err = workerFactory.SaveWorker(atc.Worker{
				Name: "some-worker",
			}, 5*time.Minute)
			Expect(err).ToNot(HaveOccurred())

			creatingContainer1, err = worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", team.ID()), db.ContainerMetadata{
				Type:     "task",
				StepName: "some-task",
			})
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when there are volumes the worker has stopped reporting", func() {
			// RemoveMissingVolumes deletes created/failed volumes whose
			// missing_since is further back than the grace period
			// (volume_repository.go:167-173). missing_since is set by the
			// container/volume reaper, so fixtures write it directly -- the same
			// thing the sibling volume_repository_test.go does.
			//
			// The volumes belong to a live container on purpose. Run() calls
			// markOrphanedVolumesAsDestroying before RemoveMissingVolumes
			// (volume_collector.go:50-56), and a volume already moved to
			// 'destroying' no longer matches the created/failed filter -- so an
			// orphan would survive this sweep for a reason that has nothing to do
			// with the grace period.
			missingVolume := func(path string, missingSince any) string {
				volume, err := volumeRepository.CreateContainerVolume(team.ID(), worker.Name(), creatingContainer1, path)
				Expect(err).NotTo(HaveOccurred())
				created, err := volume.Created()
				Expect(err).NotTo(HaveOccurred())

				_, err = psql.Update("volumes").
					Set("missing_since", missingSince).
					Where(sq.Eq{"handle": created.Handle()}).
					RunWith(dbConn).Exec()
				Expect(err).NotTo(HaveOccurred())

				return created.Handle()
			}

			handles := func() []string {
				rows, err := psql.Select("handle").From("volumes").
					Where(sq.Eq{"worker_name": worker.Name()}).RunWith(dbConn).Query()
				Expect(err).NotTo(HaveOccurred())
				defer rows.Close()
				var out []string
				for rows.Next() {
					var h string
					Expect(rows.Scan(&h)).To(Succeed())
					out = append(out, h)
				}
				return out
			}

			It("deletes the ones past the grace period and keeps the rest", func() {
				longMissing := missingVolume("some-path-1", sq.Expr("NOW() - '1 hour'::interval"))
				justMissing := missingVolume("some-path-2", sq.Expr("NOW()"))
				stillReported := missingVolume("some-path-3", nil)

				Expect(volumeCollector.Run(context.TODO())).To(Succeed())

				Expect(handles()).To(ConsistOf(justMissing, stillReported))
				Expect(handles()).NotTo(ContainElement(longMissing))
			})
		})

		Context("when there are failed volumes", func() {
			JustBeforeEach(func() {
				creatingVolume1, err := volumeRepository.CreateContainerVolume(team.ID(), worker.Name(), creatingContainer1, "some-path-1")
				Expect(err).NotTo(HaveOccurred())

				_, err = creatingVolume1.Failed()
				Expect(err).NotTo(HaveOccurred())
			})

			It("deletes all the failed volumes from the database", func() {
				failedVolumesLen, err := volumeRepository.DestroyFailedVolumes()
				Expect(err).NotTo(HaveOccurred())
				Expect(failedVolumesLen).To(Equal(1))

				err = volumeCollector.Run(context.TODO())
				Expect(err).NotTo(HaveOccurred())

				failedVolumesLen, err = volumeRepository.DestroyFailedVolumes()
				Expect(err).NotTo(HaveOccurred())
				Expect(failedVolumesLen).To(Equal(0))
			})
		})

		Context("when there are orphaned volumes", func() {
			var expectedOrphanedVolumeHandles []string

			JustBeforeEach(func() {
				creatingContainer2, err = worker.CreateContainer(db.NewBuildStepContainerOwner(build.ID(), "some-plan", team.ID()), db.ContainerMetadata{
					Type:     "task",
					StepName: "some-task",
				})
				Expect(err).ToNot(HaveOccurred())

				creatingVolume1, err := volumeRepository.CreateContainerVolume(team.ID(), worker.Name(), creatingContainer1, "some-path-1")
				Expect(err).NotTo(HaveOccurred())
				expectedOrphanedVolumeHandles = append(expectedOrphanedVolumeHandles, creatingVolume1.Handle())

				_, err = creatingVolume1.Created()
				Expect(err).NotTo(HaveOccurred())

				creatingVolume2, err := volumeRepository.CreateContainerVolume(team.ID(), worker.Name(), creatingContainer2, "some-path-1")
				Expect(err).NotTo(HaveOccurred())

				_, err = creatingVolume2.Created()
				Expect(err).NotTo(HaveOccurred())

				createdContainer1, err := creatingContainer1.Created()
				Expect(err).NotTo(HaveOccurred())

				_, err = creatingContainer2.Created()
				Expect(err).NotTo(HaveOccurred())

				destroyingContainer, err := createdContainer1.Destroying()
				Expect(err).NotTo(HaveOccurred())

				destroyed, err := destroyingContainer.Destroy()
				Expect(err).NotTo(HaveOccurred())
				Expect(destroyed).To(BeTrue())
			})

			It("marks orphaned volumes as 'destroying'", func() {
				err = volumeCollector.Run(context.TODO())
				Expect(err).NotTo(HaveOccurred())

				destroyingVolumes, err := volumeRepository.GetDestroyingVolumes(worker.Name())
				Expect(err).NotTo(HaveOccurred())
				Expect(destroyingVolumes).To(HaveLen(1))

				Expect(destroyingVolumes).To(Equal(expectedOrphanedVolumeHandles))
			})
		})
	})
})
