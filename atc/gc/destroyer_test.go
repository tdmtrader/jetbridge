package gc_test

import (
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Destroyer", func() {
	var (
		containerRepository db.ContainerRepository
		volumeRepository    db.VolumeRepository
		destroyer           gc.Destroyer

		team   db.Team
		worker db.Worker
		build  db.Build
	)

	// The handles passed to DestroyContainers/DestroyVolumes are the ones to
	// KEEP -- they are what the worker still reports (container_repository.go:196
	// names the parameter handlesToIgnore). Anything else in the destroying state
	// on that worker is deleted. So each spec makes two, keeps one, and checks
	// which survived.
	destroyingContainer := func(planID string) db.DestroyingContainer {
		creating, err := worker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), atc.PlanID(planID), team.ID()),
			db.ContainerMetadata{Type: "task", StepName: "some-task"},
		)
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		destroying, err := created.Destroying()
		Expect(err).NotTo(HaveOccurred())
		return destroying
	}

	destroyingVolume := func(path string) db.DestroyingVolume {
		creating, err := worker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("vol-"+path), team.ID()),
			db.ContainerMetadata{Type: "task", StepName: "some-task"},
		)
		Expect(err).NotTo(HaveOccurred())

		creatingVolume, err := volumeRepository.CreateContainerVolume(team.ID(), worker.Name(), creating, path)
		Expect(err).NotTo(HaveOccurred())
		createdVolume, err := creatingVolume.Created()
		Expect(err).NotTo(HaveOccurred())
		destroying, err := createdVolume.Destroying()
		Expect(err).NotTo(HaveOccurred())
		return destroying
	}

	containerHandles := func() []string {
		var handles []string
		rows, err := psql.Select("handle").From("containers").RunWith(dbConn).Query()
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for rows.Next() {
			var h string
			Expect(rows.Scan(&h)).To(Succeed())
			handles = append(handles, h)
		}
		return handles
	}

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		containerRepository = db.NewContainerRepository(dbConn)
		volumeRepository = db.NewVolumeRepository(dbConn)
		destroyer = gc.NewDestroyer(logger, containerRepository, volumeRepository)

		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "destroyer-team"})
		Expect(err).NotTo(HaveOccurred())

		build, err = team.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		worker, err = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0)).
			SaveWorker(atc.Worker{Name: "some-worker"}, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("DestroyContainers", func() {
		It("removes the destroying containers the worker no longer reports", func() {
			kept := destroyingContainer("kept-plan")
			gone := destroyingContainer("gone-plan")

			Expect(destroyer.DestroyContainers(worker.Name(), []string{kept.Handle()})).To(Succeed())

			Expect(containerHandles()).To(ContainElement(kept.Handle()))
			Expect(containerHandles()).NotTo(ContainElement(gone.Handle()))
		})

		It("leaves containers on a different worker alone", func() {
			other, err := db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0)).
				SaveWorker(atc.Worker{Name: "other-worker"}, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			mine := destroyingContainer("mine")

			Expect(destroyer.DestroyContainers(other.Name(), []string{})).To(Succeed())

			Expect(containerHandles()).To(ContainElement(mine.Handle()),
				"a destroying container on another worker should not have been touched")
		})

		It("does nothing when the handle list is nil", func() {
			survivor := destroyingContainer("survivor")

			Expect(destroyer.DestroyContainers(worker.Name(), nil)).To(Succeed())

			Expect(containerHandles()).To(ContainElement(survivor.Handle()),
				"a nil handle list means the worker did not report, not that it reported nothing")
		})

		Context("when the worker name is not provided", func() {
			It("returns an error and destroys nothing", func() {
				survivor := destroyingContainer("survivor")

				err := destroyer.DestroyContainers("", []string{})
				Expect(err).To(MatchError("worker-name-must-be-provided"))

				Expect(containerHandles()).To(ContainElement(survivor.Handle()))
			})
		})

		Context("when the container repository fails", func() {
			// Narrowly scoped: the repository is asked to fail at exactly this
			// call so the passthrough can be observed. A real database has no way
			// to fail one statement on demand.
			It("returns the error", func() {
				failing := new(dbfakes.FakeContainerRepository)
				failing.RemoveDestroyingContainersReturns(0, errors.New("I am le tired"))

				err := gc.NewDestroyer(logger, failing, volumeRepository).
					DestroyContainers("some-worker", []string{"a"})

				Expect(err).To(MatchError("I am le tired"))
			})
		})
	})

	Describe("DestroyVolumes", func() {
		It("removes the destroying volumes the worker no longer reports", func() {
			kept := destroyingVolume("kept-path")
			gone := destroyingVolume("gone-path")

			Expect(destroyer.DestroyVolumes(worker.Name(), []string{kept.Handle()})).To(Succeed())

			remaining, err := volumeRepository.GetDestroyingVolumes(worker.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(remaining).To(ContainElement(kept.Handle()))
			Expect(remaining).NotTo(ContainElement(gone.Handle()))
		})

		It("does nothing when the handle list is nil", func() {
			survivor := destroyingVolume("survivor-path")

			Expect(destroyer.DestroyVolumes(worker.Name(), nil)).To(Succeed())

			remaining, err := volumeRepository.GetDestroyingVolumes(worker.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(remaining).To(ContainElement(survivor.Handle()))
		})

		Context("when the worker name is not provided", func() {
			It("returns an error and destroys nothing", func() {
				survivor := destroyingVolume("survivor-path")

				err := destroyer.DestroyVolumes("", []string{})
				Expect(err).To(MatchError("worker-name-must-be-provided"))

				remaining, err := volumeRepository.GetDestroyingVolumes(worker.Name())
				Expect(err).NotTo(HaveOccurred())
				Expect(remaining).To(ContainElement(survivor.Handle()))
			})
		})

		Context("when the volume repository fails", func() {
			// Narrowly scoped: fail only this repository call so the
			// destroyer's error passthrough can be observed without making the
			// shared PostgreSQL service fail unrelated statements.
			It("returns the error", func() {
				failing := new(dbfakes.FakeVolumeRepository)
				failing.RemoveDestroyingVolumesReturns(0, errors.New("I am le tired"))

				err := gc.NewDestroyer(logger, containerRepository, failing).
					DestroyVolumes("some-worker", []string{"a"})

				Expect(err).To(MatchError("I am le tired"))
			})
		})
	})

	Describe("FindDestroyingVolumesForGc", func() {
		It("returns nothing when the worker has no destroying volumes", func() {
			handles, err := destroyer.FindDestroyingVolumesForGc(worker.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(handles).To(BeEmpty())
		})

		It("returns the handles of the destroying volumes", func() {
			first := destroyingVolume("first-path")
			second := destroyingVolume("second-path")

			handles, err := destroyer.FindDestroyingVolumesForGc(worker.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(handles).To(ConsistOf(first.Handle(), second.Handle()))
		})

		Context("when the volume repository fails", func() {
			// Narrowly scoped: fail only this read so the destroyer's error
			// passthrough remains covered independently of database availability.
			It("returns the error", func() {
				failing := new(dbfakes.FakeVolumeRepository)
				failing.GetDestroyingVolumesReturns(nil, errors.New("some-bad-err"))

				_, err := gc.NewDestroyer(logger, containerRepository, failing).
					FindDestroyingVolumesForGc("some-worker")

				Expect(err).To(HaveOccurred())
			})
		})
	})
})
