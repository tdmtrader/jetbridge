package gc_test

import (
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Each decorator below fails exactly the one repository call whose error
// passthrough is under test and delegates everything else to PostgreSQL.
type failRemoveDestroyingContainers struct{ db.ContainerRepository }

func (failRemoveDestroyingContainers) RemoveDestroyingContainers(string, []string) (int, error) {
	return 0, errors.New("I am le tired")
}

type failRemoveDestroyingVolumes struct{ db.VolumeRepository }

func (failRemoveDestroyingVolumes) RemoveDestroyingVolumes(string, []string) (int, error) {
	return 0, errors.New("I am le tired")
}

type failGetDestroyingVolumes struct{ db.VolumeRepository }

func (failGetDestroyingVolumes) GetDestroyingVolumes(string) ([]string, error) {
	return nil, errors.New("some-bad-err")
}

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
	// on that worker is deleted.
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
		Context("when the worker name is not provided", func() {
			It("returns an error and destroys nothing", func() {
				survivor := destroyingContainer("survivor")

				err := destroyer.DestroyContainers("", []string{})
				Expect(err).To(MatchError("worker-name-must-be-provided"))

				Expect(containerHandles()).To(ContainElement(survivor.Handle()))
			})
		})

		Context("when the container repository fails", func() {
			It("returns the error and destroys nothing", func() {
				survivor := destroyingContainer("repo-failure")

				err := gc.NewDestroyer(logger, failRemoveDestroyingContainers{containerRepository}, volumeRepository).
					DestroyContainers(worker.Name(), []string{})

				Expect(err).To(MatchError("I am le tired"))
				Expect(containerHandles()).To(ContainElement(survivor.Handle()))
			})
		})
	})

	Describe("DestroyVolumes", func() {
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
			It("returns the error and destroys nothing", func() {
				survivor := destroyingVolume("repo-failure-path")

				err := gc.NewDestroyer(logger, containerRepository, failRemoveDestroyingVolumes{volumeRepository}).
					DestroyVolumes(worker.Name(), []string{})

				Expect(err).To(MatchError("I am le tired"))

				remaining, err := volumeRepository.GetDestroyingVolumes(worker.Name())
				Expect(err).NotTo(HaveOccurred())
				Expect(remaining).To(ContainElement(survivor.Handle()))
			})
		})
	})

	Describe("FindDestroyingVolumesForGc", func() {
		It("returns nothing when the worker has no destroying volumes", func() {
			handles, err := destroyer.FindDestroyingVolumesForGc(worker.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(handles).To(BeEmpty())
		})

		Context("when the volume repository fails", func() {
			It("returns the error", func() {
				destroyingVolume("read-failure-path")

				handles, err := gc.NewDestroyer(logger, containerRepository, failGetDestroyingVolumes{volumeRepository}).
					FindDestroyingVolumesForGc(worker.Name())

				Expect(err).To(MatchError("some-bad-err"))
				Expect(handles).To(BeEmpty())
			})
		})
	})
})
