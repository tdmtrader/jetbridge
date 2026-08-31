package gc_test

import (
	"context"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Run() accumulates four independent repository failures into one multierror
// without short-circuiting. Each decorator below fails exactly one of those
// calls and delegates the rest to PostgreSQL, so the steps that survive the
// error are observed by their effect on real rows.
type failFindOrphanedContainers struct{ db.ContainerRepository }

func (failFindOrphanedContainers) FindOrphanedContainers() ([]db.CreatingContainer, []db.CreatedContainer, []db.DestroyingContainer, error) {
	return nil, nil, nil, errors.New("nope")
}

type failDestroyFailedContainers struct{ db.ContainerRepository }

func (failDestroyFailedContainers) DestroyFailedContainers() (int, error) {
	return 0, errors.New("nope")
}

type failRemoveMissingContainers struct{ db.ContainerRepository }

func (failRemoveMissingContainers) RemoveMissingContainers(time.Duration) (int, error) {
	return 0, errors.New("nope")
}

var _ = Describe("ContainerCollector", func() {
	var (
		containerRepository db.ContainerRepository
		collector           GcCollector

		team   db.Team
		worker db.Worker
		build  db.Build

		missingContainerGracePeriod time.Duration
		hijackContainerGracePeriod  time.Duration
	)

	// A container is orphaned once its build stops being interceptible
	// (container_repository.go:234-237). Everything below builds a real
	// container in that state and then varies only the column under test.
	createdContainer := func(planID string) db.CreatedContainer {
		creating, err := worker.CreateContainer(
			db.NewBuildStepContainerOwner(build.ID(), atc.PlanID(planID), team.ID()),
			db.ContainerMetadata{Type: "task", StepName: "some-task"},
		)
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		return created
	}

	createdCheckContainer := func(resourceConfig db.ResourceConfig) db.CreatedContainer {
		owner := db.NewResourceConfigCheckSessionContainerOwner(
			resourceConfig.ID(),
			resourceConfig.OriginBaseResourceType().ID,
			db.ContainerOwnerExpiries{Min: 5 * time.Minute, Max: time.Hour},
		)
		creating, err := worker.CreateContainer(owner, db.ContainerMetadata{Type: db.ContainerTypeCheck})
		Expect(err).NotTo(HaveOccurred())
		created, err := creating.Created()
		Expect(err).NotTo(HaveOccurred())
		return created
	}

	orphan := func() {
		Expect(build.SetInterceptible(false)).To(Succeed())
	}

	// last_hijack is set by the hijack path (container.go:215); these specs
	// backdate the column directly rather than hijacking and then sleeping.
	setColumn := func(handle, column string, value any) {
		_, err := psql.Update("containers").
			Set(column, value).
			Where(sq.Eq{"handle": handle}).
			RunWith(dbConn).Exec()
		Expect(err).NotTo(HaveOccurred())
	}

	stateOf := func(handle string) string {
		var state string
		Expect(dbConn.QueryRow("SELECT state FROM containers WHERE handle = $1", handle).Scan(&state)).To(Succeed())
		return state
	}

	exists := func(handle string) bool {
		var n int
		Expect(dbConn.QueryRow("SELECT count(*) FROM containers WHERE handle = $1", handle).Scan(&n)).To(Succeed())
		return n > 0
	}

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		missingContainerGracePeriod = time.Minute
		hijackContainerGracePeriod = time.Minute

		containerRepository = db.NewContainerRepository(dbConn)
		collector = gc.NewContainerCollector(
			containerRepository,
			missingContainerGracePeriod,
			hijackContainerGracePeriod,
		)

		var err error
		team, err = teamFactory.CreateTeam(atc.Team{Name: "collector-team"})
		Expect(err).NotTo(HaveOccurred())

		build, err = team.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		worker, err = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0)).
			SaveWorker(atc.Worker{
				Name: "some-worker",
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-base-type", Image: "/some-image", Version: "some-version"},
				},
			}, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Run", func() {
		It("succeeds with nothing to collect", func() {
			Expect(collector.Run(context.TODO())).To(Succeed())
		})

		// Run() does not short-circuit: a failure in one repository call is
		// accumulated and the remaining calls still run. Each spec below fails
		// exactly one call and proves the others reached PostgreSQL.
		Describe("error isolation", func() {
			var (
				orphaned db.CreatedContainer
				failed   db.CreatingContainer
				missing  db.CreatedContainer
				excess   db.CreatedContainer
			)

			BeforeEach(func() {
				orphaned = createdContainer("isolation-orphan")

				var err error
				failed, err = worker.CreateContainer(
					db.NewBuildStepContainerOwner(build.ID(), atc.PlanID("isolation-failed"), team.ID()),
					db.ContainerMetadata{Type: "task", StepName: "some-task"},
				)
				Expect(err).NotTo(HaveOccurred())
				_, err = failed.Failed()
				Expect(err).NotTo(HaveOccurred())

				// The missing container hangs off a second build that stays
				// interceptible: RemoveMissingContainers only deletes rows still
				// in the created state, so the orphan step must not touch it.
				liveBuild, err := team.CreateOneOffBuild()
				Expect(err).NotTo(HaveOccurred())
				creating, err := worker.CreateContainer(
					db.NewBuildStepContainerOwner(liveBuild.ID(), atc.PlanID("isolation-missing"), team.ID()),
					db.ContainerMetadata{Type: "task", StepName: "some-task"},
				)
				Expect(err).NotTo(HaveOccurred())
				missing, err = creating.Created()
				Expect(err).NotTo(HaveOccurred())
				setColumn(missing.Handle(), "missing_since", sq.Expr("NOW() - '1 hour'::interval"))

				resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
					"some-base-type",
					atc.Source{"repository": "some-isolation-image"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())
				excess = createdCheckContainer(resourceConfig)
				createdCheckContainer(resourceConfig)

				orphan()
			})

			It("collects everything downstream when finding orphans fails", func() {
				err := gc.NewContainerCollector(
					failFindOrphanedContainers{containerRepository},
					missingContainerGracePeriod,
					hijackContainerGracePeriod,
				).Run(context.TODO())

				Expect(err).To(MatchError(ContainSubstring("nope")))

				Expect(stateOf(failed.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
				Expect(exists(missing.Handle())).To(BeFalse())
				Expect(stateOf(excess.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			})

			It("collects everything downstream when destroying failed containers fails", func() {
				err := gc.NewContainerCollector(
					failDestroyFailedContainers{containerRepository},
					missingContainerGracePeriod,
					hijackContainerGracePeriod,
				).Run(context.TODO())

				Expect(err).To(MatchError(ContainSubstring("nope")))

				Expect(stateOf(orphaned.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
				Expect(exists(missing.Handle())).To(BeFalse())
				Expect(stateOf(excess.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			})

			It("collects everything downstream when removing missing containers fails", func() {
				err := gc.NewContainerCollector(
					failRemoveMissingContainers{containerRepository},
					missingContainerGracePeriod,
					hijackContainerGracePeriod,
				).Run(context.TODO())

				Expect(err).To(MatchError(ContainSubstring("nope")))

				Expect(stateOf(orphaned.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
				Expect(stateOf(failed.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
				Expect(stateOf(excess.Handle())).To(Equal(string(atc.ContainerStateDestroying)))
			})
		})
	})
})
